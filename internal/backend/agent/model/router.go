// router.go 按模型标识选择 OpenAI 或 Anthropic 兼容适配器。
package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	legacyruntime "cursor/internal/runtime"
	"cursor/internal/subscriptionauth"
)

// Router 是 MVP 阶段的模型适配路由器。
type Router struct {
	// openai 负责 OpenAI 兼容流式请求。
	openai ModelAdapter
	// anthropic 负责 Anthropic 兼容流式请求。
	anthropic ModelAdapter
	// resolver 负责从本地配置中解析实际模型通道。
	resolver ChannelResolver
	// credentials 负责 managed channel 的运行时凭据解析；nil 时 static 行为不变。
	credentials subscriptionauth.CredentialResolver
}

type ChannelResolver interface {
	SelectChannelForModel(context.Context, string) (*legacyruntime.ResolvedChannel, error)
	ProviderStreamIdleTimeout(context.Context) time.Duration
}

// ChannelPlanResolver 扩展 ChannelResolver，增加多渠道 fallback 计划解析能力。
// config.Manager 同时实现两个接口；FallbackAwareRouter 需要此接口。
type ChannelPlanResolver interface {
	ChannelResolver
	SelectChannelPlanForModel(context.Context, string) (*legacyruntime.ChannelPlan, error)
}

// NewRouter 创建模型适配路由器。
func NewRouter(resolver ChannelResolver) *Router {
	return &Router{
		openai:    NewOpenAIAdapter(),
		anthropic: NewAnthropicAdapter(),
		resolver:  resolver,
	}
}

func (router *Router) SetCredentialResolver(credentials subscriptionauth.CredentialResolver) {
	if router == nil {
		return
	}
	router.credentials = credentials
}

// Stream 根据模型标识选择具体 provider 并转发请求。
func (router *Router) Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	if router == nil || router.resolver == nil {
		return fmt.Errorf("model adapter resolver is unavailable")
	}
	channel, err := router.resolver.SelectChannelForModel(ctx, req.ModelID)
	if err != nil {
		return err
	}
	if channel == nil {
		return fmt.Errorf("no available channel for model %q", req.ModelID)
	}

	resolved := applyChannelToRequest(req, channel, router.resolver.ProviderStreamIdleTimeout(ctx))
	return router.streamPreResolved(ctx, resolved, sink)
}

// streamPreResolved 使用已完整填充的 StreamRequest（Provider 字段已设置）直接选择适配器。
// 供 FallbackAwareRouter 使用，跳过 SelectChannelForModel 解析步骤。
func (router *Router) streamPreResolved(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	resolved, err := router.applyRuntimeCredentials(ctx, req)
	if err != nil {
		return err
	}
	release, err := acquireUpstreamCapacity(ctx, resolved)
	if err != nil {
		return err
	}
	defer release()

	eventObserved := false
	wrappedSink := func(event ModelEvent) error {
		eventObserved = true
		return sink(event)
	}
	err = router.dispatchResolved(ctx, resolved, wrappedSink)
	if err == nil {
		return nil
	}
	retryReq, shouldRetry, retryErr := router.prepareManagedCredentialRetry(ctx, resolved, err, eventObserved)
	if retryErr != nil {
		return retryErr
	}
	if !shouldRetry {
		return err
	}
	return router.dispatchResolved(ctx, retryReq, wrappedSink)
}

func (router *Router) dispatchResolved(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	switch strings.TrimSpace(req.Provider) {
	case "anthropic":
		return router.anthropic.Stream(ctx, req, sink)
	case "openai":
		return router.openai.Stream(ctx, req, sink)
	default:
		return fmt.Errorf("unsupported provider %q", req.Provider)
	}
}

func (router *Router) applyRuntimeCredentials(ctx context.Context, req StreamRequest) (StreamRequest, error) {
	source := subscriptionauth.NormalizeCredentialSource(req.CredentialSource)
	if source == "" {
		source = subscriptionauth.CredentialSourceStatic
	}
	req.CredentialSource = string(source)
	if !source.Managed() {
		return req, nil
	}
	if router == nil || router.credentials == nil {
		return req, fmt.Errorf("managed credential source %q is unavailable", source)
	}
	cred, err := router.credentials.Resolve(ctx, source)
	if err != nil {
		return req, err
	}
	return applyCredentialToRequest(req, cred)
}

func applyCredentialToRequest(req StreamRequest, cred subscriptionauth.Credential) (StreamRequest, error) {
	req.APIKey = strings.TrimSpace(cred.AccessToken)
	req.CredentialID = strings.TrimSpace(cred.AccountID)
	req.ChatGPTAccountID = strings.TrimSpace(cred.ChatGPTAccountID)
	if req.APIKey == "" {
		return req, subscriptionauth.ErrAuthRequired
	}
	return req, nil
}

func isUnauthorizedHTTPStatus(err error) bool {
	var httpErr *HTTPStatusError
	return errors.As(err, &httpErr) && httpErr != nil && httpErr.StatusCode == 401
}

func isModelAdapterTestRequest(req StreamRequest) bool {
	const prefix = "model-adapter-test-"
	return strings.HasPrefix(req.RequestID, prefix) ||
		strings.HasPrefix(req.RunID, prefix) ||
		strings.HasPrefix(req.ModelCallID, prefix)
}

func managedCredentialRetryBudgetAvailable(req StreamRequest) bool {
	if req.FallbackBudget == nil {
		return true
	}
	remaining, _ := req.FallbackBudget.Remaining()
	return remaining > 0
}

func (router *Router) prepareManagedCredentialRetry(ctx context.Context, req StreamRequest, err error, eventObserved bool) (StreamRequest, bool, error) {
	if eventObserved || err == nil || router == nil || router.credentials == nil {
		return StreamRequest{}, false, nil
	}
	if req.FallbackSafety != nil && req.FallbackSafety.Snapshot().ModelEventObserved {
		return StreamRequest{}, false, nil
	}
	source := subscriptionauth.NormalizeCredentialSource(req.CredentialSource)
	if !source.Managed() || !managedCredentialRetryBudgetAvailable(req) {
		return StreamRequest{}, false, nil
	}
	switch source {
	case subscriptionauth.CredentialSourceCodex:
		if !isUnauthorizedHTTPStatus(err) {
			return StreamRequest{}, false, nil
		}
		cred, resolveErr := router.credentials.ResolveAfterUnauthorized(ctx, source)
		if resolveErr != nil {
			return StreamRequest{}, false, resolveErr
		}
		next, applyErr := applyCredentialToRequest(req, cred)
		if applyErr != nil {
			return StreamRequest{}, false, applyErr
		}
		return next, true, nil
	case subscriptionauth.CredentialSourceGrok:
		if isModelAdapterTestRequest(req) || isUnauthorizedHTTPStatus(err) || !subscriptionauth.IsQuotaError(err) {
			return StreamRequest{}, false, nil
		}
		if markErr := router.credentials.MarkQuotaExhausted(ctx, req.CredentialID); markErr != nil {
			if errors.Is(markErr, subscriptionauth.ErrQuotaExhausted) {
				return StreamRequest{}, false, markErr
			}
			return StreamRequest{}, false, nil
		}
		cred, resolveErr := router.credentials.Resolve(ctx, source)
		if resolveErr != nil || strings.TrimSpace(cred.AccountID) == "" || strings.TrimSpace(cred.AccountID) == strings.TrimSpace(req.CredentialID) {
			return StreamRequest{}, false, subscriptionauth.ErrQuotaExhausted
		}
		next, applyErr := applyCredentialToRequest(req, cred)
		if applyErr != nil {
			return StreamRequest{}, false, subscriptionauth.ErrQuotaExhausted
		}
		return next, true, nil
	default:
		return StreamRequest{}, false, nil
	}
}

// applyChannelToRequest 将 ResolvedChannel 的字段映射到 StreamRequest 副本中并返回。
// 深拷贝 RequestKnobs，以确保 fallback 多渠道循环中各次调用互不污染同一 map。
func applyChannelToRequest(req StreamRequest, channel *legacyruntime.ResolvedChannel, idleTimeout time.Duration) StreamRequest {
	resolved := req
	// 深拷贝 RequestKnobs，防止 fallback 循环中多个渠道共享同一 map 造成互相覆盖。
	if req.RequestKnobs != nil {
		newKnobs := make(map[string]any, len(req.RequestKnobs))
		for k, v := range req.RequestKnobs {
			newKnobs[k] = v
		}
		resolved.RequestKnobs = newKnobs
	}
	resolved.Provider = strings.TrimSpace(channel.Provider)
	resolved.BaseURL = strings.TrimSpace(channel.BaseURL)
	resolved.APIKey = strings.TrimSpace(channel.APIKey)
	resolved.CredentialSource = strings.TrimSpace(channel.CredentialSource)
	resolved.MaxConcurrentRequests = channel.MaxConcurrentRequests
	resolved.UpstreamCapacityGroupKey = strings.TrimSpace(channel.UpstreamCapacityGroupKey)
	if resolved.UpstreamCapacityGroupKey == "" {
		resolved.UpstreamCapacityGroupKey = upstreamCapacityGroupKey(resolved.Provider, resolved.BaseURL, resolved.APIKey)
	}
	resolved.ProviderModelID = strings.TrimSpace(channel.Model)
	resolved.ResolvedChannelID = strings.TrimSpace(channel.ID)
	resolved.ResolvedChannelName = strings.TrimSpace(channel.Name)
	resolved.ResolvedContextWindowTokens = channel.ContextWindowTokens
	resolved.ReasoningEffort = openAIReasoningEffortFromRuntime(channel.ReasoningEffort)
	resolved.OpenAIEndpoint = strings.TrimSpace(channel.OpenAIEndpoint)
	resolved.OpenAIExtraParamsEnabled = channel.OpenAIExtraParamsEnabled
	resolved.OpenAIExtraParamsJSON = strings.TrimSpace(channel.OpenAIExtraParamsJSON)
	resolved.CustomHeadersEnabled = channel.CustomHeadersEnabled
	resolved.CustomHeadersJSON = strings.TrimSpace(channel.CustomHeadersJSON)
	resolved.AnthropicExtraParamsEnabled = channel.AnthropicExtraParamsEnabled
	resolved.AnthropicExtraParamsJSON = strings.TrimSpace(channel.AnthropicExtraParamsJSON)
	resolved.AnthropicMaxTokens = channel.AnthropicMaxTokens
	resolved.AnthropicThinkingEffort = strings.TrimSpace(channel.AnthropicThinkingEffort)
	resolved.ThinkingBudgetTokens = channel.ThinkingBudgetTokens
	resolved.ProviderStreamIdleTimeout = idleTimeout
	runtimeThinkingEffort := normalizeRuntimeThinkingEffort(req.ThinkingEffort)
	if runtimeThinkingEffort != "" {
		resolved.ThinkingEffort = runtimeThinkingEffort
		if runtimeThinkingEffort == "disabled" {
			resolved.ReasoningEffort = ""
			resolved.AnthropicThinkingEffort = ""
		} else {
			resolved.ReasoningEffort = openAIReasoningEffortFromRuntime(runtimeThinkingEffort)
			resolved.AnthropicThinkingEffort = runtimeThinkingEffort
		}
	} else {
		resolved.ThinkingEffort = ""
	}
	if resolved.MaxTokens <= 0 && channel.MaxTokens > 0 {
		resolved.MaxTokens = channel.MaxTokens
	}
	if req.MaxTokens > 0 && (resolved.AnthropicMaxTokens <= 0 || req.MaxTokens < resolved.AnthropicMaxTokens) {
		resolved.AnthropicMaxTokens = req.MaxTokens
	}
	if resolved.AnthropicMaxTokens <= 0 && resolved.MaxTokens > 0 {
		resolved.AnthropicMaxTokens = resolved.MaxTokens
	}
	if resolved.ProviderModelID == "" {
		resolved.ProviderModelID = strings.TrimSpace(req.ModelID)
	}
	resolved.Messages = sanitizeProviderMessages(req.Messages)
	if resolved.RequestKnobs != nil {
		resolved.RequestKnobs["max_tokens"] = resolved.MaxTokens
		if runtimeThinkingEffort != "" {
			resolved.RequestKnobs["runtime_thinking_effort"] = runtimeThinkingEffort
		} else {
			delete(resolved.RequestKnobs, "runtime_thinking_effort")
		}
		if resolved.Provider == "openai" {
			if strings.TrimSpace(resolved.ReasoningEffort) != "" {
				resolved.RequestKnobs["reasoning_effort"] = strings.TrimSpace(resolved.ReasoningEffort)
			} else {
				delete(resolved.RequestKnobs, "reasoning_effort")
			}
			resolved.RequestKnobs["openai_endpoint"] = resolved.OpenAIEndpoint
			resolved.RequestKnobs["openai_extra_params_enabled"] = resolved.OpenAIExtraParamsEnabled
			resolved.RequestKnobs["custom_headers_enabled"] = resolved.CustomHeadersEnabled
		} else if resolved.Provider == "anthropic" {
			delete(resolved.RequestKnobs, "reasoning_effort")
			resolved.RequestKnobs["custom_headers_enabled"] = resolved.CustomHeadersEnabled
			resolved.RequestKnobs["anthropic_extra_params_enabled"] = resolved.AnthropicExtraParamsEnabled
			anthropicMaxTokens := maxAnthropicTokens(resolved)
			resolved.RequestKnobs["max_tokens"] = anthropicMaxTokens
			resolved.RequestKnobs["anthropic_max_tokens"] = anthropicMaxTokens
			if strings.TrimSpace(resolved.AnthropicThinkingEffort) != "" {
				resolved.RequestKnobs["anthropic_thinking_effort"] = anthropicThinkingEffort(resolved)
			} else {
				delete(resolved.RequestKnobs, "anthropic_thinking_effort")
			}
		}
	}
	return resolved
}

// sanitizeProviderMessages removes replay-only placeholders and trims trailing
// assistant prefill so providers that require a user/tool terminal message do
// not reject the request.
func sanitizeProviderMessages(input []Message) []Message {
	if len(input) == 0 {
		return nil
	}

	filtered := make([]Message, 0, len(input))
	for _, message := range input {
		if isAssistantPlaceholderMessage(message) {
			continue
		}
		filtered = append(filtered, message)
	}
	filtered = mergeAdjacentAssistantToolCallMessages(filtered)
	filtered = trimDanglingAssistantToolCalls(filtered)
	for len(filtered) > 0 && isAssistantPrefillMessage(filtered[len(filtered)-1]) {
		filtered = filtered[:len(filtered)-1]
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func isAssistantPlaceholderMessage(message Message) bool {
	if strings.TrimSpace(message.Role) != "assistant" {
		return false
	}
	if len(message.ToolCalls) > 0 || len(message.ContentParts) > 0 {
		return false
	}
	if strings.TrimSpace(message.ToolCallID) != "" || strings.TrimSpace(message.Name) != "" {
		return false
	}
	if strings.TrimSpace(message.ReasoningContent) != "" {
		return false
	}
	if strings.TrimSpace(message.ReasoningSignature) != "" {
		return false
	}
	switch strings.TrimSpace(message.Content) {
	case "":
		return true
	default:
		return false
	}
}

func isAssistantPrefillMessage(message Message) bool {
	if strings.TrimSpace(message.Role) != "assistant" {
		return false
	}
	if len(message.ToolCalls) > 0 {
		return false
	}
	if strings.TrimSpace(message.ToolCallID) != "" || strings.TrimSpace(message.Name) != "" {
		return false
	}
	return strings.TrimSpace(message.Content) != "" || strings.TrimSpace(message.ReasoningContent) != ""
}

func mergeAdjacentAssistantToolCallMessages(input []Message) []Message {
	if len(input) == 0 {
		return nil
	}
	merged := make([]Message, 0, len(input))
	for _, raw := range input {
		message := cloneProviderMessage(raw)
		if mergeProviderAssistantToolCalls(&merged, message) {
			continue
		}
		merged = append(merged, message)
	}
	return merged
}

func cloneProviderMessage(message Message) Message {
	cloned := message
	if len(message.ContentParts) > 0 {
		cloned.ContentParts = append([]ContentPart(nil), message.ContentParts...)
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = append([]ToolCallDescriptor(nil), message.ToolCalls...)
	}
	if len(message.OpenAIResponsesReasoningSummary) > 0 {
		cloned.OpenAIResponsesReasoningSummary = append([]byte(nil), message.OpenAIResponsesReasoningSummary...)
	}
	return cloned
}

func mergeProviderAssistantToolCalls(messages *[]Message, message Message) bool {
	if len(*messages) == 0 {
		return false
	}
	last := &(*messages)[len(*messages)-1]
	if !canMergeProviderAssistantToolCalls(*last, message) {
		return false
	}
	startIndex := len(last.ToolCalls)
	for index, toolCall := range message.ToolCalls {
		item := toolCall
		item.Index = startIndex + index
		last.ToolCalls = append(last.ToolCalls, item)
	}
	last.ReasoningContent = mergeProviderReasoning(last.ReasoningContent, message.ReasoningContent)
	mergeProviderReasoningMetadata(last, message)
	return true
}

func canMergeProviderAssistantToolCalls(last Message, current Message) bool {
	if strings.TrimSpace(last.Role) != "assistant" || strings.TrimSpace(current.Role) != "assistant" {
		return false
	}
	if len(current.ToolCalls) == 0 {
		return false
	}
	if strings.TrimSpace(last.ToolCallID) != "" || strings.TrimSpace(last.Name) != "" {
		return false
	}
	if strings.TrimSpace(current.ToolCallID) != "" || strings.TrimSpace(current.Name) != "" {
		return false
	}
	if strings.TrimSpace(current.Content) != "" || len(current.ContentParts) > 0 {
		return false
	}
	return true
}

func mergeProviderReasoning(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return left + "\n\n" + right
	}
}

func mergeProviderReasoningSignature(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return ""
	}
}

func mergeProviderReasoningSignatureSource(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return ""
	}
}

func mergeProviderReasoningMetadata(last *Message, current Message) {
	if last == nil {
		return
	}
	leftSignature := strings.TrimSpace(last.ReasoningSignature)
	rightSignature := strings.TrimSpace(current.ReasoningSignature)
	mergedSignature := mergeProviderReasoningSignature(leftSignature, rightSignature)
	last.ReasoningSignature = mergedSignature
	if mergedSignature == "" {
		last.ReasoningSignatureSource = ""
		last.OpenAIResponsesReasoningID = ""
		last.OpenAIResponsesReasoningStatus = ""
		last.OpenAIResponsesReasoningSummary = nil
		return
	}
	if leftSignature == "" && rightSignature != "" {
		last.ReasoningSignatureSource = strings.TrimSpace(current.ReasoningSignatureSource)
		last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		last.OpenAIResponsesReasoningSummary = append([]byte(nil), current.OpenAIResponsesReasoningSummary...)
		return
	}
	if leftSignature == rightSignature {
		last.ReasoningSignatureSource = mergeProviderReasoningSignatureSource(last.ReasoningSignatureSource, current.ReasoningSignatureSource)
		if strings.TrimSpace(last.OpenAIResponsesReasoningID) == "" {
			last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		}
		if strings.TrimSpace(last.OpenAIResponsesReasoningStatus) == "" {
			last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		}
		if len(last.OpenAIResponsesReasoningSummary) == 0 {
			last.OpenAIResponsesReasoningSummary = append([]byte(nil), current.OpenAIResponsesReasoningSummary...)
		}
	}
}

// providerToolResponseWindowEnd 返回 assistant tool-call 消息的响应收集窗口右边界（不含）。
// 窗口内除 tool 结果消息外，还允许出现同轮穿插的纯文本 assistant 消息：
// 部分模型（如 gpt-5.3-codex-spark）会在同一条响应里先输出 function_call 再输出说明文本，
// 回放顺序为 assistant[tool_call] -> assistant[text] -> tool[result]，若只收集紧邻的
// tool 消息，会把有结果回放的调用误判为悬空。
func providerToolResponseWindowEnd(messages []Message, index int) int {
	end := index + 1
	for end < len(messages) {
		candidate := messages[end]
		switch {
		case strings.TrimSpace(candidate.Role) == "tool":
			end++
		case strings.TrimSpace(candidate.Role) == "assistant" && len(candidate.ToolCalls) == 0:
			end++
		default:
			return end
		}
	}
	return end
}

func trimDanglingAssistantToolCalls(input []Message) []Message {
	if len(input) == 0 {
		return nil
	}
	survivingToolCallIDs := make(map[string]struct{})
	for index, message := range input {
		if strings.TrimSpace(message.Role) != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		responded := make(map[string]struct{}, len(message.ToolCalls))
		for scan := index + 1; scan < providerToolResponseWindowEnd(input, index); scan++ {
			if strings.TrimSpace(input[scan].Role) != "tool" {
				continue
			}
			if toolCallID := strings.TrimSpace(input[scan].ToolCallID); toolCallID != "" {
				responded[toolCallID] = struct{}{}
			}
		}
		for _, toolCall := range message.ToolCalls {
			if toolCallID := strings.TrimSpace(toolCall.ID); toolCallID != "" {
				if _, ok := responded[toolCallID]; ok {
					survivingToolCallIDs[toolCallID] = struct{}{}
				}
			}
		}
	}
	trimmed := make([]Message, 0, len(input))
	for _, item := range input {
		message := cloneProviderMessage(item)
		if strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0 {
			nextToolCalls := make([]ToolCallDescriptor, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				if _, ok := survivingToolCallIDs[strings.TrimSpace(toolCall.ID)]; !ok {
					continue
				}
				toolCall.Index = len(nextToolCalls)
				nextToolCalls = append(nextToolCalls, toolCall)
			}
			if len(nextToolCalls) == 0 {
				if strings.TrimSpace(message.Content) == "" && len(message.ContentParts) == 0 && strings.TrimSpace(message.ReasoningContent) == "" {
					continue
				}
				message.ToolCalls = nil
			} else {
				message.ToolCalls = nextToolCalls
			}
			trimmed = append(trimmed, message)
			continue
		}
		if strings.TrimSpace(message.Role) == "tool" && strings.TrimSpace(message.ToolCallID) != "" {
			if _, ok := survivingToolCallIDs[strings.TrimSpace(message.ToolCallID)]; !ok {
				continue
			}
		}
		trimmed = append(trimmed, message)
	}
	return trimmed
}

func normalizeRuntimeThinkingEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "disabled", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(raw))
	case "disable", "off", "none", "false", "no", "0":
		return "disabled"
	case "very_high", "very-high", "veryhigh", "x-high", "extra_high", "extra-high", "extrahigh":
		return "xhigh"
	case "maximum":
		return "max"
	default:
		return ""
	}
}

func openAIReasoningEffortFromRuntime(runtimeThinkingEffort string) string {
	switch normalizeRuntimeThinkingEffort(runtimeThinkingEffort) {
	case "low", "medium", "high", "xhigh", "max":
		return normalizeRuntimeThinkingEffort(runtimeThinkingEffort)
	default:
		return ""
	}
}
