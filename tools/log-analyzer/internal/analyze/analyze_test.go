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

func TestWorkspaceDiagnosesSemanticStateAndTraceIntegrity(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	writeFile(t, filepath.Join(input, "events.jsonl"), []byte(
		`{"schema_version":2,"timestamp":"2026-03-14T00:00:00Z","sequence":1,"app_session_id":"semantic","trace_id":"trace-semantic","span_id":"child","parent_span_id":"missing-parent","layer":"forwarder","event":"turn_started","capability":"agent","operation":"agent.turn","semantic_outcome":"started","implementation_state":"implemented","severity":"info"}`+"\n"+
			`{"schema_version":2,"timestamp":"2026-03-14T00:00:01Z","sequence":3,"app_session_id":"semantic","trace_id":"trace-semantic","layer":"forwarder","event":"tool_call_dispatch","capability":"tool","operation":"tool.execute","semantic_outcome":"started","implementation_state":"implemented","severity":"info","tool_call_id":"tool-1"}`+"\n"+
			`{"schema_version":2,"timestamp":"2026-03-14T00:00:02Z","sequence":3,"app_session_id":"semantic","trace_id":"trace-semantic","layer":"forwarder","event":"tool_call_canceled","capability":"tool","operation":"tool.execute","semantic_outcome":"canceled","implementation_state":"implemented","severity":"info","tool_call_id":"tool-1"}`+"\n"+
			`{"schema_version":2,"timestamp":"2026-03-14T00:00:03Z","sequence":2,"app_session_id":"semantic","trace_id":"trace-semantic","layer":"forwarder","event":"tool_call_result","capability":"tool","operation":"tool.execute","semantic_outcome":"succeeded","implementation_state":"implemented","severity":"info","tool_call_id":"tool-1","response_bytes":12}`+"\n"+
			`{"schema_version":2,"timestamp":"2026-03-14T00:00:04Z","sequence":4,"app_session_id":"semantic","trace_id":"trace-semantic","layer":"forwarder","event":"repository_call","capability":"repository","operation":"repository.index","status":"success","semantic_outcome":"compat_only","implementation_state":"compat","severity":"warning"}`+"\n"+
			`{"schema_version":2,"timestamp":"2026-03-14T00:00:05Z","sequence":5,"app_session_id":"semantic","trace_id":"trace-semantic","layer":"provider","event":"llm_summary","capability":"provider","operation":"provider.stream","semantic_outcome":"succeeded","implementation_state":"implemented","severity":"info"}`+"\n"+
			`{"schema_version":2,"timestamp":"2026-03-14T00:00:06Z","sequence":6,"app_session_id":"semantic","trace_id":"trace-semantic","layer":"runsse","event":"subscribe","capability":"agent","operation":"agent.stream","semantic_outcome":"started","implementation_state":"implemented","severity":"info"}`+"\n"+
			`{"schema_version":2,"timestamp":"2026-03-14T00:00:07Z","sequence":7,"app_session_id":"semantic","trace_id":"trace-semantic","layer":"forwarder","event":"unknown_terminal","capability":"agent","operation":"agent.unknown","semantic_outcome":"unknown","implementation_state":"implemented","severity":"info"}`+"\n"+
			`{"schema_version":2,"timestamp":"2026-03-14T00:00:08Z","sequence":8,"app_session_id":"semantic","layer":"forwarder","event":"orphan","capability":"agent","operation":"agent.orphan","semantic_outcome":"succeeded","implementation_state":"implemented","severity":"info"}`+"\n",
	))

	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetCurrent, []string{input}, load.Options{}); err != nil {
		t.Fatalf("load semantic input: %v", err)
	}
	if _, err := Workspace(ctx, ws, false); err != nil {
		t.Fatalf("Workspace() semantic diagnostics error = %v", err)
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	codes := make(map[string]bool)
	if err := ws.ForEachFinding(ctx, currentID, func(finding workspace.FindingRecord) error {
		codes[finding.Code] = true
		return nil
	}); err != nil {
		t.Fatalf("ForEachFinding() error = %v", err)
	}
	for _, code := range []string{
		"sequence_gap", "sequence_duplicate", "sequence_reversed", "parent_span_missing", "orphan_event",
		"continued_after_cancel", "capability_limitation", "semantic_outcome_gap",
		"technical_success_without_capability_success", "runsse_terminal_missing", "operation_terminal_missing", "insufficient_evidence",
	} {
		if !codes[code] {
			t.Fatalf("finding %s not produced: %#v", code, codes)
		}
	}
}

func TestWorkspaceBuildsPercentilesAndMultidimensionalBaseline(t *testing.T) {
	ctx := context.Background()
	current := t.TempDir()
	baseline := t.TempDir()
	writeProviderMetrics := func(path string, session string, durations []int) {
		var events []byte
		for index, duration := range durations {
			events = fmt.Appendf(events, `{"schema_version":2,"timestamp":"2026-03-14T00:00:%02dZ","sequence":%d,"app_session_id":"%s","project_id":"project-a","trace_id":"trace-%s-%d","layer":"provider","event":"llm_summary","capability":"provider","operation":"provider.stream","route":"/chat","execution_target":"provider-a","semantic_outcome":"succeeded","implementation_state":"implemented","severity":"info","duration_ms":%d,"request_bytes":10,"response_bytes":20,"fields":{"ttft_ms":%d}}`+"\n", index, index+1, session, session, index, duration, duration/2)
		}
		writeFile(t, filepath.Join(path, "events.jsonl"), events)
	}
	writeProviderMetrics(current, "current", []int{10, 20, 100})
	writeProviderMetrics(baseline, "baseline", []int{5, 10, 20})

	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetCurrent, []string{current}, load.Options{}); err != nil {
		t.Fatalf("load current metrics: %v", err)
	}
	if err := load.IntoWorkspace(ctx, ws, workspace.DatasetBaseline, []string{baseline}, load.Options{}); err != nil {
		t.Fatalf("load baseline metrics: %v", err)
	}
	if _, err := Workspace(ctx, ws, true); err != nil {
		t.Fatalf("Workspace() metrics error = %v", err)
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	metric, err := ws.DiagnosticMetric(ctx, currentID, "capability", "provider")
	if err != nil {
		t.Fatalf("DiagnosticMetric() error = %v", err)
	}
	if metric.CompletedCount != 3 || metric.DurationP50MS != 20 || metric.DurationP95MS != 100 || metric.DurationP99MS != 100 {
		t.Fatalf("unexpected duration metric: %+v", metric)
	}
	if metric.TTFTP50MS != 10 || metric.TTFTP95MS != 50 || metric.RequestBytes != 30 || metric.ResponseBytes != 60 {
		t.Fatalf("unexpected TTFT/byte metric: %+v", metric)
	}
	found := false
	if err := ws.ForEachDiagnosticComparison(ctx, func(comparison workspace.DiagnosticComparisonRecord) error {
		if comparison.Dimension == "capability" && comparison.Value == "provider" {
			found = true
			if comparison.CurrentCompleted != 3 || comparison.BaselineCompleted != 3 || comparison.DurationP95DeltaMS != 80 {
				t.Fatalf("unexpected diagnostic comparison: %+v", comparison)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("ForEachDiagnosticComparison() error = %v", err)
	}
	if !found {
		t.Fatal("capability baseline comparison not produced")
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
