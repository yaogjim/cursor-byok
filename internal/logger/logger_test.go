package logger

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestStdlibLogLevelKeepsSuccessAtInfo(t *testing.T) {
	if got := stdlibLogLevel("forwarder provider pass started request_id=abc model_call_id=def provider_pass=1"); got != slog.LevelInfo {
		t.Fatalf("success provider pass level = %v, want info", got)
	}
	if got := stdlibLogLevel("forwarder provider completion post failed request_id=abc err=boom"); got != slog.LevelWarn {
		t.Fatalf("failed provider completion level = %v, want warn", got)
	}
	if got := stdlibLogLevel("provider request error=timeout"); got != slog.LevelWarn {
		t.Fatalf("error message level = %v, want warn", got)
	}
	if got := stdlibLogLevel("runtime panic recovered"); got != slog.LevelError {
		t.Fatalf("panic message level = %v, want error", got)
	}
}

func TestStandardLogWriterUsesInfoForProviderPass(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if _, err := (standardLogWriter{}).Write([]byte("forwarder provider pass started request_id=abc model_call_id=def provider_pass=1")); err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	if !strings.Contains(output, "level=INFO") || !strings.Contains(output, "provider pass started") {
		t.Fatalf("success stdlib log = %q", output)
	}
	buf.Reset()
	if _, err := (standardLogWriter{}).Write([]byte("forwarder provider completion post failed err=boom")); err != nil {
		t.Fatal(err)
	}
	output = buf.String()
	if !strings.Contains(output, "level=WARN") || !strings.Contains(output, "failed") {
		t.Fatalf("failure stdlib log = %q", output)
	}
}
