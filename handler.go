package main

import (
	"encoding/base64"
	"fmt"
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

	// API key name -> raw hash
	allowdApiKeys map[string][]byte
	logger        *slog.Logger

	histTaskDuration *prometheus.HistogramVec
	counterRejection *prometheus.CounterVec
}

// keyMap: name -> key
func NewTaskHandler(name string, config *Task, keyMap map[string]string) (*TaskHandler, error) {
	logger := slog.With("handler", name)

	keys := make(map[string][]byte)
	for _, keyName := range config.ApiKeyNames {
		key := keyMap[keyName]
		if key == "" {
			return nil, fmt.Errorf("no such key (%s) in keymap", keyName)
		}
		logger.Debug("added key", "name", keyName)
		value, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			return nil, fmt.Errorf("failed to decode key (%s): %w", keyName, err)
		}
		keys[keyName] = value
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
	}, nil
}

func (h *TaskHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-API-Key")

	if key == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.counterRejection.WithLabelValues("missing_key").Inc()
		return
	}

	parsedKey, err := parseKey(key)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.counterRejection.WithLabelValues("invalid_key").Inc()
		return
	}

	keyHash := h.allowdApiKeys[parsedKey.Name]
	if keyHash == nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		h.counterRejection.WithLabelValues("unauthorized").Inc()
		return
	}

	if !verifyKey(parsedKey.Key, keyHash) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.counterRejection.WithLabelValues("invalid_hash").Inc()
		return
	}

	if !h.limiter.Allow() {
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		h.counterRejection.WithLabelValues("limit_exceeded").Inc()
		return
	}

	if !h.mutex.TryLock() {
		http.Error(w, "In progress", http.StatusTooManyRequests)
		h.counterRejection.WithLabelValues("locked").Inc()
		return
	}
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
		h.histTaskDuration.WithLabelValues(parsedKey.Name, status).Observe(float64(time.Since(startTime).Seconds()))
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
				h.histTaskDuration.WithLabelValues(parsedKey.Name, status).Observe(float64(time.Since(startTime).Seconds()))
			}()
		}
	}
}
