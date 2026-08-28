package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursor/internal/observability"
)

type recordingCapture struct {
	events []observability.Event
}

func (recorder *recordingCapture) Record(ctx context.Context, capture observability.Capture) bool {
	correlation := observability.CorrelationFromContext(ctx)
	capture.Event.TraceID = correlation.TraceID
	capture.Event.SpanID = correlation.SpanID
	capture.Event.ParentSpanID = correlation.ParentSpanID
	capture.Event.HTTPRequestID = correlation.HTTPRequestID
	recorder.events = append(recorder.events, capture.Event)
	return true
}

func (recorder *recordingCapture) RecordEvent(ctx context.Context, event observability.Event) bool {
	return recorder.Record(ctx, observability.Capture{Event: event})
}

func TestObserveCorrelatesAndMeasuresRequest(t *testing.T) {
	recorder := &recordingCapture{}
	handler := New(
		Use(Observe(recorder), ServerContext(), ErrorEncoder()),
		POST("/test",
			Name("test_route"),
			ConnectUnary(),
			Local(func(ctx *Context) error {
				ctx.Writer.WriteHeader(http.StatusCreated)
				_, _ = ctx.Writer.Write([]byte("abc"))
				return nil
			}),
		),
	)
	request := httptest.NewRequest(http.MethodPost, "/test", nil)
	request.Header.Set(HeaderTraceID, "trace-from-mitm")
	request.Header.Set(HeaderParentSpanID, "mitm-span")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if len(recorder.events) != 2 {
		t.Fatalf("event count = %d, want 2", len(recorder.events))
	}
	started, finished := recorder.events[0], recorder.events[1]
	if started.TraceID != "trace-from-mitm" || finished.TraceID != started.TraceID {
		t.Fatalf("trace correlation mismatch: started=%+v finished=%+v", started, finished)
	}
	if started.ParentSpanID != "mitm-span" || started.SpanID == "" {
		t.Fatalf("span correlation mismatch: %+v", started)
	}
	if finished.Status != "ok" || finished.ResponseBytes != 3 {
		t.Fatalf("unexpected terminal event: %+v", finished)
	}
	if finished.ExecutionTarget != "local_runtime" || finished.Protocol != string(ProtocolConnectUnary) {
		t.Fatalf("unexpected route metadata: %+v", finished)
	}
}

func TestObserveRecordsTerminalWhenHandlerPanics(t *testing.T) {
	recorder := &recordingCapture{}
	handler := New(
		Use(Recover(), Observe(recorder), ServerContext(), ErrorEncoder()),
		POST("/panic",
			Name("panic_route"),
			HTTP(),
			Local(func(*Context) error {
				panic("unexpected handler failure")
			}),
		),
	)
	request := httptest.NewRequest(http.MethodPost, "/panic", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("event count = %d, want 2", len(recorder.events))
	}
	finished := recorder.events[1]
	if finished.Event != "request_finished" || finished.Status != "error" {
		t.Fatalf("unexpected terminal event: %+v", finished)
	}
	if finished.ErrorCategory != "handler_error" || finished.Fields["status_code"] != http.StatusBadGateway {
		t.Fatalf("unexpected panic metadata: %+v", finished)
	}
}

func TestObserveClassifiesExpected404WithoutChangingHTTP(t *testing.T) {
	tests := []struct {
		path       string
		route      string
		capability string
		operation  string
	}{
		{path: "/aiserver.v1.FileSyncService/UnknownOp", route: "file_sync", capability: "filesync", operation: "filesync.request"},
		{path: "/aiserver.v1.AiService/WriteGitCommitMessage", route: "ai_write_git_commit_message", capability: "git", operation: "git.request"},
		{path: "/aiserver.v1.RepositoryService/FastUpdateFileV2", route: "repository_fast_update_file_v2", capability: "repository", operation: "repository.request"},
	}
	for _, test := range tests {
		t.Run(test.capability, func(t *testing.T) {
			recorder := &recordingCapture{}
			handler := New(
				Use(Observe(recorder), ServerContext(), ErrorEncoder()),
				POST(test.path,
					Name(test.route),
					HTTP(),
					Local(func(ctx *Context) error {
						http.NotFound(ctx.Writer, ctx.Request)
						return nil
					}),
				),
			)
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("HTTP status = %d, want 404", response.Code)
			}
			if len(recorder.events) != 2 {
				t.Fatalf("event count = %d, want 2", len(recorder.events))
			}
			started, finished := recorder.events[0], recorder.events[1]
			if started.Capability != test.capability || started.Operation != test.operation {
				t.Fatalf("started classification = %+v", started)
			}
			if finished.Event != "request_finished" || finished.Status != "error" || finished.ErrorCategory != "client_error" {
				t.Fatalf("finished HTTP semantics changed: %+v", finished)
			}
			if finished.Capability != test.capability || finished.Operation != test.operation || finished.Severity != observability.SeverityWarning {
				t.Fatalf("finished classification = %+v", finished)
			}
			if finished.Fields["status_code"] != http.StatusNotFound || finished.Fields["path"] != test.path {
				t.Fatalf("query fields lost: %#v", finished.Fields)
			}
		})
	}
}

func TestObserveFileSync5xxStaysErrorWithoutChangingHTTP(t *testing.T) {
	recorder := &recordingCapture{}
	handler := New(
		Use(Observe(recorder), ServerContext(), ErrorEncoder()),
		POST("/aiserver.v1.FileSyncService/UnknownOp",
			Name("file_sync"),
			HTTP(),
			Local(func(ctx *Context) error {
				ctx.Writer.WriteHeader(http.StatusBadGateway)
				return nil
			}),
		),
	)
	request := httptest.NewRequest(http.MethodPost, "/aiserver.v1.FileSyncService/UnknownOp", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("HTTP status = %d, want 502", response.Code)
	}
	finished := recorder.events[1]
	if finished.Event != "request_finished" || finished.Status != "error" || finished.ErrorCategory != "server_error" {
		t.Fatalf("finished HTTP semantics changed: %+v", finished)
	}
	if finished.Capability != "filesync" || finished.Severity != "" {
		t.Fatalf("filesync 5xx must not be pre-labeled expected noise: %+v", finished)
	}
	if finished.Fields["status_code"] != http.StatusBadGateway {
		t.Fatalf("status_code lost: %#v", finished.Fields)
	}
}

func TestObserveUnknown404KeepsTransportCapability(t *testing.T) {
	recorder := &recordingCapture{}
	handler := New(
		Use(Observe(recorder), ServerContext(), ErrorEncoder()),
		POST("/mystery/endpoint",
			Name("mystery"),
			HTTP(),
			Local(func(ctx *Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
		),
	)
	request := httptest.NewRequest(http.MethodPost, "/mystery/endpoint", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("HTTP status = %d, want 404", response.Code)
	}
	finished := recorder.events[1]
	if finished.Capability != "unknown" || finished.Operation != "transport.request" || finished.Status != "error" {
		t.Fatalf("unknown 404 event = %+v", finished)
	}
	if finished.Severity != "" {
		t.Fatalf("unknown 404 should not be pre-labeled expected noise: %+v", finished)
	}
}
