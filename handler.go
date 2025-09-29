package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
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

type TaskManager struct {
	taskName string
	config   *Task

	requestRateLimiter *rate.Limiter
	taskSemaphore      chan struct{}

	keyVerifier *KeyVerifier
	logger      *slog.Logger

	histTaskDuration *prometheus.HistogramVec
	counterRejection *prometheus.CounterVec

	taskHistory      map[string]*taskExecutionHistoryItem
	taskHistoryMutex sync.RWMutex
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

	tm := &TaskManager{
		taskName:           name,
		config:             config,
		requestRateLimiter: rateLimiter,
		taskSemaphore:      taskSemaphore,
		keyVerifier:        keyVerifier,
		logger:             logger,

		histTaskDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "task_duration_seconds",
				Help:    "Duration of task execution",
				Buckets: config.DurationHistogramBuckets,
				ConstLabels: prometheus.Labels{
					"handler": name,
				},
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
		taskHistory: make(map[string]*taskExecutionHistoryItem),
	}

	// Start cleanup goroutine
	go tm.cleanupHistory()

	return tm, nil
}

func (tm *TaskManager) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")

		if authHeader == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			tm.counterRejection.WithLabelValues("missing_key").Inc()
			return
		}

		if !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			tm.counterRejection.WithLabelValues("invalid_header").Inc()
			return
		}

		strKey := authHeader[7:]
		verificationResult := tm.keyVerifier.Verify(strKey)
		if !verificationResult.Success {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			tm.counterRejection.WithLabelValues(verificationResult.FailureReason).Inc()
			return
		}

		ctx := context.WithValue(r.Context(), keyNameContextKey, verificationResult.KeyName)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (tm *TaskManager) concurrentExecutionLimiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case tm.taskSemaphore <- struct{}{}:
			tm.logger.Debug("semaphore enter")
			defer func() {
				<-tm.taskSemaphore
				tm.logger.Debug("semaphore exit")
			}()
		default:
			http.Error(w, "Max concurrent tasks reached", http.StatusTooManyRequests)
			tm.counterRejection.WithLabelValues("max_concurrent").Inc()
			return
		}

		next.ServeHTTP(w, r)
	})
}
func (tm *TaskManager) rateLimiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tm.requestRateLimiter.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			tm.counterRejection.WithLabelValues("limit_exceeded").Inc()
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (tm *TaskManager) requestTimeout(next http.Handler) http.Handler {
	executionTimeout := tm.config.ExecutionTimeoutSeconds
	if executionTimeout <= 0 {
		executionTimeout = 30
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(executionTimeout)*time.Second)
		defer cancel()

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func (tm *TaskManager) cleanupHistory() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		tm.taskHistoryMutex.Lock()
		cutoff := time.Now().Add(-24 * time.Hour)

		// Remove items older than 24 hours
		for id, item := range tm.taskHistory {
			if item.StartedAt.Before(cutoff) {
				delete(tm.taskHistory, id)
			}
		}

		tm.taskHistoryMutex.Unlock()
	}
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

	status := "success"
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
		status = "timeout"
	} else if err != nil {
		tm.logger.Warn("failed to run the task", "error", err)
		status = "failure"
		if exitError, ok := err.(*exec.ExitError); ok {
			if waitStatus, ok := exitError.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = waitStatus.ExitStatus()
			}
		}
	}

	keyName := r.Context().Value(keyNameContextKey).(string)
	tm.histTaskDuration.WithLabelValues(keyName, status).Observe(float64(duration.Seconds()))
	tm.logger.Info("done", "status", status, "stdout", result.StdOut, "stderr", result.StdErr)

	// Store in history
	historyItem := &taskExecutionHistoryItem{
		KeyName:   keyName,
		StartedAt: startTime,
		Duration:  duration,
		Result:    result,
	}

	tm.taskHistoryMutex.Lock()
	tm.taskHistory[executionID] = historyItem
	tm.taskHistoryMutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(result)
}

func (tm *TaskManager) ConfigureRoutes(r chi.Router) {
	r.Route("/tasks/"+tm.taskName, func(r chi.Router) {
		r.Use(tm.authorize)

		r.Get("/status/{executionID}", tm.getStatus)

		r.Route("/", func(r chi.Router) {
			if tm.requestRateLimiter != nil {
				r.Use(tm.rateLimiter)
			}
			if tm.taskSemaphore != nil {
				r.Use(tm.concurrentExecutionLimiter)
			}
			r.Use(tm.requestTimeout)
			r.Post("/", tm.runTask)
		})
	})
}

func (tm *TaskManager) getStatus(w http.ResponseWriter, r *http.Request) {
	executionID := chi.URLParam(r, "executionID")

	tm.taskHistoryMutex.RLock()
	historyItem, exists := tm.taskHistory[executionID]
	tm.taskHistoryMutex.RUnlock()

	if !exists {
		http.Error(w, "Execution not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(historyItem)
}

type taskExecutionResult struct {
	TaskID   string `json:"task_id"`
	ExitCode int    `json:"exit_code"`
	StdOut   string `json:"stdout,omitempty"`
	StdErr   string `json:"stderr,omitempty"`
}

type taskExecutionHistoryItem struct {
	KeyName   string               `json:"key_name"`
	StartedAt time.Time            `json:"started_at"`
	Duration  time.Duration        `json:"duration"`
	Result    *taskExecutionResult `json:"result"`
}
