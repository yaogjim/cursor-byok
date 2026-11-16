package modeladapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursor/internal/modelchannel"
	legacyruntime "cursor/internal/runtime"
	"cursor/internal/subscriptionauth"
)

func newCodexAffinityTestRouter(t *testing.T, server *httptest.Server, creds subscriptionauth.CredentialResolver) *Router {
	t.Helper()
	retry := instantRetry()
	retry.maxAttempts = 1
	return &Router{
		openai:      &OpenAIAdapter{client: chatgptLoopbackClient(t, server), retry: retry},
		credentials: creds,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:               "channel-managed",
			Provider:         "openai",
			BaseURL:          subscriptionauth.CodexResponsesURL,
			CredentialSource: "codex",
			Model:            "gpt-5.6",
			OpenAIEndpoint:   modelchannel.OpenAIEndpointResponses,
		}},
	}
}

type codexAffinityRequestCapture struct {
	PromptCacheKey string
	SessionID      string
	ThreadID       string
	ClientRequest  string
	Authorization  string
	AccountID      string
}

func captureCodexAffinityRequest(t *testing.T, request *http.Request) codexAffinityRequestCapture {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	key, _ := body["prompt_cache_key"].(string)
	return codexAffinityRequestCapture{
		PromptCacheKey: key,
		SessionID:      request.Header.Get("session-id"),
		ThreadID:       request.Header.Get("thread-id"),
		ClientRequest:  request.Header.Get("x-client-request-id"),
		Authorization:  request.Header.Get("Authorization"),
		AccountID:      request.Header.Get("ChatGPT-Account-Id"),
	}
}

func writeCodexCompletedSSE(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-5.6\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
}

func TestManagedCodexUnauthorizedRefreshKeepsAffinityInHTTPRequest(t *testing.T) {
	t.Setenv(CodexAffinityProfileEnv, CodexAffinityProfileFull)
	var captured []codexAffinityRequestCapture
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = append(captured, captureCodexAffinityRequest(t, request))
		if len(captured) == 1 {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(writer, `{"error":{"message":"invalid token"}}`)
			return
		}
		writeCodexCompletedSSE(writer)
	}))
	defer server.Close()

	creds := &stubCredentialResolver{
		token:       "tok-old",
		refreshTok:  "tok-new",
		accountID:   "codex:account-a",
		stable:      true,
		affinityKey: bytes.Repeat([]byte{0x44}, subscriptionauth.AffinityKeySize),
	}
	router := newCodexAffinityTestRouter(t, server, creds)
	if err := router.Stream(context.Background(), StreamRequest{
		ModelID:        "channel-managed",
		ConversationID: "conversation-a",
		ModelCallID:    "call-a",
		Messages:       []Message{{Role: "user", Content: "hello"}},
	}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("provider hits = %d, want 2", len(captured))
	}
	if creds.refreshN != 1 {
		t.Fatalf("ResolveAfterUnauthorized calls = %d, want 1", creds.refreshN)
	}
	first, second := captured[0], captured[1]
	if first.PromptCacheKey == "" || first.SessionID == "" || first.ThreadID == "" || first.ClientRequest == "" {
		t.Fatalf("first request missing affinity: %+v", first)
	}
	if second.PromptCacheKey != first.PromptCacheKey || second.SessionID != first.SessionID || second.ThreadID != first.ThreadID {
		t.Fatalf("same-account refresh changed affinity: first=%+v second=%+v", first, second)
	}
	if second.ClientRequest != first.ClientRequest {
		t.Fatalf("retry changed x-client-request-id: %q -> %q", first.ClientRequest, second.ClientRequest)
	}
	if first.Authorization != "Bearer tok-old" || second.Authorization != "Bearer tok-new" {
		t.Fatalf("authorization = %q, %q", first.Authorization, second.Authorization)
	}
}

func TestManagedCodexQuotaRotationChangesAffinityInHTTPRequest(t *testing.T) {
	t.Setenv(CodexAffinityProfileEnv, CodexAffinityProfileFull)
	var captured []codexAffinityRequestCapture
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = append(captured, captureCodexAffinityRequest(t, request))
		if len(captured) == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":{"code":"usage_limit_reached","message":"5-hour limit reached"}}`)
			return
		}
		writeCodexCompletedSSE(writer)
	}))
	defer server.Close()

	creds := &stubCredentialResolver{
		affinityKey: bytes.Repeat([]byte{0x55}, subscriptionauth.AffinityKeySize),
		resolveSeq: []subscriptionauth.Credential{
			{Provider: subscriptionauth.ProviderCodex, AccountID: "codex:account-a", AccessToken: "tok-a", ChatGPTAccountID: "acct-a", StableAccountID: true},
			{Provider: subscriptionauth.ProviderCodex, AccountID: "codex:account-b", AccessToken: "tok-b", ChatGPTAccountID: "acct-b", StableAccountID: true},
		},
	}
	router := newCodexAffinityTestRouter(t, server, creds)
	if err := router.Stream(context.Background(), StreamRequest{
		ModelID:              "channel-managed",
		ConversationID:       "conversation-a",
		ModelCallID:          "call-a",
		Messages:             []Message{{Role: "user", Content: "hello"}},
		CustomHeadersEnabled: true,
		CustomHeadersJSON:    `{"Authorization":"Bearer attacker","session-id":"session-attacker","thread-id":"thread-attacker","x-client-request-id":"request-attacker"}`,
	}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("provider hits = %d, want 2", len(captured))
	}
	if len(creds.quotaCalls) != 1 || creds.quotaCalls[0] != "codex:account-a" {
		t.Fatalf("quota calls = %#v", creds.quotaCalls)
	}
	first, second := captured[0], captured[1]
	if first.PromptCacheKey == "" || second.PromptCacheKey == "" {
		t.Fatalf("missing prompt cache key: first=%+v second=%+v", first, second)
	}
	if second.PromptCacheKey == first.PromptCacheKey || second.SessionID == first.SessionID || second.ThreadID == first.ThreadID {
		t.Fatalf("account rotation did not change affinity: first=%+v second=%+v", first, second)
	}
	if first.AccountID != "acct-a" || second.AccountID != "acct-b" {
		t.Fatalf("account headers = %q, %q", first.AccountID, second.AccountID)
	}
	if second.Authorization != "Bearer tok-b" || second.SessionID == "session-attacker" || second.ThreadID == "thread-attacker" || second.ClientRequest == "request-attacker" {
		t.Fatalf("custom headers overrode managed contract after rotation: %+v", second)
	}
}
