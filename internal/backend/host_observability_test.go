package backend

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"cursor/internal/logger"
	"cursor/internal/observability"
)

func TestLogObservabilityEventUsesEventSeverity(t *testing.T) {
	logger.Init()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	logObservabilityEvent(observability.Event{
		Layer:    "provider",
		Event:    "provider_response",
		Severity: observability.SeverityInfo,
		Status:   "completed",
	})
	if !strings.Contains(buf.String(), "level=INFO") {
		t.Fatalf("success observability log = %q", buf.String())
	}
	buf.Reset()
	logObservabilityEvent(observability.Event{
		Layer:    "provider",
		Event:    "provider_response",
		Severity: observability.SeverityWarning,
		Status:   "retrying",
	})
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("retry observability log = %q", buf.String())
	}
	buf.Reset()
	logObservabilityEvent(observability.Event{
		Layer:    "provider",
		Event:    "provider_response",
		Severity: observability.SeverityError,
		Status:   "error",
	})
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("error observability log = %q", buf.String())
	}
}
