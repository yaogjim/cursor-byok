package modeladapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cursor/internal/subscriptionauth"
)

func TestBuildLLMSummaryPayloadRecordsCodexAffinityProfileWithoutDerivedValues(t *testing.T) {
	now := time.Now()
	derived := CodexAffinity{
		PromptCacheKey:  "derived-prompt-key",
		SessionID:       "derived-session",
		ThreadID:        "derived-thread",
		ClientRequestID: "derived-request",
	}
	full := buildLLMSummaryPayload(StreamRequest{
		CodexAffinityProfile: CodexAffinityProfileFull,
		CodexAffinity:        derived,
	}, "openai", "gpt-5.6", now, now, now, "stop", 10, 20, 30, 0, nil)
	if full["codex_affinity_profile"] != CodexAffinityProfileFull || full["codex_affinity_injected"] != true {
		t.Fatalf("full profile payload = %#v", full)
	}
	for key, value := range full {
		if text := fmt.Sprint(value); strings.Contains(text, "derived-") {
			t.Fatalf("derived affinity value leaked into %q: %q", key, text)
		}
	}

	control := buildLLMSummaryPayload(StreamRequest{
		CodexAffinityProfile: CodexAffinityProfileControl,
	}, "openai", "gpt-5.6", now, now, now, "stop", 10, 20, 30, 0, nil)
	if control["codex_affinity_profile"] != CodexAffinityProfileControl || control["codex_affinity_injected"] != false {
		t.Fatalf("control profile payload = %#v", control)
	}

	static := buildLLMSummaryPayload(StreamRequest{}, "openai", "gpt-5.6", now, now, now, "stop", 10, 20, 30, 0, nil)
	if _, ok := static["codex_affinity_profile"]; ok {
		t.Fatalf("non-codex payload must omit affinity profile: %#v", static)
	}
}

type codexSummaryObserver struct {
	privacyArtifactObserver
	summary map[string]any
}

func (observer *codexSummaryObserver) RecordLLMSummary(_ string, _ string, _ string, payload map[string]any) (string, error) {
	observer.summary = payload
	return "", nil
}

func TestManagedCodexCacheReadPresenceReachesSummary(t *testing.T) {
	for _, test := range []struct {
		name    string
		details string
		present bool
	}{
		{"explicit zero", `,"input_tokens_details":{"cached_tokens":0}`, true},
		{"absent", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(CodexAffinityProfileEnv, CodexAffinityProfileControl)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output_text\":\"done\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1%s}}}\n\n", test.details)
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			observer := &codexSummaryObserver{}
			router := newCodexAffinityTestRouter(t, server, &stubCredentialResolver{token: "test-token"})
			err := router.Stream(context.Background(), StreamRequest{
				ModelID: "channel-managed", Messages: []Message{{Role: "user", Content: "hello"}}, Observer: observer,
			}, func(ModelEvent) error { return nil })
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if observer.summary["cache_read_present"] != test.present || observer.summary["cache_read_tokens"] != int64(0) {
				t.Fatalf("cache presence summary = %#v", observer.summary)
			}
		})
	}
}

func TestAttachCodexAffinityRecordsProfileForObservability(t *testing.T) {
	key := bytes.Repeat([]byte{0x66}, subscriptionauth.AffinityKeySize)
	base := StreamRequest{
		CredentialSource: "codex",
		CredentialID:     "codex:account-a",
		StableAccountID:  true,
		ConversationID:   "conversation-a",
		ModelCallID:      "call-a",
	}

	t.Setenv(CodexAffinityProfileEnv, CodexAffinityProfileFull)
	full := attachCodexAffinity(base, key)
	if full.CodexAffinityProfile != CodexAffinityProfileFull || full.CodexAffinity == (CodexAffinity{}) {
		t.Fatalf("full attach = %+v", full)
	}

	t.Setenv(CodexAffinityProfileEnv, CodexAffinityProfileControl)
	control := attachCodexAffinity(base, key)
	if control.CodexAffinityProfile != CodexAffinityProfileControl || control.CodexAffinity != (CodexAffinity{}) {
		t.Fatalf("control attach = %+v", control)
	}

	static := attachCodexAffinity(StreamRequest{CredentialSource: "static", CodexAffinityProfile: CodexAffinityProfileFull}, key)
	if static.CodexAffinityProfile != "" {
		t.Fatalf("static source kept profile: %+v", static)
	}
}
