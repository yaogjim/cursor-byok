package forwarder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type enabledDebugLogConfig struct{}

func (enabledDebugLogConfig) IsObservabilityLogEnabled(context.Context) bool {
	return true
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
		path := filepath.Join(root, "conversation-1", "debug", "provider.jsonl")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat provider debug log: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("provider debug log permissions = %04o, want 0600", got)
		}
	})
}
