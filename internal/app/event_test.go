package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSlogEmitterLogsHookAtInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	emit := SlogEmitter(logger)

	emit(Event{Watch: "disk-root", Kind: "hook", Message: "fired"})

	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "watch=disk-root") {
		t.Fatalf("hook event not logged at info with watch attr: %q", out)
	}
}
