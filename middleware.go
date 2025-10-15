package main

import (
	"context"
	"net/http"
	"strings"
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
