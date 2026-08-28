package forwarder

import (
	"context"
	"reflect"
	"testing"

	"cursor/gen/agentv1"
	"cursor/internal/observability"
)

type debugRecorderTestConfig string

func (config debugRecorderTestConfig) ObservabilityLogMode(context.Context) string {
	return string(config)
}

type debugRecorderTestCapture struct {
	captures []observability.Capture
}

func (capture *debugRecorderTestCapture) Record(_ context.Context, value observability.Capture) bool {
	capture.captures = append(capture.captures, value)
	return true
}

func (capture *debugRecorderTestCapture) onlyCapture(t *testing.T) observability.Capture {
	t.Helper()
	if len(capture.captures) != 1 {
		t.Fatalf("capture count = %d, want 1", len(capture.captures))
	}
	return capture.captures[0]
}

func TestClassifyDebugSemantics(t *testing.T) {
	provider := classifyDebugSemantics("provider", "llm_request", "started", "", nil)
	if provider.Capability != "provider" || provider.Operation != "provider.llm_request" || provider.Direction != observability.DirectionProxyToProvider || provider.Outcome != observability.OutcomeStarted || provider.Implementation != observability.ImplementationImplemented {
		t.Fatalf("unexpected provider semantics: %+v", provider)
	}

	tool := classifyDebugSemantics("runtime", "tool_result", "partial", "", nil)
	if tool.Capability != "tool" || tool.Operation != "agent.tool_result" || tool.Direction != observability.DirectionProxyInternal || tool.Outcome != observability.OutcomePartial || tool.Implementation != observability.ImplementationPartial {
		t.Fatalf("unexpected tool semantics: %+v", tool)
	}

	failed := classifyDebugSemantics("runsse", "terminal", "completed", "terminal_error", nil)
	if failed.Outcome != observability.OutcomeFailed || failed.Direction != observability.DirectionProxyToCursor {
		t.Fatalf("unexpected failed terminal semantics: %+v", failed)
	}
}

func TestDebugRecorderProjectsLLMSummaryFailureAcrossCaptureModes(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		payload       map[string]any
		payloadField  string
		wantOutcome   string
		wantStatus    string
		wantErrorType string
	}{
		{
			name:          "basic payload summary error",
			mode:          "basic",
			payload:       map[string]any{"error": "provider unavailable", "body": "sensitive provider response"},
			payloadField:  "payload_summary",
			wantOutcome:   observability.OutcomeFailed,
			wantStatus:    "error",
			wantErrorType: "provider_error",
		},
		{
			name:          "full payload error",
			mode:          "full",
			payload:       map[string]any{"error": "provider unavailable", "body": "sensitive provider response"},
			payloadField:  "payload",
			wantOutcome:   observability.OutcomeFailed,
			wantStatus:    "error",
			wantErrorType: "provider_error",
		},
		{
			name:         "successful summary remains completed",
			mode:         "basic",
			payload:      map[string]any{"finish_reason": "stop"},
			payloadField: "payload_summary",
			wantOutcome:  observability.OutcomeSucceeded,
			wantStatus:   "completed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := &debugRecorderTestCapture{}
			recorder := newDebugRecorder(t.TempDir(), nil, debugRecorderTestConfig(test.mode), capture)
			t.Cleanup(recorder.Close)
			recorder.LogProviderArtifact(context.Background(), "request-1", "conversation-1", "model-call-1", "llm_summary", test.payload)

			captured := capture.onlyCapture(t)
			if captured.Event.SemanticOutcome != test.wantOutcome || captured.Event.Status != test.wantStatus || captured.Event.ErrorCategory != test.wantErrorType {
				t.Fatalf("event = %+v, want outcome=%q status=%q error_category=%q", captured.Event, test.wantOutcome, test.wantStatus, test.wantErrorType)
			}
			rawEvent, ok := captured.Payload.Data.(map[string]any)
			if !ok {
				t.Fatalf("payload data type = %T, want map", captured.Payload.Data)
			}
			if _, ok := rawEvent[test.payloadField]; !ok {
				t.Fatalf("payload data missing %q: %#v", test.payloadField, rawEvent)
			}
			if test.mode == "basic" {
				if _, ok := rawEvent["payload"]; ok {
					t.Fatalf("basic event retained full payload: %#v", rawEvent)
				}
				if summary, _ := rawEvent["payload_summary"].(map[string]any); summary["body"] != nil {
					t.Fatalf("basic summary retained provider body: %#v", summary)
				}
			}
		})
	}
}

func TestBidiRawDecodeErrorOnlyOnFailedDecode(t *testing.T) {
	capture := &debugRecorderTestCapture{}
	recorder := newDebugRecorder(t.TempDir(), nil, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(recorder.Close)
	recorder.LogBidiRaw(context.Background(), "request-1", "conversation-1", 1, "00", "accepted", nil)
	recorder.LogBidiRaw(context.Background(), "request-1", "conversation-1", 2, "zz", "decode_error", map[string]any{"error": "invalid hex"})
	if len(capture.captures) != 2 {
		t.Fatalf("capture count = %d, want 2", len(capture.captures))
	}
	if capture.captures[0].Event.DecodeError {
		t.Fatal("accepted bidi_raw set decode_error")
	}
	if !capture.captures[1].Event.DecodeError {
		t.Fatal("decode_error status did not set DecodeError")
	}
}

func TestLogProviderArtifactKeepsCanonicalModelCallID(t *testing.T) {
	capture := &debugRecorderTestCapture{}
	recorder := newDebugRecorder(t.TempDir(), nil, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(recorder.Close)
	recorder.LogProviderArtifact(context.Background(), "request-1", "conversation-1", "model-call-1_fb0", "llm_request", map[string]any{
		"model_call_id": "model-call-1",
	})
	captured := capture.onlyCapture(t)
	rawEvent, _ := captured.Payload.Data.(map[string]any)
	if rawEvent["model_call_id"] != "model-call-1" {
		t.Fatalf("payload model_call_id = %#v", rawEvent["model_call_id"])
	}
	if rawEvent["artifact_model_call_id"] != "model-call-1_fb0" || rawEvent["fallback_channel_index"] != 0 {
		t.Fatalf("fallback identity = %#v", rawEvent)
	}
}

func TestWorkspacePathsFromIntent(t *testing.T) {
	intent := InboundIntent{RequestContext: &agentv1.RequestContext{Env: &agentv1.RequestContextEnv{
		WorkspacePaths: []string{"/workspace/a", "/workspace/b"},
		ProjectFolder:  "/workspace/a",
	}}}
	paths := workspacePathsFromIntent(intent)
	if want := []string{"/workspace/a", "/workspace/b"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestDebugProjectPathsUsesOnlyExplicitWorkspaceKeys(t *testing.T) {
	paths := debugProjectPaths(
		map[string]any{
			"workspace_paths": []any{"/workspace/a", "/workspace/b"},
			"path":            "/must/not/be/used",
		},
		map[string]any{"project_folder": "/workspace/c"},
	)
	want := []string{"/workspace/a", "/workspace/b", "/workspace/c"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}
