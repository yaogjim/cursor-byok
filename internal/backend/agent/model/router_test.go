package modeladapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/modelchannel"
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

func TestSanitizeProviderMessagesForTargetStripsIncompatibleOpaqueMetadataAsAGroup(t *testing.T) {
	origin := ReasoningOrigin{
		Provider:         "openai",
		Endpoint:         "openai_responses:api.openai.com",
		CredentialSource: "codex",
		AccountID:        "acct-1",
		ModelID:          "gpt-5",
	}
	summary := json.RawMessage(`[{"type":"summary_text","text":"summary"}]`)
	input := []Message{
		{
			Role:                            "assistant",
			Content:                         "I will inspect both files.",
			ReasoningContent:                "Inspect both files.",
			ReasoningSignature:              "encrypted-reasoning",
			ReasoningSignatureSource:        ReasoningSignatureSourceOpenAIResponses,
			OpenAIResponsesReasoningID:      "reasoning_1",
			OpenAIResponsesReasoningStatus:  "completed",
			OpenAIResponsesReasoningSummary: summary,
			ReasoningOrigin:                 origin,
			ToolCalls: []ToolCallDescriptor{{
				ID:                    "call_1",
				Type:                  "function",
				OpenAIResponsesID:     "item_1",
				OpenAIResponsesCallID: "provider_call_1",
				OpenAIResponsesStatus: "completed",
				Function:              ToolCallFunctionShape{Name: "Read", Arguments: `{"path":"first.go"}`},
			}},
		},
		{Role: "tool", ToolCallID: "call_1", Name: "Read", Content: "first"},
	}

	kept := sanitizeProviderMessagesForTarget(input, origin)
	if len(kept) != 2 {
		t.Fatalf("compatible message count = %d, want 2: %#v", len(kept), kept)
	}
	assistant := kept[0]
	if assistant.Content != input[0].Content || assistant.ReasoningContent != input[0].ReasoningContent {
		t.Fatalf("compatible keep dropped visible text: %#v", assistant)
	}
	if assistant.ReasoningSignature != input[0].ReasoningSignature || assistant.ReasoningSignatureSource != input[0].ReasoningSignatureSource {
		t.Fatalf("compatible keep dropped signature: %#v", assistant)
	}
	if assistant.OpenAIResponsesReasoningID != "reasoning_1" || assistant.OpenAIResponsesReasoningStatus != "completed" || string(assistant.OpenAIResponsesReasoningSummary) != string(summary) {
		t.Fatalf("compatible keep dropped Responses reasoning: %#v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[0].OpenAIResponsesID != "item_1" {
		t.Fatalf("compatible keep dropped tool metadata: %#v", assistant.ToolCalls)
	}
	if kept[1].ToolCallID != "call_1" || kept[1].Content != "first" {
		t.Fatalf("compatible keep broke tool result pairing: %#v", kept[1])
	}

	mismatches := []ReasoningOrigin{
		{Provider: "anthropic", Endpoint: "anthropic:api.anthropic.com", CredentialSource: "static", AccountID: "acct-1", ModelID: "claude-4"},
		{Provider: "openai", Endpoint: "openai_chat_completions:api.openai.com", CredentialSource: "codex", AccountID: "acct-1", ModelID: "gpt-5"},
		{Provider: "openai", Endpoint: origin.Endpoint, CredentialSource: "static", AccountID: "acct-1", ModelID: "gpt-5"},
		{Provider: "openai", Endpoint: origin.Endpoint, CredentialSource: "codex", AccountID: "acct-2", ModelID: "gpt-5"},
		{Provider: "openai", Endpoint: origin.Endpoint, CredentialSource: "codex", AccountID: "acct-1", ModelID: "gpt-4.1"},
	}
	for _, target := range mismatches {
		stripped := sanitizeProviderMessagesForTarget(input, target)
		if len(stripped) != 2 {
			t.Fatalf("stripped message count = %d, want 2 for target %#v: %#v", len(stripped), target, stripped)
		}
		got := stripped[0]
		if got.Content != input[0].Content || got.ReasoningContent != input[0].ReasoningContent {
			t.Fatalf("strip dropped visible text for target %#v: %#v", target, got)
		}
		if got.ReasoningSignature != "" || got.ReasoningSignatureSource != "" || !got.ReasoningOrigin.IsZero() {
			t.Fatalf("strip left reasoning signature group for target %#v: %#v", target, got)
		}
		if got.OpenAIResponsesReasoningID != "" || got.OpenAIResponsesReasoningStatus != "" || len(got.OpenAIResponsesReasoningSummary) != 0 {
			t.Fatalf("strip left Responses reasoning group for target %#v: %#v", target, got)
		}
		if len(got.ToolCalls) != 1 {
			t.Fatalf("strip dropped local tool call for target %#v: %#v", target, got.ToolCalls)
		}
		toolCall := got.ToolCalls[0]
		if toolCall.ID != "call_1" || toolCall.Function.Name != "Read" || toolCall.Function.Arguments != `{"path":"first.go"}` {
			t.Fatalf("strip dropped local tool identity for target %#v: %#v", target, toolCall)
		}
		if toolCall.OpenAIResponsesID != "" || toolCall.OpenAIResponsesCallID != "" || toolCall.OpenAIResponsesStatus != "" {
			t.Fatalf("strip left Responses tool ids for target %#v: %#v", target, toolCall)
		}
		if stripped[1].ToolCallID != "call_1" || stripped[1].Name != "Read" || stripped[1].Content != "first" {
			t.Fatalf("strip broke tool result pairing for target %#v: %#v", target, stripped[1])
		}
	}
}

func TestSanitizeProviderMessagesForTargetFailClosedLegacyExceptAnthropicFamily(t *testing.T) {
	openaiLegacy := Message{
		Role:                            "assistant",
		Content:                         "legacy openai",
		ReasoningContent:                "think",
		ReasoningSignature:              "encrypted-reasoning",
		ReasoningSignatureSource:        ReasoningSignatureSourceOpenAIResponses,
		OpenAIResponsesReasoningID:      "reasoning_1",
		OpenAIResponsesReasoningStatus:  "completed",
		OpenAIResponsesReasoningSummary: json.RawMessage(`[{"type":"summary_text","text":"summary"}]`),
		ToolCalls: []ToolCallDescriptor{{
			ID:                    "call_1",
			Type:                  "function",
			OpenAIResponsesID:     "item_1",
			OpenAIResponsesCallID: "provider_call_1",
			OpenAIResponsesStatus: "completed",
			Function:              ToolCallFunctionShape{Name: "Read", Arguments: `{"path":"first.go"}`},
		}},
	}
	openaiTarget := ReasoningOrigin{
		Provider:         "openai",
		Endpoint:         "openai_responses:api.openai.com",
		CredentialSource: "codex",
		AccountID:        "acct-1",
		ModelID:          "gpt-5",
	}
	legacyToolResult := Message{Role: "tool", ToolCallID: "call_1", Name: "Read", Content: "first"}
	stripped := sanitizeProviderMessagesForTarget([]Message{openaiLegacy, legacyToolResult}, openaiTarget)
	if len(stripped) != 2 {
		t.Fatalf("legacy openai message count = %d, want 2: %#v", len(stripped), stripped)
	}
	got := stripped[0]
	if got.Content != openaiLegacy.Content || got.ReasoningContent != openaiLegacy.ReasoningContent || got.ToolCalls[0].ID != "call_1" {
		t.Fatalf("legacy openai fail-closed dropped visible fields: %#v", got)
	}
	if got.ReasoningSignature != "" || got.OpenAIResponsesReasoningID != "" || got.ToolCalls[0].OpenAIResponsesID != "" {
		t.Fatalf("legacy openai missing origin should fail-closed: %#v", got)
	}
	if stripped[1].ToolCallID != "call_1" || stripped[1].Content != "first" {
		t.Fatalf("legacy openai fail-closed broke tool result pairing: %#v", stripped[1])
	}

	anthropicLegacy := Message{
		Role:                     "assistant",
		Content:                  "legacy anthropic",
		ReasoningContent:         "think",
		ReasoningSignature:       "anth-sig",
		ReasoningSignatureSource: ReasoningSignatureSourceAnthropic,
	}
	anthropicTarget := ReasoningOrigin{
		Provider:         "anthropic",
		Endpoint:         "anthropic:api.anthropic.com",
		CredentialSource: "static",
		ModelID:          "claude-4",
	}
	followUp := Message{Role: "user", Content: "continue"}
	kept := sanitizeProviderMessagesForTarget([]Message{anthropicLegacy, followUp}, anthropicTarget)
	if len(kept) != 2 {
		t.Fatalf("legacy anthropic message count = %d, want 2: %#v", len(kept), kept)
	}
	if kept[0].ReasoningSignature != "anth-sig" || kept[0].ReasoningSignatureSource != ReasoningSignatureSourceAnthropic {
		t.Fatalf("legacy anthropic same-family should keep signature: %#v", kept[0])
	}

	unsignedAnthropic := anthropicLegacy
	unsignedAnthropic.ReasoningSignatureSource = ""
	keptUnsigned := sanitizeProviderMessagesForTarget([]Message{unsignedAnthropic, followUp}, anthropicTarget)
	if keptUnsigned[0].ReasoningSignature != "anth-sig" {
		t.Fatalf("legacy anthropic empty source should still keep signature: %#v", keptUnsigned[0])
	}

	crossed := sanitizeProviderMessagesForTarget([]Message{anthropicLegacy, followUp}, openaiTarget)
	if crossed[0].ReasoningSignature != "" || crossed[0].ReasoningSignatureSource != "" {
		t.Fatalf("legacy anthropic to openai should strip signature: %#v", crossed[0])
	}
	if crossed[0].Content != anthropicLegacy.Content || crossed[0].ReasoningContent != anthropicLegacy.ReasoningContent {
		t.Fatalf("legacy anthropic to openai dropped visible text: %#v", crossed[0])
	}
}

func TestReasoningOriginFromRequestUsesProviderEndpointCredentialAccountAndModel(t *testing.T) {
	req := StreamRequest{
		Provider:         "OpenAI",
		BaseURL:          "https://api.openai.com/v1",
		OpenAIEndpoint:   "/v1/responses",
		CredentialSource: "codex",
		CredentialID:     "acct-1",
		StableAccountID:  true,
		ProviderModelID:  "gpt-5",
		ModelID:          "ignored-model-id",
	}
	got := reasoningOriginFromRequest(req)
	want := ReasoningOrigin{
		Provider:         "openai",
		Endpoint:         "openai_responses:api.openai.com/v1",
		CredentialSource: "codex",
		AccountID:        "acct-1",
		ModelID:          "gpt-5",
	}
	if !got.Equal(want) {
		t.Fatalf("origin = %#v, want %#v", got, want)
	}

	unstable := req
	unstable.StableAccountID = false
	if got := reasoningOriginFromRequest(unstable); got.AccountID != "" {
		t.Fatalf("unstable account should not populate AccountID: %#v", got)
	}

	fallbackModel := req
	fallbackModel.ProviderModelID = ""
	if got := reasoningOriginFromRequest(fallbackModel); got.ModelID != "ignored-model-id" {
		t.Fatalf("empty ProviderModelID should fall back to ModelID: %#v", got)
	}

	codex := req
	codex.BaseURL = "https://chatgpt.com/backend-api"
	if got := reasoningOriginFromRequest(codex); got.Endpoint != "chatgpt_codex" {
		t.Fatalf("chatgpt host should use chatgpt_codex endpoint identity: %#v", got)
	}
}

func TestSanitizeProviderMessagesForTargetStripsStaticKeyPortAndPathMismatches(t *testing.T) {
	base := StreamRequest{
		Provider:         "openai",
		BaseURL:          "https://proxy.example.test/v1",
		OpenAIEndpoint:   "/v1/chat/completions",
		CredentialSource: "static",
		APIKey:           "sk-static-key-a",
		ProviderModelID:  "gpt-test",
	}
	originA := reasoningOriginFromRequest(base)
	sum := sha256.Sum256([]byte("sk-static-key-a"))
	wantFingerprint := hex.EncodeToString(sum[:])
	if originA.AccountID != wantFingerprint {
		t.Fatalf("static AccountID = %q, want sha256 hex fingerprint", originA.AccountID)
	}
	if strings.Contains(originA.AccountID, "sk-") || originA.AccountID == "sk-static-key-a" {
		t.Fatalf("static AccountID leaked plaintext: %#v", originA)
	}

	otherKey := base
	otherKey.APIKey = "sk-static-key-b"
	originB := reasoningOriginFromRequest(otherKey)
	if originB.AccountID == originA.AccountID {
		t.Fatal("two static keys must not share AccountID")
	}

	sameEndpoint := base
	sameEndpoint.BaseURL = "https://proxy.example.test/v1/?unused=1#frag"
	if got := reasoningOriginFromRequest(sameEndpoint); !got.Equal(originA) {
		t.Fatalf("query/fragment/trailing slash should keep origin: %#v vs %#v", got, originA)
	}
	defaultPort := base
	defaultPort.BaseURL = "https://proxy.example.test:443/v1/"
	if got := reasoningOriginFromRequest(defaultPort); !got.Equal(originA) {
		t.Fatalf("default https port should keep origin: %#v vs %#v", got, originA)
	}

	otherPort := base
	otherPort.BaseURL = "https://proxy.example.test:8443/v1"
	originPort := reasoningOriginFromRequest(otherPort)
	if originPort.Equal(originA) {
		t.Fatal("same host different port must not share origin")
	}

	otherPath := base
	otherPath.BaseURL = "https://proxy.example.test/openai"
	originPath := reasoningOriginFromRequest(otherPath)
	if originPath.Equal(originA) {
		t.Fatal("same host different path must not share origin")
	}

	followUp := Message{Role: "user", Content: "continue"}
	input := []Message{opaqueAssistantMessageForSanitizeTest(originA), followUp}

	kept := sanitizeProviderMessagesForTarget(input, originA)
	if kept[0].ReasoningSignature != "encrypted-reasoning" {
		t.Fatalf("same key/same endpoint should keep opaque metadata: %#v", kept[0])
	}
	keptNormalized := sanitizeProviderMessagesForTarget(input, reasoningOriginFromRequest(sameEndpoint))
	if keptNormalized[0].ReasoningSignature != "encrypted-reasoning" {
		t.Fatalf("normalized same endpoint should keep opaque metadata: %#v", keptNormalized[0])
	}

	mustStrip := []struct {
		name   string
		target ReasoningOrigin
	}{
		{name: "two static keys", target: originB},
		{name: "different port", target: originPort},
		{name: "different path", target: originPath},
	}
	for _, test := range mustStrip {
		stripped := sanitizeProviderMessagesForTarget(input, test.target)
		if stripped[0].ReasoningSignature != "" || !stripped[0].ReasoningOrigin.IsZero() {
			t.Fatalf("%s must strip opaque metadata: %#v", test.name, stripped[0])
		}
		if stripped[0].Content != input[0].Content {
			t.Fatalf("%s dropped visible text: %#v", test.name, stripped[0])
		}
	}
}

func opaqueAssistantMessageForSanitizeTest(origin ReasoningOrigin) Message {
	return Message{
		Role:                            "assistant",
		Content:                         "I will inspect both files.",
		ReasoningContent:                "Inspect both files.",
		ReasoningSignature:              "encrypted-reasoning",
		ReasoningSignatureSource:        ReasoningSignatureSourceOpenAIResponses,
		OpenAIResponsesReasoningID:      "reasoning_1",
		OpenAIResponsesReasoningStatus:  "completed",
		OpenAIResponsesReasoningSummary: json.RawMessage(`[{"type":"summary_text","text":"summary"}]`),
		ReasoningOrigin:                 origin,
		ToolCalls: []ToolCallDescriptor{{
			ID:                    "call_1",
			Type:                  "function",
			OpenAIResponsesID:     "item_1",
			OpenAIResponsesCallID: "provider_call_1",
			OpenAIResponsesStatus: "completed",
			Function:              ToolCallFunctionShape{Name: "Read", Arguments: `{"path":"first.go"}`},
		}},
	}
}

func TestRouterStreamSanitizesOpaqueMetadataAgainstResolvedTarget(t *testing.T) {
	openAI := &recordingModelAdapter{}
	creds := &stubCredentialResolver{token: "managed-token", accountID: "acct-1", stable: true}
	router := &Router{
		openai:      openAI,
		credentials: creds,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:               "channel-codex",
			Provider:         "openai",
			BaseURL:          "https://api.openai.com/v1",
			CredentialSource: "codex",
			Model:            "gpt-5",
			OpenAIEndpoint:   "/v1/responses",
		}},
	}
	compatible := ReasoningOrigin{
		Provider:         "openai",
		Endpoint:         "openai_responses:api.openai.com/v1",
		CredentialSource: "codex",
		AccountID:        "acct-1",
		ModelID:          "gpt-5",
	}
	toolResult := Message{Role: "tool", ToolCallID: "call_1", Name: "Read", Content: "first"}
	if err := router.Stream(context.Background(), StreamRequest{
		ModelID:  "channel-codex",
		Messages: []Message{opaqueAssistantMessageForSanitizeTest(compatible), toolResult},
	}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("compatible Stream: %v", err)
	}
	kept := openAI.request.Messages
	if len(kept) != 2 {
		t.Fatalf("compatible message count = %d, want 2: %#v", len(kept), kept)
	}
	if kept[0].ReasoningSignature != "encrypted-reasoning" || kept[0].OpenAIResponsesReasoningID != "reasoning_1" || kept[0].ToolCalls[0].OpenAIResponsesID != "item_1" {
		t.Fatalf("compatible send dropped opaque group: %#v", kept[0])
	}

	incompatible := compatible
	incompatible.ModelID = "gpt-4.1"
	if err := router.Stream(context.Background(), StreamRequest{
		ModelID:  "channel-codex",
		Messages: []Message{opaqueAssistantMessageForSanitizeTest(incompatible), toolResult},
	}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("incompatible Stream: %v", err)
	}
	stripped := openAI.request.Messages
	if len(stripped) != 2 {
		t.Fatalf("incompatible message count = %d, want 2: %#v", len(stripped), stripped)
	}
	got := stripped[0]
	if got.Content != "I will inspect both files." || got.ReasoningContent != "Inspect both files." || got.ToolCalls[0].ID != "call_1" {
		t.Fatalf("incompatible send dropped visible fields: %#v", got)
	}
	if got.ReasoningSignature != "" || got.OpenAIResponsesReasoningID != "" || got.ToolCalls[0].OpenAIResponsesID != "" || !got.ReasoningOrigin.IsZero() {
		t.Fatalf("incompatible send left opaque group: %#v", got)
	}
	if stripped[1].ToolCallID != "call_1" || stripped[1].Content != "first" {
		t.Fatalf("incompatible send broke tool result pairing: %#v", stripped[1])
	}

	legacy := opaqueAssistantMessageForSanitizeTest(ReasoningOrigin{})
	if err := router.Stream(context.Background(), StreamRequest{
		ModelID:  "channel-codex",
		Messages: []Message{legacy, toolResult},
	}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("legacy Stream: %v", err)
	}
	legacyGot := openAI.request.Messages[0]
	if legacyGot.ReasoningSignature != "" || legacyGot.OpenAIResponsesReasoningID != "" || legacyGot.ToolCalls[0].OpenAIResponsesID != "" {
		t.Fatalf("legacy openai missing origin should fail-closed on send: %#v", legacyGot)
	}

	anthropicAdapter := &recordingModelAdapter{}
	anthropicRouter := &Router{
		anthropic: anthropicAdapter,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:               "channel-anthropic",
			Provider:         "anthropic",
			BaseURL:          "https://api.anthropic.com",
			CredentialSource: "static",
			Model:            "claude-4",
		}},
	}
	anthropicLegacy := Message{
		Role:                     "assistant",
		Content:                  "legacy anthropic",
		ReasoningContent:         "think",
		ReasoningSignature:       "anth-sig",
		ReasoningSignatureSource: ReasoningSignatureSourceAnthropic,
	}
	if err := anthropicRouter.Stream(context.Background(), StreamRequest{
		ModelID:  "channel-anthropic",
		Messages: []Message{anthropicLegacy, {Role: "user", Content: "continue"}},
	}, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("legacy anthropic Stream: %v", err)
	}
	if len(anthropicAdapter.request.Messages) == 0 {
		t.Fatalf("legacy anthropic send dropped messages")
	}
	if got := anthropicAdapter.request.Messages[0]; got.ReasoningSignature != "anth-sig" {
		t.Fatalf("legacy anthropic same-family send should keep signature: %#v", got)
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
	if openAI.request.APIKey == "cursor-byok-local" {
		t.Fatal("static provider received catalog sentinel")
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
	if openAI.request.APIKey == "cursor-byok-local" {
		t.Fatal("managed provider received catalog sentinel")
	}
}

func TestCLICatalogIDsApplyRuntimeCredentialsToSyntheticManagedProviders(t *testing.T) {
	const (
		sentinel     = "cursor-byok-local"
		codexToken   = "codex-runtime-token"
		grokToken    = "grok-runtime-token"
		codexAccount = "codex:synthetic"
		grokAccount  = "grok:synthetic"
		chatgptID    = "chatgpt-acct-1"
	)

	codexCap := &managedProviderCapture{}
	codexProvider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !codexCap.recordHTTP(t, request, sentinel) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeOpenAIResponsesSSE(writer, "ok")
	}))
	defer codexProvider.Close()

	grokCap := &managedProviderCapture{}
	grokProvider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !grokCap.recordHTTP(t, request, sentinel) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeOpenAIChatSSE(writer, "ok")
	}))
	defer grokProvider.Close()

	codexTarget, err := url.Parse(codexProvider.URL)
	if err != nil {
		t.Fatal(err)
	}
	grokTarget, err := url.Parse(grokProvider.URL)
	if err != nil {
		t.Fatal(err)
	}

	var originalURLs cliCatalogAtomicStringSlice
	client := &http.Client{Transport: rewriteHostRoundTripper{
		byHost: map[string]*url.URL{
			"chatgpt.com": codexTarget,
			"api.x.ai":    grokTarget,
		},
		onRequest: func(req *http.Request) {
			originalURLs.Add(req.URL.String())
		},
	}}

	manager := newCLICatalogTestManager(t, []serverconfig.ModelAdapterConfig{
		{
			Sort:            1,
			DisplayName:     "static-byok",
			Type:            "openai",
			BaseURL:         "https://static.example/v1",
			APIKey:          "static-runtime-secret",
			TooltipData:     "static-byok",
			ModelID:         "static-model",
			ReasoningEffort: "medium",
			OpenAIEndpoint:  modelchannel.OpenAIEndpointChatCompletions,
		},
		{
			Sort:             2,
			DisplayName:      "managed-codex",
			Type:             "openai",
			BaseURL:          "https://ignored.example/v1",
			CredentialSource: "codex",
			TooltipData:      "managed-codex",
			ModelID:          "managed-model",
			ReasoningEffort:  "medium",
			OpenAIEndpoint:   modelchannel.OpenAIEndpointChatCompletions,
		},
		{
			Sort:             3,
			DisplayName:      "managed-grok",
			Type:             "openai",
			BaseURL:          "https://ignored.example/v1",
			CredentialSource: "grok",
			TooltipData:      "managed-grok",
			ModelID:          "grok-model",
			ReasoningEffort:  "medium",
			OpenAIEndpoint:   modelchannel.OpenAIEndpointResponses,
		},
	})
	idByName := map[string]string{}
	for _, adapter := range manager.Current().ModelAdapters {
		idByName[adapter.DisplayName] = adapter.ID
	}
	codexID := idByName["managed-codex"]
	grokID := idByName["managed-grok"]
	if codexID == "" || grokID == "" {
		t.Fatalf("missing catalog IDs: %#v", idByName)
	}

	codexCh, err := manager.SelectChannelForModel(context.Background(), codexID)
	if err != nil {
		t.Fatalf("codex SelectChannelForModel: %v", err)
	}
	if codexCh.APIKey != "" || codexCh.APIKey == sentinel || codexCh.BaseURL != subscriptionauth.CodexResponsesURL {
		t.Fatalf("codex channel = %+v", codexCh)
	}
	if codexCh.OpenAIEndpoint != modelchannel.OpenAIEndpointResponses || codexCh.Model != "managed-model" {
		t.Fatalf("codex endpoint/model = %q %q", codexCh.OpenAIEndpoint, codexCh.Model)
	}

	grokCh, err := manager.SelectChannelForModel(context.Background(), grokID)
	if err != nil {
		t.Fatalf("grok SelectChannelForModel: %v", err)
	}
	if grokCh.APIKey != "" || grokCh.APIKey == sentinel || grokCh.BaseURL != subscriptionauth.GrokAPIBaseURL {
		t.Fatalf("grok channel = %+v", grokCh)
	}
	if grokCh.OpenAIEndpoint != modelchannel.OpenAIEndpointChatCompletions || grokCh.Model != "grok-model" {
		t.Fatalf("grok endpoint/model = %q %q", grokCh.OpenAIEndpoint, grokCh.Model)
	}

	creds := &sourceCredentialStub{creds: map[subscriptionauth.CredentialSource]subscriptionauth.Credential{
		subscriptionauth.CredentialSourceCodex: {
			Provider:         subscriptionauth.ProviderCodex,
			AccountID:        codexAccount,
			AccessToken:      codexToken,
			ChatGPTAccountID: chatgptID,
			StableAccountID:  true,
		},
		subscriptionauth.CredentialSourceGrok: {
			Provider:        subscriptionauth.ProviderGrok,
			AccountID:       grokAccount,
			AccessToken:     grokToken,
			StableAccountID: true,
		},
	}}
	retry := instantRetry()
	retry.maxAttempts = 1
	openai := &capturingModelAdapter{inner: &OpenAIAdapter{client: client, retry: retry}}
	router := &Router{
		openai:      openai,
		credentials: creds,
		resolver:    manager,
	}
	fallback := NewFallbackAwareRouter(router, manager)
	req := func(modelID string) StreamRequest {
		return StreamRequest{
			ModelID:  modelID,
			Messages: []Message{{Role: "user", Content: "ping"}},
		}
	}
	if err := fallback.Stream(context.Background(), req(codexID), func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("codex Stream: %v", err)
	}
	if err := fallback.Stream(context.Background(), req(grokID), func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("grok Stream: %v", err)
	}

	if got := creds.resolveCount(subscriptionauth.CredentialSourceCodex); got != 1 {
		t.Fatalf("codex Resolve calls = %d, want 1", got)
	}
	if got := creds.resolveCount(subscriptionauth.CredentialSourceGrok); got != 1 {
		t.Fatalf("grok Resolve calls = %d, want 1", got)
	}
	if creds.refreshN != 0 {
		t.Fatalf("ResolveAfterUnauthorized calls = %d, want 0", creds.refreshN)
	}

	captured := openai.snapshot()
	if len(captured) != 2 {
		t.Fatalf("adapter requests = %d, want 2", len(captured))
	}
	assertManagedStreamRequest(t, captured[0], managedStreamWant{
		channelID:        codexID,
		channelName:      "managed-codex",
		providerModel:    "managed-model",
		baseURL:          subscriptionauth.CodexResponsesURL,
		endpoint:         modelchannel.OpenAIEndpointResponses,
		source:           "codex",
		token:            codexToken,
		accountID:        codexAccount,
		chatGPTAccountID: chatgptID,
		sentinel:         sentinel,
		reasoningEffort:  "medium",
	})
	assertManagedStreamRequest(t, captured[1], managedStreamWant{
		channelID:       grokID,
		channelName:     "managed-grok",
		providerModel:   "grok-model",
		baseURL:         subscriptionauth.GrokAPIBaseURL,
		endpoint:        modelchannel.OpenAIEndpointChatCompletions,
		source:          "grok",
		token:           grokToken,
		accountID:       grokAccount,
		sentinel:        sentinel,
		reasoningEffort: "medium",
	})

	codexHits, codexAuth, codexPath, codexModel, codexOriginator, codexAccountHdr := codexCap.snapshot()
	if codexHits != 1 || codexAuth != codexToken || codexAuth == sentinel {
		t.Fatalf("codex provider hits=%d auth=%q", codexHits, codexAuth)
	}
	if codexPath != "/backend-api/codex/responses" {
		t.Fatalf("codex path = %q", codexPath)
	}
	if codexModel != "managed-model" {
		t.Fatalf("codex body model = %q", codexModel)
	}
	if codexOriginator != "codex_cli_rs" || codexAccountHdr != chatgptID {
		t.Fatalf("codex metadata originator=%q account=%q", codexOriginator, codexAccountHdr)
	}

	grokHits, grokAuth, grokPath, grokModel, grokOriginator, grokAccountHdr := grokCap.snapshot()
	if grokHits != 1 || grokAuth != grokToken || grokAuth == sentinel {
		t.Fatalf("grok provider hits=%d auth=%q", grokHits, grokAuth)
	}
	if grokPath != "/v1/chat/completions" {
		t.Fatalf("grok path = %q", grokPath)
	}
	if grokModel != "grok-model" {
		t.Fatalf("grok body model = %q", grokModel)
	}
	if grokOriginator != "" || grokAccountHdr != "" {
		t.Fatalf("grok must not send Codex identity headers originator=%q account=%q", grokOriginator, grokAccountHdr)
	}

	gotURLs := originalURLs.Load()
	if len(gotURLs) != 2 {
		t.Fatalf("original URLs = %#v, want 2 official targets", gotURLs)
	}
	if gotURLs[0] != subscriptionauth.CodexResponsesURL {
		t.Fatalf("codex original URL = %q, want pinned official endpoint", gotURLs[0])
	}
	wantGrokURL := OpenAIEndpointURL(subscriptionauth.GrokAPIBaseURL, modelchannel.OpenAIEndpointChatCompletions)
	if gotURLs[1] != wantGrokURL {
		t.Fatalf("grok original URL = %q, want %q", gotURLs[1], wantGrokURL)
	}
	if err := fallback.Stream(context.Background(), req("stale-catalog-id"), func(ModelEvent) error { return nil }); !errors.Is(err, legacyruntime.ErrChannelNotAvailable) {
		t.Fatalf("stale Stream error = %v, want ErrChannelNotAvailable", err)
	}
	if creds.resolveCount(subscriptionauth.CredentialSourceCodex) != 1 || creds.resolveCount(subscriptionauth.CredentialSourceGrok) != 1 {
		t.Fatalf("stale ID must not refresh managed credentials")
	}
}

type managedStreamWant struct {
	channelID        string
	channelName      string
	providerModel    string
	baseURL          string
	endpoint         string
	source           string
	token            string
	accountID        string
	chatGPTAccountID string
	sentinel         string
	reasoningEffort  string
}

func assertManagedStreamRequest(t *testing.T, got StreamRequest, want managedStreamWant) {
	t.Helper()
	if got.ResolvedChannelID != want.channelID || got.ResolvedChannelName != want.channelName {
		t.Fatalf("channel metadata = %q %q, want %q %q", got.ResolvedChannelID, got.ResolvedChannelName, want.channelID, want.channelName)
	}
	if got.ProviderModelID != want.providerModel || got.ModelID != want.channelID {
		t.Fatalf("model identity provider=%q catalog=%q, want provider=%q catalog=%q", got.ProviderModelID, got.ModelID, want.providerModel, want.channelID)
	}
	if got.BaseURL != want.baseURL || got.OpenAIEndpoint != want.endpoint {
		t.Fatalf("target = %q %q, want %q %q", got.BaseURL, got.OpenAIEndpoint, want.baseURL, want.endpoint)
	}
	if got.CredentialSource != want.source || got.CredentialID != want.accountID || got.ChatGPTAccountID != want.chatGPTAccountID {
		t.Fatalf("credential metadata source=%q id=%q chatgpt=%q", got.CredentialSource, got.CredentialID, got.ChatGPTAccountID)
	}
	if got.APIKey != want.token || got.APIKey == want.sentinel || got.APIKey == "" {
		t.Fatalf("APIKey = %q, want runtime token", got.APIKey)
	}
	if want.reasoningEffort != "" && got.ReasoningEffort != want.reasoningEffort {
		t.Fatalf("ReasoningEffort = %q, want %q", got.ReasoningEffort, want.reasoningEffort)
	}
}

func newCLICatalogTestManager(t *testing.T, adapters []serverconfig.ModelAdapterConfig) *serverconfig.Manager {
	t.Helper()
	root := t.TempDir()
	store := serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	manager, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	cfg := serverconfig.DefaultConfig()
	cfg.ModelAdapters = adapters
	if _, err := manager.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return manager
}

type capturingModelAdapter struct {
	inner    ModelAdapter
	mu       sync.Mutex
	requests []StreamRequest
}

func (adapter *capturingModelAdapter) Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	adapter.mu.Lock()
	adapter.requests = append(adapter.requests, req)
	adapter.mu.Unlock()
	if adapter.inner == nil {
		return nil
	}
	return adapter.inner.Stream(ctx, req, sink)
}

func (adapter *capturingModelAdapter) snapshot() []StreamRequest {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	out := make([]StreamRequest, len(adapter.requests))
	copy(out, adapter.requests)
	return out
}

type managedProviderCapture struct {
	mu         sync.Mutex
	hits       int
	auth       string
	path       string
	model      string
	originator string
	accountID  string
}

func (cap *managedProviderCapture) recordHTTP(t *testing.T, request *http.Request, sentinel string) bool {
	t.Helper()
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	var body map[string]any
	_ = json.NewDecoder(request.Body).Decode(&body)
	model, _ := body["model"].(string)
	cap.mu.Lock()
	cap.hits++
	cap.auth = token
	cap.path = request.URL.Path
	cap.model = model
	cap.originator = request.Header.Get("originator")
	cap.accountID = request.Header.Get("ChatGPT-Account-Id")
	cap.mu.Unlock()
	if token == sentinel || token == "" {
		t.Errorf("provider received catalog sentinel or empty key %q", token)
		return false
	}
	return true
}

func (cap *managedProviderCapture) snapshot() (hits int, auth, path, model, originator, accountID string) {
	cap.mu.Lock()
	defer cap.mu.Unlock()
	return cap.hits, cap.auth, cap.path, cap.model, cap.originator, cap.accountID
}

type cliCatalogAtomicStringSlice struct {
	mu     sync.Mutex
	values []string
}

func (box *cliCatalogAtomicStringSlice) Add(value string) {
	box.mu.Lock()
	box.values = append(box.values, value)
	box.mu.Unlock()
}

func (box *cliCatalogAtomicStringSlice) Load() []string {
	box.mu.Lock()
	defer box.mu.Unlock()
	out := make([]string, len(box.values))
	copy(out, box.values)
	return out
}

type sourceCredentialStub struct {
	mu       sync.Mutex
	creds    map[subscriptionauth.CredentialSource]subscriptionauth.Credential
	calls    map[subscriptionauth.CredentialSource]int
	refreshN int
}

func (stub *sourceCredentialStub) Resolve(_ context.Context, source subscriptionauth.CredentialSource) (subscriptionauth.Credential, error) {
	normalized := subscriptionauth.NormalizeCredentialSource(string(source))
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.calls == nil {
		stub.calls = map[subscriptionauth.CredentialSource]int{}
	}
	stub.calls[normalized]++
	cred, ok := stub.creds[normalized]
	if !ok {
		return subscriptionauth.Credential{}, fmt.Errorf("unexpected credential source %q", source)
	}
	return cred, nil
}

func (stub *sourceCredentialStub) ResolveAfterUnauthorized(context.Context, subscriptionauth.CredentialSource, string) (subscriptionauth.Credential, error) {
	stub.mu.Lock()
	stub.refreshN++
	stub.mu.Unlock()
	return subscriptionauth.Credential{}, fmt.Errorf("unexpected ResolveAfterUnauthorized")
}

func (stub *sourceCredentialStub) MarkQuotaExhausted(context.Context, string) error {
	return nil
}

func (stub *sourceCredentialStub) RefreshUsage(context.Context, subscriptionauth.ProviderKind) (subscriptionauth.UsageSnapshot, error) {
	return subscriptionauth.UsageSnapshot{}, nil
}

func (stub *sourceCredentialStub) resolveCount(source subscriptionauth.CredentialSource) int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.calls == nil {
		return 0
	}
	return stub.calls[subscriptionauth.NormalizeCredentialSource(string(source))]
}

func writeOpenAIResponsesSSE(writer http.ResponseWriter, text string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output_text\":%q}}\n\n", text)
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
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
			wantHits := 1
			if status == http.StatusTooManyRequests {
				wantHits = providerRequestMaxAttempts
			}
			if hits != wantHits {
				t.Fatalf("hits=%d, want %d", hits, wantHits)
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
