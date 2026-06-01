package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ctxKey string

const ctxRequestID ctxKey = "request_id"

// StaticBearer returns middleware that validates the Authorization header
// against the configured static token using constant-time comparison.
func StaticBearer(token string) func(http.Handler) http.Handler {
	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authz := r.Header.Get("Authorization")
			if !strings.HasPrefix(authz, "Bearer ") {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"status": "error",
					"reply":  "Não autorizado.",
					"error":  map[string]string{"code": "UNAUTHORIZED", "details": "missing bearer"},
				})
				return
			}
			got := []byte(strings.TrimPrefix(authz, "Bearer "))
			if subtle.ConstantTimeCompare(got, expected) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"status": "error",
					"reply":  "Não autorizado.",
					"error":  map[string]string{"code": "UNAUTHORIZED", "details": "invalid bearer"},
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequestID injects a request id into the context for log correlation.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rid := r.Header.Get("X-Request-ID")
			if rid == "" {
				rid = uuid.NewString()
			}
			ctx := contextWithRequestID(r.Context(), rid)
			w.Header().Set("X-Request-ID", rid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AccessLog logs basic request info using slog.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(ww, r)
			logger.Info("http_request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFrom(r.Context())),
			)
		})
	}
}

// Recover catches panics, logs them and returns a generic 500 reply.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic_recovered",
						slog.Any("panic", rec),
						slog.String("stack", string(debug.Stack())),
						slog.String("path", r.URL.Path),
					)
					writeJSON(w, http.StatusInternalServerError, map[string]any{
						"status": "error",
						"reply":  "Tive um problema interno. Tente novamente em alguns segundos.",
						"error":  map[string]string{"code": "INTERNAL_ERROR"},
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
