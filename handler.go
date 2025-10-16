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
	taskID TaskID
	ctx    context.Context
	stdin  io.Reader
	env    []string
	result *taskExecutionResult
	logger *slog.Logger
}

func NewTaskContext(tm *TaskManager, w http.ResponseWriter, r *http.Request) *taskContext {
	input := http.MaxBytesReader(w, r.Body, tm.config.MaxInputBytes)

	taskID := newTaskID()
	return &taskContext{
		taskID: taskID,
		ctx:    r.Context(),
		stdin:  input,
		env:    tm.getEnv(r),
		result: &taskExecutionResult{Status: "running", ExitCode: -1},
		logger: tm.logger.With("taskID", taskID),
	}
}

type taskExecutionResult struct {
	mu       sync.RWMutex `json:"-"`
	Status   string       `json:"status,omitempty"`
	ExitCode int          `json:"exit_code"`
	StdOut   string       `json:"stdout,omitempty"`
	StdErr   string       `json:"stderr,omitempty"`
}

func (r *taskExecutionResult) copy() taskExecutionResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return taskExecutionResult{
		Status:   r.Status,
		ExitCode: r.ExitCode,
		StdOut:   r.StdOut,
		StdErr:   r.StdErr,
	}
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
		// Copy the result using thread-safe copy method
		resultCopy := result.copy()
		json.NewEncoder(w).Encode(&resultCopy)
	} else {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "task not found"})
	}

}
func (tm *TaskManager) handleRunTask(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	ctx := NewTaskContext(tm, w, r)

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
			tm.logger.Debug("semaphore acquired", "async", tm.config.Async)

			defer func() {
				if !tm.config.Async {
					<-tm.taskSemaphore
					tm.logger.Debug("semaphore released", "async", tm.config.Async)
				} else {
					if err != nil {
						<-tm.taskSemaphore
						tm.logger.Debug("semaphore released", "async", tm.config.Async, "error", err)
					}
				}
			}()
		default:
			http.Error(w, "Max concurrent tasks reached", http.StatusTooManyRequests)
			tm.counterRejection.WithLabelValues("max_concurrent").Inc()
			return
		}
	}

	// Read and validate input for both async and sync cases
	var bb []byte
	bb, err = io.ReadAll(ctx.stdin)
	if err != nil {
		http.Error(w, "Invalid content", http.StatusBadRequest)
		tm.counterRejection.WithLabelValues("invalid_content").Inc()
		return
	}
	ctx.stdin = bytes.NewReader(bb)

	// == async / synchronous
	if tm.config.Async {
		// Copy key name
		ctx.ctx = context.WithValue(context.Background(), keyNameContextKey, ctx.ctx.Value(keyNameContextKey))
		go func() {
			// Release semaphore
			defer func() {
				<-tm.taskSemaphore
				tm.logger.Debug("semaphore released", "async", tm.config.Async, "error", err, "goroutine", true)
			}()

			// Register task ID
			tm.taskRunsMutex.Lock()
			tm.taskRuns[ctx.taskID] = ctx.result
			tm.taskRunsMutex.Unlock()
			defer func() {
				retentionDuration := time.Duration(tm.config.AsyncResultRetentionSeconds) * time.Second
				time.AfterFunc(retentionDuration, func() {
					tm.cleanupTask(ctx)
				})
			}()

			tm.runTask(ctx)
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"task_id": ctx.taskID})
	} else {
		// Synchronous execution
		tm.runTask(ctx)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctx.result)
	}
}

func (tm *TaskManager) cleanupTask(ctx *taskContext) {
	tm.taskRunsMutex.Lock()
	delete(tm.taskRuns, ctx.taskID)
	remaining := len(tm.taskRuns)
	tm.taskRunsMutex.Unlock()

	ctx.logger.Debug("removed async task result", "task_id", ctx.taskID, "remaining_tasks", remaining)
}

func (tm *TaskManager) runTask(taskCtx *taskContext) {
	taskCtx.logger.Debug("starting task")
	startTime := time.Now()

	// == timeout
	var cancel context.CancelFunc
	taskCtx.ctx, cancel = context.WithTimeout(taskCtx.ctx, time.Duration(tm.config.ExecutionTimeoutSeconds)*time.Second)
	defer cancel()

	// == exec
	cmd := exec.CommandContext(taskCtx.ctx, tm.config.Command[0], tm.config.Command[1:]...)
	cmd.Env = taskCtx.env
	cmd.Dir = tm.config.Workdir
	cmd.Stdin = taskCtx.stdin

	limitedStdout := &OutputCollector{MaxRemaining: tm.config.MaxOutputBytes}
	limitedStderr := &OutputCollector{MaxRemaining: tm.config.MaxOutputBytes}

	cmd.Stdout = limitedStdout
	cmd.Stderr = limitedStderr
	if tm.config.MergeStderr {
		cmd.Stderr = limitedStdout
	}

	err := cmd.Run()
	duration := time.Since(startTime)

	// Lock the result for all updates
	taskCtx.result.mu.Lock()
	defer taskCtx.result.mu.Unlock()

	taskCtx.result.StdOut = limitedStdout.Buffer.String()
	taskCtx.result.StdErr = limitedStderr.Buffer.String()

	status := "success"
	if taskCtx.ctx.Err() == context.DeadlineExceeded {
		status = "timeout"
		taskCtx.result.ExitCode = -1
		taskCtx.logger.Warn("timeout")
	} else if err != nil {
		status = "failure"
		taskCtx.logger.Warn("failed to run the task", "error", err)
		taskCtx.result.ExitCode = -2
		if exitError, ok := err.(*exec.ExitError); ok {
			if waitStatus, ok := exitError.Sys().(syscall.WaitStatus); ok {
				taskCtx.result.ExitCode = waitStatus.ExitStatus()
			}
		}
	} else {
		// Success case
		taskCtx.result.ExitCode = 0
	}
	taskCtx.result.Status = status

	keyName := taskCtx.ctx.Value(keyNameContextKey).(string)
	tm.histTaskDuration.WithLabelValues(keyName, status).Observe(float64(duration.Seconds()))
	taskCtx.logger.Info("done", "stdout", taskCtx.result.StdOut, "stderr", taskCtx.result.StdErr)
}
