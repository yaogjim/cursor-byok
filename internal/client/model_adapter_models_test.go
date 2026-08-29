package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"cursor/internal/subscriptionauth"

	serverconfig "cursor/internal/backend/server/config"
)

func TestBuildModelListEndpointCandidates(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		baseURL  string
		want     []string
	}{
		{
			name:     "openai 带版本段不再补 v1",
			provider: "openai",
			baseURL:  "https://api.openai.com/v1",
			want:     []string{"https://api.openai.com/v1/models"},
		},
		{
			name:     "openai 裸域名优先试 v1",
			provider: "openai",
			baseURL:  "https://api.openai.com",
			want:     []string{"https://api.openai.com/v1/models", "https://api.openai.com/models"},
		},
		{
			name:     "anthropic 裸域名优先试 v1",
			provider: "anthropic",
			baseURL:  "https://api.anthropic.com",
			want:     []string{"https://api.anthropic.com/v1/models", "https://api.anthropic.com/models"},
		},
		{
			name:     "anthropic 带版本段不再补 v1",
			provider: "anthropic",
			baseURL:  "https://api.anthropic.com/v1",
			want:     []string{"https://api.anthropic.com/v1/models"},
		},
		{
			name:     "剥离 chat completions 后缀",
			provider: "openai",
			baseURL:  "https://api.example.com/v1/chat/completions",
			want:     []string{"https://api.example.com/v1/models"},
		},
		{
			name:     "剥离 responses 后缀",
			provider: "openai",
			baseURL:  "https://api.example.com/v1/responses",
			want:     []string{"https://api.example.com/v1/models"},
		},
		{
			name:     "剥离 anthropic messages 后缀",
			provider: "anthropic",
			baseURL:  "https://api.example.com/v1/messages",
			want:     []string{"https://api.example.com/v1/models"},
		},
		{
			name:     "已填到 models 地址本身则原样使用",
			provider: "openai",
			baseURL:  "https://api.example.com/openai/v1/models",
			want:     []string{"https://api.example.com/openai/v1/models"},
		},
		{
			name:     "自定义网关前缀会补 v1",
			provider: "openai",
			baseURL:  "https://gateway.example.com/proxy",
			want: []string{
				"https://gateway.example.com/proxy/v1/models",
				"https://gateway.example.com/proxy/models",
			},
		},
		{
			name:     "尾部斜杠不影响推导",
			provider: "anthropic",
			baseURL:  "  https://api.anthropic.com/v1/  ",
			want:     []string{"https://api.anthropic.com/v1/models"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule, ok := modelListProviderRules[test.provider]
			if !ok {
				t.Fatalf("provider %q 没有对应规则", test.provider)
			}
			got := buildModelListEndpointCandidates(rule, test.baseURL)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("buildModelListEndpointCandidates(%q) = %v, want %v", test.baseURL, got, test.want)
			}
		})
	}
}

func TestFetchModelAdapterModelsOpenAIUsesBearer(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotAnthropicVersion string
	var gotAPIKeyHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAnthropicVersion = r.Header.Get("anthropic-version")
		gotAPIKeyHeader = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"},{"id":"gpt-4o"}]}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	if gotPath != "/v1/models" {
		t.Fatalf("请求路径 = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotAPIKeyHeader != "" {
		t.Fatalf("openai 不应发送 x-api-key，实际 = %q", gotAPIKeyHeader)
	}
	if gotAnthropicVersion != "" {
		t.Fatalf("openai 不应发送 anthropic-version，实际 = %q", gotAnthropicVersion)
	}
	want := []string{"gpt-4o", "gpt-5"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
}

func TestFetchModelAdapterModelsAnthropicUsesAPIKeyHeader(t *testing.T) {
	var gotAuth string
	var gotAPIKeyHeader string
	var gotAnthropicVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKeyHeader = r.Header.Get("x-api-key")
		gotAnthropicVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4"}],"has_more":false}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "anthropic",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	if gotAPIKeyHeader != "sk-ant-test" {
		t.Fatalf("x-api-key = %q, want sk-ant-test", gotAPIKeyHeader)
	}
	if gotAnthropicVersion != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", gotAnthropicVersion)
	}
	if gotAuth != "" {
		t.Fatalf("anthropic 不应发送 Authorization，实际 = %q", gotAuth)
	}
	want := []string{"claude-sonnet-4"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
}

func TestFetchModelAdapterModelsAnthropicFollowsCursor(t *testing.T) {
	var requestedQueries []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedQueries = append(requestedQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-a"}],"has_more":true,"last_id":"claude-a"}`))
		case "claude-a":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-c"}],"has_more":false}`))
		}
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "anthropic",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	want := []string{"claude-a", "claude-b", "claude-c"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
	if len(requestedQueries) != 3 {
		t.Fatalf("请求次数 = %d, want 3（两次翻页后停止）", len(requestedQueries))
	}
	for _, query := range requestedQueries {
		if !strings.Contains(query, "limit=1000") {
			t.Fatalf("翻页请求缺少 limit 参数：%q", query)
		}
	}
	if !strings.Contains(requestedQueries[1], "after_id=claude-a") {
		t.Fatalf("第二页未带上游标：%q", requestedQueries[1])
	}
}

func TestFetchModelAdapterModelsOpenAIDoesNotPaginate(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.RawQuery != "" {
			t.Errorf("openai 不应附加分页参数，实际 = %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}],"has_more":true,"last_id":"gpt-5"}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	if _, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	}); err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	if requestCount != 1 {
		t.Fatalf("请求次数 = %d, want 1（openai 忽略 has_more）", requestCount)
	}
}

func TestFetchModelAdapterModelsReadsLargeBody(t *testing.T) {
	models := make([]map[string]string, 0, 400)
	for index := 0; index < 400; index++ {
		models = append(models, map[string]string{
			"id": "vendor/model-with-a-fairly-long-identifier-" + strings.Repeat("x", 40) + "-" + string(rune('a'+index%26)) + strconv.Itoa(index),
		})
	}
	body, err := json.Marshal(map[string]any{"data": models})
	if err != nil {
		t.Fatalf("构造响应失败：%v", err)
	}
	if len(body) <= modelAdapterTestMaxErrorBodyBytes {
		t.Fatalf("测试响应体只有 %d 字节，需要大于 %d 才能覆盖截断场景", len(body), modelAdapterTestMaxErrorBodyBytes)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}
	if len(result.Models) != len(models) {
		t.Fatalf("Models 数量 = %d, want %d", len(result.Models), len(models))
	}
}

func TestFetchModelAdapterModelsSupportsStringItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":["gpt-4o","gpt-4.1"]}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}
	want := []string{"gpt-4.1", "gpt-4o"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
}

func TestFetchModelAdapterModelsRejectsPaginationTruncation(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-model"}],"has_more":true,"last_id":"next"}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	_, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "anthropic",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-ant-test",
	})
	if err == nil || !strings.Contains(err.Error(), "结果可能不完整") {
		t.Fatalf("期望分页截断错误，实际 = %v", err)
	}
	if requestCount != modelAdapterListMaxPages {
		t.Fatalf("请求次数 = %d, want %d", requestCount, modelAdapterListMaxPages)
	}
}

func TestFetchModelAdapterModelsRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		request ModelAdapterModelsRequest
	}{
		{name: "未知类型", request: ModelAdapterModelsRequest{Type: "gemini", BaseURL: "https://x.com", APIKey: "k"}},
		{name: "缺少地址", request: ModelAdapterModelsRequest{Type: "openai", APIKey: "k"}},
		{name: "缺少密钥", request: ModelAdapterModelsRequest{Type: "openai", BaseURL: "https://x.com"}},
		{name: "static 缺少密钥", request: ModelAdapterModelsRequest{Type: "openai", BaseURL: "https://x.com", CredentialSource: "static"}},
		{name: "非法 credentialSource", request: ModelAdapterModelsRequest{Type: "openai", BaseURL: "https://x.com", APIKey: "k", CredentialSource: "vault"}},
	}

	service := &ProxyService{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.FetchModelAdapterModels(test.request); err == nil {
				t.Fatal("期望返回错误，实际为 nil")
			}
		})
	}
}

func TestMain(m *testing.M) {
	previous := modelListHTTPDo
	modelListHTTPDo = func(req *http.Request) (*http.Response, error) {
		if req != nil && req.URL != nil && strings.EqualFold(req.URL.Host, "chatgpt.com") {
			return nil, errors.New("real chatgpt.com model list request is forbidden in tests")
		}
		return previous(req)
	}
	code := m.Run()
	modelListHTTPDo = previous
	os.Exit(code)
}

func stubModelListHTTPDo(t *testing.T, doer func(*http.Request) (*http.Response, error)) {
	t.Helper()
	previous := modelListHTTPDo
	modelListHTTPDo = doer
	t.Cleanup(func() { modelListHTTPDo = previous })
}

func stubCodexModelListURL(t *testing.T, endpoint string) {
	t.Helper()
	previous := codexModelListURL
	codexModelListURL = endpoint
	t.Cleanup(func() { codexModelListURL = previous })
}

func stubModelAdapterCredential(t *testing.T, cred subscriptionauth.Credential, resolveErr error) {
	stubModelAdapterCredentialForSource(t, "", cred, resolveErr)
}

func stubModelAdapterCredentialForSource(t *testing.T, want subscriptionauth.CredentialSource, cred subscriptionauth.Credential, resolveErr error) {
	t.Helper()
	previous := resolveModelAdapterCredential
	resolveModelAdapterCredential = func(_ *ProxyService, _ context.Context, source subscriptionauth.CredentialSource) (subscriptionauth.Credential, error) {
		if want != "" && source != want {
			t.Errorf("Resolve source = %q, want %q", source, want)
		}
		return cred, resolveErr
	}
	t.Cleanup(func() { resolveModelAdapterCredential = previous })
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestFetchModelAdapterModelsCodexUsesFixedURLAndFilters(t *testing.T) {
	const token = "codex-access-token-secret"
	var gotURL string
	var gotAuth string
	var gotOriginator string
	var gotAccountID string
	var gotXAIHeader string

	stubModelAdapterCredentialForSource(t, subscriptionauth.CredentialSourceCodex, subscriptionauth.Credential{
		AccessToken:      token,
		ChatGPTAccountID: "acct-fixture",
	}, nil)
	stubModelListHTTPDo(t, func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		gotAuth = req.Header.Get("Authorization")
		gotOriginator = req.Header.Get("originator")
		gotAccountID = req.Header.Get("ChatGPT-Account-Id")
		gotXAIHeader = req.Header.Get("x-xai-token-auth")
		return jsonResponse(http.StatusOK, `{
			"models": [
				{"id": "gpt-5.1", "name": "GPT-5.1", "slug": "gpt-5-1", "supported_in_api": true, "visibility": "list"},
				{"id": "internal-only", "supported_in_api": false, "visibility": "list"},
				{"id": "hidden-model", "supported_in_api": true, "visibility": "hidden"},
				{"name": "codex-name-only", "visibility": "list"},
				{"slug": "codex-slug-only", "visibility": "list"}
			]
		}`), nil
	})

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:             "openai",
		CredentialSource: "codex",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	if gotURL != defaultCodexModelListURL {
		t.Fatalf("Codex discovery URL = %q, want %q", gotURL, defaultCodexModelListURL)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want Bearer <resolved token>", gotAuth)
	}
	if gotOriginator != "codex_cli_rs" {
		t.Fatalf("originator = %q, want codex_cli_rs", gotOriginator)
	}
	if gotAccountID != "acct-fixture" {
		t.Fatalf("ChatGPT-Account-Id = %q, want acct-fixture", gotAccountID)
	}
	if gotXAIHeader != "" {
		t.Fatalf("Codex discovery 不应发送 x-xai-token-auth，实际 = %q", gotXAIHeader)
	}
	want := []string{"codex-name-only", "codex-slug-only", "gpt-5.1"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if bytes.Contains(encoded, []byte(token)) {
		t.Fatalf("模型列表结果泄漏了临时 token：%s", encoded)
	}
}

func TestFetchModelAdapterModelsCodexRequiresInjectedHelper(t *testing.T) {
	stubModelAdapterCredentialForSource(t, subscriptionauth.CredentialSourceCodex, subscriptionauth.Credential{
		AccessToken:      "tok",
		ChatGPTAccountID: "acct-fixture",
	}, nil)
	service := &ProxyService{}
	_, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:             "openai",
		CredentialSource: "codex",
	})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("未注入 helper 时应拒绝真实 chatgpt.com 请求，实际 = %v", err)
	}
}

func TestFetchModelAdapterModelsCodexRequiresAccountID(t *testing.T) {
	httpCalled := false
	stubModelAdapterCredentialForSource(t, subscriptionauth.CredentialSourceCodex, subscriptionauth.Credential{
		AccessToken: "tok",
	}, nil)
	stubModelListHTTPDo(t, func(*http.Request) (*http.Response, error) {
		httpCalled = true
		return jsonResponse(http.StatusOK, `{"models":[{"id":"gpt-5.1","visibility":"list"}]}`), nil
	})
	service := &ProxyService{}
	_, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:             "openai",
		CredentialSource: "codex",
	})
	if httpCalled {
		t.Fatal("缺少 ChatGPT-Account-Id 时不应发出 HTTP 请求")
	}
	if err == nil || !strings.Contains(err.Error(), "ChatGPT-Account-Id") {
		t.Fatalf("期望非空 ChatGPT-Account-Id 错误，实际 = %v", err)
	}
}

func TestFetchModelAdapterModelsCodexUsesInjectedEndpoint(t *testing.T) {
	const token = "codex-injected-endpoint-token"
	var gotPath string
	var gotAuth string
	var gotOriginator string
	var gotAccountID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		gotOriginator = r.Header.Get("originator")
		gotAccountID = r.Header.Get("ChatGPT-Account-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-injected","supported_in_api":true,"visibility":"list"}]}`))
	}))
	t.Cleanup(server.Close)

	stubCodexModelListURL(t, server.URL+"/backend-api/codex/models?client_version=1.0.0")
	stubModelAdapterCredentialForSource(t, subscriptionauth.CredentialSourceCodex, subscriptionauth.Credential{
		AccessToken:      token,
		ChatGPTAccountID: "acct-injected",
	}, nil)

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:             "openai",
		CredentialSource: "codex",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}
	if gotPath != "/backend-api/codex/models?client_version=1.0.0" {
		t.Fatalf("注入 endpoint 路径 = %q, want /backend-api/codex/models?client_version=1.0.0", gotPath)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want Bearer <resolved token>", gotAuth)
	}
	if gotOriginator != "codex_cli_rs" {
		t.Fatalf("originator = %q, want codex_cli_rs", gotOriginator)
	}
	if gotAccountID != "acct-injected" {
		t.Fatalf("ChatGPT-Account-Id = %q, want acct-injected", gotAccountID)
	}
	want := []string{"gpt-injected"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
}

func TestFetchModelAdapterModelsGrokUsesBaseURLWithoutXAIHeader(t *testing.T) {
	const token = "grok-access-token-secret"
	var gotPath string
	var gotAuth string
	var gotXAIHeader string
	var gotOriginator string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotXAIHeader = r.Header.Get("x-xai-token-auth")
		gotOriginator = r.Header.Get("originator")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4"},{"id":"grok-3"}]}`))
	}))
	defer server.Close()

	stubModelAdapterCredentialForSource(t, subscriptionauth.CredentialSourceGrok, subscriptionauth.Credential{AccessToken: token}, nil)
	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:             "openai",
		BaseURL:          server.URL + "/v1",
		CredentialSource: "grok",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("Grok 请求路径 = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want Bearer <resolved token>", gotAuth)
	}
	if gotXAIHeader != "" {
		t.Fatalf("Grok discovery 不应发送 x-xai-token-auth，实际 = %q", gotXAIHeader)
	}
	if gotOriginator != "" {
		t.Fatalf("Grok discovery 不应发送 originator，实际 = %q", gotOriginator)
	}
	want := []string{"grok-3", "grok-4"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if bytes.Contains(encoded, []byte(token)) {
		t.Fatalf("模型列表结果泄漏了临时 token：%s", encoded)
	}
}

func TestFetchModelAdapterModelsManagedWithoutResolverFails(t *testing.T) {
	service := &ProxyService{}
	_, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:             "openai",
		BaseURL:          "https://api.x.ai/v1",
		CredentialSource: "grok",
	})
	if err == nil || !strings.Contains(err.Error(), "订阅认证服务未初始化") {
		t.Fatalf("期望订阅认证未初始化错误，实际 = %v", err)
	}
}

func TestFetchModelAdapterModelsGrokRequiresBaseURL(t *testing.T) {
	service := &ProxyService{}
	_, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:             "openai",
		CredentialSource: "grok",
	})
	if err == nil || !strings.Contains(err.Error(), "接口地址不能为空") {
		t.Fatalf("Grok 缺少 baseURL 时期望地址错误，实际 = %v", err)
	}
}

func TestExtractCodexModelIDsPrefersIDThenNameThenSlug(t *testing.T) {
	payload := map[string]any{
		"models": []any{
			map[string]any{"id": "id-1", "name": "name-1", "slug": "slug-1", "visibility": "list"},
			map[string]any{"name": "name-2", "slug": "slug-2", "visibility": "list"},
			map[string]any{"slug": "slug-3", "visibility": "list"},
			map[string]any{"id": "dropped", "supported_in_api": false, "visibility": "list"},
			map[string]any{"id": "not-listed", "visibility": "unlisted"},
		},
	}
	got := normalizeFetchedModelIDs(extractCodexModelIDs(payload))
	want := []string{"id-1", "name-2", "slug-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractCodexModelIDs = %v, want %v", got, want)
	}
}

func TestModelAdapterTestRequestIDUsesQuotaSkipPrefix(t *testing.T) {
	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:      "Managed Codex",
		Type:             "openai",
		BaseURL:          "https://example.test/v1",
		CredentialSource: "codex",
		TooltipData:      "备注",
		ModelID:          "gpt-5.1",
	}
	requestID := modelAdapterTestRequestID(adapter)
	if !strings.HasPrefix(requestID, modelAdapterTestRequestIDPrefix) {
		t.Fatalf("requestID = %q, want prefix %q", requestID, modelAdapterTestRequestIDPrefix)
	}
	cred := subscriptionauth.Credential{AccessToken: "managed-test-token-secret"}
	if got := modelAdapterTestRuntimeAPIKey(adapter, cred); got != cred.AccessToken {
		t.Fatalf("runtime API key = %q, want resolved token", got)
	}
	if adapter.APIKey != "" {
		t.Fatalf("adapter.APIKey 被写回：%q", adapter.APIKey)
	}
}
