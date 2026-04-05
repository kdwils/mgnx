package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/kdwils/mgnx/logger"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// WithLogger injects the provided logger into each request's context and logs
// each request at INFO level with method, path, status code, and latency.
func WithLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx := logger.WithContext(r.Context(), base)
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r.WithContext(ctx))
			base.InfoContext(ctx, "request",
				"method", r.Method,
				"path", r.URL.Path,
				"query", r.URL.RawQuery,
				"status", sw.status,
				"latency_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
