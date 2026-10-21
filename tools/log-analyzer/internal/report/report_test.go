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
		MitmDiagnostics   *analyze.MitmDiagnostics   `json:"mitm_diagnostics"`
	}
	if err := json.Unmarshal(reportPayload, &reportDocument); err != nil {
		t.Fatalf("decode report.json: %v", err)
	}
	if reportDocument.SchemaVersion != 1 || len(reportDocument.DiagnosticMetrics) == 0 || reportDocument.MitmDiagnostics == nil {
		t.Fatalf("report compatibility or diagnostic metrics missing: %+v", reportDocument)
	}
	htmlPayload, err := os.ReadFile(filepath.Join(output, "report.html"))
	if err != nil {
		t.Fatalf("read report.html: %v", err)
	}
	if !strings.Contains(string(htmlPayload), "已观测") || !strings.Contains(string(htmlPayload), "相关但未证实") || !strings.Contains(string(htmlPayload), "未知") {
		t.Fatalf("report.html missing evidence tiers: %s", htmlPayload)
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

func TestEventFromRecordKeepsSeverityAndCorrelation(t *testing.T) {
	record := workspace.EventRecord{
		DatasetID:         1,
		SourceFileID:      2,
		LineNumber:        3,
		IngestOrder:       4,
		TraceKey:          "trace-correlation",
		SchemaVersion:     2,
		AppSessionID:      "app-correlation",
		SubagentRunID:     "run-1",
		SubagentAttemptID: "attempt-2",
		SubagentAttemptNo: 2,
		Layer:             "provider",
		Event:             "provider_terminal",
		Severity:          "error",
		SafeFieldsJSON:    `{"error_summary":"upstream unavailable","retryable":true,"attempt":2,"max_attempts":2,"retry_suppression_reason":"budget_exhausted"}`,
	}
	event := eventFromRecord(record)
	if event.Severity != "error" {
		t.Fatalf("eventFromRecord() severity = %q, want error", event.Severity)
	}
	if event.SubagentRunID != "run-1" || event.SubagentAttemptID != "attempt-2" || event.SubagentAttemptNo != 2 {
		t.Fatalf("eventFromRecord() correlation = %q/%q/%d", event.SubagentRunID, event.SubagentAttemptID, event.SubagentAttemptNo)
	}
	safe, err := sanitizeEvent(event)
	if err != nil {
		t.Fatalf("sanitizeEvent() error = %v", err)
	}
	if safe.Severity != "error" {
		t.Fatalf("sanitized severity = %q, want error", safe.Severity)
	}
	if !strings.HasPrefix(safe.SubagentRunID, "id_") || !strings.HasPrefix(safe.SubagentAttemptID, "id_") || safe.SubagentAttemptNo != 2 {
		t.Fatalf("sanitized correlation leaked raw ids: %q/%q/%d", safe.SubagentRunID, safe.SubagentAttemptID, safe.SubagentAttemptNo)
	}
	if safe.Fields["error_summary"] != "upstream unavailable" || safe.Fields["retry_suppression_reason"] != "budget_exhausted" {
		t.Fatalf("sanitized fields dropped: %#v", safe.Fields)
	}
}

func TestDiagnosticBundleKeepsSeverityAndSafeFields(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	output := t.TempDir()
	writeFile(t, filepath.Join(input, "logs", "diagnostics", "diagnostics-bundle.jsonl"), []byte(`{"schema_version":2,"timestamp":"2026-09-10T00:00:00Z","sequence":11,"app_session_id":"app-bundle","trace_id":"trace-bundle","subagent_run_id":"run-bundle","subagent_attempt_id":"attempt-bundle","subagent_attempt_no":1,"layer":"provider","event":"model_call_final","severity":"error","status":"error","fields":{"error_summary":"upstream unavailable","provider_error_summary_type":"json_error","retry_decision":"retry","retryable":true,"attempt":2,"max_attempts":2,"retry_suppression_reason":"budget_exhausted","failure_stage":"response_body","business_outcome":"failed","model_call_final_status":"failed"}}`+"\n"))

	ws, err := workspace.Open(ctx, workspace.Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer ws.CloseAndRemove()
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetCurrent, []string{input}, load.Options{}); err != nil {
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
	// A diagnostics-only session must surface its partial-material warning
	// through the existing report.json warnings array, not just the dataset
	// stats.
	reportPayload, err := os.ReadFile(filepath.Join(output, "report.json"))
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	var reportDocument struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(reportPayload, &reportDocument); err != nil {
		t.Fatalf("decode report.json: %v", err)
	}
	warned := false
	for _, message := range reportDocument.Warnings {
		if strings.Contains(message, "材料不完整") {
			warned = true
		}
		if strings.Contains(message, "app-bundle") {
			t.Fatalf("report warning leaked a raw session id: %q", message)
		}
	}
	if !warned {
		t.Fatalf("report.json did not surface the partial-material warning: %#v", reportDocument.Warnings)
	}
	archive, err := zip.OpenReader(filepath.Join(output, "diagnostic-bundle.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var bundleEvents strings.Builder
	for _, file := range archive.File {
		if file.Name != "events.jsonl" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		bundleEvents.Write(payload)
	}
	lines := strings.Split(strings.TrimSpace(bundleEvents.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("bundle events.jsonl lines = %d, want 1: %s", len(lines), bundleEvents.String())
	}
	var event struct {
		Severity          string         `json:"severity"`
		SubagentRunID     string         `json:"subagent_run_id"`
		SubagentAttemptID string         `json:"subagent_attempt_id"`
		Fields            map[string]any `json:"fields"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("decode bundle event: %v", err)
	}
	if event.Severity != "error" {
		t.Fatalf("bundle severity = %q, want error", event.Severity)
	}
	if event.SubagentRunID == "run-bundle" || event.SubagentAttemptID == "attempt-bundle" || !strings.HasPrefix(event.SubagentRunID, "id_") {
		t.Fatalf("bundle correlation not pseudonymized: %q/%q", event.SubagentRunID, event.SubagentAttemptID)
	}
	for _, key := range []string{"error_summary", "provider_error_summary_type", "retry_decision", "retryable", "attempt", "max_attempts", "retry_suppression_reason", "failure_stage", "business_outcome", "model_call_final_status"} {
		if _, ok := event.Fields[key]; !ok {
			t.Fatalf("bundle dropped safe field %q: %#v", key, event.Fields)
		}
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
