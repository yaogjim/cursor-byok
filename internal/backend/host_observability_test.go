package backend

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"cursor/internal/logger"
	"cursor/internal/observability"
)

func TestLogObservabilityEventSummaryIsBoundedAndSanitized(t *testing.T) {
	logger.Init()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	longSummary := strings.Repeat("e", 900)
	logObservabilityEvent(observability.Event{
		Layer:           "provider",
		Event:           "provider_response",
		AppSessionID:    "session-123",
		Sequence:        7,
		Severity:        observability.SeverityWarning,
		SemanticOutcome: observability.OutcomeDegraded,
		Direction:       observability.DirectionProxyInternal,
		Status:          "retrying",
		Fields: map[string]any{
			"http_status":            503,
			"error_summary":          "auth header Authorization=Bearer sk-secret-token",
			"retry_decision":         "retry",
			"provider_error_summary": longSummary,
			"missing_blob_keys":      "secret-blob-key",
			"body":                   "raw request body",
		},
	})
	output := buf.String()
	for _, want := range []string{
		"app_session_id=session-123",
		"sequence=7",
		"severity=warning",
		"semantic_outcome=degraded",
		"direction=proxy_internal",
		"http_status=503",
		"retry_decision=retry",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("summary missing %q in %q", want, output)
		}
	}
	if strings.Contains(output, "sk-secret-token") {
		t.Fatalf("summary leaked a credential: %q", output)
	}
	if strings.Contains(output, "missing_blob_keys") || strings.Contains(output, "secret-blob-key") {
		t.Fatalf("summary retained a non-whitelisted key: %q", output)
	}
	if strings.Contains(output, "raw request body") {
		t.Fatalf("summary leaked a body field: %q", output)
	}
	if strings.Contains(output, strings.Repeat("e", observabilitySummaryMaxRunes+1)) {
		t.Fatalf("summary did not cap a long field to %d runes", observabilitySummaryMaxRunes)
	}
}

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
