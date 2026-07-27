package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRelayCorrelatesRequestAndStripsInternalHeaders(t *testing.T) {
	var receivedTraceID string
	var receivedParentSpanID string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedTraceID = request.Header.Get(headerTraceID)
		receivedParentSpanID = request.Header.Get(headerParentSpan)
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("ok"))
	}))
	defer upstream.Close()

	recorder, err := openRelayRecorder(t.TempDir())
	if err != nil {
		t.Fatalf("open relay recorder: %v", err)
	}
	handler := newServerAppWithRecorder(
		appConfig{Token: "relay-secret"},
		upstream.Client(),
		map[string]string{"/test": upstream.URL + "/test"},
		recorder,
	)
	request := httptest.NewRequest(http.MethodPost, "http://relay.local/test", nil)
	request.Header.Set(headerTraceID, "trace-123")
	request.Header.Set(headerParentSpan, "span-parent")
	request.Header.Set("x-request-id", "http-123")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if receivedTraceID != "" || receivedParentSpanID != "" {
		t.Fatalf("internal correlation headers leaked upstream: trace=%q parent=%q", receivedTraceID, receivedParentSpanID)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close relay recorder: %v", err)
	}

	file, err := os.Open(filepath.Join(recorder.dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	events := make([]relayEvent, 0, 2)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event relayEvent
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatalf("decode event: %v", err)
			}
			events = append(events, event)
		}
		if readErr != nil {
			if readErr != io.EOF {
				t.Fatalf("read events: %v", readErr)
			}
			break
		}
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	for _, event := range events {
		if event.TraceID != "trace-123" || event.ParentSpanID != "span-parent" {
			t.Fatalf("unexpected correlation: %+v", event)
		}
		if event.ExecutionTarget != "official_upstream" {
			t.Fatalf("execution target = %q", event.ExecutionTarget)
		}
	}
	if events[1].Status != "ok" || events[1].ResponseBytes != 2 {
		t.Fatalf("unexpected terminal event: %+v", events[1])
	}
}
