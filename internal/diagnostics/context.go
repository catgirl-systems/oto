package diagnostics

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
)

type loggerKey struct{}

var ids atomic.Uint64
var quiet = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.Level(100)}))

// WithLogger links a daemon operation to its network work without wire changes.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}
func FromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	if fallback != nil {
		return fallback
	}
	return quiet
}
func NextID(prefix string) string { return fmt.Sprintf("%s-%d", prefix, ids.Add(1)) }

// Event never incorporates arbitrary error text in diagnostic output.
func Event(logger *slog.Logger, level slog.Level, event string, err error, attrs ...slog.Attr) {
	if logger == nil || !logger.Enabled(context.Background(), level) {
		return
	}
	attrs = append(attrs, SafeError(err)...)
	logger.LogAttrs(context.Background(), level, event, attrs...)
}
