package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	legacyruntime "cursor/internal/runtime"
	"cursor/internal/subscriptionauth"
)

type recordingModelAdapter struct {
	request StreamRequest
}

func (adapter *recordingModelAdapter) Stream(_ context.Context, req StreamRequest, _ func(ModelEvent) error) error {
	adapter.request = req
	return nil
}

type staticChannelResolver struct {
	channel *legacyruntime.ResolvedChannel
}

func (resolver staticChannelResolver) SelectChannelForModel(context.Context, string) (*legacyruntime.ResolvedChannel, error) {
	return resolver.channel, nil
}

func (staticChannelResolver) ProviderStreamIdleTimeout(context.Context) time.Duration {
	return time.Second
}

func TestRouterRuntimeDisabledClearsReasoningEffort(t *testing.T) {
	openAI := &recordingModelAdapter{}
	router := &Router{
		openai: openAI,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:              "channel-a",
			Provider:        "openai",
			Model:           "grok-composer-2.5-fast",
			ReasoningEffort: "medium",
		}},
	}
	requestKnobs := map[string]any{"reasoning_effort": "medium"}

	err := router.Stream(context.Background(), StreamRequest{
		ModelID:        "channel-a",
		ThinkingEffort: "disabled",
		RequestKnobs:   requestKnobs,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if got := openAI.request.ReasoningEffort; got != "" {
		t.Fatalf("ReasoningEffort = %q, want blank", got)
	}
	if _, exists := openAI.request.RequestKnobs["reasoning_effort"]; exists {
		t.Fatalf("reasoning_effort knob should be removed: %#v", openAI.request.RequestKnobs)
	}
}

func TestSanitizeProviderMessagesMergesLegacyAssistantTextAndToolCallTurnsIdempotently(t *testing.T) {
	input := []Message{
		{
			Role:             "assistant",
			Content:          "Now let me pass stream.Mode in service.go.",
			ReasoningContent: "I need to update service.go.",
		},
		{
			Role:             "assistant",
			Content:          "",
			ReasoningContent: "I need to update service.go.",
			ToolCalls: []ToolCallDescriptor{
				{
					ID:   "call_1",
					Type: "function",
					Function: ToolCallFunctionShape{
						Name:      "PatchEdit",
						Arguments: `{"path":"/workspace/service.go"}`,
					},
				},
			},
		},
		{
			Role:       "tool",
			Content:    `{"success":{"path":"/workspace/service.go"}}`,
			ToolCallID: "call_1",
			Name:       "PatchEdit",
		},
	}

	first := sanitizeProviderMessages(input)
	if len(first) != 2 {
		t.Fatalf("message count = %d, want 2: %#v", len(first), first)
	}

	assistant := first[0]
	if assistant.Content != input[0].Content {
		t.Fatalf("assistant content = %q", assistant.Content)
	}
	if assistant.ReasoningContent != input[0].ReasoningContent {
		t.Fatalf("assistant reasoning = %q, want one copy of %q", assistant.ReasoningContent, input[0].ReasoningContent)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool calls = %#v", assistant.ToolCalls)
	}

	second := sanitizeProviderMessages(first)
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("sanitizing normalized messages changed them:\nfirst: %#v\nsecond: %#v", first, second)
	}
}

func TestSanitizeProviderMessagesKeepsMultipleToolCallsAndReasoningMetadata(t *testing.T) {
	reasoningSummary := []byte(`[{"type":"summary_text","text":"summary"}]`)
	metadata := Message{
		Role:                            "assistant",
		ReasoningContent:                "Inspect both files.",
		ReasoningSignature:              "encrypted-reasoning",
		ReasoningSignatureSource:        ReasoningSignatureSourceOpenAIResponses,
		OpenAIResponsesReasoningID:      "reasoning_1",
		OpenAIResponsesReasoningStatus:  "completed",
		OpenAIResponsesReasoningSummary: reasoningSummary,
	}
	firstAssistant := metadata
	firstAssistant.Content = "I will inspect both files."
	firstToolCall := metadata
	firstToolCall.ToolCalls = []ToolCallDescriptor{{
		ID:                    "call_1",
		Type:                  "function",
		OpenAIResponsesID:     "item_1",
		OpenAIResponsesCallID: "provider_call_1",
		OpenAIResponsesStatus: "completed",
		Function:              ToolCallFunctionShape{Name: "Read", Arguments: `{"path":"first.go"}`},
	}}
	secondToolCall := metadata
	secondToolCall.ToolCalls = []ToolCallDescriptor{{
		ID:                    "call_2",
		Type:                  "function",
		OpenAIResponsesID:     "item_2",
		OpenAIResponsesCallID: "provider_call_2",
		OpenAIResponsesStatus: "completed",
		Function:              ToolCallFunctionShape{Name: "Read", Arguments: `{"path":"second.go"}`},
	}}
	input := []Message{
		firstAssistant,
		firstToolCall,
		secondToolCall,
		{Role: "tool", ToolCallID: "call_1", Name: "Read", Content: "first"},
		{Role: "tool", ToolCallID: "call_2", Name: "Read", Content: "second"},
	}

	first := sanitizeProviderMessages(input)
	if len(first) != 3 {
		t.Fatalf("message count = %d, want assistant plus two tool results: %#v", len(first), first)
	}
	assistant := first[0]
	if assistant.Content != firstAssistant.Content || assistant.ReasoningContent != metadata.ReasoningContent {
		t.Fatalf("assistant text/reasoning = %#v", assistant)
	}
	if assistant.ReasoningSignature != metadata.ReasoningSignature || assistant.ReasoningSignatureSource != metadata.ReasoningSignatureSource {
		t.Fatalf("assistant reasoning signature metadata = %#v", assistant)
	}
	if assistant.OpenAIResponsesReasoningID != metadata.OpenAIResponsesReasoningID || assistant.OpenAIResponsesReasoningStatus != metadata.OpenAIResponsesReasoningStatus || string(assistant.OpenAIResponsesReasoningSummary) != string(reasoningSummary) {
		t.Fatalf("assistant Responses reasoning metadata = %#v", assistant)
	}
	if len(assistant.ToolCalls) != 2 {
		t.Fatalf("assistant tool calls = %#v", assistant.ToolCalls)
	}
	for index, expectedID := range []string{"call_1", "call_2"} {
		if assistant.ToolCalls[index].ID != expectedID || assistant.ToolCalls[index].Index != index {
			t.Fatalf("tool call %d = %#v", index, assistant.ToolCalls[index])
		}
	}
	if first[1].ToolCallID != "call_1" || first[2].ToolCallID != "call_2" {
		t.Fatalf("tool result order = %#v", first[1:])
	}

	second := sanitizeProviderMessages(first)
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("sanitizing normalized messages changed them:\nfirst: %#v\nsecond: %#v", first, second)
	}
}

type stubCredentialResolver struct {
	token       string
	accountID   string
	chatgptID   string
	stable      bool
	refreshTok  string
	refreshCred *subscriptionauth.Credential
	calls       int
	refreshN    int
	quotaCalls  []string
	quotaErr    error
	resolveSeq  []subscriptionauth.Credential
}

func (stub *stubCredentialResolver) Resolve(context.Context, subscriptionauth.CredentialSource) (subscriptionauth.Credential, error) {
	stub.calls++
	if len(stub.resolveSeq) > 0 {
		idx := stub.calls - 1
		if idx >= len(stub.resolveSeq) {
			idx = len(stub.resolveSeq) - 1
		}
		return stub.resolveSeq[idx], nil
	}
	return subscriptionauth.Credential{
		Provider:         subscriptionauth.ProviderCodex,
		AccountID:        stub.accountID,
		AccessToken:      stub.token,
		ChatGPTAccountID: stub.chatgptID,
		StableAccountID:  stub.stable,
	}, nil
}

func (stub *stubCredentialResolver) ResolveAfterUnauthorized(_ context.Context, _ subscriptionauth.CredentialSource, _ string) (subscriptionauth.Credential, error) {
	stub.refreshN++
	if stub.refreshCred != nil {
		return *stub.refreshCred, nil
	}
	token := stub.refreshTok
	if token == "" {
		token = stub.token + "-refreshed"
	}
	return subscriptionauth.Credential{
		Provider:         subscriptionauth.ProviderCodex,
		AccountID:        stub.accountID,
		AccessToken:      token,
		ChatGPTAccountID: stub.chatgptID,
		StableAccountID:  stub.stable,
	}, nil
}

func (stub *stubCredentialResolver) MarkQuotaExhausted(_ context.Context, credentialID string) error {
	stub.quotaCalls = append(stub.quotaCalls, credentialID)
	if stub.quotaErr != nil {
		return stub.quotaErr
	}
	return nil
}

func (stub *stubCredentialResolver) RefreshUsage(context.Context, subscriptionauth.ProviderKind) (subscriptionauth.UsageSnapshot, error) {
	return subscriptionauth.UsageSnapshot{}, nil
}

func TestRouterStaticChannelKeepsConfiguredAPIKey(t *testing.T) {
	openAI := &recordingModelAdapter{}
	creds := &stubCredentialResolver{token: "managed-token", accountID: "codex:acct"}
	router := &Router{
		openai:      openAI,
		credentials: creds,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:               "channel-static",
			Provider:         "openai",
			APIKey:           "static-key",
			CredentialSource: "static",
			Model:            "gpt-test",
		}},
	}
	if err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-static"}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if openAI.request.APIKey != "static-key" {
		t.Fatalf("APIKey = %q, want static-key", openAI.request.APIKey)
	}
	if creds.calls != 0 {
		t.Fatalf("static channel must not resolve managed credentials, calls=%d", creds.calls)
	}
}

func TestRouterManagedChannelUsesResolverToken(t *testing.T) {
	openAI := &recordingModelAdapter{}
	creds := &stubCredentialResolver{token: "managed-token", accountID: "codex:acct"}
	router := &Router{
		openai:      openAI,
		credentials: creds,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:               "channel-codex",
			Provider:         "openai",
			APIKey:           "",
			CredentialSource: "codex",
			Model:            "gpt-test",
		}},
	}
	if err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-codex"}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if openAI.request.APIKey != "managed-token" {
		t.Fatalf("APIKey = %q", openAI.request.APIKey)
	}
	if openAI.request.CredentialID != "codex:acct" {
		t.Fatalf("CredentialID = %q", openAI.request.CredentialID)
	}
}

func writeOpenAIChatSSE(writer http.ResponseWriter, text string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(writer, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", text)
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
}

type scriptedStream struct {
	events []ModelEvent
	err    error
}

type scriptedModelAdapter struct {
	requests []StreamRequest
	script   []scriptedStream
}

func (adapter *scriptedModelAdapter) Stream(_ context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	adapter.requests = append(adapter.requests, req)
	idx := len(adapter.requests) - 1
	if idx >= len(adapter.script) {
		return nil
	}
	step := adapter.script[idx]
	for _, event := range step.events {
		if err := sink(event); err != nil {
			return err
		}
	}
	return step.err
}

func newManagedTestRouter(t *testing.T, server *httptest.Server, creds subscriptionauth.CredentialResolver, source string) *Router {
	t.Helper()
	retry := instantRetry()
	retry.maxAttempts = 1
	return &Router{
		openai:      &OpenAIAdapter{client: server.Client(), retry: retry},
		credentials: creds,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:               "channel-managed",
			Provider:         "openai",
			BaseURL:          server.URL,
			CredentialSource: source,
			Model:            "gpt-test",
			OpenAIEndpoint:   "/v1/chat/completions",
		}},
	}
}

func TestRouterManagedCodexRetriesUnauthorizedOnce(t *testing.T) {
	var auths []string
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		auths = append(auths, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
		if hits == 1 {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(writer, `{"error":{"message":"invalid_api_key"}}`)
			return
		}
		writeOpenAIChatSSE(writer, "ok")
	}))
	defer server.Close()

	creds := &stubCredentialResolver{token: "tok-old", refreshTok: "tok-new", accountID: "codex:acct"}
	router := newManagedTestRouter(t, server, creds, "codex")
	if err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-managed"}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits=%d, want 2", hits)
	}
	if creds.refreshN != 1 {
		t.Fatalf("ResolveAfterUnauthorized calls=%d, want 1", creds.refreshN)
	}
	if len(auths) != 2 || auths[0] != "tok-old" || auths[1] != "tok-new" {
		t.Fatalf("authorization tokens = %#v", auths)
	}
}

func TestRouterManagedCodexUnauthorizedRetryAtMostOnce(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":{"message":"invalid_api_key"}}`)
	}))
	defer server.Close()

	creds := &stubCredentialResolver{token: "tok-old", refreshTok: "tok-new", accountID: "codex:acct"}
	router := newManagedTestRouter(t, server, creds, "codex")
	err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-managed"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if hits != 2 {
		t.Fatalf("hits=%d, want 2", hits)
	}
	if creds.refreshN != 1 {
		t.Fatalf("ResolveAfterUnauthorized calls=%d, want 1", creds.refreshN)
	}
}

func TestRouterManagedCodexSkipsUnauthorizedAfterModelEvent(t *testing.T) {
	creds := &stubCredentialResolver{token: "tok-old", refreshTok: "tok-new", accountID: "codex:acct"}
	openai := &scriptedModelAdapter{script: []scriptedStream{{
		events: []ModelEvent{{Kind: ModelEventKindTextDelta, Text: "partial"}},
		err:    &HTTPStatusError{Provider: "openai adapter", StatusCode: 401, Body: "invalid_api_key"},
	}}}
	router := &Router{
		openai:      openai,
		credentials: creds,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:               "channel-codex",
			Provider:         "openai",
			CredentialSource: "codex",
			Model:            "gpt-test",
		}},
	}
	err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-codex"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if creds.refreshN != 0 {
		t.Fatalf("ResolveAfterUnauthorized calls=%d, want 0", creds.refreshN)
	}
	if len(openai.requests) != 1 {
		t.Fatalf("adapter calls=%d, want 1", len(openai.requests))
	}
}

func TestRouterManagedCodexUnauthorizedSharesFallbackBudget(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":{"message":"invalid_api_key"}}`)
	}))
	defer server.Close()

	creds := &stubCredentialResolver{token: "tok-old", refreshTok: "tok-new", accountID: "codex:acct"}
	router := newManagedTestRouter(t, server, creds, "codex")
	err := router.Stream(context.Background(), StreamRequest{
		ModelID:        "channel-managed",
		FallbackBudget: NewFallbackRetryBudget(1, 0),
	}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if hits != 1 {
		t.Fatalf("hits=%d, want 1 when fallback attempt budget is 1", hits)
	}
	if creds.refreshN != 0 {
		t.Fatalf("ResolveAfterUnauthorized calls=%d, want 0", creds.refreshN)
	}
}

func TestRouterManagedCodexRotatesOnQuotaError(t *testing.T) {
	var auths []string
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		auths = append(auths, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
		if hits == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":{"code":"usage_limit_reached","message":"5-hour limit reached"}}`)
			return
		}
		writeOpenAIChatSSE(writer, "ok")
	}))
	defer server.Close()

	creds := &stubCredentialResolver{
		resolveSeq: []subscriptionauth.Credential{
			{Provider: subscriptionauth.ProviderCodex, AccountID: "codex:one", AccessToken: "tok-a"},
			{Provider: subscriptionauth.ProviderCodex, AccountID: "codex:two", AccessToken: "tok-b"},
		},
	}
	router := newManagedTestRouter(t, server, creds, "codex")
	if err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-managed"}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits != 2 || len(creds.quotaCalls) != 1 || creds.quotaCalls[0] != "codex:one" {
		t.Fatalf("hits=%d quota calls=%#v", hits, creds.quotaCalls)
	}
	if creds.refreshN != 0 {
		t.Fatalf("quota rotation must not refresh, got %d", creds.refreshN)
	}
	if len(auths) != 2 || auths[0] != "tok-a" || auths[1] != "tok-b" {
		t.Fatalf("authorization tokens = %#v", auths)
	}
}

func TestRouterManagedGrokRotatesOnQuotaError(t *testing.T) {
	var auths []string
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		auths = append(auths, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
		if hits == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":{"code":"insufficient_quota","message":"exceeded your current quota"}}`)
			return
		}
		writeOpenAIChatSSE(writer, "ok")
	}))
	defer server.Close()

	creds := &stubCredentialResolver{
		resolveSeq: []subscriptionauth.Credential{
			{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:one", AccessToken: "tok-a"},
			{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:two", AccessToken: "tok-b"},
		},
	}
	router := newManagedTestRouter(t, server, creds, "grok")
	if err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-managed"}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits=%d, want 2", hits)
	}
	if len(creds.quotaCalls) != 1 || creds.quotaCalls[0] != "grok:one" {
		t.Fatalf("quota calls = %#v", creds.quotaCalls)
	}
	if creds.refreshN != 0 {
		t.Fatalf("Grok must not call ResolveAfterUnauthorized, got %d", creds.refreshN)
	}
	if len(auths) != 2 || auths[0] != "tok-a" || auths[1] != "tok-b" {
		t.Fatalf("authorization tokens = %#v", auths)
	}
}

func TestRouterManagedGrokDoesNotRotateOnBareStatus(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			hits := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				hits++
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, `{"error":{"message":"rate_limit_reached"}}`)
			}))
			defer server.Close()

			creds := &stubCredentialResolver{
				resolveSeq: []subscriptionauth.Credential{
					{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:one", AccessToken: "tok-a"},
					{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:two", AccessToken: "tok-b"},
				},
			}
			router := newManagedTestRouter(t, server, creds, "grok")
			err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-managed"}, func(ModelEvent) error { return nil })
			if err == nil {
				t.Fatal("expected provider error")
			}
			if hits != 1 {
				t.Fatalf("hits=%d, want 1", hits)
			}
			if len(creds.quotaCalls) != 0 {
				t.Fatalf("quota calls = %#v, want none", creds.quotaCalls)
			}
			if creds.refreshN != 0 {
				t.Fatalf("refresh calls = %d, want 0", creds.refreshN)
			}
		})
	}
}

func TestRouterManagedGrokSuppressesModelAdapterTest(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"code":"insufficient_quota"}}`)
	}))
	defer server.Close()

	creds := &stubCredentialResolver{
		resolveSeq: []subscriptionauth.Credential{
			{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:one", AccessToken: "tok-a"},
			{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:two", AccessToken: "tok-b"},
		},
	}
	router := newManagedTestRouter(t, server, creds, "grok")
	err := router.Stream(context.Background(), StreamRequest{
		ModelID:   "channel-managed",
		RequestID: "model-adapter-test-abc",
	}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected quota error to surface without rotation")
	}
	if hits != 1 {
		t.Fatalf("hits=%d, want 1", hits)
	}
	if len(creds.quotaCalls) != 0 {
		t.Fatalf("quota calls = %#v, want none", creds.quotaCalls)
	}
}

func TestRouterManagedCodexSuppressesModelAdapterQuotaRotation(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"code":"insufficient_quota"}}`)
	}))
	defer server.Close()

	creds := &stubCredentialResolver{token: "tok-a", accountID: "codex:one"}
	router := newManagedTestRouter(t, server, creds, "codex")
	err := router.Stream(context.Background(), StreamRequest{
		ModelID:   "channel-managed",
		RequestID: "model-adapter-test-abc",
	}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected quota error to surface without rotation")
	}
	if hits != 1 || len(creds.quotaCalls) != 0 {
		t.Fatalf("hits=%d quota calls=%#v", hits, creds.quotaCalls)
	}
}

func TestRouterManagedGrokNoNextAccountReturnsQuotaExhausted(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"code":"insufficient_quota"}}`)
	}))
	defer server.Close()

	creds := &stubCredentialResolver{
		quotaErr: subscriptionauth.ErrQuotaExhausted,
		resolveSeq: []subscriptionauth.Credential{
			{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:one", AccessToken: "tok-a"},
		},
	}
	router := newManagedTestRouter(t, server, creds, "grok")
	err := router.Stream(context.Background(), StreamRequest{ModelID: "channel-managed"}, func(ModelEvent) error { return nil })
	if !errors.Is(err, subscriptionauth.ErrQuotaExhausted) {
		t.Fatalf("err=%v, want ErrQuotaExhausted", err)
	}
	if hits != 1 {
		t.Fatalf("hits=%d, want 1", hits)
	}
	if len(creds.quotaCalls) != 1 {
		t.Fatalf("quota calls = %#v", creds.quotaCalls)
	}
}

func TestRouterManagedGrokQuotaSharesFallbackBudget(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"code":"insufficient_quota"}}`)
	}))
	defer server.Close()

	creds := &stubCredentialResolver{
		resolveSeq: []subscriptionauth.Credential{
			{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:one", AccessToken: "tok-a"},
			{Provider: subscriptionauth.ProviderGrok, AccountID: "grok:two", AccessToken: "tok-b"},
		},
	}
	router := newManagedTestRouter(t, server, creds, "grok")
	err := router.Stream(context.Background(), StreamRequest{
		ModelID:        "channel-managed",
		FallbackBudget: NewFallbackRetryBudget(1, 0),
	}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected quota error")
	}
	if hits != 1 {
		t.Fatalf("hits=%d, want 1 when fallback attempt budget is 1", hits)
	}
	if len(creds.quotaCalls) != 0 {
		t.Fatalf("quota calls = %#v, want none", creds.quotaCalls)
	}
}
