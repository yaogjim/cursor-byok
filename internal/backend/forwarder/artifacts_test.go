package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	"cursor/internal/observability"
)

type enabledDebugLogConfig struct{}

func (enabledDebugLogConfig) ObservabilityLogMode(context.Context) string {
	return "full"
}

func TestDebugRecorderUsesUnifiedSanitizedCapture(t *testing.T) {
	historyRoot := filepath.Join(t.TempDir(), "history")
	logsRoot := filepath.Join(t.TempDir(), "logs")
	capture, err := observability.NewRecorder(logsRoot, observability.Settings{
		Mode:          observability.ModeFull,
		RetentionDays: 7,
		MaxDiskMB:     64,
	})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	status := capture.Status()
	debug := newDebugRecorder(historyRoot, nil, enabledDebugLogConfig{}, capture)
	t.Cleanup(debug.Close)
	debug.LogProviderArtifact(context.Background(), "request-1", "conversation-1", "model-call-1", "llm_request", map[string]any{
		"prompt": "full prompt",
		"apiKey": "provider-secret",
	})
	if err := capture.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	legacyPath := filepath.Join(historyRoot, "conversation-1", "debug", "provider.jsonl")
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy debug artifact still exists: %v", err)
	}
	payloadDir := filepath.Join(status.SessionPath, "payloads")
	entries, err := os.ReadDir(payloadDir)
	if err != nil {
		t.Fatalf("read unified payload directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("payload count = %d, want 1", len(entries))
	}
	payload, err := os.ReadFile(filepath.Join(payloadDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read unified payload: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "full prompt") || !strings.Contains(text, observability.RedactedValue) {
		t.Fatalf("unexpected unified payload: %s", text)
	}
	if strings.Contains(text, "provider-secret") {
		t.Fatalf("unified payload retained credential: %s", text)
	}
}

func TestUnifiedCaptureOmitsUnknownBidiRawBytes(t *testing.T) {
	logsRoot := filepath.Join(t.TempDir(), "logs")
	capture, err := observability.NewRecorder(logsRoot, observability.Settings{
		Mode:          observability.ModeFull,
		RetentionDays: 7,
		MaxDiskMB:     64,
	})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	status := capture.Status()
	debug := newDebugRecorder(t.TempDir(), nil, enabledDebugLogConfig{}, capture)
	t.Cleanup(debug.Close)
	debug.LogBidiRaw(context.Background(), "request-raw", "conversation-raw", 1, "deadbeef", "accepted", nil)
	if err := capture.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(status.SessionPath, "payloads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("payload count = %d, want 1", len(entries))
	}
	payload, err := os.ReadFile(filepath.Join(status.SessionPath, "payloads", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "deadbeef") || strings.Contains(text, "data_hex") {
		t.Fatalf("unknown raw bytes were persisted: %s", text)
	}
	if !strings.Contains(text, "raw_omitted") {
		t.Fatalf("omission metadata missing: %s", text)
	}
}

func TestHTTPTraceReplacesPrematureBackgroundCorrelation(t *testing.T) {
	capture, err := observability.NewRecorder(t.TempDir(), observability.Settings{
		Mode: observability.ModeBasic, RetentionDays: 7, MaxDiskMB: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := capture.Status()
	debug := newDebugRecorder(t.TempDir(), nil, enabledDebugLogConfig{}, capture)
	t.Cleanup(debug.Close)
	debug.LogRuntime(context.Background(), "request-correlation", "conversation-1", "premature_background_event", nil)
	authoritative := observability.Correlation{
		TraceID: "trace-from-mitm", SpanID: "backend-span", HTTPRequestID: "http-request-1",
	}
	httpContext := observability.WithCorrelation(context.Background(), authoritative)
	debug.LogBidiRaw(httpContext, "request-correlation", "conversation-1", 1, "00", "accepted", nil)
	debug.LogProviderArtifact(context.Background(), "request-correlation", "conversation-1", "model-call-1", "llm_request", map[string]any{"model": "test"})
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(status.SessionPath, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	if len(lines) != 3 {
		t.Fatalf("event count = %d, want 3", len(lines))
	}
	var bidiEvent observability.Event
	var providerEvent observability.Event
	if err := json.Unmarshal([]byte(lines[1]), &bidiEvent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &providerEvent); err != nil {
		t.Fatal(err)
	}
	if bidiEvent.TraceID != authoritative.TraceID || providerEvent.TraceID != authoritative.TraceID {
		t.Fatalf("authoritative trace was not retained: bidi=%+v provider=%+v", bidiEvent, providerEvent)
	}
	if bidiEvent.DecodeError {
		t.Fatal("accepted bidi_raw set decode_error")
	}
	if providerEvent.HTTPRequestID != authoritative.HTTPRequestID {
		t.Fatalf("provider HTTP correlation = %q, want %q", providerEvent.HTTPRequestID, authoritative.HTTPRequestID)
	}
}

func TestProviderHTTPContextSharesStoredCorrelation(t *testing.T) {
	capture, err := observability.NewRecorder(t.TempDir(), observability.Settings{
		Mode: observability.ModeBasic, RetentionDays: 7, MaxDiskMB: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	debug := newDebugRecorder(t.TempDir(), nil, enabledDebugLogConfig{}, capture)
	t.Cleanup(debug.Close)
	authoritative := observability.Correlation{
		TraceID:              "trace-from-bidi",
		SpanID:               "bidi-span",
		HTTPRequestID:        "http-request-9",
		RootConversationID:   "root-1",
		ParentConversationID: "parent-1",
		ParentToolCallID:     "parent-tool-1",
		SubagentRunID:        "run-1",
		ChildConversationID:  "conversation-1",
		AgentID:              "agent-1",
		TurnID:               "conversation-1:4",
		TurnSequence:         4,
	}
	httpContext := observability.WithCorrelation(context.Background(), authoritative)
	debug.LogBidiRaw(httpContext, "request-shared", "conversation-1", 1, "00", "accepted", nil)
	debug.LogProvider(context.Background(), "request-shared", "conversation-1", "provider_request_prepared", map[string]any{
		"model_call_id":        "model-call-9",
		"parent_model_call_id": "parent-model-1",
		"provider_pass":        2,
		"http_attempt":         3,
		"turn_seq":             4,
	})
	providerCtx := debug.contextWithRequestCorrelation(context.Background(), "request-shared", "conversation-1", "model-call-9")
	got := observability.CorrelationFromContext(providerCtx)
	if got.TraceID != authoritative.TraceID || got.HTTPRequestID != authoritative.HTTPRequestID || got.ModelCallID != "model-call-9" || got.TurnID != "conversation-1:4" {
		t.Fatalf("provider http context correlation = %+v", got)
	}
	if got.RootConversationID != "root-1" || got.ParentConversationID != "parent-1" || got.ParentModelCallID != "parent-model-1" || got.ParentToolCallID != "parent-tool-1" {
		t.Fatalf("provider parent correlation = %+v", got)
	}
	if got.SubagentRunID != "run-1" || got.ChildConversationID != "conversation-1" || got.AgentID != "agent-1" || got.ProviderPass != 2 || got.HTTPAttempt != 3 {
		t.Fatalf("provider subagent correlation = %+v", got)
	}
	empty := debug.contextWithRequestCorrelation(context.Background(), "unknown-request", "", "")
	if observability.CorrelationFromContext(empty).TraceID != "" {
		t.Fatal("missing stored correlation forged a trace_id")
	}
}

func TestDebugArtifactsOmitSubagentAndProviderRawContent(t *testing.T) {
	root := t.TempDir()
	debug := newDebugRecorder(root, nil, enabledDebugLogConfig{})
	const childSecret = "child-result-sensitive-body"
	const providerSecret = "provider-stream-sensitive-body"
	const rawSecret = "raw-bidi-sensitive-body"

	execMessage := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_SubagentResult{
			SubagentResult: &agentv1.SubagentResult{
				Result: &agentv1.SubagentResult_Error{Error: &agentv1.SubagentError{Error: childSecret}},
			},
		},
	}
	clientMessage := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ExecClientMessage{ExecClientMessage: execMessage},
	}
	intent := InboundIntent{Kind: "exec_result", RequestID: "request-sensitive", ConversationID: "conversation-sensitive", ExecClientMessage: execMessage}
	debug.LogBidiRaw(context.Background(), intent.RequestID, intent.ConversationID, 1, rawSecret, "accepted", nil)
	debug.LogBidiDecoded(context.Background(), intent.RequestID, intent.ConversationID, 1, "exec_client_message", clientMessage, intent, nil)
	recorder := newArtifactRecorder(nil, nil, debug)
	if _, err := recorder.AppendLLMResponseChunk(intent.RequestID, "run-sensitive", "model-call-sensitive", providerSecret); err != nil {
		t.Fatal(err)
	}

	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ExecServerMessage{
			ExecServerMessage: &agentv1.ExecServerMessage{
				Message: &agentv1.ExecServerMessage_SubagentArgs{SubagentArgs: &agentv1.SubagentArgs{}},
			},
		},
	}
	if payload := debug.ServerMessagePayload(context.Background(), serverMessage); payload != nil {
		t.Fatalf("subagent dispatch payload was exposed: %#v", payload)
	}
	debug.Close()

	var persisted strings.Builder
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		persisted.Write(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	text := persisted.String()
	for _, secret := range []string{childSecret, providerSecret, rawSecret} {
		if strings.Contains(text, secret) {
			t.Fatalf("debug artifacts retained sensitive content %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "subagent_sensitive_content") || !strings.Contains(text, "raw_sensitive_content") || !strings.Contains(text, "byte_len") {
		t.Fatalf("debug artifacts omitted controlled metadata: %s", text)
	}
}

func TestArtifactRecorderRetainsOnlyRequestPrefixUntilCleared(t *testing.T) {
	recorder := newArtifactRecorder(nil, nil, nil)
	const sensitivePayload = "complete replay payload must not remain in memory"
	payload := map[string]any{
		"provider":        "openai",
		"model":           "gpt-test",
		"openai_endpoint": "/v1/responses",
		"messages_summary": []any{
			map[string]any{"role": "system"},
			map[string]any{"role": "user"},
			map[string]any{"role": "assistant"},
		},
		"full_payload": sensitivePayload,
	}
	if _, err := recorder.RecordLLMRequest("request-1", "run-1", "model-call-1", payload); err != nil {
		t.Fatalf("RecordLLMRequest() error = %v", err)
	}

	recorder.mu.Lock()
	session, ok := recorder.sessions[artifactSessionKey("request-1", "model-call-1")]
	recorder.mu.Unlock()
	if !ok {
		t.Fatal("request prefix session was not created")
	}
	if !session.hasRequestPrefix {
		t.Fatal("request prefix was not retained")
	}
	if session.requestPrefix.Provider != "openai" || session.requestPrefix.Model != "gpt-test" || session.requestPrefix.ReplayMessageCount != 2 {
		t.Fatalf("request prefix = %#v", session.requestPrefix)
	}
	if strings.Contains(fmt.Sprintf("%#v", session), sensitivePayload) {
		t.Fatal("artifact session retained the complete provider payload")
	}
	for _, suffix := range []string{"_fb0", "_fb1"} {
		if _, err := recorder.RecordLLMRequest("request-1", "run-1", "model-call-1"+suffix, payload); err != nil {
			t.Fatalf("RecordLLMRequest(%s) error = %v", suffix, err)
		}
	}

	recorder.ClearActiveArtifacts("request-1", "model-call-1")
	recorder.mu.Lock()
	remaining := len(recorder.sessions)
	recorder.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("active artifact sessions = %d, want 0", remaining)
	}
}

func TestArtifactFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits consistently")
	}

	t.Run("atomic conversation file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		file, tempPath, err := openUniqueArtifactTempFile(path)
		if err != nil {
			t.Fatalf("openUniqueArtifactTempFile() error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close temp artifact: %v", err)
		}
		info, err := os.Stat(tempPath)
		if err != nil {
			t.Fatalf("stat temp artifact: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("artifact permissions = %04o, want 0600", got)
		}
	})

	t.Run("provider debug log", func(t *testing.T) {
		root := t.TempDir()
		recorder := newDebugRecorder(root, nil, enabledDebugLogConfig{})
		recorder.LogProviderArtifact(context.Background(), "request-1", "conversation-1", "model-call-1", "llm_request", map[string]any{"payload": "sensitive"})
		recorder.Close()
		paths, err := filepath.Glob(filepath.Join(root, "conversation-1", "debug", "provider", "event-*.jsonl"))
		if err != nil {
			t.Fatalf("glob provider debug logs: %v", err)
		}
		if len(paths) != 1 {
			t.Fatalf("provider debug log count = %d, want 1", len(paths))
		}
		info, err := os.Stat(paths[0])
		if err != nil {
			t.Fatalf("stat provider debug log: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("provider debug log permissions = %04o, want 0600", got)
		}
	})
}
