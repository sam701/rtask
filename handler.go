package main

import (
	"log/slog"
	"net/http"
	"os/exec"
	"sync"

	"golang.org/x/time/rate"
)

type TaskHandler struct {
	name          string
	config        *Task
	limiter       *rate.Limiter
	mutex         sync.Mutex
	allowdApiKeys map[string]bool
	logger        *slog.Logger
}

// keyMap: name -> key
func NewTaskHandler(name string, config *Task, keyMap map[string]string) *TaskHandler {
	logger := slog.With("handler", name)

	keys := make(map[string]bool)
	for _, keyName := range config.ApiKeyNames {
		key := keyMap[keyName]
		if key != "" {
			logger.Debug("added key", "name", keyName)
			keys[key] = true
		}
	}
	if len(keys) == 0 {
		logger.Warn("no API keys configured")
	}

	logger.Info("added handler")
	return &TaskHandler{
		name:          name,
		config:        config,
		limiter:       rate.NewLimiter(0.5, 1),
		allowdApiKeys: keys,
		logger:        logger,
	}
}

func (h *TaskHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check API token
	token := r.Header.Get("X-API-Token")
	h.logger.Debug("auth", "token", token)
	if !h.allowdApiKeys[token] {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !h.limiter.Allow() {
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	h.mutex.Lock()
	defer h.mutex.Unlock()

	cmd := exec.Command(h.config.Command[0], h.config.Command[1:]...)
	cmd.Dir = h.config.Workdir
	cmd.Stdin = r.Body
	defer r.Body.Close()

	if h.config.Blocking {
		cmd.Stdout = w
		cmd.Stderr = w
		err := cmd.Run()
		if err != nil {
			h.logger.Error("failed to run the task", "error", err)
		}
	} else {
		err := cmd.Start()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			go func() {
				err := cmd.Wait()
				if err != nil {
					h.logger.Error("failed to wait for task", "error", err)
				}
			}()
		}
	}

}
