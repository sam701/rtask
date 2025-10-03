package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
)

const (
	labelAPIKeyUsed = "api_key_used"
	labelExitCode   = "exit_code"
)

type contextKey string

const keyNameContextKey contextKey = "keyName"

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
			[]string{labelAPIKeyUsed, labelExitCode},
		),
		counterRejection: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "task_rejection_total",
			Help: "Total number of rejected requests",
			ConstLabels: prometheus.Labels{
				"handler": name,
			},
		}, []string{"reason"}),
	}

	return tm, nil
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func sanitizeEnvVarName(s string) string {
	var result strings.Builder
	result.Grow(len(s))
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			result.WriteRune(r)
		} else {
			result.WriteRune('_')
		}
	}
	return strings.ToUpper(result.String())
}

func (tm *TaskManager) generateExecutionID() string {
	bytes := make([]byte, 8)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func (tm *TaskManager) runTask(w http.ResponseWriter, r *http.Request) {
	executionID := tm.generateExecutionID()
	startTime := time.Now()

	maxInputBytes := tm.config.MaxInputBytes
	if maxInputBytes <= 0 {
		maxInputBytes = 16 * 1024
	}
	stdin := http.MaxBytesReader(w, r.Body, maxInputBytes)

	cmd := exec.CommandContext(r.Context(), tm.config.Command[0], tm.config.Command[1:]...)
	env := tm.environment
	if tm.config.PassRequestHeaders != nil {
		env = append([]string{}, tm.environment...)
		for _, h := range tm.config.PassRequestHeaders {
			values := r.Header.Values(h)
			env = append(env, "REQUEST_HEADER_"+sanitizeEnvVarName(h)+"="+strings.Join(values, ","))
		}
	}
	cmd.Env = env
	cmd.Dir = tm.config.Workdir
	cmd.Stdin = stdin
	// TODO: set env
	defer r.Body.Close()

	var stdout, stderr bytes.Buffer

	// RedirectStderr defaults to true, so redirect stderr to stdout unless explicitly set to false
	redirect := tm.config.RedirectStderr == nil || *tm.config.RedirectStderr
	if redirect {
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	err := cmd.Run()
	duration := time.Since(startTime)

	httpStatus := http.StatusOK
	maxOutput := tm.config.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = 16 * 1024
	}
	result := &taskExecutionResult{
		TaskID:   executionID,
		ExitCode: 0,
	}

	if redirect {
		result.StdOut = truncateString(stdout.String(), int(maxOutput))
	} else {
		result.StdOut = truncateString(stdout.String(), int(maxOutput))
		result.StdErr = truncateString(stderr.String(), int(maxOutput))
	}

	if r.Context().Err() == context.DeadlineExceeded {
		result.ExitCode = -1
		httpStatus = http.StatusRequestTimeout
		tm.counterRejection.WithLabelValues("timeout").Inc()
	} else if err != nil {
		tm.logger.Warn("failed to run the task", "error", err)
		if exitError, ok := err.(*exec.ExitError); ok {
			if waitStatus, ok := exitError.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = waitStatus.ExitStatus()
			}
		}
	}

	keyName := r.Context().Value(keyNameContextKey).(string)
	tm.histTaskDuration.WithLabelValues(keyName, strconv.Itoa(result.ExitCode)).Observe(float64(duration.Seconds()))
	tm.logger.Info("done", "stdout", result.StdOut, "stderr", result.StdErr)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(result)
}

func (tm *TaskManager) ConfigureRoutes(r chi.Router) {
	taskRouter := func(r chi.Router) {
		if tm.requestRateLimiter != nil {
			r.Use(tm.rateLimiter)
		}
		if tm.taskSemaphore != nil {
			r.Use(tm.concurrentExecutionLimiter)
		}
		r.Use(tm.requestTimeout)
		r.HandleFunc("/", tm.runTask)
	}

	for key, hashPath := range tm.config.WebhookSecretFiles {
		binaryHash, err := os.ReadFile(hashPath)
		if err != nil {
			panic(err)
		}
		hash := strings.TrimSpace(string(binaryHash))

		r.With(middleware.WithValue(keyNameContextKey, key)).Route("/wh/"+string(hash), taskRouter)
		tm.logger.Debug("Configured webhook", "key", key)
	}

	if tm.config.APIKeyNames != nil {
		tm.logger.Debug("Configuring API key based route")
		r.Route("/tasks/"+tm.taskName, func(r chi.Router) {
			r.Use(tm.authorize)
			r.Route("/", taskRouter)
		})
	}
}

type taskExecutionResult struct {
	TaskID   string `json:"task_id"`
	ExitCode int    `json:"exit_code"`
	StdOut   string `json:"stdout,omitempty"`
	StdErr   string `json:"stderr,omitempty"`
}
