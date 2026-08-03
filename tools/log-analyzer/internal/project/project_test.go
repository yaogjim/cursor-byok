package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cursor-log-analyzer/internal/workspace"
)

func TestOpenStagesReportAndClosesWorkspace(t *testing.T) {
	input := t.TempDir()
	output := t.TempDir()
	writeFixture(t, input)

	var stages []Stage
	analysis, err := Open(context.Background(), OpenRequest{
		Inputs:  []string{input},
		TempDir: t.TempDir(),
		Observer: func(progress Progress) {
			stages = append(stages, progress.Stage)
		},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if summary := analysis.Summary(); summary.EventCount != 2 || summary.TraceCount != 1 {
		t.Fatalf("Summary() = %+v", summary)
	}
	staged, err := analysis.StageReport(context.Background(), output)
	if err != nil {
		t.Fatalf("StageReport() error = %v", err)
	}
	if err := analysis.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := staged.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	for _, name := range []string{"report.json", "report.html", "diagnostic-bundle.zip"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	want := []Stage{StageWorkspaceOpened, StageCurrentLoaded, StageAnalyzed, StageReportStaged, StageClosed}
	assertStages(t, stages, want)
	if err := analysis.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestProjectProvidesInteractiveQueriesAndSafePayloadRead(t *testing.T) {
	input := t.TempDir()
	writeFile(t, filepath.Join(input, "events.jsonl"), []byte(
		`{"schema_version":2,"timestamp":"2026-07-29T00:00:00Z","sequence":1,"app_session_id":"session","trace_id":"trace","layer":"tool","event":"result","capability":"tool","operation":"tool.result","direction":"proxy_to_cursor","semantic_outcome":"failed","implementation_state":"implemented","severity":"error","payload_ref":"payloads/0001.json"}`+"\n",
	))
	writeFile(t, filepath.Join(input, "manifest.json"), []byte(`{"schema_version":2,"app_session_id":"session","mode":"full","status":"closed","source_kind":"client","started_at":"2026-07-29T00:00:00Z","closed_at":"2026-07-29T00:00:01Z"}`))
	writeFile(t, filepath.Join(input, "app-20260729T000000.000000000Z-000001.log"), []byte("2026/07/29 00:00:00 ERR tool failed\n"))
	writeFile(t, filepath.Join(input, "payloads", "0001.json"), []byte(`{"detail":"sensitive"}`))

	analysis, err := Open(context.Background(), OpenRequest{Inputs: []string{input}, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	events, err := analysis.SearchEvents(context.Background(), workspace.DatasetCurrent, workspace.EventSearchRequest{Query: "severity:error capability:tool", Limit: 10})
	if err != nil {
		t.Fatalf("SearchEvents() error = %v", err)
	}
	if events.Total != 1 || len(events.Events) != 1 {
		t.Fatalf("events = %+v, want one result", events)
	}
	logs, err := analysis.SearchAppLogs(context.Background(), workspace.DatasetCurrent, workspace.AppLogSearchRequest{Keyword: "tool", Severity: "error"})
	if err != nil {
		t.Fatalf("SearchAppLogs() error = %v", err)
	}
	if logs.Total != 1 || len(logs.Lines) != 1 {
		t.Fatalf("logs = %+v, want one result", logs)
	}
	document, err := analysis.ReadEventPayload(context.Background(), workspace.DatasetCurrent, events.Events[0].IngestOrder, 1024)
	if err != nil {
		t.Fatalf("ReadEventPayload() error = %v", err)
	}
	if !document.Sensitive || !strings.Contains(string(document.Content), "sensitive") {
		t.Fatalf("unexpected payload document: %+v", document)
	}
	if err := analysis.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := analysis.SearchEvents(context.Background(), workspace.DatasetCurrent, workspace.EventSearchRequest{}); err == nil {
		t.Fatal("SearchEvents() succeeded after close")
	}
}

func TestOpenCleansWorkspaceWhenInputFails(t *testing.T) {
	input := t.TempDir()
	writeFile(t, filepath.Join(input, "events.jsonl"), []byte("not-json\n"))
	tempRoot := t.TempDir()

	if _, err := Open(context.Background(), OpenRequest{Inputs: []string{input}, TempDir: tempRoot}); err == nil {
		t.Fatal("Open() accepted malformed input")
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace was not cleaned: %v", entries)
	}
}

func writeFixture(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "events.jsonl"), []byte(
		`{"schema_version":1,"timestamp":"2026-07-29T00:00:00Z","sequence":1,"app_session_id":"session","trace_id":"trace","layer":"backend","event":"request_started","execution_target":"local_runtime"}`+"\n"+
			`{"schema_version":1,"timestamp":"2026-07-29T00:00:01Z","sequence":2,"app_session_id":"session","trace_id":"trace","layer":"backend","event":"request_finished","execution_target":"local_runtime","status":"success","duration_ms":1000}`+"\n",
	))
	writeFile(t, filepath.Join(root, "manifest.json"), []byte(`{"schema_version":1,"app_session_id":"session","mode":"basic","status":"closed","started_at":"2026-07-29T00:00:00Z","closed_at":"2026-07-29T00:00:01Z"}`))
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

func assertStages(t *testing.T, got []Stage, want []Stage) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("stages[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
