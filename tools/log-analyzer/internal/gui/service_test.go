package gui

import (
	"context"
	"errors"
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
