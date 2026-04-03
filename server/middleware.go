package server

import (
	"log/slog"
	"net/http"

	"github.com/kdwils/magnetite/logger"
)

// WithLogger injects the provided logger into each request's context.
func WithLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := logger.WithContext(r.Context(), base)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
