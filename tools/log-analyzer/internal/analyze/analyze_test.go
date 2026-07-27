package analyze

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"cursor-log-analyzer/internal/load"
	"cursor-log-analyzer/internal/workspace"
)

func TestSchemaV1FixtureReconstructsClosedTrace(t *testing.T) {
	ctx := context.Background()
	fixture := filepath.Join("..", "..", "testdata", "schema-v1")
	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetCurrent, []string{fixture}, load.Options{}); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	result, err := Workspace(ctx, ws, false)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}
	if result.EventCount != 6 || result.TraceCount != 1 {
		t.Fatalf("unexpected fixture summary: %+v", result)
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	if err := ws.ForEachFinding(ctx, currentID, func(finding workspace.FindingRecord) error {
		if finding.Code == "missing_terminal" || finding.Code == "session_not_closed" {
			t.Fatalf("closed fixture reported as incomplete: %+v", finding)
		}
		return nil
	}); err != nil {
		t.Fatalf("ForEachFinding() error = %v", err)
	}
}

func TestWorkspaceFindsMissingTerminalOrphanAndComparesTargets(t *testing.T) {
	ctx := context.Background()
	current := t.TempDir()
	baseline := t.TempDir()
	writeFile(t, filepath.Join(current, "events.jsonl"), []byte(
		`{"schema_version":1,"timestamp":"2026-03-14T00:00:00Z","sequence":1,"app_session_id":"current","trace_id":"trace-current","layer":"backend","event":"request_started","route":"/run","execution_target":"local_runtime"}`+"\n"+
			`{"schema_version":1,"timestamp":"2026-03-14T00:00:01Z","sequence":2,"app_session_id":"current","trace_id":"trace-current","layer":"provider","event":"llm_request","model_call_id":"call-1","execution_target":"provider"}`+"\n"+
			`{"schema_version":1,"timestamp":"2026-03-14T00:00:02Z","sequence":3,"app_session_id":"current","layer":"backend","event":"request_finished","execution_target":"local_runtime","status":"error","error_category":"boom","duration_ms":30000}`+"\n",
	))
	writeFile(t, filepath.Join(baseline, "events.jsonl"), []byte(
		`{"schema_version":1,"timestamp":"2026-03-14T00:00:00Z","sequence":1,"app_session_id":"baseline","trace_id":"trace-baseline","layer":"backend","event":"request_started","route":"/run","execution_target":"local_runtime"}`+"\n"+
			`{"schema_version":1,"timestamp":"2026-03-14T00:00:01Z","sequence":2,"app_session_id":"baseline","trace_id":"trace-baseline","layer":"backend","event":"request_finished","route":"/run","execution_target":"local_runtime","status":"ok","duration_ms":100}`+"\n",
	))

	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetCurrent, []string{current}, load.Options{}); err != nil {
		t.Fatalf("load current: %v", err)
	}
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetBaseline, []string{baseline}, load.Options{}); err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	result, err := Workspace(ctx, ws, true)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}
	if result.TraceCount != 2 || result.FindingCount == 0 {
		t.Fatalf("unexpected summary: %+v", result)
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	codes := make(map[string]bool)
	if err := ws.ForEachFinding(ctx, currentID, func(finding workspace.FindingRecord) error {
		codes[finding.Code] = true
		return nil
	}); err != nil {
		t.Fatalf("ForEachFinding() error = %v", err)
	}
	for _, code := range []string{"missing_terminal", "request_error", "slow_stage"} {
		if !codes[code] {
			t.Fatalf("finding %s not produced: %#v", code, codes)
		}
	}
	comparisonCount, err := ws.CountRows(ctx, "comparisons", currentID)
	if err != nil {
		t.Fatalf("CountRows(comparisons) error = %v", err)
	}
	if comparisonCount == 0 {
		t.Fatal("baseline comparison not produced")
	}
}

func TestWorkspaceFinalizesHighCardinalityTraceWithPagedScratch(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	count := stateFlushLimit + 3
	var events []byte
	for index := 0; index < count; index++ {
		events = fmt.Appendf(events, `{"schema_version":1,"timestamp":"2026-03-14T00:00:00Z","sequence":%d,"app_session_id":"session-high","trace_id":"trace-high","layer":"backend","event":"request_started","route":"/route-%d","execution_target":"target-%d"}`+"\n", index+1, index, index)
	}
	writeFile(t, filepath.Join(input, "events.jsonl"), events)

	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetCurrent, []string{input}, load.Options{BatchEventLimit: 257}); err != nil {
		t.Fatalf("load high cardinality input: %v", err)
	}
	result, err := Workspace(ctx, ws, false)
	if err != nil {
		t.Fatalf("Workspace() high cardinality error = %v", err)
	}
	if result.TraceCount != 1 || result.FindingCount != int64(count) {
		t.Fatalf("high cardinality summary = %+v, want 1 trace and %d findings", result, count)
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	pairs, err := ws.ListTracePairStates(ctx, currentID, "trace-high", "", 1)
	if err != nil {
		t.Fatalf("ListTracePairStates() error = %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("trace scratch was not cleaned: %#v", pairs)
	}
}

func openWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Open(context.Background(), workspace.Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return ws
}

func mustDatasetID(t *testing.T, ws *workspace.Workspace, kind workspace.DatasetKind) int64 {
	t.Helper()
	id, err := ws.DatasetID(context.Background(), kind)
	if err != nil {
		t.Fatalf("DatasetID(%s) error = %v", kind, err)
	}
	return id
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
