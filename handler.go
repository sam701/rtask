package main

import (
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
)

const (
	labelAPIKeyUsed = "api_key_used"
	labelStatus     = "status"
)

type TaskHandler struct {
	taskName  string
	config    *Task
	limiter   *rate.Limiter
	taskMutex sync.Mutex

	keyVerifier *KeyVerifier
	logger      *slog.Logger

	histTaskDuration *prometheus.HistogramVec
	counterRejection *prometheus.CounterVec
}

func NewTaskHandler(name string, config *Task, keyStore *APIKeyStore) (*TaskHandler, error) {
	logger := slog.With("handler", name)

	if len(config.APIKeyNames) == 0 {
		logger.Warn("no API keys configured")
	}

	keyVerifier, err := keyStore.KeyVerifier(config.APIKeyNames)
	if err != nil {
		return nil, err
	}

	rateLimit := 0.5
	if config.RateLimit > 0 {
		rateLimit = config.RateLimit
	}

	logger.Info("added handler", "rate-limit", rateLimit)
	return &TaskHandler{
		taskName:    name,
		config:      config,
		limiter:     rate.NewLimiter(rate.Limit(rateLimit), 1),
		keyVerifier: keyVerifier,
		logger:      logger,

		histTaskDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: "task_duration_seconds",
				Help: "Duration of task execution",
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
	}, nil
}

func (h *TaskHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")

	if authHeader == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.counterRejection.WithLabelValues("missing_key").Inc()
		return
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.counterRejection.WithLabelValues("invalid_header").Inc()
		return
	}

	strKey := authHeader[7:]
	verificationResult := h.keyVerifier.Verify(strKey)
	if !verificationResult.Success {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.counterRejection.WithLabelValues(verificationResult.FailureReason).Inc()
		return
	}

	if !h.limiter.Allow() {
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		h.counterRejection.WithLabelValues("limit_exceeded").Inc()
		return
	}

	if !h.taskMutex.TryLock() {
		http.Error(w, "In progress", http.StatusTooManyRequests)
		h.counterRejection.WithLabelValues("locked").Inc()
		return
	}
	defer h.taskMutex.Unlock()

	startTime := time.Now()
	cmd := exec.Command(h.config.Command[0], h.config.Command[1:]...)
	cmd.Dir = h.config.Workdir
	cmd.Stdin = r.Body
	defer r.Body.Close()

	if h.config.Blocking {
		cmd.Stdout = w
		cmd.Stderr = w
		err := cmd.Run()
		status := "success"
		if err != nil {
			h.logger.Error("failed to run the task", "error", err)
			status = "failure"
		}
		h.histTaskDuration.WithLabelValues(verificationResult.KeyName, status).Observe(float64(time.Since(startTime).Seconds()))
	} else {
		err := cmd.Start()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			go func() {
				err := cmd.Wait()
				status := "success"
				if err != nil {
					h.logger.Error("failed to wait for task", "error", err)
					status = "failure"
				}
				h.histTaskDuration.WithLabelValues(verificationResult.KeyName, status).Observe(float64(time.Since(startTime).Seconds()))
			}()
		}
	}
}
