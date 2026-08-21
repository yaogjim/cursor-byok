package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerRegistersProcessSink(t *testing.T) {
	previous := ProcessSink()
	t.Cleanup(func() { SetProcessSink(previous) })

	controller, err := NewController(t.TempDir(), Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	if ProcessSink() != controller {
		t.Fatal("NewController did not register process sink")
	}
	status := controller.Status()
	if !controller.RecordEvent(context.Background(), Event{Layer: "mitm", Event: "connect_decided"}) {
		t.Fatal("process sink rejected event")
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if ProcessSink() == controller {
		t.Fatal("Close left controller registered as process sink")
	}

	payload, err := os.ReadFile(filepath.Join(status.SessionPath, eventsFilename))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(payload), `"event":"connect_decided"`) {
		t.Fatalf("events.jsonl missing connect_decided: %s", payload)
	}
}

func TestClearProcessSinkIgnoresOtherSink(t *testing.T) {
	previous := ProcessSink()
	t.Cleanup(func() { SetProcessSink(previous) })

	first, err := NewRecorder(t.TempDir(), Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("first recorder: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := NewRecorder(t.TempDir(), Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("second recorder: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	SetProcessSink(first)
	ClearProcessSink(second)
	if ProcessSink() != first {
		t.Fatal("ClearProcessSink removed a different sink")
	}
	ClearProcessSink(first)
	if ProcessSink() != nil {
		t.Fatal("ClearProcessSink did not remove matching sink")
	}
}

func TestSetProcessSinkRejectsTypedNil(t *testing.T) {
	previous := ProcessSink()
	t.Cleanup(func() { SetProcessSink(previous) })

	var controller *Controller
	SetProcessSink(controller)
	if ProcessSink() != nil {
		t.Fatal("typed-nil controller was stored as process sink")
	}
}

func TestProcessSinkWritesSanitizedJSONL(t *testing.T) {
	previous := ProcessSink()
	t.Cleanup(func() { SetProcessSink(previous) })

	recorder, err := NewRecorder(t.TempDir(), Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	SetProcessSink(recorder)
	status := recorder.Status()
	if !ProcessSink().Record(context.Background(), Capture{
		Event: Event{
			Layer:  "mitm",
			Event:  "tls_handshake_failed",
			Fields: map[string]any{"authorization": "Bearer secret-token", "host": "api2.cursor.sh"},
		},
	}) {
		t.Fatal("process sink rejected event")
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	payload, err := os.ReadFile(filepath.Join(status.SessionPath, eventsFilename))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	text := string(payload)
	if strings.Contains(text, "secret-token") {
		t.Fatalf("events.jsonl leaked secret: %s", text)
	}
	var event Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.Event != "tls_handshake_failed" || event.Layer != "mitm" {
		t.Fatalf("event = %+v", event)
	}
}
