package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/modelchannel"
	"cursor/internal/netproxy"
	"cursor/internal/subscriptionauth"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	modelAdapterTestUpdatedEvent      = "model-adapter-test:updated"
	modelAdapterTestPrompt            = "Output the numbers 1 through 120 separated by a single space. No commas, no newlines, no explanation."
	modelAdapterTestTimeout           = 45 * time.Second
	modelAdapterTestDefaultMaxTokens  = 65_536
	modelAdapterTestEmptyTextError    = "未收到文本输出，无法计算测速结果"
	modelAdapterTestMaxErrorBodyBytes = 8192
	modelAdapterListTimeout           = 20 * time.Second
	modelAdapterListMaxBodyBytes      = 8 << 20
	modelAdapterListPageSize          = 1000
	modelAdapterListMaxPages          = 50
	defaultCodexModelListURL          = "https://chatgpt.com/backend-api/codex/models?client_version=1.0.0"
	codexModelListOriginator          = "codex_cli_rs"
	chatgptAccountIDHeader            = "ChatGPT-Account-Id"
	modelAdapterTestRequestIDPrefix   = "model-adapter-test-"
)

// modelListProviderRule 收敛各家模型列表接口的协议差异，避免判断散落到多个函数。
type modelListProviderRule struct {
	// paths 按优先级排列，逐个尝试直到某个返回可用模型
	paths       []string
	authHeader  string
	authPrefix  string
	extraHeader map[string]string
	// paginated 为真时按 limit + after_id 游标翻页，直到 has_more 为 false
	paginated bool
}

var modelListProviderRules = map[string]modelListProviderRule{
	"openai": {
		paths:      []string{"/models"},
		authHeader: "Authorization",
		authPrefix: "Bearer ",
	},
	"anthropic": {
		paths:       []string{"/models"},
		authHeader:  "x-api-key",
		extraHeader: map[string]string{"anthropic-version": "2023-06-01"},
		paginated:   true,
	},
}

// modelListVersionSegments 用于判断 base url 是否已带版本前缀，带了就不再补 /v1。
var modelListVersionSegments = map[string]bool{
	"v1":         true,
	"v1beta":     true,
	"v2":         true,
	"beta":       true,
	"openai":     true,
	"compat":     true,
	"compatible": true,
}

var modelListHTTPClient = netproxy.NewHTTPClient(modelAdapterListTimeout)

func defaultModelListHTTPDo(req *http.Request) (*http.Response, error) {
	return modelListHTTPClient.Do(req)
}

func defaultResolveModelAdapterCredential(s *ProxyService, ctx context.Context, source subscriptionauth.CredentialSource) (subscriptionauth.Credential, error) {
	if s == nil || s.subscriptionAuth == nil {
		return subscriptionauth.Credential{}, errors.New("订阅认证服务未初始化")
	}
	return s.subscriptionAuth.Resolve(ctx, source)
}

// modelListHTTPDo 仅供测试注入；默认走 netproxy.NewHTTPClient，禁止测试打真实 chatgpt.com。
var modelListHTTPDo = defaultModelListHTTPDo

// resolveModelAdapterCredential 仅供测试注入；默认走 ProxyService.subscriptionAuth.Resolve。
var resolveModelAdapterCredential = defaultResolveModelAdapterCredential

// streamModelAdapterTestOpenAI 仅供测试注入；默认走真实 OpenAI adapter。
var streamModelAdapterTestOpenAI = func(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
	return modeladapter.NewOpenAIAdapter().Stream(ctx, req, sink)
}

// codexModelListURL 仅供测试注入 fake endpoint；生产默认固定 chatgpt.com Codex models 地址。
var codexModelListURL = defaultCodexModelListURL

type ModelAdapterTestStatus string

const (
	ModelAdapterTestStatusIdle    ModelAdapterTestStatus = "idle"
	ModelAdapterTestStatusRunning ModelAdapterTestStatus = "running"
	ModelAdapterTestStatusSuccess ModelAdapterTestStatus = "success"
	ModelAdapterTestStatusError   ModelAdapterTestStatus = "error"
)

// ModelAdapterTestResult 表示一次模型测速结果。
type ModelAdapterTestResult struct {
	AdapterID        string  `json:"adapterID"`
	RequestHash      string  `json:"requestHash"`
	Status           string  `json:"status"`
	TokensPerSecond  float64 `json:"tokensPerSecond"`
	FirstTextTokenMS int64   `json:"firstTextTokenMS"`
	TotalDurationMS  int64   `json:"totalDurationMS"`
	OutputTokens     int64   `json:"outputTokens"`
	TokensEstimated  bool    `json:"tokensEstimated"`
	SummaryText      string  `json:"summaryText"`
	Error            string  `json:"error"`
	RawResponse      string  `json:"rawResponse"`
	TestedAt         string  `json:"testedAt"`
}

// ModelAdapterModelsRequest 定义从兼容接口读取模型列表所需的最小配置。
type ModelAdapterModelsRequest struct {
	Type                 string `json:"type"`
	BaseURL              string `json:"baseURL"`
	APIKey               string `json:"apiKey"`
	CredentialSource     string `json:"credentialSource"`
	CustomHeadersEnabled bool   `json:"customHeadersEnabled"`
	CustomHeadersJSON    string `json:"customHeadersJSON"`
}

// ModelAdapterModelsResult 定义可供前端下拉选择的模型列表。
type ModelAdapterModelsResult struct {
	Models []string `json:"models"`
}

// ModelAdapterTestResultsPayload 用于向前端广播当前测速结果快照。
type ModelAdapterTestResultsPayload struct {
	Results []ModelAdapterTestResult `json:"results"`
}

type modelAdapterTestMetrics struct {
	firstTextTokenAt time.Time
	finishedAt       time.Time
	outputTokens     int64
	outputProvided   bool
	text             strings.Builder
	rawResponse      string
}

type modelAdapterTestArtifactObserver struct {
	mu       sync.Mutex
	response strings.Builder
}

func (observer *modelAdapterTestArtifactObserver) RecordLLMRequest(string, string, string, map[string]any) (string, error) {
	return "", nil
}

func (observer *modelAdapterTestArtifactObserver) AppendLLMResponseChunk(_ string, _ string, _ string, chunk string) (string, error) {
	if observer == nil {
		return "", nil
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	_, _ = observer.response.WriteString(chunk)
	return "", nil
}

func (observer *modelAdapterTestArtifactObserver) RecordLLMSummary(string, string, string, map[string]any) (string, error) {
	return "", nil
}

func (observer *modelAdapterTestArtifactObserver) RawResponse() string {
	if observer == nil {
		return ""
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return strings.TrimSpace(observer.response.String())
}

func (s *ProxyService) GetModelAdapterTestResults() []ModelAdapterTestResult {
	return s.snapshotModelAdapterTestResults()
}

func (s *ProxyService) FetchModelAdapterModels(input ModelAdapterModelsRequest) (ModelAdapterModelsResult, error) {
	provider := strings.ToLower(strings.TrimSpace(input.Type))
	baseURL := strings.TrimSpace(input.BaseURL)
	apiKey := strings.TrimSpace(input.APIKey)
	source := subscriptionauth.NormalizeCredentialSource(input.CredentialSource)
	if source == "" {
		return ModelAdapterModelsResult{}, errors.New("credentialSource 仅支持 static、codex 或 grok")
	}

	ctx, cancel := context.WithTimeout(context.Background(), modelAdapterListTimeout)
	defer cancel()

	if source == subscriptionauth.CredentialSourceCodex {
		apiKey, chatgptAccountID, err := resolveManagedModelListCredential(s, ctx, source)
		if err != nil {
			return ModelAdapterModelsResult{}, err
		}
		models, err := fetchCodexModelList(ctx, apiKey, chatgptAccountID, input)
		if err != nil {
			return ModelAdapterModelsResult{}, err
		}
		return ModelAdapterModelsResult{Models: models}, nil
	}

	rule, supported := modelListProviderRules[provider]
	if !supported {
		return ModelAdapterModelsResult{}, errors.New("模型类型仅支持 OpenAI 或 Anthropic")
	}
	if baseURL == "" {
		return ModelAdapterModelsResult{}, errors.New("接口地址不能为空")
	}
	if source.Managed() {
		resolvedKey, _, err := resolveManagedModelListCredential(s, ctx, source)
		if err != nil {
			return ModelAdapterModelsResult{}, err
		}
		apiKey = resolvedKey
	} else if apiKey == "" {
		return ModelAdapterModelsResult{}, errors.New("访问密钥不能为空")
	}

	var lastErr error
	for _, endpoint := range buildModelListEndpointCandidates(rule, baseURL) {
		models, err := fetchModelListEndpoint(ctx, rule, endpoint, apiKey, input)
		if err == nil {
			return ModelAdapterModelsResult{Models: models}, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return ModelAdapterModelsResult{}, lastErr
	}
	return ModelAdapterModelsResult{}, errors.New("未找到可用的模型列表接口")
}

func buildModelListEndpointCandidates(rule modelListProviderRule, rawBaseURL string) []string {
	base := strings.TrimRight(strings.TrimSpace(rawBaseURL), "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		if strings.HasSuffix(strings.ToLower(base), suffix) {
			base = base[:len(base)-len(suffix)]
		}
	}
	base = strings.TrimRight(base, "/")

	tail := strings.ToLower(base[strings.LastIndex(base, "/")+1:])
	var candidates []string
	switch {
	case tail == "models" || tail == "model":
		// 用户已经填到模型列表地址本身，直接用
		candidates = []string{base}
	case modelListVersionSegments[tail]:
		candidates = prefixModelListPaths(base, "", rule.paths)
	default:
		// base 没带版本段，优先试 /v1，再退回裸路径
		candidates = append(
			prefixModelListPaths(base, "/v1", rule.paths),
			prefixModelListPaths(base, "", rule.paths)...,
		)
	}

	seen := map[string]struct{}{}
	endpoints := make([]string, 0, len(candidates))
	for _, endpoint := range candidates {
		if _, err := url.ParseRequestURI(endpoint); err != nil {
			continue
		}
		if _, exists := seen[endpoint]; exists {
			continue
		}
		seen[endpoint] = struct{}{}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints
}

func prefixModelListPaths(base string, version string, paths []string) []string {
	endpoints := make([]string, 0, len(paths))
	for _, path := range paths {
		endpoints = append(endpoints, base+version+path)
	}
	return endpoints
}

func fetchModelListEndpoint(ctx context.Context, rule modelListProviderRule, endpoint string, apiKey string, input ModelAdapterModelsRequest) ([]string, error) {
	collected := []string{}
	cursor := ""
	for page := 0; page < modelAdapterListMaxPages; page++ {
		requestURL := endpoint
		if rule.paginated {
			requestURL = appendModelListCursor(endpoint, cursor)
		}
		payload, err := requestModelListPayload(ctx, rule, requestURL, apiKey, input)
		if err != nil {
			return nil, err
		}
		collected = append(collected, extractModelIDs(payload)...)
		if !rule.paginated {
			break
		}
		cursor = nextModelListCursor(payload)
		if cursor == "" {
			break
		}
		if page == modelAdapterListMaxPages-1 {
			return nil, fmt.Errorf("模型列表分页超过 %d 页，结果可能不完整", modelAdapterListMaxPages)
		}
	}

	models := normalizeFetchedModelIDs(collected)
	if len(models) == 0 {
		return nil, errors.New("模型列表响应中没有可用模型")
	}
	return models, nil
}

func requestModelListPayload(
	ctx context.Context,
	rule modelListProviderRule,
	requestURL string,
	apiKey string,
	input ModelAdapterModelsRequest,
) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(rule.authHeader, rule.authPrefix+apiKey)
	for key, value := range rule.extraHeader {
		req.Header.Set(key, value)
	}
	req.Header.Set("Accept", "application/json")
	applyModelListCustomHeaders(req.Header, input)

	resp, err := modelListHTTPDo(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, modelAdapterListMaxBodyBytes))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if len(message) > modelAdapterTestMaxErrorBodyBytes {
			message = message[:modelAdapterTestMaxErrorBodyBytes]
		}
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("读取模型列表失败：%s", message)
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("模型列表响应不是合法 JSON：%w", err)
	}
	return payload, nil
}

func appendModelListCursor(endpoint string, cursor string) string {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(modelAdapterListPageSize))
	if cursor != "" {
		query.Set("after_id", cursor)
	}
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return endpoint + separator + query.Encode()
}

func nextModelListCursor(payload any) string {
	object, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	if hasMore, _ := object["has_more"].(bool); !hasMore {
		return ""
	}
	cursor, _ := object["last_id"].(string)
	return strings.TrimSpace(cursor)
}

func resolveManagedModelListCredential(s *ProxyService, ctx context.Context, source subscriptionauth.CredentialSource) (string, string, error) {
	cred, err := resolveModelAdapterCredential(s, ctx, source)
	if err != nil {
		return "", "", err
	}
	apiKey := strings.TrimSpace(cred.AccessToken)
	if apiKey == "" {
		return "", "", subscriptionauth.ErrAuthRequired
	}
	return apiKey, strings.TrimSpace(cred.ChatGPTAccountID), nil
}

func fetchCodexModelList(ctx context.Context, apiKey string, chatgptAccountID string, input ModelAdapterModelsRequest) ([]string, error) {
	accountID := strings.TrimSpace(chatgptAccountID)
	if accountID == "" {
		return nil, errors.New("Codex 模型列表需要非空 ChatGPT-Account-Id")
	}
	endpoint := strings.TrimSpace(codexModelListURL)
	if endpoint == "" {
		endpoint = defaultCodexModelListURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("originator", codexModelListOriginator)
	req.Header.Set(chatgptAccountIDHeader, accountID)
	req.Header.Set("Accept", "application/json")
	applyModelListCustomHeaders(req.Header, input)

	resp, err := modelListHTTPDo(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, modelAdapterListMaxBodyBytes))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if len(message) > modelAdapterTestMaxErrorBodyBytes {
			message = message[:modelAdapterTestMaxErrorBodyBytes]
		}
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("读取模型列表失败：%s", message)
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("模型列表响应不是合法 JSON：%w", err)
	}
	models := normalizeFetchedModelIDs(extractCodexModelIDs(payload))
	if len(models) == 0 {
		return nil, errors.New("模型列表响应中没有可用模型")
	}
	return models, nil
}

func extractCodexModelIDs(value any) []string {
	switch typed := value.(type) {
	case []any:
		models := make([]string, 0, len(typed))
		for _, item := range typed {
			models = append(models, extractCodexModelIDs(item)...)
		}
		return models
	case map[string]any:
		for _, key := range []string{"data", "models"} {
			if child, ok := typed[key]; ok {
				if _, isList := child.([]any); isList {
					return extractCodexModelIDs(child)
				}
			}
		}
		if !codexModelVisibleInAPI(typed) {
			return nil
		}
		if id := firstCodexModelID(typed); id != "" {
			return []string{id}
		}
		return nil
	default:
		return nil
	}
}

func firstCodexModelID(item map[string]any) string {
	for _, key := range []string{"id", "name", "slug"} {
		text, ok := item[key].(string)
		if !ok {
			continue
		}
		id := strings.TrimSpace(text)
		if id != "" {
			return id
		}
	}
	return ""
}

func codexModelVisibleInAPI(item map[string]any) bool {
	if supported, ok := item["supported_in_api"].(bool); ok && !supported {
		return false
	}
	visibility, ok := item["visibility"].(string)
	if ok && strings.TrimSpace(visibility) != "list" {
		return false
	}
	return true
}

func applyModelListCustomHeaders(header http.Header, input ModelAdapterModelsRequest) {
	if !input.CustomHeadersEnabled || strings.TrimSpace(input.CustomHeadersJSON) == "" {
		return
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(input.CustomHeadersJSON), &parsed); err != nil {
		return
	}
	for key, value := range parsed {
		if strings.TrimSpace(key) == "" {
			continue
		}
		header.Set(key, value)
	}
}

func extractModelIDs(value any) []string {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return []string{}
		}
		return []string{typed}
	case []any:
		models := make([]string, 0, len(typed))
		for _, item := range typed {
			models = append(models, extractModelIDs(item)...)
		}
		return models
	case map[string]any:
		for _, key := range []string{"id", "name"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return []string{text}
			}
		}
		models := []string{}
		for _, key := range []string{"data", "models"} {
			if child, ok := typed[key]; ok {
				models = append(models, extractModelIDs(child)...)
			}
		}
		return models
	default:
		return []string{}
	}
}

func normalizeFetchedModelIDs(input []string) []string {
	seen := map[string]struct{}{}
	models := make([]string, 0, len(input))
	for _, item := range input {
		model := strings.TrimSpace(item)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func (s *ProxyService) TestModelAdapter(adapter serverconfig.ModelAdapterConfig) (ModelAdapterTestResult, error) {
	requestHash := buildModelAdapterTestRequestHash(adapter)
	adapterID := buildModelAdapterTestCacheKey(adapter, requestHash)

	if cached, ok := s.getRunningModelAdapterTestResult(adapterID, requestHash); ok {
		return cached, nil
	}

	normalized, err := normalizeSingleModelAdapterConfig(adapter)
	if err != nil {
		result := ModelAdapterTestResult{
			AdapterID:   adapterID,
			RequestHash: requestHash,
			Status:      string(ModelAdapterTestStatusError),
			SummaryText: buildModelAdapterTestErrorSummary(err),
			Error:       buildModelAdapterTestErrorSummary(err),
			RawResponse: strings.TrimSpace(modelAdapterTestErrorMessage(err)),
			TestedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		}
		s.storeAndEmitModelAdapterTestResult(result)
		return result, err
	}

	running := ModelAdapterTestResult{
		AdapterID:   normalized.ID,
		RequestHash: requestHash,
		Status:      string(ModelAdapterTestStatusRunning),
		TestedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	s.storeAndEmitModelAdapterTestResult(running)

	result, testErr := s.runModelAdapterTest(normalized, requestHash)
	s.storeAndEmitModelAdapterTestResult(result)
	if testErr != nil {
		return result, testErr
	}
	return result, nil
}

func normalizeSingleModelAdapterConfig(adapter serverconfig.ModelAdapterConfig) (serverconfig.ModelAdapterConfig, error) {
	normalized, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{adapter})
	if err != nil {
		return serverconfig.ModelAdapterConfig{}, err
	}
	if len(normalized) == 0 {
		return serverconfig.ModelAdapterConfig{}, errors.New("模型配置不能为空")
	}
	return normalized[0], nil
}

func (s *ProxyService) resolveModelAdapterTestCredentials(ctx context.Context, adapter serverconfig.ModelAdapterConfig) (subscriptionauth.Credential, error) {
	source := subscriptionauth.NormalizeCredentialSource(adapter.CredentialSource)
	if !source.Managed() {
		return subscriptionauth.Credential{}, nil
	}
	cred, err := resolveModelAdapterCredential(s, ctx, source)
	if err != nil {
		return subscriptionauth.Credential{}, err
	}
	if strings.TrimSpace(cred.AccessToken) == "" {
		return subscriptionauth.Credential{}, subscriptionauth.ErrAuthRequired
	}
	return cred, nil
}

func (s *ProxyService) runModelAdapterTest(adapter serverconfig.ModelAdapterConfig, requestHash string) (ModelAdapterTestResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelAdapterTestTimeout)
	defer cancel()

	cred, err := s.resolveModelAdapterTestCredentials(ctx, adapter)
	if err != nil {
		result := buildErroredModelAdapterTestResult(adapter.ID, requestHash, err)
		return result, err
	}

	startedAt := time.Now().UTC()
	metrics, requestErr := s.executeModelAdapterNonStreamingTest(ctx, adapter, cred)
	if requestErr != nil {
		result := buildErroredModelAdapterTestResult(adapter.ID, requestHash, requestErr)
		return result, requestErr
	}

	if metrics.finishedAt.IsZero() {
		metrics.finishedAt = time.Now().UTC()
	}
	if metrics.firstTextTokenAt.IsZero() {
		emptyTextErr := errors.New(modelAdapterTestEmptyTextError)
		result := buildErroredModelAdapterTestResult(adapter.ID, requestHash, emptyTextErr)
		return result, emptyTextErr
	}

	outputTokens := metrics.outputTokens
	tokensEstimated := false
	if !metrics.outputProvided || outputTokens <= 0 {
		outputTokens = estimateBenchmarkTextTokens(metrics.text.String())
		tokensEstimated = true
	}

	firstTextTokenMS := metrics.firstTextTokenAt.Sub(startedAt).Milliseconds()
	if firstTextTokenMS < 0 {
		firstTextTokenMS = 0
	}
	totalDurationMS := metrics.finishedAt.Sub(startedAt).Milliseconds()
	if totalDurationMS < 0 {
		totalDurationMS = 0
	}

	tokensPerSecond := 0.0
	totalDuration := metrics.finishedAt.Sub(startedAt)
	if outputTokens > 0 && totalDuration > 0 {
		tokensPerSecond = float64(outputTokens) / totalDuration.Seconds()
	}

	result := ModelAdapterTestResult{
		AdapterID:        adapter.ID,
		RequestHash:      requestHash,
		Status:           string(ModelAdapterTestStatusSuccess),
		TokensPerSecond:  tokensPerSecond,
		FirstTextTokenMS: firstTextTokenMS,
		TotalDurationMS:  totalDurationMS,
		OutputTokens:     outputTokens,
		TokensEstimated:  tokensEstimated,
		TestedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		RawResponse:      strings.TrimSpace(metrics.rawResponse),
	}
	return result, nil
}

func (s *ProxyService) executeModelAdapterNonStreamingTest(ctx context.Context, adapter serverconfig.ModelAdapterConfig, cred subscriptionauth.Credential) (*modelAdapterTestMetrics, error) {
	switch strings.TrimSpace(adapter.Type) {
	case "openai":
		return s.executeOpenAIStreamingTest(ctx, adapter, cred)
	case "anthropic":
		return s.executeAnthropicStreamingTest(ctx, adapter)
	default:
		return nil, fmt.Errorf("unsupported provider %q", strings.TrimSpace(adapter.Type))
	}
}

func (s *ProxyService) executeOpenAIStreamingTest(ctx context.Context, adapter serverconfig.ModelAdapterConfig, cred subscriptionauth.Credential) (*modelAdapterTestMetrics, error) {
	_ = s
	metrics := &modelAdapterTestMetrics{}
	observer := &modelAdapterTestArtifactObserver{}
	maxTokens := modelAdapterTestConfiguredOpenAIMaxTokens(adapter)
	requestID := modelAdapterTestRequestID(adapter)
	req := modeladapter.StreamRequest{
		RequestID:                   requestID,
		RunID:                       requestID,
		ModelCallID:                 requestID,
		ModelID:                     strings.TrimSpace(adapter.ID),
		Provider:                    "openai",
		BaseURL:                     strings.TrimSpace(adapter.BaseURL),
		APIKey:                      modelAdapterTestRuntimeAPIKey(adapter, cred),
		CredentialSource:            strings.TrimSpace(adapter.CredentialSource),
		CredentialID:                strings.TrimSpace(cred.AccountID),
		ChatGPTAccountID:            strings.TrimSpace(cred.ChatGPTAccountID),
		ProviderModelID:             strings.TrimSpace(adapter.ModelID),
		ResolvedChannelID:           strings.TrimSpace(adapter.ID),
		ResolvedChannelName:         strings.TrimSpace(adapter.DisplayName),
		ResolvedContextWindowTokens: adapter.ContextWindowTokens,
		ReasoningEffort:             strings.TrimSpace(adapter.ReasoningEffort),
		OpenAIEndpoint:              strings.TrimSpace(adapter.OpenAIEndpoint),
		OpenAIExtraParamsEnabled:    adapter.OpenAIExtraParamsEnabled,
		OpenAIExtraParamsJSON:       strings.TrimSpace(adapter.OpenAIExtraParamsJSON),
		CustomHeadersEnabled:        adapter.CustomHeadersEnabled,
		CustomHeadersJSON:           strings.TrimSpace(adapter.CustomHeadersJSON),
		Messages:                    []modeladapter.Message{{Role: "user", Content: modelAdapterTestPrompt}},
		MaxTokens:                   maxTokens,
		Stream:                      true,
		RequestKnobs:                map[string]any{"stream": true, "max_tokens": maxTokens},
		Observer:                    observer,
		ProviderStreamIdleTimeout:   modelAdapterTestTimeout,
	}
	err := streamModelAdapterTestOpenAI(ctx, req, func(event modeladapter.ModelEvent) error {
		now := time.Now().UTC()
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			if strings.TrimSpace(event.Text) != "" && metrics.firstTextTokenAt.IsZero() {
				metrics.firstTextTokenAt = now
			}
			_, _ = metrics.text.WriteString(event.Text)
		case modeladapter.ModelEventKindTurnFinished:
			metrics.finishedAt = now
			if event.OutputTokens > 0 {
				metrics.outputTokens = event.OutputTokens
				metrics.outputProvided = true
			}
		case modeladapter.ModelEventKindProviderError:
			if event.Err != nil {
				return event.Err
			}
			return errors.New("provider error")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if metrics.finishedAt.IsZero() {
		metrics.finishedAt = time.Now().UTC()
	}
	metrics.rawResponse = observer.RawResponse()
	if strings.TrimSpace(metrics.rawResponse) == "" {
		metrics.rawResponse = strings.TrimSpace(metrics.text.String())
	}
	return metrics, nil
}

func (s *ProxyService) executeAnthropicStreamingTest(ctx context.Context, adapter serverconfig.ModelAdapterConfig) (*modelAdapterTestMetrics, error) {
	_ = s
	metrics := &modelAdapterTestMetrics{}
	observer := &modelAdapterTestArtifactObserver{}
	maxTokens := modelAdapterTestConfiguredAnthropicMaxTokens(adapter)
	thinkingEffort := normalizeModelAdapterTestAnthropicThinkingEffort(adapter.AnthropicThinkingEffort)
	requestID := modelAdapterTestRequestID(adapter)
	req := modeladapter.StreamRequest{
		RequestID:                   requestID,
		RunID:                       requestID,
		ModelCallID:                 requestID,
		ModelID:                     strings.TrimSpace(adapter.ID),
		Provider:                    "anthropic",
		BaseURL:                     strings.TrimSpace(adapter.BaseURL),
		APIKey:                      strings.TrimSpace(adapter.APIKey),
		ProviderModelID:             strings.TrimSpace(adapter.ModelID),
		ResolvedChannelID:           strings.TrimSpace(adapter.ID),
		ResolvedChannelName:         strings.TrimSpace(adapter.DisplayName),
		ResolvedContextWindowTokens: adapter.ContextWindowTokens,
		ThinkingEffort:              thinkingEffort,
		AnthropicMaxTokens:          maxTokens,
		AnthropicThinkingEffort:     thinkingEffort,
		CustomHeadersEnabled:        adapter.CustomHeadersEnabled,
		CustomHeadersJSON:           strings.TrimSpace(adapter.CustomHeadersJSON),
		AnthropicExtraParamsEnabled: adapter.AnthropicExtraParamsEnabled,
		AnthropicExtraParamsJSON:    strings.TrimSpace(adapter.AnthropicExtraParamsJSON),
		ThinkingBudgetTokens:        adapter.ThinkingBudgetTokens,
		Messages:                    []modeladapter.Message{{Role: "user", Content: modelAdapterTestPrompt}},
		MaxTokens:                   maxTokens,
		Stream:                      true,
		RequestKnobs:                map[string]any{"stream": true, "anthropic_max_tokens": maxTokens, "max_tokens": maxTokens},
		Observer:                    observer,
		ProviderStreamIdleTimeout:   modelAdapterTestTimeout,
	}
	err := modeladapter.NewAnthropicAdapter().Stream(ctx, req, func(event modeladapter.ModelEvent) error {
		now := time.Now().UTC()
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			if strings.TrimSpace(event.Text) != "" && metrics.firstTextTokenAt.IsZero() {
				metrics.firstTextTokenAt = now
			}
			_, _ = metrics.text.WriteString(event.Text)
		case modeladapter.ModelEventKindTurnFinished:
			metrics.finishedAt = now
			if event.OutputTokens > 0 {
				metrics.outputTokens = event.OutputTokens
				metrics.outputProvided = true
			}
		case modeladapter.ModelEventKindProviderError:
			if event.Err != nil {
				return event.Err
			}
			return errors.New("provider error")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if metrics.finishedAt.IsZero() {
		metrics.finishedAt = time.Now().UTC()
	}
	metrics.rawResponse = observer.RawResponse()
	if strings.TrimSpace(metrics.rawResponse) == "" {
		metrics.rawResponse = strings.TrimSpace(metrics.text.String())
	}
	return metrics, nil
}

func (s *ProxyService) getRunningModelAdapterTestResult(adapterID string, requestHash string) (ModelAdapterTestResult, bool) {
	s.modelTestMu.RLock()
	defer s.modelTestMu.RUnlock()

	if s.modelTestResults == nil {
		return ModelAdapterTestResult{}, false
	}
	result, ok := s.modelTestResults[adapterID]
	if !ok {
		return ModelAdapterTestResult{}, false
	}
	if strings.TrimSpace(result.Status) != string(ModelAdapterTestStatusRunning) {
		return ModelAdapterTestResult{}, false
	}
	if strings.TrimSpace(result.RequestHash) != strings.TrimSpace(requestHash) {
		return ModelAdapterTestResult{}, false
	}
	return result, true
}

func (s *ProxyService) storeAndEmitModelAdapterTestResult(result ModelAdapterTestResult) {
	if strings.TrimSpace(result.AdapterID) == "" {
		return
	}
	s.modelTestMu.Lock()
	if s.modelTestResults == nil {
		s.modelTestResults = make(map[string]ModelAdapterTestResult)
	}
	s.modelTestResults[result.AdapterID] = result
	snapshot := snapshotModelAdapterTestResultsLocked(s.modelTestResults)
	s.modelTestMu.Unlock()
	s.emitModelAdapterTestResults(snapshot)
}

func (s *ProxyService) snapshotModelAdapterTestResults() []ModelAdapterTestResult {
	s.modelTestMu.RLock()
	defer s.modelTestMu.RUnlock()
	return snapshotModelAdapterTestResultsLocked(s.modelTestResults)
}

func snapshotModelAdapterTestResultsLocked(items map[string]ModelAdapterTestResult) []ModelAdapterTestResult {
	if len(items) == 0 {
		return []ModelAdapterTestResult{}
	}
	results := make([]ModelAdapterTestResult, 0, len(items))
	for _, item := range items {
		results = append(results, item)
	}
	sort.Slice(results, func(i int, j int) bool {
		if results[i].TestedAt == results[j].TestedAt {
			return results[i].AdapterID < results[j].AdapterID
		}
		return results[i].TestedAt > results[j].TestedAt
	})
	return results
}

func (s *ProxyService) emitModelAdapterTestResults(results []ModelAdapterTestResult) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit(modelAdapterTestUpdatedEvent, ModelAdapterTestResultsPayload{
		Results: results,
	})
}

func buildErroredModelAdapterTestResult(adapterID string, requestHash string, err error) ModelAdapterTestResult {
	message := strings.TrimSpace(modelAdapterTestErrorMessage(err))
	summary := buildModelAdapterTestErrorSummary(err)
	return ModelAdapterTestResult{
		AdapterID:   strings.TrimSpace(adapterID),
		RequestHash: strings.TrimSpace(requestHash),
		Status:      string(ModelAdapterTestStatusError),
		SummaryText: summary,
		Error:       summary,
		RawResponse: message,
		TestedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func buildModelAdapterHTTPStatusError(prefix string, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("%s response is nil", strings.TrimSpace(prefix))
	}
	limitedBody, err := io.ReadAll(io.LimitReader(resp.Body, modelAdapterTestMaxErrorBodyBytes))
	if err != nil {
		if retrySummary := modeladapter.ProviderRetryAttemptSummary(resp); retrySummary != "" {
			return fmt.Errorf("%s status=%d %s body_read_error=%v", strings.TrimSpace(prefix), resp.StatusCode, retrySummary, err)
		}
		return fmt.Errorf("%s status=%d body_read_error=%v", strings.TrimSpace(prefix), resp.StatusCode, err)
	}
	retrySummary := modeladapter.ProviderRetryAttemptSummary(resp)
	bodyText := strings.TrimSpace(string(limitedBody))
	if bodyText == "" {
		if retrySummary != "" {
			return fmt.Errorf("%s status=%d %s", strings.TrimSpace(prefix), resp.StatusCode, retrySummary)
		}
		return fmt.Errorf("%s status=%d", strings.TrimSpace(prefix), resp.StatusCode)
	}
	if retrySummary != "" {
		return fmt.Errorf("%s status=%d %s body=%s", strings.TrimSpace(prefix), resp.StatusCode, retrySummary, bodyText)
	}
	return fmt.Errorf("%s status=%d body=%s", strings.TrimSpace(prefix), resp.StatusCode, bodyText)
}

func buildModelAdapterProviderBodyError(prefix string, body []byte) error {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	errorValue, ok := payload["error"]
	if !ok || errorValue == nil {
		return nil
	}
	message := ""
	details := make([]string, 0, 2)
	switch value := errorValue.(type) {
	case string:
		message = strings.TrimSpace(value)
	case map[string]any:
		message = strings.TrimSpace(fmt.Sprint(value["message"]))
		if errorType := strings.TrimSpace(fmt.Sprint(value["type"])); errorType != "" && errorType != "<nil>" {
			details = append(details, "type="+errorType)
		}
		if code := strings.TrimSpace(fmt.Sprint(value["code"])); code != "" && code != "<nil>" {
			details = append(details, "code="+code)
		}
	default:
		message = strings.TrimSpace(fmt.Sprint(value))
	}
	if message == "" || message == "<nil>" {
		message = "provider returned error response"
	}
	summary := strings.TrimSpace(prefix)
	if summary == "" {
		summary = "model adapter"
	}
	if len(details) > 0 {
		return fmt.Errorf("%s provider error %s: %s", summary, strings.Join(details, " "), message)
	}
	return fmt.Errorf("%s provider error: %s", summary, message)
}

func modelAdapterTestErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "模型测试失败"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "模型测试超时，请稍后重试"
	}
	return message
}

func buildModelAdapterTestErrorSummary(err error) string {
	if err == nil {
		return "测试失败"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "测试超时"
	}
	message := strings.TrimSpace(err.Error())
	switch {
	case strings.Contains(message, modelAdapterTestEmptyTextError):
		return "无正文返回"
	case strings.Contains(strings.ToLower(message), "context canceled"):
		return "测试已停止"
	default:
		return "测试失败"
	}
}

func modelAdapterTestRequestID(adapter serverconfig.ModelAdapterConfig) string {
	return modelAdapterTestRequestIDPrefix + buildModelAdapterTestRequestHash(adapter)
}

func modelAdapterTestRuntimeAPIKey(adapter serverconfig.ModelAdapterConfig, cred subscriptionauth.Credential) string {
	if token := strings.TrimSpace(cred.AccessToken); token != "" {
		return token
	}
	return strings.TrimSpace(adapter.APIKey)
}

func estimateBenchmarkTextTokens(text string) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount <= 0 {
		return 0
	}
	estimated := int64((runeCount + 3) / 4)
	estimated += int64(strings.Count(trimmed, "\n"))
	if estimated < 1 {
		return 1
	}
	return estimated
}

func buildModelAdapterTestCacheKey(adapter serverconfig.ModelAdapterConfig, requestHash string) string {
	baseURL, baseURLErr := modelchannel.NormalizeBaseURL(adapter.BaseURL)
	source := subscriptionauth.NormalizeCredentialSource(adapter.CredentialSource)
	secret := strings.TrimSpace(subscriptionauth.ChannelIDSecret(source, adapter.APIKey))
	if baseURLErr == nil &&
		strings.TrimSpace(adapter.DisplayName) != "" &&
		strings.TrimSpace(adapter.ModelID) != "" &&
		secret != "" {
		return modelchannel.BuildChannelID(baseURL, adapter.ModelID, secret, adapter.DisplayName, modelchannel.NormalizeOpenAIEndpoint(adapter.Type, adapter.OpenAIEndpoint))
	}
	return "invalid:" + strings.TrimSpace(requestHash)
}

func buildModelAdapterTestRequestHash(adapter serverconfig.ModelAdapterConfig) string {
	source := normalizeModelAdapterTestHashSource(adapter)
	payload := strings.Join([]string{
		source.Type,
		source.BaseURL,
		source.APIKey,
		source.CredentialSource,
		source.ModelID,
		source.ReasoningEffort,
		source.OpenAIEndpoint,
		strconv.Itoa(source.OpenAIExtraParamsEnabled),
		source.OpenAIExtraParamsJSON,
		strconv.Itoa(source.CustomHeadersEnabled),
		source.CustomHeadersJSON,
		strconv.Itoa(source.AnthropicExtraParamsEnabled),
		source.AnthropicExtraParamsJSON,
		strconv.Itoa(source.ContextWindowTokens),
		strconv.Itoa(source.MaxCompletionTokens),
		strconv.Itoa(source.AnthropicMaxTokens),
		source.AnthropicThinkingEffort,
	}, "\n")
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(payload))
	sum := hasher.Sum(nil)
	return hex.EncodeToString(sum)
}

type modelAdapterTestHashSource struct {
	Type                        string
	BaseURL                     string
	APIKey                      string
	CredentialSource            string
	ModelID                     string
	ReasoningEffort             string
	OpenAIEndpoint              string
	OpenAIExtraParamsEnabled    int
	OpenAIExtraParamsJSON       string
	CustomHeadersEnabled        int
	CustomHeadersJSON           string
	AnthropicExtraParamsEnabled int
	AnthropicExtraParamsJSON    string
	ContextWindowTokens         int
	MaxCompletionTokens         int
	AnthropicMaxTokens          int
	AnthropicThinkingEffort     string
}

func normalizeModelAdapterTestHashSource(adapter serverconfig.ModelAdapterConfig) modelAdapterTestHashSource {
	baseURL := strings.TrimSpace(adapter.BaseURL)
	if normalizedBaseURL, err := modelchannel.NormalizeBaseURL(adapter.BaseURL); err == nil {
		baseURL = normalizedBaseURL
	}
	return modelAdapterTestHashSource{
		Type:                        normalizeModelAdapterTestType(adapter.Type),
		BaseURL:                     baseURL,
		APIKey:                      strings.TrimSpace(adapter.APIKey),
		CredentialSource:            string(subscriptionauth.NormalizeCredentialSource(adapter.CredentialSource)),
		ModelID:                     strings.TrimSpace(adapter.ModelID),
		ReasoningEffort:             normalizeModelAdapterTestProviderReasoning(adapter),
		OpenAIEndpoint:              modelchannel.NormalizeOpenAIEndpoint(adapter.Type, adapter.OpenAIEndpoint),
		OpenAIExtraParamsEnabled:    normalizeModelAdapterTestBool(adapter.Type == "openai" && adapter.OpenAIExtraParamsEnabled),
		OpenAIExtraParamsJSON:       normalizeModelAdapterTestOpenAIExtraParamsJSON(adapter),
		CustomHeadersEnabled:        normalizeModelAdapterTestBool(adapter.CustomHeadersEnabled),
		CustomHeadersJSON:           normalizeModelAdapterTestCustomHeadersJSON(adapter),
		AnthropicExtraParamsEnabled: normalizeModelAdapterTestBool(adapter.Type == "anthropic" && adapter.AnthropicExtraParamsEnabled),
		AnthropicExtraParamsJSON:    normalizeModelAdapterTestAnthropicExtraParamsJSON(adapter),
		ContextWindowTokens:         normalizeModelAdapterTestInt(adapter.ContextWindowTokens),
		MaxCompletionTokens:         normalizeModelAdapterTestInt(adapter.MaxCompletionTokens),
		AnthropicMaxTokens:          normalizeModelAdapterTestInt(adapter.AnthropicMaxTokens),
		AnthropicThinkingEffort:     normalizeModelAdapterTestProviderAnthropicThinkingEffort(adapter),
	}
}

func normalizeModelAdapterTestType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anthropic":
		return "anthropic"
	case "openai":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeModelAdapterTestReasoning(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "medium"
	}
}

func normalizeModelAdapterTestProviderReasoning(adapter serverconfig.ModelAdapterConfig) string {
	if normalizeModelAdapterTestType(adapter.Type) != "openai" {
		return ""
	}
	return normalizeModelAdapterTestReasoning(adapter.ReasoningEffort)
}

func normalizeModelAdapterTestAnthropicThinkingEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "xhigh"
	}
}

func normalizeModelAdapterTestProviderAnthropicThinkingEffort(adapter serverconfig.ModelAdapterConfig) string {
	if normalizeModelAdapterTestType(adapter.Type) != "anthropic" {
		return ""
	}
	return normalizeModelAdapterTestAnthropicThinkingEffort(adapter.AnthropicThinkingEffort)
}

func modelAdapterTestConfiguredAnthropicMaxTokens(adapter serverconfig.ModelAdapterConfig) int {
	if adapter.AnthropicMaxTokens > 0 {
		return adapter.AnthropicMaxTokens
	}
	if adapter.MaxCompletionTokens > 0 {
		return adapter.MaxCompletionTokens
	}
	return modelAdapterTestDefaultMaxTokens
}

func modelAdapterTestConfiguredOpenAIMaxTokens(adapter serverconfig.ModelAdapterConfig) int {
	if adapter.MaxCompletionTokens > 0 {
		return adapter.MaxCompletionTokens
	}
	if adapter.AnthropicMaxTokens > 0 {
		return adapter.AnthropicMaxTokens
	}
	return modelAdapterTestDefaultMaxTokens
}

func normalizeModelAdapterTestBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeModelAdapterTestOpenAIExtraParamsJSON(adapter serverconfig.ModelAdapterConfig) string {
	if normalizeModelAdapterTestType(adapter.Type) != "openai" || !adapter.OpenAIExtraParamsEnabled {
		return ""
	}
	return strings.TrimSpace(adapter.OpenAIExtraParamsJSON)
}

func normalizeModelAdapterTestCustomHeadersJSON(adapter serverconfig.ModelAdapterConfig) string {
	if !adapter.CustomHeadersEnabled {
		return ""
	}
	return strings.TrimSpace(adapter.CustomHeadersJSON)
}

func normalizeModelAdapterTestAnthropicExtraParamsJSON(adapter serverconfig.ModelAdapterConfig) string {
	if normalizeModelAdapterTestType(adapter.Type) != "anthropic" || !adapter.AnthropicExtraParamsEnabled {
		return ""
	}
	return strings.TrimSpace(adapter.AnthropicExtraParamsJSON)
}

func normalizeModelAdapterTestInt(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
