package config

import (
	"context"
	"fmt"
	"strings"

	"cursor/internal/modelchannel"
	legacyruntime "cursor/internal/runtime"
	"cursor/internal/subscriptionauth"
)

const (
	defaultChannelTimeoutMS           = int((2 * 60 * 60) * 1000)
	defaultChannelContextWindowTokens = 200_000
	defaultChannelMaxTokens           = 65_536
	defaultChannelThinkingBudget      = 4_096
	defaultChannelAnthropicEffort     = "xhigh"
)

func (manager *Manager) SelectChannelForModel(_ context.Context, modelID string) (*legacyruntime.ResolvedChannel, error) {
	if manager == nil {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	adapters, err := NormalizeModelAdapterConfigs(manager.Current().ModelAdapters)
	if err != nil {
		return nil, err
	}
	return resolveModelAdapterChannel(adapters, modelID)
}

func resolveModelAdapterChannel(adapters []ModelAdapterConfig, requestedModel string) (*legacyruntime.ResolvedChannel, error) {
	matchIndex, ok := modelchannel.ResolveAdapterIndex(
		adapters,
		requestedModel,
		func(adapter ModelAdapterConfig) string { return adapter.ID },
		func(adapter ModelAdapterConfig) string { return adapter.ModelID },
		func(adapter ModelAdapterConfig) string {
			return modelchannel.BuildLegacyChannelID(adapter.BaseURL, adapter.ModelID, adapter.APIKey, adapter.DisplayName)
		},
	)
	if !ok {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	resolved := resolveAdapterToChannel(adapters[matchIndex])
	return &resolved, nil
}

// SelectChannelPlanForModel 解析指定模型的渠道计划（含 fallback 链）。
// 若模型适配器启用了 ProviderFallback，返回多渠道 ChannelPlan；否则返回单渠道计划。
func (manager *Manager) SelectChannelPlanForModel(_ context.Context, modelID string) (*legacyruntime.ChannelPlan, error) {
	if manager == nil {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	adapters, err := NormalizeModelAdapterConfigs(manager.Current().ModelAdapters)
	if err != nil {
		return nil, err
	}
	return resolveModelAdapterChannelPlan(adapters, modelID)
}

// resolveModelAdapterChannelPlan 按模型 ID 解析渠道计划。
// 若匹配适配器启用了 ProviderFallback，计划包含 primary + candidates；否则只含一个渠道。
func resolveModelAdapterChannelPlan(adapters []ModelAdapterConfig, requestedModel string) (*legacyruntime.ChannelPlan, error) {
	matchIndex, ok := modelchannel.ResolveAdapterIndex(
		adapters,
		requestedModel,
		func(adapter ModelAdapterConfig) string { return adapter.ID },
		func(adapter ModelAdapterConfig) string { return adapter.ModelID },
		func(adapter ModelAdapterConfig) string {
			return modelchannel.BuildLegacyChannelID(adapter.BaseURL, adapter.ModelID, adapter.APIKey, adapter.DisplayName)
		},
	)
	if !ok {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	matched := adapters[matchIndex]
	fb := matched.ProviderFallback
	attempts, waitSeconds := legacyruntime.ClampFallbackChainBudget(fb.MaxHttpAttempts, fb.MaxWaitSeconds)
	if !fb.Enabled || len(fb.CandidateChannelIDs) == 0 {
		ch := resolveAdapterToChannel(matched)
		return &legacyruntime.ChannelPlan{
			Channels:        []legacyruntime.ResolvedChannel{ch},
			FallbackEnabled: false,
			MaxHttpAttempts: attempts,
			MaxWaitSeconds:  waitSeconds,
		}, nil
	}
	// Fallback 已启用：构建链 [primary, candidates...]
	// primary != matched.ID 已由 NormalizeModelAdapterConfigs 校验。
	primaryCh, err := resolveChannelByID(adapters, fb.PrimaryChannelID)
	if err != nil {
		return nil, fmt.Errorf("fallback primaryChannelID %q: %w", fb.PrimaryChannelID, err)
	}
	channels := []legacyruntime.ResolvedChannel{*primaryCh}
	for _, candidateID := range fb.CandidateChannelIDs {
		candidateCh, err := resolveChannelByID(adapters, candidateID)
		if err != nil {
			return nil, fmt.Errorf("fallback candidateChannelIDs %q: %w", candidateID, err)
		}
		channels = append(channels, *candidateCh)
	}
	return &legacyruntime.ChannelPlan{
		Channels:        channels,
		FallbackEnabled: true,
		MaxHttpAttempts: attempts,
		MaxWaitSeconds:  waitSeconds,
	}, nil
}

// resolveChannelByID 按渠道 ID（adapter.ID）在已归一化列表中查找并转换为 ResolvedChannel。
func resolveChannelByID(adapters []ModelAdapterConfig, channelID string) (*legacyruntime.ResolvedChannel, error) {
	for _, adapter := range adapters {
		if adapter.ID == channelID {
			ch := resolveAdapterToChannel(adapter)
			return &ch, nil
		}
	}
	return nil, legacyruntime.ErrChannelNotAvailable
}

// resolveAdapterToChannel 将单条 ModelAdapterConfig 转换为 ResolvedChannel。
// 与 resolveModelAdapterChannel 保持相同字段映射语义，供 fallback 路径复用。
func resolveAdapterToChannel(matched ModelAdapterConfig) legacyruntime.ResolvedChannel {
	resolved := legacyruntime.ResolvedChannel{
		ID:                          strings.TrimSpace(matched.ID),
		Name:                        strings.TrimSpace(matched.DisplayName),
		GroupName:                   "local",
		Code:                        strings.TrimSpace(matched.ID),
		Provider:                    strings.TrimSpace(matched.Type),
		BaseURL:                     strings.TrimSpace(matched.BaseURL),
		APIKey:                      strings.TrimSpace(matched.APIKey),
		CredentialSource:            strings.TrimSpace(matched.CredentialSource),
		Model:                       strings.TrimSpace(matched.ModelID),
		OpenAIEndpoint:              strings.TrimSpace(matched.OpenAIEndpoint),
		OpenAIExtraParamsEnabled:    matched.OpenAIExtraParamsEnabled,
		OpenAIExtraParamsJSON:       strings.TrimSpace(matched.OpenAIExtraParamsJSON),
		CustomHeadersEnabled:        matched.CustomHeadersEnabled,
		CustomHeadersJSON:           strings.TrimSpace(matched.CustomHeadersJSON),
		AnthropicExtraParamsEnabled: matched.AnthropicExtraParamsEnabled,
		AnthropicExtraParamsJSON:    strings.TrimSpace(matched.AnthropicExtraParamsJSON),
		TimeoutMS:                   defaultChannelTimeoutMS,
		ContextWindowTokens:         defaultChannelContextWindowTokens,
		MaxTokens:                   defaultChannelMaxTokens,
		ReasoningEffort:             strings.TrimSpace(matched.ReasoningEffort),
		AnthropicMaxTokens:          defaultChannelMaxTokens,
		AnthropicThinkingEffort:     defaultChannelAnthropicEffort,
		ThinkingEnabled:             true,
		ThinkingBudgetTokens:        defaultChannelThinkingBudget,
		MaxConcurrentRequests:       matched.MaxConcurrentRequests,
		UpstreamCapacityGroupKey:    legacyruntime.BuildUpstreamCapacityGroupKey(matched.Type, matched.BaseURL, subscriptionauth.ChannelIDSecret(subscriptionauth.NormalizeCredentialSource(matched.CredentialSource), matched.APIKey)),
	}
	if matched.ContextWindowTokens > 0 {
		resolved.ContextWindowTokens = matched.ContextWindowTokens
	}
	if matched.MaxCompletionTokens > 0 {
		resolved.MaxTokens = matched.MaxCompletionTokens
	}
	if matched.AnthropicMaxTokens > 0 {
		resolved.AnthropicMaxTokens = matched.AnthropicMaxTokens
	}
	if matched.ThinkingBudgetTokens > 0 {
		resolved.ThinkingBudgetTokens = matched.ThinkingBudgetTokens
	}
	if strings.TrimSpace(matched.AnthropicThinkingEffort) != "" {
		resolved.AnthropicThinkingEffort = strings.TrimSpace(matched.AnthropicThinkingEffort)
	}
	return resolved
}
