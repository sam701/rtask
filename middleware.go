package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

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
