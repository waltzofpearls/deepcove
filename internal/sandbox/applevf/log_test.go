package applevf

import (
	"context"
	"log/slog"
)

// testLogger counts warnings, so a test can assert what an operator would see.
type countingHandler struct {
	onWarn func()
}

func testLogger(onWarn func()) *slog.Logger {
	return slog.New(&countingHandler{onWarn: onWarn})
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *countingHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelWarn {
		h.onWarn()
	}
	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *countingHandler) WithGroup(string) slog.Handler {
	return h
}
