package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
)

const (
	labelAPIKeyUsed = "api_key_used"
	labelStatus     = "status"
)

type contextKey string

const keyNameContextKey contextKey = "keyName"

type taskContext struct {
	id     TaskID
	ctx    context.Context
	stdin  io.Reader
	env    []string
	result *taskExecutionResult
}

type taskExecutionResult struct {
	Status   string `json:"status,omitempty"`
	ExitCode int    `json:"exit_code"`
	StdOut   string `json:"stdout,omitempty"`
	StdErr   string `json:"stderr,omitempty"`
}

type TaskManager struct {
	taskName string
	config   *Task

	requestRateLimiter *rate.Limiter
	taskSemaphore      chan struct{}
	environment        []string

	keyVerifier *KeyVerifier
	logger      *slog.Logger

	histTaskDuration *prometheus.HistogramVec
	counterRejection *prometheus.CounterVec

	taskRuns      map[string]*taskExecutionResult
	taskRunsMutex sync.RWMutex
}

func NewTaskManager(name string, config *Task, keyStore *APIKeyStore) (*TaskManager, error) {
	logger := slog.With("handler", name)

	if len(config.APIKeyNames) == 0 {
		logger.Warn("no API keys configured")
	}

	keyVerifier, err := keyStore.KeyVerifier(config.APIKeyNames)
	if err != nil {
		return nil, err
	}

	logger.Info("added handler", "config", config)

	var rateLimiter *rate.Limiter = nil
	if config.RateLimit > 0 {
		rateLimiter = rate.NewLimiter(rate.Limit(config.RateLimit), 1)
	}

	var taskSemaphore chan struct{}
	if config.MaxConcurrentTasks > 0 {
		taskSemaphore = make(chan struct{}, config.MaxConcurrentTasks)
	}

	var env = append([]string{}, os.Environ()...)
	for k, v := range config.Environment {
		env = append(env, k+"="+v)
	}

	tm := &TaskManager{
		taskName:           name,
		config:             config,
		requestRateLimiter: rateLimiter,
		taskSemaphore:      taskSemaphore,
		keyVerifier:        keyVerifier,
		logger:             logger,
		environment:        env,

		histTaskDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:        "task_duration_seconds",
				Help:        "Duration of task execution",
				Buckets:     config.DurationHistogramBuckets,
				ConstLabels: prometheus.Labels{"handler": name},
			},
			[]string{labelAPIKeyUsed, labelStatus},
		),
		counterRejection: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "task_rejection_total",
			Help: "Total number of rejected requests",
			ConstLabels: prometheus.Labels{
				"handler": name,
			},
		}, []string{"reason"}),
		taskRuns: make(map[string]*taskExecutionResult),
	}

	return tm, nil
}

func (tm *TaskManager) getEnv(r *http.Request) []string {
	env := tm.environment
	if tm.config.PassRequestHeaders != nil {
		env = append([]string{}, tm.environment...)
		for _, h := range tm.config.PassRequestHeaders {
			values := r.Header.Values(h)
			env = append(env, "REQUEST_HEADER_"+sanitizeEnvVarName(h)+"="+strings.Join(values, ","))
		}
	}
	return env
}

func (tm *TaskManager) getTaskResult(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	tm.taskRunsMutex.RLock()
	result, exists := tm.taskRuns[taskID]
	tm.taskRunsMutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if exists {
		json.NewEncoder(w).Encode(result)
	} else {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "task not found"})
	}

}
func (tm *TaskManager) handleRunTask(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	maxInputBytes := tm.config.MaxInputBytes
	if maxInputBytes <= 0 {
		maxInputBytes = 16 * 1024
	}
	input := http.MaxBytesReader(w, r.Body, maxInputBytes)

	ctx := &taskContext{
		id:     newTaskID(),
		ctx:    r.Context(),
		stdin:  input,
		env:    tm.getEnv(r),
		result: &taskExecutionResult{Status: "running", ExitCode: -1},
	}

	// == rate limiter
	if tm.requestRateLimiter != nil {
		if !tm.requestRateLimiter.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			tm.counterRejection.WithLabelValues("limit_exceeded").Inc()
			return
		}
	}

	// == concurrent executions
	var err error = nil
	if tm.taskSemaphore != nil {
		select {
		case tm.taskSemaphore <- struct{}{}:
			tm.logger.Debug("semaphore acquired", "blocking", tm.config.Blocking)

			defer func() {
				if tm.config.Blocking {
					<-tm.taskSemaphore
					tm.logger.Debug("semaphore released", "blocking", tm.config.Blocking)
				} else {
					if err != nil {
						<-tm.taskSemaphore
						tm.logger.Debug("semaphore released", "blocking", tm.config.Blocking, "error", err)
					}
				}
			}()
		default:
			http.Error(w, "Max concurrent tasks reached", http.StatusTooManyRequests)
			tm.counterRejection.WithLabelValues("max_concurrent").Inc()
			return
		}
	}

	// == async / blocking
	if tm.config.Blocking {
		tm.runTask(ctx)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctx.result)
	} else {
		var bb []byte
		bb, err = io.ReadAll(input)
		if err != nil {
			http.Error(w, "Invalid content", http.StatusBadRequest)
			tm.counterRejection.WithLabelValues("invalid_content").Inc()
			return
		}
		ctx.stdin = bytes.NewReader(bb)

		// Copy key name
		ctx.ctx = context.WithValue(context.Background(), keyNameContextKey, ctx.ctx.Value(keyNameContextKey))
		go func() {
			// Release semaphore
			defer func() {
				<-tm.taskSemaphore
				tm.logger.Debug("semaphore released", "blocking", tm.config.Blocking, "error", err, "goroutine", true)
			}()

			// Register task ID
			tm.taskRunsMutex.Lock()
			tm.taskRuns[ctx.id] = ctx.result
			tm.taskRunsMutex.Unlock()
			defer func() {
				time.AfterFunc(20*time.Second, func() {
					tm.cleanupTask(ctx.id)
				})
			}()

			tm.runTask(ctx)
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"task_id": ctx.id})
	}
}

func (tm *TaskManager) cleanupTask(taskID TaskID) {
	tm.taskRunsMutex.Lock()
	delete(tm.taskRuns, taskID)
	remaining := len(tm.taskRuns)
	tm.taskRunsMutex.Unlock()

	tm.logger.Debug("removed async task result", "task_id", taskID, "remaining_tasks", remaining)
}

func (tm *TaskManager) runTask(taskCtx *taskContext) {
	tm.logger.Debug("starting task")
	startTime := time.Now()

	// == timeout
	executionTimeout := tm.config.ExecutionTimeoutSeconds
	if executionTimeout <= 0 {
		executionTimeout = 30
	}
	var cancel context.CancelFunc
	taskCtx.ctx, cancel = context.WithTimeout(taskCtx.ctx, time.Duration(executionTimeout)*time.Second)
	defer cancel()

	// == exec
	cmd := exec.CommandContext(taskCtx.ctx, tm.config.Command[0], tm.config.Command[1:]...)
	cmd.Env = taskCtx.env
	cmd.Dir = tm.config.Workdir
	cmd.Stdin = taskCtx.stdin

	maxOutput := tm.config.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = 16 * 1024
	}
	limitedStdout := &OutputCollector{MaxRemaining: maxOutput}
	limitedStderr := &OutputCollector{MaxRemaining: maxOutput}

	// RedirectStderr defaults to true, so redirect stderr to stdout unless explicitly set to false
	redirect := tm.config.RedirectStderr == nil || *tm.config.RedirectStderr
	cmd.Stdout = limitedStdout
	cmd.Stderr = limitedStderr
	if redirect {
		cmd.Stderr = limitedStdout
	}

	err := cmd.Run()
	duration := time.Since(startTime)

	taskCtx.result.StdOut = limitedStdout.Buffer.String()
	taskCtx.result.StdErr = limitedStderr.Buffer.String()

	status := "success"
	if taskCtx.ctx.Err() == context.DeadlineExceeded {
		status = "timeout"
		taskCtx.result.ExitCode = -1
	} else if err != nil {
		status = "failure"
		tm.logger.Warn("failed to run the task", "error", err)
		taskCtx.result.ExitCode = -2
		if exitError, ok := err.(*exec.ExitError); ok {
			if waitStatus, ok := exitError.Sys().(syscall.WaitStatus); ok {
				taskCtx.result.ExitCode = waitStatus.ExitStatus()
			}
		}
	}
	taskCtx.result.Status = status

	keyName := taskCtx.ctx.Value(keyNameContextKey).(string)
	tm.histTaskDuration.WithLabelValues(keyName, status).Observe(float64(duration.Seconds()))
	tm.logger.Info("done", "stdout", taskCtx.result.StdOut, "stderr", taskCtx.result.StdErr)
}
