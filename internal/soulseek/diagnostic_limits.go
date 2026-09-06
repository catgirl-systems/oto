package soulseek

import (
	"github.com/catgirl-systems/oto/internal/diagnostics"
	"log/slog"
)

func firstLogger(loggers []*slog.Logger) *slog.Logger {
	if len(loggers) > 0 {
		return loggers[0]
	}
	return nil
}
func logLimit(logger *slog.Logger, name string, limit, actual uint64) {
	diagnostics.Event(logger, slog.LevelWarn, "browse_limit_rejected", nil, slog.String("limit_name", name), slog.Uint64("limit", limit), slog.Uint64("actual", actual))
}
