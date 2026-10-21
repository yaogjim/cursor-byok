package config

import (
	"context"
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
	matchIndex, err := matchModelAdapterIndex(adapters, requestedModel)
	if err != nil {
		return nil, err
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
	matchIndex, err := matchModelAdapterIndex(adapters, requestedModel)
	if err != nil {
		return nil, err
	}
	matched := adapters[matchIndex]
	fb := matched.ProviderFallback
	if !fb.Enabled || len(fb.CandidateChannelIDs) == 0 {
		ch := resolveAdapterToChannel(matched)
		plan := &legacyruntime.ChannelPlan{
			Channels:                 []legacyruntime.ResolvedChannel{ch},
			FallbackEnabled:          false,
			MaxHttpAttempts:          fb.MaxHttpAttempts,
			MaxWaitSeconds:           fb.MaxWaitSeconds,
			MaxAttemptsPerChannel:    fb.MaxAttemptsPerChannel,
			ConnectTimeoutSeconds:    fb.ConnectTimeoutSeconds,
			FirstEventTimeoutSeconds: fb.FirstEventTimeoutSeconds,
			StreamIdleTimeoutSeconds: fb.StreamIdleTimeoutSeconds,
			CallTimeoutSeconds:       fb.CallTimeoutSeconds,
		}
		legacyruntime.ClampChannelPlanRecovery(plan)
		return plan, nil
	}
	channels := make([]legacyruntime.ResolvedChannel, 0, 1+len(fb.CandidateChannelIDs))
	if primaryCh, primaryErr := resolveEnabledChannelByID(adapters, fb.PrimaryChannelID); primaryErr == nil {
		channels = append(channels, *primaryCh)
	}
	for _, candidateID := range fb.CandidateChannelIDs {
		candidateCh, candidateErr := resolveEnabledChannelByID(adapters, candidateID)
		if candidateErr != nil {
			continue
		}
		channels = append(channels, *candidateCh)
	}
	if len(channels) == 0 {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	plan := &legacyruntime.ChannelPlan{
		Channels:                 channels,
		FallbackEnabled:          len(channels) > 1,
		MaxHttpAttempts:          fb.MaxHttpAttempts,
		MaxWaitSeconds:           fb.MaxWaitSeconds,
		MaxAttemptsPerChannel:    fb.MaxAttemptsPerChannel,
		ConnectTimeoutSeconds:    fb.ConnectTimeoutSeconds,
		FirstEventTimeoutSeconds: fb.FirstEventTimeoutSeconds,
		StreamIdleTimeoutSeconds: fb.StreamIdleTimeoutSeconds,
		CallTimeoutSeconds:       fb.CallTimeoutSeconds,
	}
	legacyruntime.ClampChannelPlanRecovery(plan)
	return plan, nil
}

func matchModelAdapterIndex(adapters []ModelAdapterConfig, requestedModel string) (int, error) {
	requested := strings.TrimSpace(requestedModel)
	if requested == "" || modelchannel.IsMetaModelAlias(requested) {
		index, ok := firstEnabledModelAdapterIndex(adapters)
		if !ok {
			return -1, legacyruntime.ErrChannelNotAvailable
		}
		return index, nil
	}
	matchIndex, ok := modelchannel.ResolveAdapterIndex(
		adapters,
		requested,
		func(adapter ModelAdapterConfig) string { return adapter.ID },
		func(adapter ModelAdapterConfig) string { return adapter.ModelID },
		func(adapter ModelAdapterConfig) string {
			return modelchannel.BuildLegacyChannelID(adapter.BaseURL, adapter.ModelID, adapter.APIKey, adapter.DisplayName)
		},
	)
	if !ok {
		return -1, legacyruntime.ErrChannelNotAvailable
	}
	if !ModelAdapterEnabled(adapters[matchIndex]) {
		return -1, legacyruntime.ErrChannelNotAvailable
	}
	return matchIndex, nil
}

func firstEnabledModelAdapterIndex(adapters []ModelAdapterConfig) (int, bool) {
	for index, adapter := range adapters {
		if ModelAdapterEnabled(adapter) {
			return index, true
		}
	}
	return -1, false
}

func resolveEnabledChannelByID(adapters []ModelAdapterConfig, channelID string) (*legacyruntime.ResolvedChannel, error) {
	for _, adapter := range adapters {
		if adapter.ID != channelID {
			continue
		}
		if !ModelAdapterEnabled(adapter) {
			return nil, legacyruntime.ErrChannelNotAvailable
		}
		ch := resolveAdapterToChannel(adapter)
		return &ch, nil
	}
	return nil, legacyruntime.ErrChannelNotAvailable
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
		ID:                           strings.TrimSpace(matched.ID),
		Name:                         strings.TrimSpace(matched.DisplayName),
		GroupName:                    "local",
		Code:                         strings.TrimSpace(matched.ID),
		Provider:                     strings.TrimSpace(matched.Type),
		BaseURL:                      strings.TrimSpace(matched.BaseURL),
		APIKey:                       strings.TrimSpace(matched.APIKey),
		CredentialSource:             strings.TrimSpace(matched.CredentialSource),
		Model:                        strings.TrimSpace(matched.ModelID),
		OpenAIEndpoint:               strings.TrimSpace(matched.OpenAIEndpoint),
		OpenAIExtraParamsEnabled:     matched.OpenAIExtraParamsEnabled,
		OpenAIExtraParamsJSON:        strings.TrimSpace(matched.OpenAIExtraParamsJSON),
		OpenAIImageGenerationEnabled: matched.OpenAIImageGenerationEnabled,
		CustomHeadersEnabled:         matched.CustomHeadersEnabled,
		CustomHeadersJSON:            strings.TrimSpace(matched.CustomHeadersJSON),
		AnthropicExtraParamsEnabled:  matched.AnthropicExtraParamsEnabled,
		AnthropicExtraParamsJSON:     strings.TrimSpace(matched.AnthropicExtraParamsJSON),
		TimeoutMS:                    defaultChannelTimeoutMS,
		ContextWindowTokens:          defaultChannelContextWindowTokens,
		MaxTokens:                    defaultChannelMaxTokens,
		ReasoningEffort:              strings.TrimSpace(matched.ReasoningEffort),
		AnthropicMaxTokens:           defaultChannelMaxTokens,
		AnthropicThinkingEffort:      defaultChannelAnthropicEffort,
		ThinkingEnabled:              true,
		ThinkingBudgetTokens:         defaultChannelThinkingBudget,
		MaxConcurrentRequests:        matched.MaxConcurrentRequests,
		UpstreamCapacityGroupKey:     legacyruntime.BuildUpstreamCapacityGroupKey(matched.Type, matched.BaseURL, subscriptionauth.ChannelIDSecret(subscriptionauth.NormalizeCredentialSource(matched.CredentialSource), matched.APIKey)),
		OutboundProxy:                matched.OutboundProxy,
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
