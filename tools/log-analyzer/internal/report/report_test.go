package report

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cursor-log-analyzer/internal/analyze"
	"cursor-log-analyzer/internal/load"
	"cursor-log-analyzer/internal/workspace"
)

func TestDiagnosticBundleRemovesSensitiveContent(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	output := t.TempDir()
	secret := "sk-super-secret-value"
	writeFile(t, filepath.Join(input, "events.jsonl"), []byte(`{"schema_version":99,"timestamp":"2026-03-14T00:00:00Z","sequence":1,"app_session_id":"session-private","trace_id":"trace-private","conversation_id":"conversation-private","layer":"provider","event":"request_finished","route":"https://api.example.test/v1/users/1234567890123456?token=`+secret+`","execution_target":"provider","status":"error","error_category":"provider_error","fields":{"authorization":"Bearer `+secret+`","status_code":500}}`+"\n"))
	writeFile(t, filepath.Join(input, "manifest.json"), []byte(`{"schema_version":1,"app_session_id":"session-private","mode":"capture","status":"closed","started_at":"2026-03-14T00:00:00Z"}`))

	ws, err := workspace.Open(ctx, workspace.Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer ws.CloseAndRemove()
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetCurrent, []string{input}, load.Options{AllowUnknownSchema: true}); err != nil {
		t.Fatalf("IntoWorkspace() error = %v", err)
	}
	if _, err := analyze.Workspace(ctx, ws, false); err != nil {
		t.Fatalf("analyze workspace: %v", err)
	}
	staged, err := StageWorkspace(ctx, output, ws, false)
	if err != nil {
		t.Fatalf("StageWorkspace() error = %v", err)
	}
	if err := staged.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	reportPayload, err := os.ReadFile(filepath.Join(output, "report.json"))
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	var reportDocument struct {
		SchemaVersion     int                        `json:"schema_version"`
		DiagnosticMetrics []analyze.DiagnosticMetric `json:"diagnostic_metrics"`
	}
	if err := json.Unmarshal(reportPayload, &reportDocument); err != nil {
		t.Fatalf("decode report.json: %v", err)
	}
	if reportDocument.SchemaVersion != 1 || len(reportDocument.DiagnosticMetrics) == 0 {
		t.Fatalf("report compatibility or diagnostic metrics missing: %+v", reportDocument)
	}

	archive, err := zip.OpenReader(filepath.Join(output, "diagnostic-bundle.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var combined strings.Builder
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		combined.Write(payload)
	}
	text := combined.String()
	for _, forbidden := range []string{secret, "Bearer", input, "conversation-private", "payloads/"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("bundle contains forbidden value %q: %s", forbidden, text)
		}
	}
	for _, name := range []string{"report.json", "report.html", "diagnostic-bundle.zip"} {
		info, err := os.Stat(filepath.Join(output, name))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("permissions for %s = %04o", name, info.Mode().Perm())
		}
	}
}

func TestPublishRollbackRemovesNewFinalWhenLaterRenameFails(t *testing.T) {
	output := t.TempDir()
	staging := filepath.Join(output, ".log-analyzer-staging-test")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(staging, "report.json"), []byte(`{"ok":true}`))

	staged := &StagedReport{output: output, dir: staging}
	err := staged.Publish()
	if err == nil {
		t.Fatal("Publish() succeeded with incomplete staging directory")
	}
	if _, statErr := os.Stat(filepath.Join(output, "report.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rollback left new final report.json, stat error = %v", statErr)
	}
	if _, statErr := os.Stat(staging); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rollback did not clean staging directory, stat error = %v", statErr)
	}
}

func writeFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
