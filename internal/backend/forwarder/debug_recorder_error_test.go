package forwarder

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDebugRecorderRuntimeErrorFieldAcrossModes(t *testing.T) {
	for _, mode := range []string{"basic", "full"} {
		t.Run(mode, func(t *testing.T) {
			capture := &debugRecorderTestCapture{}
			recorder := newDebugRecorder(t.TempDir(), nil, debugRecorderTestConfig(mode), capture)
			t.Cleanup(recorder.Close)

			recorder.LogRuntime(context.Background(), "request-1", "conversation-1", "dispatch_error", map[string]any{
				"kind":  "run",
				"error": "resume failed: status 500",
			})

			captured := capture.onlyCapture(t)
			if got := captured.Event.Fields["error"]; got != "resume failed: status 500" {
				t.Fatalf("fields.error = %#v, want error text in %s mode", got, mode)
			}
		})
	}
}

func TestDebugRecorderRuntimeErrorTextTruncatedAndRedacted(t *testing.T) {
	capture := &debugRecorderTestCapture{}
	recorder := newDebugRecorder(t.TempDir(), nil, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(recorder.Close)

	const secret = "super-secret-key"
	longBody := strings.Repeat("错", maxDebugErrorTextRunes+100)
	recorder.LogRuntime(context.Background(), "request-1", "conversation-1", "dispatch_error", map[string]any{
		"error": "api_key=" + secret + " " + longBody,
	})

	captured := capture.onlyCapture(t)
	text, ok := captured.Event.Fields["error"].(string)
	if !ok {
		t.Fatalf("fields.error type = %T, want string", captured.Event.Fields["error"])
	}
	if strings.Contains(text, secret) {
		t.Fatalf("error text persisted credential: %q", text)
	}
	if !strings.HasPrefix(text, "api_key=[REDACTED] ") {
		shown := text
		if len(shown) > 40 {
			shown = shown[:40]
		}
		t.Fatalf("error text prefix = %q, want redacted credential", shown)
	}
	sanitized := "api_key=[REDACTED] " + longBody
	wantMarker := fmt.Sprintf("... (truncated, total %d bytes)", len([]byte(sanitized)))
	if !strings.HasSuffix(text, wantMarker) {
		t.Fatalf("error text suffix = %q, want marker %q", text[len(text)-len(wantMarker):], wantMarker)
	}
	if strings.Contains(text, longBody) {
		t.Fatal("error text retained the untruncated body")
	}
	truncated := strings.TrimSuffix(text, wantMarker)
	if got := len([]rune(truncated)); got != maxDebugErrorTextRunes {
		t.Fatalf("truncated rune count = %d, want %d", got, maxDebugErrorTextRunes)
	}
}

func TestDebugRecorderRuntimeShortErrorTextUnchanged(t *testing.T) {
	capture := &debugRecorderTestCapture{}
	recorder := newDebugRecorder(t.TempDir(), nil, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(recorder.Close)

	recorder.LogRuntime(context.Background(), "request-1", "conversation-1", "dispatch_error", map[string]any{
		"error": "boom",
	})

	captured := capture.onlyCapture(t)
	if got := captured.Event.Fields["error"]; got != "boom" {
		t.Fatalf("fields.error = %#v, want %q", got, "boom")
	}
}
