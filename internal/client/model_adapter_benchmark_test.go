package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	var gotAuth string
	var gotOriginator string
	var gotAccountID string
	var gotXAIHeader string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotOriginator = r.Header.Get("originator")
		gotAccountID = r.Header.Get("ChatGPT-Account-Id")
		gotXAIHeader = r.Header.Get("x-xai-token-auth")
		gotBody, _ = ioReadAllLimited(r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	stubModelAdapterCredentialForSource(t, subscriptionauth.CredentialSourceCodex, subscriptionauth.Credential{
		AccessToken:      token,
		ChatGPTAccountID: "acct-should-not-be-copied",
	}, nil)

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:      "Managed Codex",
		Type:             "openai",
		BaseURL:          server.URL + "/v1",
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
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want Bearer <resolved token>", gotAuth)
	}
	if gotOriginator != "" {
		t.Fatalf("TestModelAdapter 不应复制 Codex originator，实际 = %q", gotOriginator)
	}
	if gotAccountID != "" {
		t.Fatalf("TestModelAdapter 不应复制 ChatGPT-Account-Id，实际 = %q", gotAccountID)
	}
	if gotXAIHeader != "" {
		t.Fatalf("TestModelAdapter 不应发送 x-xai-token-auth，实际 = %q", gotXAIHeader)
	}
	if bytes.Contains(gotBody, []byte(`"store"`)) && bytes.Contains(gotBody, []byte("false")) {
		// chat/completions 路径本身没有 store 字段；若出现则说明复制了 Responses/Codex body 逻辑。
		t.Fatalf("TestModelAdapter 不应复制 Codex store:false body：%s", gotBody)
	}
	if adapter.APIKey != "" {
		t.Fatalf("原始 adapter.APIKey 被写回：%q", adapter.APIKey)
	}
	if strings.Contains(result.Error, token) || strings.Contains(result.RawResponse, token) || strings.Contains(result.SummaryText, token) {
		t.Fatalf("测速结果泄漏了临时 token：%+v", result)
	}
	encoded, err := json.Marshal(service.GetModelAdapterTestResults())
	if err != nil {
		t.Fatalf("marshal stored results: %v", err)
	}
	if bytes.Contains(encoded, []byte(token)) {
		t.Fatalf("测速缓存泄漏了临时 token：%s", encoded)
	}
}

func ioReadAllLimited(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	buf := &bytes.Buffer{}
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}
