package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey string

const loggerKey ctxKey = "logger"

func LevelFromString(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithContext returns a new context with the provided logger
func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// FromContext gets a logger from context or returns a no-op logger
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if logger, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func New(level ...string) *slog.Logger {
	var slogLevel string
	if len(level) != 0 {
		slogLevel = level[0]
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: LevelFromString(slogLevel)})
	return slog.New(handler)
}
