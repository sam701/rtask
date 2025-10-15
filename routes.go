package main

import (
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func (tm *TaskManager) configureWebhooks(r chi.Router) {
	webhookRouter := func(key, hash string) {
		r.With(middleware.WithValue(keyNameContextKey, key)).Post("/wh/"+string(hash), tm.handleRunTask)
		tm.logger.Debug("Configured webhook", "key", key)
	}

	for key, hash := range tm.config.WebhookSecrets {
		webhookRouter(key, hash)
	}
	for key, hashPath := range tm.config.WebhookSecretFiles {
		binaryHash, err := os.ReadFile(hashPath)
		if err != nil {
			panic(err)
		}
		hash := strings.TrimSpace(string(binaryHash))
		webhookRouter(key, hash)
	}
}

func (tm *TaskManager) ConfigureRoutes(r chi.Router) {
	tm.configureWebhooks(r)

	if tm.config.APIKeyNames != nil {
		tm.logger.Debug("Configuring API key based route")
		r.Route("/tasks/"+tm.taskName, func(r chi.Router) {
			r.Use(tm.authorize)
			r.Post("/", tm.handleRunTask)
			r.Get("/{taskID}", tm.getTaskResult)
		})
	}
}
