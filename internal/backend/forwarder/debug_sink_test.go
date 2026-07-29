package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cursor/internal/logsink"
)

func TestDebugSinkExternalizesProviderPayload(t *testing.T) {
	dir := t.TempDir()
	sink := &debugSink{
		writers:  make(map[string]*logsink.RotatingFile),
		payloads: make(map[string]*logsink.PayloadPackStore),
	}
	event := map[string]any{
		"request_id":      "req-1",
		"conversation_id": "conv-1",
		"event":           "llm_request",
		"payload": map[string]any{
			"body": strings.Repeat("large payload ", 4096),
		},
	}
	if err := sink.write(debugWriteTask{dir: dir, stream: "provider", event: event}); err != nil {
		t.Fatalf("write debug event: %v", err)
	}
	writer := sink.writers[debugWriterKey(dir, "provider")]
	if writer == nil {
		t.Fatal("provider event writer was not created")
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close provider writer: %v", err)
	}
	if store := sink.payloads[dir]; store != nil {
		if err := store.Close(); err != nil {
			t.Fatalf("close payload store: %v", err)
		}
	}

	eventFiles, err := os.ReadDir(filepath.Join(dir, "provider"))
	if err != nil {
		t.Fatalf("read provider event directory: %v", err)
	}
	if len(eventFiles) != 1 {
		t.Fatalf("expected one provider event segment, got %d", len(eventFiles))
	}
	body, err := os.ReadFile(filepath.Join(dir, "provider", eventFiles[0].Name()))
	if err != nil {
		t.Fatalf("read provider event: %v", err)
	}
	var recorded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(body))), &recorded); err != nil {
		t.Fatalf("decode provider event: %v", err)
	}
	if _, exists := recorded["payload"]; exists {
		t.Fatal("provider payload must not remain inline")
	}
	ref, ok := recorded["payload_ref"].(map[string]any)
	if !ok {
		t.Fatalf("provider event is missing payload_ref: %v", recorded)
	}
	path, _ := ref["path"].(string)
	if !strings.HasPrefix(path, "payloads/pack-") {
		t.Fatalf("unexpected payload ref path %q", path)
	}
	payloadFiles, err := os.ReadDir(filepath.Join(dir, "payloads"))
	if err != nil {
		t.Fatalf("read payload pack directory: %v", err)
	}
	if len(payloadFiles) != 1 {
		t.Fatalf("expected one payload pack, got %d", len(payloadFiles))
	}
}

func TestDebugSinkCloseFlushesPendingEvents(t *testing.T) {
	dir := t.TempDir()
	sink := newDebugSink()
	for index := 0; index < 32; index++ {
		sink.Append(dir, "runtime.jsonl", map[string]any{
			"event": "queued",
			"index": index,
		})
	}
	sink.Close()
	sink.Close()
	sink.Append(dir, "runtime.jsonl", map[string]any{"event": "after_close"})

	entries, err := os.ReadDir(filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatalf("read flushed runtime stream: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one flushed runtime segment, got %d", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(dir, "runtime", entries[0].Name()))
	if err != nil {
		t.Fatalf("read flushed runtime events: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(string(body)), "\n") + 1; lines != 32 {
		t.Fatalf("expected 32 flushed events, got %d", lines)
	}
	if strings.Contains(string(body), "after_close") {
		t.Fatal("event submitted after close must be ignored")
	}
}

func TestDebugSinkQueueDropIsReportedByNextWrittenEvent(t *testing.T) {
	dir := t.TempDir()
	sink := &debugSink{
		queue:    make(chan debugWriteTask, 1),
		writers:  make(map[string]*logsink.RotatingFile),
		payloads: make(map[string]*logsink.PayloadPackStore),
	}
	sink.lastWarning.Store(time.Now().UnixNano())
	sink.Append(dir, "runtime.jsonl", map[string]any{"event": "first"})
	sink.Append(dir, "runtime.jsonl", map[string]any{"event": "dropped"})
	if dropped := sink.dropped.Load(); dropped != 1 {
		t.Fatalf("expected one queued event drop, got %d", dropped)
	}
	task := <-sink.queue
	if err := sink.write(task); err != nil {
		t.Fatalf("write event after queue drop: %v", err)
	}
	writer := sink.writers[debugWriterKey(dir, "runtime")]
	if writer == nil {
		t.Fatal("runtime writer was not created")
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close runtime writer: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatalf("read runtime stream: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "runtime", entries[0].Name()))
	if err != nil {
		t.Fatalf("read runtime event: %v", err)
	}
	var recorded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(body))), &recorded); err != nil {
		t.Fatalf("decode runtime event: %v", err)
	}
	if recorded["dropped_before"] != float64(1) {
		t.Fatalf("expected dropped_before=1, got %v", recorded["dropped_before"])
	}
}

func TestDebugSinkDefersLargeFieldEncodingToWorker(t *testing.T) {
	dir := t.TempDir()
	var encoded atomic.Bool
	sink := &debugSink{
		queue:    make(chan debugWriteTask, 1),
		writers:  make(map[string]*logsink.RotatingFile),
		payloads: make(map[string]*logsink.PayloadPackStore),
	}
	sink.Append(dir, "provider.jsonl", map[string]any{
		"event": "deferred",
		"payload": debugFieldEncoder(func() ([]byte, error) {
			encoded.Store(true)
			return []byte(`{"body":"deferred"}`), nil
		}),
	})
	if encoded.Load() {
		t.Fatal("large field was encoded on the submitter path")
	}
	if err := sink.write(<-sink.queue); err != nil {
		t.Fatalf("write deferred event: %v", err)
	}
	if !encoded.Load() {
		t.Fatal("large field was not encoded by the worker path")
	}
}

func TestDebugSinkConcurrentAppendAndClose(t *testing.T) {
	dir := t.TempDir()
	sink := newDebugSink()
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for index := 0; index < 128; index++ {
				sink.Append(dir, "runtime.jsonl", map[string]any{
					"event":  "concurrent",
					"worker": worker,
					"index":  index,
				})
			}
		}(worker)
	}
	sink.Close()
	workers.Wait()
}

func TestDebugRecorderDirectoryStaysWithinHistoryRoot(t *testing.T) {
	historyRoot := t.TempDir()
	recorder := &debugRecorder{historyRoot: historyRoot}
	for _, conversationID := range []string{"..", "../outside", `..\\outside`} {
		dir := recorder.debugDir("request-1", conversationID)
		relative, err := filepath.Rel(historyRoot, dir)
		if err != nil {
			t.Fatalf("resolve debug directory %q: %v", conversationID, err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			t.Fatalf("debug directory escaped history root: id=%q dir=%q", conversationID, dir)
		}
	}
}

func TestDebugSinkWriteFailureIsReturnedToIsolationBoundary(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	sink := &debugSink{
		writers:  make(map[string]*logsink.RotatingFile),
		payloads: make(map[string]*logsink.PayloadPackStore),
	}
	err := sink.write(debugWriteTask{
		dir:    blocked,
		stream: "runtime",
		event:  map[string]any{"event": "test"},
	})
	if err == nil {
		t.Fatal("expected isolated debug write error")
	}
}
