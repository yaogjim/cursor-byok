package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"cursor/internal/modelchannel"
)

const (
	DefaultBackendListenAddr                = "127.0.0.1:18090"
	DefaultProxyListenAddr                  = "127.0.0.1:18080"
	DefaultFrontendBaseURL                  = "http://127.0.0.1"
	DefaultRoutingMode                      = "local"
	DefaultTheme                            = "light"
	DefaultProviderStreamIdleTimeoutSeconds = 240
	MinProviderStreamIdleTimeoutSeconds     = 30
	DefaultObservabilityMode                = "basic"
	ObservabilityModeOff                    = "off"
	ObservabilityModeBasic                  = "basic"
	ObservabilityModeFull                   = "full"
	DefaultObservabilityRetentionDays       = 7
	DefaultObservabilityMaxDiskMB           = 1024
	MinObservabilityRetentionDays           = 1
	MaxObservabilityRetentionDays           = 90
	MinObservabilityMaxDiskMB               = 64
	MaxObservabilityMaxDiskMB               = 10240

	DefaultProviderFallbackMaxHttpAttempts = 5
	MinProviderFallbackMaxHttpAttempts     = 2
	MaxProviderFallbackMaxHttpAttempts     = 9
	DefaultProviderFallbackMaxWaitSeconds  = 8
	MinProviderFallbackMaxWaitSeconds      = 1
	MaxProviderFallbackMaxWaitSeconds      = 30

	MinMaxConcurrentRequests = 1
	MaxMaxConcurrentRequests = 16
)

type ModelAdapterConfig struct {
	ID                          string `json:"id,omitempty" yaml:"-"`
	Sort                        int    `json:"sort" yaml:"sort"`
	DisplayName                 string `json:"displayName" yaml:"displayName"`
	Type                        string `json:"type" yaml:"type"`
	BaseURL                     string `json:"baseURL" yaml:"baseURL"`
	APIKey                      string `json:"apiKey" yaml:"apiKey"`
	TooltipData                 string `json:"tooltipData" yaml:"tooltipData"`
	ModelID                     string `json:"modelID" yaml:"modelID"`
	ReasoningEffort             string `json:"reasoningEffort" yaml:"reasoningEffort"`
	OpenAIEndpoint              string `json:"openAIEndpoint" yaml:"openAIEndpoint"`
	OpenAIExtraParamsEnabled    bool   `json:"openAIExtraParamsEnabled" yaml:"openAIExtraParamsEnabled"`
	OpenAIExtraParamsJSON       string `json:"openAIExtraParamsJSON" yaml:"openAIExtraParamsJSON"`
	CustomHeadersEnabled        bool   `json:"customHeadersEnabled" yaml:"customHeadersEnabled"`
	CustomHeadersJSON           string `json:"customHeadersJSON" yaml:"customHeadersJSON"`
	AnthropicExtraParamsEnabled bool   `json:"anthropicExtraParamsEnabled" yaml:"anthropicExtraParamsEnabled"`
	AnthropicExtraParamsJSON    string `json:"anthropicExtraParamsJSON" yaml:"anthropicExtraParamsJSON"`
	ContextWindowTokens         int    `json:"contextWindowTokens" yaml:"contextWindowTokens"`
	MaxCompletionTokens         int    `json:"maxCompletionTokens" yaml:"maxCompletionTokens"`
	AnthropicMaxTokens          int    `json:"anthropicMaxTokens" yaml:"anthropicMaxTokens"`
	AnthropicThinkingEffort     string `json:"anthropicThinkingEffort,omitempty" yaml:"anthropicThinkingEffort,omitempty"`
	ThinkingBudgetTokens        int    `json:"thinkingBudgetTokens" yaml:"thinkingBudgetTokens"`
	// MaxConcurrentRequests 是物理上游组共享的可选并发上限。缺失/0 表示不限制，非零合法范围 1–16。
	MaxConcurrentRequests int `json:"maxConcurrentRequests,omitempty" yaml:"maxConcurrentRequests,omitempty"`
	// ProviderFallback 表示该渠道的 provider fallback 配置；默认关闭。
	ProviderFallback ProviderFallbackConfig `json:"providerFallback,omitempty" yaml:"providerFallback,omitempty"`
}

type RoutingConfig struct {
	Mode string `json:"mode" yaml:"mode"`
}

type HomeMetricsConfig struct {
	IncludeCacheWriteInHitRate bool `json:"includeCacheWriteInHitRate" yaml:"includeCacheWriteInHitRate"`
}

type AppearanceConfig struct {
	Theme string `json:"theme" yaml:"theme"`
}

type AdvertisingConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

type UpdatesConfig struct {
	CheckOnStartup bool `json:"checkOnStartup" yaml:"checkOnStartup"`
}

// ProviderFallbackConfig 表示显式有序的 provider fallback 链配置。
// 默认关闭（Enabled=false）；启用后 chain 由 PrimaryChannelID 与有序
// CandidateChannelIDs 显式定义，最多支持主渠道 + 2 个候选。
type ProviderFallbackConfig struct {
	// Enabled 表示是否启用 fallback；默认 false。
	Enabled bool `json:"enabled" yaml:"enabled"`
	// PrimaryChannelID 表示首先尝试的渠道 ID；仅 Enabled=true 时有意义。
	PrimaryChannelID string `json:"primaryChannelID" yaml:"primaryChannelID"`
	// CandidateChannelIDs 表示有序候选渠道 ID 列表，最多 2 个；仅 Enabled=true 时有意义。
	CandidateChannelIDs []string `json:"candidateChannelIDs" yaml:"candidateChannelIDs"`
	// MaxHttpAttempts 是整条 fallback 链共享的 HTTP 尝试上限。缺失/0 归一化为 5，合法范围 2–9。
	MaxHttpAttempts int `json:"maxHttpAttempts,omitempty" yaml:"maxHttpAttempts,omitempty"`
	// MaxWaitSeconds 是整条 fallback 链共享的退避等待上限（秒）。缺失/0 归一化为 8，合法范围 1–30。
	MaxWaitSeconds int `json:"maxWaitSeconds,omitempty" yaml:"maxWaitSeconds,omitempty"`
}

// InvalidProviderFallbackBudgetError 表示保存入口拒绝的非零越界 fallback 预算。
type InvalidProviderFallbackBudgetError struct {
	Field string
	Value int
	Min   int
	Max   int
}

func (e *InvalidProviderFallbackBudgetError) Error() string {
	if e == nil {
		return "providerFallback 预算非法"
	}
	return fmt.Sprintf("模型适配器 providerFallback.%s=%d 超出合法范围 %d–%d", e.Field, e.Value, e.Min, e.Max)
}

// InvalidMaxConcurrentRequestsError 表示保存入口拒绝的非零越界上游并发上限。
type InvalidMaxConcurrentRequestsError struct {
	Value int
}

func (e *InvalidMaxConcurrentRequestsError) Error() string {
	if e == nil {
		return "maxConcurrentRequests 非法"
	}
	return fmt.Sprintf("模型适配器 maxConcurrentRequests=%d 超出合法范围：缺失或 0 表示不限制，非零必须为 %d–%d", e.Value, MinMaxConcurrentRequests, MaxMaxConcurrentRequests)
}

type ObservabilityConfig struct {
	Mode          string `json:"mode" yaml:"mode"`
	RetentionDays int    `json:"retentionDays" yaml:"retentionDays"`
	MaxDiskMB     int    `json:"maxDiskMB" yaml:"maxDiskMB"`
}

type Config struct {
	LegacyLog                 *bool                `json:"-" yaml:"log,omitempty"`
	Observability             ObservabilityConfig  `json:"observability" yaml:"observability"`
	ProviderStreamIdleTimeout int                  `json:"providerStreamIdleTimeout" yaml:"providerStreamIdleTimeout"`
	BackendListenAddr         string               `json:"backendListenAddr" yaml:"backendListenAddr"`
	ProxyListenAddr           string               `json:"proxyListenAddr" yaml:"proxyListenAddr"`
	ModelAdapters             []ModelAdapterConfig `json:"modelAdapters" yaml:"modelAdapters"`
	Routing                   RoutingConfig        `json:"routing" yaml:"routing"`
	HomeMetrics               HomeMetricsConfig    `json:"homeMetrics" yaml:"homeMetrics"`
	Appearance                AppearanceConfig     `json:"appearance" yaml:"appearance"`
	Advertising               AdvertisingConfig    `json:"advertising" yaml:"advertising"`
	Updates                   UpdatesConfig        `json:"updates" yaml:"updates"`
	LastAgentModelHash        string               `json:"lastAgentModelHash" yaml:"lastAgentModelHash"`
}

func DefaultConfig() Config {
	return Config{
		Observability: ObservabilityConfig{
			Mode:          DefaultObservabilityMode,
			RetentionDays: DefaultObservabilityRetentionDays,
			MaxDiskMB:     DefaultObservabilityMaxDiskMB,
		},
		ProviderStreamIdleTimeout: DefaultProviderStreamIdleTimeoutSeconds,
		BackendListenAddr:         DefaultBackendListenAddr,
		ProxyListenAddr:           DefaultProxyListenAddr,
		ModelAdapters:             []ModelAdapterConfig{},
		Routing: RoutingConfig{
			Mode: DefaultRoutingMode,
		},
		Appearance: AppearanceConfig{
			Theme: DefaultTheme,
		},
		Advertising: AdvertisingConfig{
			Enabled: false,
		},
		Updates: UpdatesConfig{
			CheckOnStartup: false,
		},
	}
}

func NormalizeConfig(input Config) (Config, error) {
	output := DefaultConfig()
	if strings.TrimSpace(input.Observability.Mode) != "" && normalizeObservabilityMode(input.Observability.Mode) == "" {
		return Config{}, errors.New("observability.mode 仅支持 off、basic 或 full")
	}
	output.Observability = normalizeObservabilityConfig(input.Observability, input.LegacyLog)
	output.ProviderStreamIdleTimeout = normalizeProviderStreamIdleTimeout(input.ProviderStreamIdleTimeout)
	backendListenAddr, err := normalizeListenAddr(input.BackendListenAddr, DefaultBackendListenAddr, "backendListenAddr")
	if err != nil {
		return Config{}, err
	}
	proxyListenAddr, err := normalizeListenAddr(input.ProxyListenAddr, DefaultProxyListenAddr, "proxyListenAddr")
	if err != nil {
		return Config{}, err
	}
	output.BackendListenAddr = backendListenAddr
	output.ProxyListenAddr = proxyListenAddr
	output.HomeMetrics.IncludeCacheWriteInHitRate = input.HomeMetrics.IncludeCacheWriteInHitRate
	output.Appearance.Theme = normalizeTheme(input.Appearance.Theme)
	output.Advertising.Enabled = input.Advertising.Enabled
	output.Updates.CheckOnStartup = input.Updates.CheckOnStartup
	output.LastAgentModelHash = strings.TrimSpace(input.LastAgentModelHash)
	output.Routing.Mode = normalizeRoutingMode(input.Routing.Mode)
	if output.Routing.Mode == "" {
		output.Routing.Mode = DefaultRoutingMode
	}
	adapters, err := NormalizeModelAdapterConfigs(input.ModelAdapters)
	if err != nil {
		return Config{}, err
	}
	output.ModelAdapters = adapters
	return output, nil
}

func NormalizeModelAdapterConfigs(input []ModelAdapterConfig) ([]ModelAdapterConfig, error) {
	if len(input) == 0 {
		return []ModelAdapterConfig{}, nil
	}

	normalized := make([]ModelAdapterConfig, 0, len(input))
	seenChannelIDs := make(map[string]struct{}, len(input))
	for _, item := range input {
		baseURL, err := modelchannel.NormalizeBaseURL(item.BaseURL)
		if err != nil {
			return nil, err
		}
		nextType := normalizeModelAdapterType(item.Type)
		next := ModelAdapterConfig{
			Sort:                  item.Sort,
			DisplayName:           strings.TrimSpace(item.DisplayName),
			Type:                  nextType,
			BaseURL:               baseURL,
			APIKey:                strings.TrimSpace(item.APIKey),
			TooltipData:           strings.TrimSpace(item.TooltipData),
			ModelID:               strings.TrimSpace(item.ModelID),
			ReasoningEffort:       normalizeReasoningEffort(item.ReasoningEffort),
			OpenAIEndpoint:        modelchannel.NormalizeOpenAIEndpoint(item.Type, item.OpenAIEndpoint),
			ContextWindowTokens:   normalizeMaxCompletionTokens(item.ContextWindowTokens),
			MaxCompletionTokens:   normalizeMaxCompletionTokens(item.MaxCompletionTokens),
			AnthropicMaxTokens:    normalizeMaxCompletionTokens(item.AnthropicMaxTokens),
			ThinkingBudgetTokens:  normalizeMaxCompletionTokens(item.ThinkingBudgetTokens),
			MaxConcurrentRequests: item.MaxConcurrentRequests,
		}
		if next.Type == "openai" {
			next.OpenAIExtraParamsEnabled = item.OpenAIExtraParamsEnabled
			next.OpenAIExtraParamsJSON = strings.TrimSpace(item.OpenAIExtraParamsJSON)
		} else if next.Type == "anthropic" {
			next.AnthropicThinkingEffort = normalizeAnthropicThinkingEffort(item.AnthropicThinkingEffort)
			next.AnthropicExtraParamsEnabled = item.AnthropicExtraParamsEnabled
			next.AnthropicExtraParamsJSON = strings.TrimSpace(item.AnthropicExtraParamsJSON)
		}
		next.CustomHeadersEnabled = item.CustomHeadersEnabled
		next.CustomHeadersJSON = strings.TrimSpace(item.CustomHeadersJSON)
		switch {
		case next.DisplayName == "":
			return nil, errors.New("模型适配器 displayName 不能为空")
		case next.Type == "":
			return nil, errors.New("模型适配器 type 仅支持 openai 或 anthropic")
		case next.APIKey == "":
			return nil, errors.New("模型适配器 apiKey 不能为空")
		case next.TooltipData == "":
			return nil, errors.New("模型适配器 tooltipData 不能为空")
		case next.ModelID == "":
			return nil, errors.New("模型适配器 modelID 不能为空")
		case next.Type == "openai" && !isSupportedReasoningEffort(next.ReasoningEffort):
			return nil, errors.New("模型适配器 reasoningEffort 仅支持空值、low、medium、high、xhigh、max")
		case next.Type == "openai" && next.OpenAIEndpoint == "":
			return nil, errors.New("模型适配器 openAIEndpoint 仅支持 /v1/responses、/v1/chat/completions 或 /custom（自定义路径）")
		case next.Type == "openai" && next.OpenAIExtraParamsEnabled:
			if err := validateJSONMap(next.OpenAIExtraParamsJSON, "openAIExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.CustomHeadersEnabled:
			if err := validateHeadersJSON(next.CustomHeadersJSON); err != nil {
				return nil, err
			}
		case next.Type == "anthropic" && next.AnthropicExtraParamsEnabled:
			if err := validateJSONMap(next.AnthropicExtraParamsJSON, "anthropicExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.Type == "anthropic" && next.AnthropicThinkingEffort == "":
			return nil, errors.New("模型适配器 anthropicThinkingEffort 仅支持 low、medium、high、xhigh、max")
		}
		limit, err := normalizeMaxConcurrentRequests(next.MaxConcurrentRequests)
		if err != nil {
			return nil, err
		}
		next.MaxConcurrentRequests = limit
		next.ID = modelchannel.BuildChannelID(next.BaseURL, next.ModelID, next.APIKey, next.DisplayName, next.OpenAIEndpoint)
		if _, exists := seenChannelIDs[next.ID]; exists {
			return nil, errors.New("模型适配器渠道不能重复，请检查 url、modelID、apiKey、displayName、endpoint 组合")
		}
		seenChannelIDs[next.ID] = struct{}{}
		// 原样复制 ProviderFallback；第二遍在 validateProviderFallbacks 中校验与归一化。
		next.ProviderFallback = item.ProviderFallback
		normalized = append(normalized, next)
	}
	normalizeModelAdapterSorts(normalized)
	if err := validateProviderFallbacks(normalized, seenChannelIDs); err != nil {
		return nil, err
	}
	if err := validateUpstreamCapacityGroups(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeModelAdapterSorts(adapters []ModelAdapterConfig) {
	sort.SliceStable(adapters, func(leftIndex, rightIndex int) bool {
		left := adapters[leftIndex].Sort
		right := adapters[rightIndex].Sort
		switch {
		case left <= 0 && right <= 0:
			return false
		case left <= 0:
			return false
		case right <= 0:
			return true
		default:
			return left < right
		}
	})
	for index := range adapters {
		adapters[index].Sort = index + 1
	}
}

func validateJSONMap(value string, fieldName string) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return fmt.Errorf("模型适配器 %s 不能为空", fieldName)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return fmt.Errorf("模型适配器 %s 必须是合法 JSON 对象", fieldName)
	}
	if parsed == nil {
		return fmt.Errorf("模型适配器 %s 必须是 JSON 对象", fieldName)
	}
	return nil
}

func validateHeadersJSON(value string) error {
	text := strings.TrimSpace(value)
	if err := validateJSONMap(text, "customHeadersJSON"); err != nil {
		return err
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return errors.New("模型适配器 customHeadersJSON 的值必须是字符串")
	}
	for key := range parsed {
		if strings.TrimSpace(key) == "" {
			return errors.New("模型适配器 customHeadersJSON 的请求头名称不能为空")
		}
	}
	return nil
}

func normalizeReasoningEffort(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isSupportedReasoningEffort(value string) bool {
	switch value {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func normalizeAnthropicThinkingEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "xhigh":
		return "xhigh"
	case "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeListenAddr(value string, defaultValue string, fieldName string) (string, error) {
	addr := strings.TrimSpace(value)
	if addr == "" {
		addr = defaultValue
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%s 必须是 host:port 格式", fieldName)
	}
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("%s host 不能为空", fieldName)
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", fmt.Errorf("%s port 必须在 1-65535 之间", fieldName)
	}
	return net.JoinHostPort(host, strconv.Itoa(parsedPort)), nil
}

func normalizeProviderStreamIdleTimeout(value int) int {
	if value <= 0 {
		return DefaultProviderStreamIdleTimeoutSeconds
	}
	if value < MinProviderStreamIdleTimeoutSeconds {
		return MinProviderStreamIdleTimeoutSeconds
	}
	return value
}

func normalizeObservabilityConfig(input ObservabilityConfig, legacyLog *bool) ObservabilityConfig {
	mode := normalizeObservabilityMode(input.Mode)
	if mode == "" {
		mode = DefaultObservabilityMode
		if input == (ObservabilityConfig{}) && legacyLog != nil {
			if *legacyLog {
				mode = ObservabilityModeFull
			} else {
				mode = ObservabilityModeOff
			}
		}
	}
	return ObservabilityConfig{
		Mode:          mode,
		RetentionDays: clampPositiveInt(input.RetentionDays, DefaultObservabilityRetentionDays, MinObservabilityRetentionDays, MaxObservabilityRetentionDays),
		MaxDiskMB:     clampPositiveInt(input.MaxDiskMB, DefaultObservabilityMaxDiskMB, MinObservabilityMaxDiskMB, MaxObservabilityMaxDiskMB),
	}
}

func isFullObservabilityMode(value string) bool {
	return normalizeObservabilityMode(value) == ObservabilityModeFull
}

func normalizeObservabilityMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ObservabilityModeOff:
		return ObservabilityModeOff
	case ObservabilityModeBasic:
		return ObservabilityModeBasic
	case ObservabilityModeFull:
		return ObservabilityModeFull
	default:
		return ""
	}
}

func clampPositiveInt(value int, fallback int, minimum int, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func normalizeMaxCompletionTokens(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func normalizeModelAdapterType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	default:
		return ""
	}
}

// validateProviderFallbacks 在所有渠道 ID 已计算完毕后，对每个启用 fallback 的适配器
// 执行引用完整性、自引用、重复检测，并原地归一化（trim）候选列表。
func validateProviderFallbacks(normalized []ModelAdapterConfig, knownIDs map[string]struct{}) error {
	logicalIDs := make(map[string]struct{}, len(normalized))
	for i := range normalized {
		if normalized[i].ProviderFallback.Enabled {
			logicalIDs[normalized[i].ID] = struct{}{}
		}
	}
	for i := range normalized {
		fb := normalized[i].ProviderFallback
		if err := normalizeProviderFallbackBudget(&fb); err != nil {
			return err
		}
		if !fb.Enabled {
			normalized[i].ProviderFallback = fb
			continue
		}
		primary := strings.TrimSpace(fb.PrimaryChannelID)
		if primary == "" {
			return errors.New("模型适配器 providerFallback.primaryChannelID 不能为空")
		}
		if _, ok := knownIDs[primary]; !ok {
			return fmt.Errorf("模型适配器 providerFallback.primaryChannelID 引用了不存在的渠道 %q", primary)
		}
		if primary == normalized[i].ID {
			return errors.New("模型适配器 providerFallback.primaryChannelID 不能与当前渠道相同")
		}
		if _, logical := logicalIDs[primary]; logical {
			return fmt.Errorf("模型适配器 providerFallback.primaryChannelID 必须引用未启用 fallback 的物理渠道 %q", primary)
		}
		if len(fb.CandidateChannelIDs) == 0 || len(fb.CandidateChannelIDs) > 2 {
			return fmt.Errorf("模型适配器 providerFallback.candidateChannelIDs 数量必须为 1–2 个，当前为 %d 个", len(fb.CandidateChannelIDs))
		}
		// seenInChain 防重复；预置 primary 与当前渠道自身 ID
		seenInChain := map[string]struct{}{
			primary:          {},
			normalized[i].ID: {},
		}
		trimmed := make([]string, 0, len(fb.CandidateChannelIDs))
		for _, rawCID := range fb.CandidateChannelIDs {
			cid := strings.TrimSpace(rawCID)
			if cid == "" {
				return errors.New("模型适配器 providerFallback.candidateChannelIDs 包含空渠道 ID")
			}
			if _, ok := knownIDs[cid]; !ok {
				return fmt.Errorf("模型适配器 providerFallback.candidateChannelIDs 引用了不存在的渠道 %q", cid)
			}
			if _, dup := seenInChain[cid]; dup {
				return fmt.Errorf("模型适配器 providerFallback.candidateChannelIDs 包含重复或自引用渠道 %q", cid)
			}
			if _, logical := logicalIDs[cid]; logical {
				return fmt.Errorf("模型适配器 providerFallback.candidateChannelIDs 必须引用未启用 fallback 的物理渠道 %q", cid)
			}
			seenInChain[cid] = struct{}{}
			trimmed = append(trimmed, cid)
		}
		// 写回归一化后的 fallback 配置
		normalized[i].ProviderFallback = ProviderFallbackConfig{
			Enabled:             true,
			PrimaryChannelID:    primary,
			CandidateChannelIDs: trimmed,
			MaxHttpAttempts:     fb.MaxHttpAttempts,
			MaxWaitSeconds:      fb.MaxWaitSeconds,
		}
	}
	return nil
}

func normalizeProviderFallbackBudget(fb *ProviderFallbackConfig) error {
	if fb == nil {
		return nil
	}
	attempts, err := normalizeProviderFallbackInt(fb.MaxHttpAttempts, "maxHttpAttempts", DefaultProviderFallbackMaxHttpAttempts, MinProviderFallbackMaxHttpAttempts, MaxProviderFallbackMaxHttpAttempts)
	if err != nil {
		return err
	}
	wait, err := normalizeProviderFallbackInt(fb.MaxWaitSeconds, "maxWaitSeconds", DefaultProviderFallbackMaxWaitSeconds, MinProviderFallbackMaxWaitSeconds, MaxProviderFallbackMaxWaitSeconds)
	if err != nil {
		return err
	}
	fb.MaxHttpAttempts = attempts
	fb.MaxWaitSeconds = wait
	return nil
}

func normalizeProviderFallbackInt(value int, field string, defaultValue, minValue, maxValue int) (int, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < minValue || value > maxValue {
		return 0, &InvalidProviderFallbackBudgetError{Field: field, Value: value, Min: minValue, Max: maxValue}
	}
	return value, nil
}

func normalizeMaxConcurrentRequests(value int) (int, error) {
	if value == 0 {
		return 0, nil
	}
	if value < MinMaxConcurrentRequests || value > MaxMaxConcurrentRequests {
		return 0, &InvalidMaxConcurrentRequestsError{Value: value}
	}
	return value, nil
}

func upstreamCapacityGroupIdentity(adapter ModelAdapterConfig) string {
	return strings.Join([]string{
		strings.TrimSpace(adapter.Type),
		strings.TrimSpace(adapter.BaseURL),
		strings.TrimSpace(adapter.APIKey),
	}, "\n")
}

func validateUpstreamCapacityGroups(normalized []ModelAdapterConfig) error {
	type groupState struct {
		limit int
		name  string
	}
	groups := make(map[string]groupState, len(normalized))
	for i := range normalized {
		adapter := normalized[i]
		if adapter.ProviderFallback.Enabled {
			if adapter.MaxConcurrentRequests != 0 {
				return errors.New("启用 fallback 的逻辑 alias 的 maxConcurrentRequests 必须为 0")
			}
			continue
		}
		key := upstreamCapacityGroupIdentity(adapter)
		state, seen := groups[key]
		if !seen {
			groups[key] = groupState{limit: adapter.MaxConcurrentRequests, name: adapter.DisplayName}
			continue
		}
		if state.limit != adapter.MaxConcurrentRequests {
			return fmt.Errorf("物理适配器 %q 与 %q 共享同一上游接口和密钥，maxConcurrentRequests 必须相同", state.name, adapter.DisplayName)
		}
	}
	return nil
}

func normalizeTheme(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "light":
		return "light"
	case "dark":
		return "dark"
	default:
		return DefaultTheme
	}
}

func normalizeRoutingMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "local":
		return "local"
	case "upstream":
		return "upstream"
	default:
		return ""
	}
}
