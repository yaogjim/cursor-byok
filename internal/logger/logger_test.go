package logger

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cursor/internal/logsink"
)

// fileAppLogWriter adapts a RotatingFile to the AppLogWriter seam the
// observability controller satisfies in production.
type fileAppLogWriter struct {
	writer *logsink.RotatingFile
}

func (w *fileAppLogWriter) WriteAppLog(payload []byte) (int, error) {
	result, err := w.writer.Append(payload)
	return int(result.Length), err
}

func TestAppLogGateDefersUntilConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Init()
	appLogMu.Lock()
	previous := appLogWriter
	appLogWriter = nil
	appLogMu.Unlock()
	t.Cleanup(func() {
		appLogMu.Lock()
		appLogWriter = previous
		appLogMu.Unlock()
	})

	dir := t.TempDir()
	sink := &fileAppLogWriter{writer: logsink.NewRotatingFile(dir, logsink.RotationConfig{
		Prefix:    "app",
		Extension: ".log",
		MaxBytes:  1 << 20,
	})}

	// Before configuration the gate is a no-op, so no app file is created.
	gate := appLogGateWriter{}
	if n, err := gate.Write([]byte("before-config")); err != nil || n != len("before-config") {
		t.Fatalf("gate.Write before configure = (%d, %v)", n, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("app shards before configure = %v (err=%v), want none", entries, err)
	}

	// After configuration the logger routes records to the configured sink.
	ConfigureAppLogWriter(sink)
	Info("hello-from-logger")
	if err := sink.writer.Close(); err != nil {
		t.Fatalf("close app sink: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read app dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("app shards after configure = %d, want 1", len(entries))
	}
	payload, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read app shard: %v", err)
	}
	if !strings.Contains(string(payload), "hello-from-logger") {
		t.Fatalf("app shard missing record: %q", string(payload))
	}
}

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
