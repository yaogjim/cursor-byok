package client

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/subscriptionauth"
)

func TestNormalizeModelAdapterTestProviderReasoningPreservesBlank(t *testing.T) {
	adapter := serverconfig.ModelAdapterConfig{Type: "openai", ReasoningEffort: ""}

	if got := normalizeModelAdapterTestProviderReasoning(adapter); got != "" {
		t.Fatalf("reasoning effort = %q, want blank", got)
	}
}

func TestModelAdapterManagedResolvesTokenWithoutWritingBack(t *testing.T) {
	const token = "managed-test-token-secret"
	stubModelAdapterCredentialForSource(t, subscriptionauth.CredentialSourceCodex, subscriptionauth.Credential{
		AccessToken:      token,
		AccountID:        "credential-account",
		ChatGPTAccountID: "chatgpt-account",
	}, nil)

	originalStream := streamModelAdapterTestOpenAI
	t.Cleanup(func() { streamModelAdapterTestOpenAI = originalStream })
	var captured modeladapter.StreamRequest
	streamModelAdapterTestOpenAI = func(_ context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
		captured = req
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "done"}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, OutputTokens: 1})
	}

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:      "Managed Codex",
		Type:             "openai",
		BaseURL:          "http://127.0.0.1:18091/v1",
		CredentialSource: "codex",
		TooltipData:      "备注",
		ModelID:          "gpt-5.1",
		OpenAIEndpoint:   "/v1/chat/completions",
	}
	service := &ProxyService{modelTestResults: map[string]ModelAdapterTestResult{}}
	result, err := service.TestModelAdapter(adapter)
	if err != nil {
		t.Fatalf("TestModelAdapter 返回错误：%v", err)
	}
	if result.Status != string(ModelAdapterTestStatusSuccess) {
		t.Fatalf("status = %q error=%q raw=%q", result.Status, result.Error, result.RawResponse)
	}
	if captured.BaseURL != subscriptionauth.CodexResponsesURL || captured.OpenAIEndpoint != "/v1/responses" {
		t.Fatalf("Codex 测试地址 = %q %q", captured.BaseURL, captured.OpenAIEndpoint)
	}
	if captured.APIKey != token || captured.CredentialID != "credential-account" || captured.ChatGPTAccountID != "chatgpt-account" {
		t.Fatalf("运行时凭据元数据不匹配：key=%t account=%q chatgptAccount=%q", captured.APIKey == token, captured.CredentialID, captured.ChatGPTAccountID)
	}
	if adapter.APIKey != "" || adapter.BaseURL != "http://127.0.0.1:18091/v1" {
		t.Fatalf("原始 adapter 被写回：apiKey=%q baseURL=%q", adapter.APIKey, adapter.BaseURL)
	}
	encoded, err := json.Marshal(service.GetModelAdapterTestResults())
	if err != nil {
		t.Fatalf("marshal stored results: %v", err)
	}
	if bytes.Contains(encoded, []byte(token)) {
		t.Fatalf("测速缓存泄漏了临时 token：%s", encoded)
	}
}

func TestModelAdapterTestRequestHashIncludesOpenAIImageGenerationEnabled(t *testing.T) {
	base := serverconfig.ModelAdapterConfig{
		DisplayName:    "gpt",
		Type:           "openai",
		BaseURL:        "https://api.example.com/v1",
		APIKey:         "test-key",
		TooltipData:    "gpt",
		ModelID:        "gpt-test",
		OpenAIEndpoint: "/v1/responses",
	}
	off := buildModelAdapterTestRequestHash(base)
	enabled := base
	enabled.OpenAIImageGenerationEnabled = true
	on := buildModelAdapterTestRequestHash(enabled)
	if off == "" || off == on {
		t.Fatalf("toggling OpenAIImageGenerationEnabled must change request hash: off=%q on=%q", off, on)
	}
}

func TestModelAdapterTestStreamRequestProjectsOpenAIImageGenerationEnabled(t *testing.T) {
	originalStream := streamModelAdapterTestOpenAI
	t.Cleanup(func() { streamModelAdapterTestOpenAI = originalStream })
	var captured modeladapter.StreamRequest
	streamModelAdapterTestOpenAI = func(_ context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
		captured = req
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "done"}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, OutputTokens: 1})
	}

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:                  "gpt",
		Type:                         "openai",
		BaseURL:                      "https://api.example.com/v1",
		APIKey:                       "test-key",
		TooltipData:                  "gpt",
		ModelID:                      "gpt-test",
		OpenAIEndpoint:               "/v1/responses",
		OpenAIImageGenerationEnabled: true,
	}
	service := &ProxyService{modelTestResults: map[string]ModelAdapterTestResult{}}
	if _, err := service.TestModelAdapter(adapter); err != nil {
		t.Fatalf("TestModelAdapter 返回错误：%v", err)
	}
	if !captured.OpenAIImageGenerationEnabled {
		t.Fatal("StreamRequest lost OpenAIImageGenerationEnabled")
	}
}
