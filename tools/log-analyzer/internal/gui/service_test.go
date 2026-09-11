package gui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor-log-analyzer/internal/project"
	"cursor-log-analyzer/internal/savedquery"
)

func TestCloseProjectCancelsOpenInProgress(t *testing.T) {
	service, err := NewService(nil, filepath.Join(t.TempDir(), "saved-queries.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	started := make(chan struct{})
	service.openProject = func(ctx context.Context, _ project.OpenRequest) (*project.Project, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	openResult := make(chan error, 1)
	go func() {
		_, openErr := service.OpenProject(OpenRequest{Input: "/logs"})
		openResult <- openErr
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("OpenProject() did not start")
	}
	if err := service.CloseProject(); err != nil {
		t.Fatalf("CloseProject() error = %v", err)
	}
	select {
	case openErr := <-openResult:
		if !errors.Is(openErr, context.Canceled) {
			t.Fatalf("OpenProject() error = %v, want context canceled", openErr)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseProject() did not cancel OpenProject()")
	}
}

func TestServiceInitializeOpensDefaultClientLogs(t *testing.T) {
	service, err := NewService(nil, filepath.Join(t.TempDir(), "saved-queries.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	fixture := filepath.Join("..", "..", "testdata", "schema-v1")
	service.defaultInputPath = func() (string, error) { return fixture, nil }

	initialization := service.Initialize()
	if initialization.Warning != "" {
		t.Fatalf("Initialize() warning = %q", initialization.Warning)
	}
	if !initialization.State.Opened || initialization.State.Summary.EventCount != 6 {
		t.Fatalf("Initialize() state = %+v", initialization.State)
	}
	if initialization.DefaultInput != fixture {
		t.Fatalf("Initialize() default input = %q, want %q", initialization.DefaultInput, fixture)
	}
	if err := service.CloseProject(); err != nil {
		t.Fatalf("CloseProject() error = %v", err)
	}
}

func TestServiceInitializeFallsBackWhenDefaultLogsAreMissing(t *testing.T) {
	service, err := NewService(nil, filepath.Join(t.TempDir(), "saved-queries.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing", "logs")
	service.defaultInputPath = func() (string, error) { return missing, nil }

	initialization := service.Initialize()
	if initialization.State.Opened {
		t.Fatalf("Initialize() unexpectedly opened state: %+v", initialization.State)
	}
	if initialization.DefaultInput != missing {
		t.Fatalf("Initialize() default input = %q, want %q", initialization.DefaultInput, missing)
	}
	if !strings.Contains(initialization.Warning, "尚不存在") {
		t.Fatalf("Initialize() warning = %q", initialization.Warning)
	}
}

func TestDefaultClientLogsPathMatchesClientContract(t *testing.T) {
	path, err := DefaultClientLogsPath()
	if err != nil {
		t.Fatalf("DefaultClientLogsPath() error = %v", err)
	}
	wantSuffix := filepath.Join(clientDataDirName, clientLogsDirName)
	if !strings.HasSuffix(path, wantSuffix) {
		t.Fatalf("DefaultClientLogsPath() = %q, want suffix %q", path, wantSuffix)
	}
}

func TestServiceOpensSearchesAndClosesProject(t *testing.T) {
	service, err := NewService(nil, filepath.Join(t.TempDir(), "saved-queries.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	fixture := filepath.Join("..", "..", "testdata", "schema-v1")
	state, err := service.OpenProject(OpenRequest{Input: fixture})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	if !state.Opened || state.Summary.EventCount != 6 || state.Summary.TraceCount != 1 {
		t.Fatalf("unexpected state: %+v", state)
	}
	events, err := service.SearchEvents(EventRequest{Limit: 2, Descending: true})
	if err != nil {
		t.Fatalf("SearchEvents() error = %v", err)
	}
	if len(events.Events) != 2 || events.Total != 6 {
		t.Fatalf("unexpected event page: %+v", events)
	}
	metrics, err := service.ListDiagnosticMetrics("", 10)
	if err != nil {
		t.Fatalf("ListDiagnosticMetrics() error = %v", err)
	}
	if metrics.Total == 0 || len(metrics.Metrics) == 0 {
		t.Fatalf("diagnostic metrics missing: %+v", metrics)
	}
	stored, err := service.SaveQuery(savedQuery("errors", "severity:error"))
	if err != nil {
		t.Fatalf("SaveQuery() error = %v", err)
	}
	if stored.ID == "" || len(service.ListSavedQueries()) != 1 {
		t.Fatalf("saved query missing: %+v", stored)
	}
	if err := service.CloseProject(); err != nil {
		t.Fatalf("CloseProject() error = %v", err)
	}
	if service.GetState().Opened {
		t.Fatal("service remained open")
	}
	if _, err := service.SearchEvents(EventRequest{}); err == nil {
		t.Fatal("SearchEvents() succeeded after close")
	}
}

func savedQuery(name string, dsl string) savedquery.Query {
	return savedquery.Query{Name: name, DSL: dsl}
}

// TestServiceSummaryCarriesLoaderWarnings proves the existing overview summary
// surfaces loader completeness warnings to the frontend. The fixture is a
// diagnostics-only shard, which the loader flags as partial material; the raw
// session id used in the fixture must not appear in the warning text.
func TestServiceSummaryCarriesLoaderWarnings(t *testing.T) {
	service, err := NewService(nil, filepath.Join(t.TempDir(), "saved-queries.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	input := t.TempDir()
	diagnostics := filepath.Join(input, "logs", "diagnostics")
	if err := os.MkdirAll(diagnostics, 0o700); err != nil {
		t.Fatalf("mkdir diagnostics: %v", err)
	}
	event := `{"schema_version":2,"timestamp":"2026-09-10T00:00:00Z","sequence":1,"app_session_id":"app-gui-partial","trace_id":"trace-gui-partial","layer":"provider","event":"provider_terminal","severity":"error","status":"error","fields":{"error_summary":"upstream unavailable"}}`
	if err := os.WriteFile(filepath.Join(diagnostics, "diagnostics-gui.jsonl"), []byte(event+"\n"), 0o600); err != nil {
		t.Fatalf("write diagnostics shard: %v", err)
	}

	state, err := service.OpenProject(OpenRequest{Input: input})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	if !state.Opened || state.Summary.EventCount != 1 {
		t.Fatalf("unexpected state: %+v", state)
	}
	if len(state.Summary.Warnings) == 0 {
		t.Fatalf("summary carried no loader warnings: %+v", state.Summary)
	}
	joined := strings.Join(state.Summary.Warnings, "\n")
	if !strings.Contains(joined, "材料不完整") {
		t.Fatalf("summary warnings missing partial-material text: %#v", state.Summary.Warnings)
	}
	if strings.Contains(joined, "app-gui-partial") {
		t.Fatalf("summary warnings leaked a raw session id: %#v", state.Summary.Warnings)
	}
	if err := service.CloseProject(); err != nil {
		t.Fatalf("CloseProject() error = %v", err)
	}
}
