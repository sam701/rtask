package main

import (
	"log/slog"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
)

const (
	labelApiKeyUsed = "api_key_used"
	labelStatus     = "status"
)

type TaskHandler struct {
	name    string
	config  *Task
	limiter *rate.Limiter
	mutex   sync.Mutex

	// API key -> key name
	allowdApiKeys map[string]string
	logger        *slog.Logger

	histTaskDuration *prometheus.HistogramVec
	counterRejection *prometheus.CounterVec
}

// keyMap: name -> key
func NewTaskHandler(name string, config *Task, keyMap map[string]string) *TaskHandler {
	logger := slog.With("handler", name)

	keys := make(map[string]string)
	for _, keyName := range config.ApiKeyNames {
		key := keyMap[keyName]
		if key != "" {
			logger.Debug("added key", "name", keyName)
			keys[key] = keyName
		}
	}
	if len(keys) == 0 {
		logger.Warn("no API keys configured")
	}

	rateLimit := 0.5
	if config.RateLimit > 0 {
		rateLimit = config.RateLimit
	}

	logger.Info("added handler", "rate-limit", rateLimit)
	return &TaskHandler{
		name:          name,
		config:        config,
		limiter:       rate.NewLimiter(rate.Limit(rateLimit), 1),
		allowdApiKeys: keys,
		logger:        logger,

		histTaskDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: "task_duration_seconds",
				Help: "Duration of task execution",
				ConstLabels: prometheus.Labels{
					"handler": name,
				},
			},
			[]string{labelApiKeyUsed, labelStatus},
		),
		counterRejection: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "task_rejection_total",
			Help: "Total number of rejected requests",
			ConstLabels: prometheus.Labels{
				"handler": name,
			},
		}, []string{"reason"}),
	}
}

func (h *TaskHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-API-Token")
	apiKeyName := h.allowdApiKeys[token]
	if apiKeyName == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.counterRejection.WithLabelValues("unauthorized").Inc()
		return
	}

	if !h.limiter.Allow() {
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		h.counterRejection.WithLabelValues("limit_exceeded").Inc()
		return
	}

	h.mutex.Lock()
	defer h.mutex.Unlock()

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
		h.histTaskDuration.WithLabelValues(apiKeyName, status).Observe(float64(time.Now().Sub(startTime).Seconds()))
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
				h.histTaskDuration.WithLabelValues(apiKeyName, status).Observe(float64(time.Now().Sub(startTime).Seconds()))
			}()
		}
	}
}
