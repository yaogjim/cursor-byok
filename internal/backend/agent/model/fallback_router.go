// fallback_router.go 实现显式有序 Provider Fallback 路由器。
// 默认关闭；启用后仅在零原始字节、零 model event、零副作用的安全窗口内切换渠道。
// 所有渠道共用同一总 attempt / 等待预算，不允许每个 provider 重置。
package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cursor/internal/audit"
	"cursor/internal/observability"
	legacyruntime "cursor/internal/runtime"
)

const (
	// fallbackChainTotalAttempts 是默认全链 HTTP 尝试次数；ChannelPlan 预算优先，缺失时回落到此值。
	fallbackChainTotalAttempts = legacyruntime.DefaultFallbackMaxHttpAttempts
	// fallbackChainMaxWait 是默认全链等待预算；ChannelPlan 预算优先。
	fallbackChainMaxWait = time.Duration(legacyruntime.DefaultFallbackMaxWaitSeconds) * time.Second
)

// FallbackAwareRouter 在 ChannelPlan.FallbackEnabled=true 时按有序渠道列表依次尝试；
// 禁用时（单渠道计划）完全等同于现有 Router，不增加任何开销和行为差异。
type FallbackAwareRouter struct {
	underlying *Router
	resolver   ChannelPlanResolver
}

// NewFallbackAwareRouter 创建 FallbackAwareRouter。
// underlying 复用已有 Router 的 openai/anthropic 适配器；resolver 提供多渠道计划。
func NewFallbackAwareRouter(underlying *Router, resolver ChannelPlanResolver) *FallbackAwareRouter {
	return &FallbackAwareRouter{
		underlying: underlying,
		resolver:   resolver,
	}
}

// Stream 实现 ModelAdapterRouter 接口。
// 单渠道计划（FallbackEnabled=false）：行为与 Router.Stream 完全一致。
// 多渠道计划（FallbackEnabled=true）：共享总 attempt 预算，仅在安全窗口内切换。
func (r *FallbackAwareRouter) Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	if r == nil || r.underlying == nil || r.resolver == nil {
		return fmt.Errorf("fallback router is unavailable")
	}

	plan, err := r.resolver.SelectChannelPlanForModel(ctx, req.ModelID)
	if err != nil {
		return err
	}
	if plan == nil || len(plan.Channels) == 0 {
		return fmt.Errorf("no available channel for model %q", req.ModelID)
	}

	idleTimeout := r.resolver.ProviderStreamIdleTimeout(ctx)

	// 单渠道或 fallback 禁用：完全等同于现有路径。
	if !plan.FallbackEnabled || len(plan.Channels) <= 1 {
		channel := plan.Channels[0]
		channelReq := applyChannelToRequest(req, &channel, idleTimeout)
		return r.underlying.streamPreResolved(ctx, channelReq, sink)
	}

	return r.streamWithFallback(ctx, req, sink, plan, idleTimeout)
}

func fallbackChainBudgetFromPlan(plan *legacyruntime.ChannelPlan) (int, time.Duration) {
	attempts := fallbackChainTotalAttempts
	waitSeconds := int(fallbackChainMaxWait / time.Second)
	if plan != nil {
		attempts = plan.MaxHttpAttempts
		waitSeconds = plan.MaxWaitSeconds
	}
	attempts, waitSeconds = legacyruntime.ClampFallbackChainBudget(attempts, waitSeconds)
	return attempts, time.Duration(waitSeconds) * time.Second
}

// countSubsequentReservableChannels 统计从 currentIdx 之后、按实际切换顺序
// 会被尝试的兼容渠道数。不兼容候选不占预留；兼容性基准随“将被尝试”的渠道推进，
// 与 streamWithFallback 在失败后查找下一候选的方式一致。
func countSubsequentReservableChannels(req StreamRequest, channels []legacyruntime.ResolvedChannel, currentIdx int) int {
	if currentIdx < 0 || currentIdx >= len(channels) {
		return 0
	}
	from := channels[currentIdx]
	count := 0
	for idx := currentIdx + 1; idx < len(channels); idx++ {
		if ok, _ := checkFallbackChannelCompatibility(req, from, channels[idx]); !ok {
			continue
		}
		count++
		from = channels[idx]
	}
	return count
}

// allocateFallbackChannelAttempts 按“保证渠道覆盖，再用剩余预算重试”分配当前渠道的 HTTP 次数。
// 每个后续可尝试/兼容渠道预留 1 次；当前渠道使用 remaining-reserved，且不超过 providerRequestMaxAttempts。
// 当剩余预算不足以覆盖当前+全部后续时，仍给当前 1 次（按顺序覆盖），不突破 remainingAttempts。
func allocateFallbackChannelAttempts(remainingAttempts, subsequentReservable int) int {
	if remainingAttempts <= 0 {
		return 0
	}
	if subsequentReservable < 0 {
		subsequentReservable = 0
	}
	reserved := subsequentReservable
	if reserved > remainingAttempts-1 {
		reserved = remainingAttempts - 1
	}
	usable := remainingAttempts - reserved
	if usable > providerRequestMaxAttempts {
		usable = providerRequestMaxAttempts
	}
	return usable
}

// streamWithFallback 执行多渠道 fallback 循环。
// 共享预算规则：
//   - HTTP attempt 总上限由 ChannelPlan 决定（默认 fallbackChainTotalAttempts），每渠道单次分配不超过 providerRequestMaxAttempts（3）。
//   - 覆盖优先：为每个后续实际可尝试/兼容渠道预留至少 1 次 HTTP；当前渠道最多使用 remaining-reserved。
//   - sleep/backoff 预算 fallbackChainMaxWait（8s）通过 wall-clock 扣减方式传入各渠道；
//     不使用 context.WithTimeout，避免连带截断正在进行的 HTTP 请求。
//   - 安全门禁（原始字节、model event、context cancel、非可重试错误）任一触发即阻断 fallback。
func (r *FallbackAwareRouter) streamWithFallback(
	ctx context.Context,
	req StreamRequest,
	sink func(ModelEvent) error,
	plan *legacyruntime.ChannelPlan,
	idleTimeout time.Duration,
) error {
	attempts, maxWait := fallbackChainBudgetFromPlan(plan)
	budget := NewFallbackRetryBudget(attempts, maxWait)

	var lastErr error
	var prevChannel *legacyruntime.ResolvedChannel

	for channelIdx := 0; channelIdx < len(plan.Channels); channelIdx++ {
		channel := plan.Channels[channelIdx]
		// 上下文已取消：立即返回，不再尝试
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 共享 attempt / retry-sleep 预算由 retry 层在真正 client.Do / sleep 前消费。
		remainingAttempts, remainingWait := budget.Remaining()
		if remainingAttempts <= 0 {
			break
		}
		previousChannelID := ""
		if prevChannel != nil {
			previousChannelID = prevChannel.ID
		}

		// 渠道兼容性检查（仅在渠道切换时执行）。显式 allowlist 授权模型语义切换，
		// 这里继续保守校验 provider 投影与上下文/输出容量。
		if prevChannel != nil {
			if ok, incompatReason := checkFallbackChannelCompatibility(req, *prevChannel, channel); !ok {
				recordFallbackIncompatible(ctx, req.RequestID, req.ModelCallID, channelIdx+1,
					channel.ID, prevChannel.ID, incompatReason)
				// 该候选从未实际尝试，兼容性基准仍保持为最后一个已尝试渠道；
				// 否则连续的不兼容候选可能通过“与前一个被跳过候选同 provider”绕过门禁。
				continue
			}
		}

		// 覆盖优先：后续实际可尝试/兼容渠道各预留 1 次，当前渠道使用剩余额度（不超过单渠道上限）。
		perChannelAttempts := allocateFallbackChannelAttempts(
			remainingAttempts,
			countSubsequentReservableChannels(req, plan.Channels, channelIdx),
		)
		if perChannelAttempts <= 0 {
			break
		}

		channelReq := applyChannelToRequest(req, &channel, idleTimeout)
		channelReq.FallbackMaxAttempts = perChannelAttempts
		channelReq.FallbackRemainingWait = remainingWait
		channelReq.FallbackBudget = budget
		channelSafety := &FallbackSafetyInfo{}
		channelReq.FallbackSafety = channelSafety
		recordAttempt := func(reason, fallbackTo, suppressionReason string, success bool) {
			if channelSafety.Snapshot().WaitBudgetBlocked && (suppressionReason == "" || suppressionReason == "chain_exhausted") {
				suppressionReason = "wait_budget_exhausted"
			}
			recordFallbackAttempt(ctx, fallbackAttemptRecord{
				requestID:         req.RequestID,
				modelCallID:       req.ModelCallID,
				channelAttempt:    channelIdx + 1,
				channelID:         channel.ID,
				previousChannelID: previousChannelID,
				reason:            reason,
				fallbackTo:        fallbackTo,
				suppressionReason: suppressionReason,
				success:           success,
				budget:            budget,
				allocation:        perChannelAttempts,
				retryDelay:        channelSafety.Snapshot().LastRetryDelay,
			})
		}
		// 只有后续仍存在兼容候选时，本次渠道才使用隔离后缀。
		// 若剩余候选都会被跳过，本次就是实际终点，必须保留原始 ModelCallID。
		if nextFallbackChannelIndex(req, plan.Channels, channelIdx) >= 0 {
			channelReq.FallbackArtifactSuffix = fmt.Sprintf("_fb%d", channelIdx)
		}

		// 所有 model event（含工具进度和下游发布）先更新 typed safety 再交给下游。
		wrappedSink := func(event ModelEvent) error {
			channelSafety.MarkModelEventObserved()
			return sink(event)
		}

		callErr := r.underlying.streamPreResolved(ctx, channelReq, wrappedSink)
		if ClassifyProviderError(callErr) == ProviderErrorRequestBuild {
			channelSafety.MarkRequestBuildFailed()
		}
		callErr = WrapFallbackSafetyError(callErr, channelSafety)

		if callErr == nil {
			recordAttempt("", "", "", true)
			return nil
		}

		lastErr = callErr
		reason := ClassifyProviderError(callErr)

		// ── 安全门禁检查（任一条件阻断 fallback）──────────────────────────────

		// 1. 已有任意原始字节或 model event（含工具进度/下游发布）→ 禁止切换。
		safety := channelSafety.Snapshot()
		if safety.RawBytesObserved || safety.ModelEventObserved {
			recordAttempt(reason, "", "output_observed", false)
			return callErr
		}

		// 2. 上下文取消 / deadline 超时 → 禁止切换
		if ctx.Err() != nil {
			recordAttempt(reason, "", "context_done", false)
			return ctx.Err()
		}

		// 3. HTTP 500 可同渠道重试但禁止切换；仅 transport/429/502/503/504/524/零字节截断可切。
		if !isFallbackEligibleError(callErr) {
			recordAttempt(reason, "", fallbackSuppressionReason(callErr), false)
			return callErr
		}

		remainingAfterAttempt, _ := budget.Remaining()
		if remainingAfterAttempt <= 0 {
			recordAttempt(reason, "", "attempt_budget_exhausted", false)
			return callErr
		}

		// 查找下一个真正兼容的候选，并为被跳过的候选记录受控原因。
		// 容量超时后跳过相同上游组，避免对同一组再等 2 秒。
		skipSameGroup := isCapacityUnavailable(callErr)
		nextChannelIdx := -1
		for candidateIdx := channelIdx + 1; candidateIdx < len(plan.Channels); candidateIdx++ {
			candidate := plan.Channels[candidateIdx]
			if skipSameGroup && sameUpstreamCapacityGroup(channel, candidate) {
				recordFallbackIncompatible(ctx, req.RequestID, req.ModelCallID, candidateIdx+1,
					candidate.ID, channel.ID, "same_upstream_group")
				continue
			}
			if ok, incompatReason := checkFallbackChannelCompatibility(req, channel, candidate); !ok {
				recordFallbackIncompatible(ctx, req.RequestID, req.ModelCallID, candidateIdx+1,
					candidate.ID, channel.ID, incompatReason)
				continue
			}
			nextChannelIdx = candidateIdx
			break
		}
		if nextChannelIdx < 0 {
			recordAttempt(reason, "", "chain_exhausted", false)
			return callErr
		}

		// 全部安全检查通过，切换到下一个实际兼容渠道。
		nextChannelID := plan.Channels[nextChannelIdx].ID
		recordAttempt(reason, nextChannelID, "", false)

		attemptedChannel := channel
		prevChannel = &attemptedChannel
		channelIdx = nextChannelIdx - 1
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("provider fallback: all channels exhausted for model %q", req.ModelID)
}

// isFallbackEligibleError 判断该错误是否允许切换到下一渠道。
//
// 允许切换：transport、429、502/503/504、524、零字节 pre-event 截断 / stream idle、
// 以及零输出窗口内的 typed capacity_unavailable。
// 禁止切换：HTTP 500（仅同渠道 P0 重试）、HTTP 529、context cancel/deadline、4xx 非429、
// request_build、未知零 HTTP、RawBytesObserved=true 及其他。
func isFallbackEligibleError(err error) bool {
	if err == nil {
		return false
	}
	if isCapacityUnavailable(err) {
		if safety, ok := fallbackSafetyFromError(err); ok {
			if safety.RawBytesObserved || safety.ModelEventObserved {
				return false
			}
		}
		return true
	}
	if safety, ok := fallbackSafetyFromError(err); ok {
		if safety.RawBytesObserved || safety.ModelEventObserved || safety.RequestBuildFailed || safety.HTTPAttempts == 0 {
			return false
		}
	}
	// 向后兼容旧的 typed StreamTruncatedError；新 adapter 路径同时提供 FallbackSafetyError。
	var trunc *StreamTruncatedError
	if errors.As(err, &trunc) && trunc != nil && trunc.RawBytesObserved {
		return false
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil {
		return isFallbackEligibleHTTPStatus(httpErr.StatusCode)
	}
	switch ClassifyProviderError(err) {
	case ProviderErrorTransport, ProviderErrorRateLimited,
		ProviderErrorStreamIdleTimeout, ProviderErrorStreamDecode:
		return true
	default:
		return false
	}
}

func isFallbackEligibleHTTPStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, HTTPStatusCloudflareTimeout:
		return true
	default:
		return false
	}
}

func fallbackSuppressionReason(err error) string {
	if safety, ok := fallbackSafetyFromError(err); ok {
		switch {
		case safety.RawBytesObserved || safety.ModelEventObserved:
			return "output_observed"
		case safety.RequestBuildFailed:
			return "request_build"
		case safety.HTTPAttempts == 0:
			return "no_http_attempt"
		}
	}
	switch ClassifyProviderError(err) {
	case ProviderErrorContextCanceled:
		return "context_done"
	case ProviderErrorStatus4xx:
		return "non_retryable_http_status"
	default:
		return "ineligible_error"
	}
}

func sameUpstreamCapacityGroup(a, b legacyruntime.ResolvedChannel) bool {
	return upstreamCapacityGroupKey(a.Provider, a.BaseURL, a.APIKey) == upstreamCapacityGroupKey(b.Provider, b.BaseURL, b.APIKey)
}

func nextFallbackChannelIndex(req StreamRequest, channels []legacyruntime.ResolvedChannel, currentIdx int) int {
	if currentIdx < 0 || currentIdx >= len(channels) {
		return -1
	}
	fromChannel := channels[currentIdx]
	for candidateIdx := currentIdx + 1; candidateIdx < len(channels); candidateIdx++ {
		if ok, _ := checkFallbackChannelCompatibility(req, fromChannel, channels[candidateIdx]); ok {
			return candidateIdx
		}
	}
	return -1
}

func checkFallbackChannelCompatibility(req StreamRequest, fromChannel, toChannel legacyruntime.ResolvedChannel) (bool, string) {
	if ok, reason := checkFallbackCompatibility(req, fromChannel.Provider, toChannel.Provider); !ok {
		return false, reason
	}
	if fromChannel.ContextWindowTokens > 0 && toChannel.ContextWindowTokens > 0 && toChannel.ContextWindowTokens < fromChannel.ContextWindowTokens {
		return false, "context_window"
	}
	if req.MaxTokens > 0 && toChannel.MaxTokens > 0 && toChannel.MaxTokens < req.MaxTokens {
		return false, "max_output_tokens"
	}
	if strings.TrimSpace(toChannel.Provider) == "anthropic" && req.MaxTokens > 0 && toChannel.AnthropicMaxTokens > 0 && toChannel.AnthropicMaxTokens < req.MaxTokens {
		return false, "max_output_tokens"
	}
	return true, ""
}

// checkFallbackCompatibility 校验跨 provider 切换的兼容性。
// 返回 (compatible=false, reason) 时应跳过该渠道并记录 fallback_incompatible 事件。
//
// 策略：宁可拒绝不安全的跨 provider 切换，不可静默发送语义不等价的请求。
// 遇到无法证明可安全投影的 provider-specific 字段时，一律抑制 fallback。
func checkFallbackCompatibility(req StreamRequest, fromProvider, toProvider string) (bool, string) {
	from := strings.TrimSpace(fromProvider)
	to := strings.TrimSpace(toProvider)
	// RequestBodyOverride 是 provider-opaque 数据。即使两个渠道属于同一
	// provider family，不同 endpoint/model 也无法证明该原始 body 可安全复用。
	if req.RequestBodyOverride != nil {
		return false, "request_body_override"
	}
	if from == to {
		return true, ""
	}
	// 检查消息中的 provider-specific 状态。
	for _, msg := range req.Messages {
		// provider 签发的 reasoning signature 是 provider-opaque 数据，无法跨 provider 传递。
		if strings.TrimSpace(msg.ReasoningSignature) != "" {
			return false, "provider_reasoning_signature"
		}
		// OpenAI Responses API 特有的推理 item（任意跨 provider 方向均不安全）。
		if strings.TrimSpace(msg.OpenAIResponsesReasoningID) != "" ||
			len(msg.OpenAIResponsesReasoningSummary) > 0 {
			return false, "openai_responses_reasoning_state"
		}
		// tool_calls 中带有 provider-specific ID（Responses API）的无法安全投影。
		for _, tc := range msg.ToolCalls {
			if strings.TrimSpace(tc.OpenAIResponsesID) != "" ||
				strings.TrimSpace(tc.OpenAIResponsesCallID) != "" {
				return false, "provider_tool_call_state"
			}
		}
		// 非纯文本 ContentParts（如图片）的编码格式因 provider 而异，
		// 无法在不了解目标 provider 具体格式要求的情况下安全重发。
		for _, part := range msg.ContentParts {
			if normalizeContentPartType(part.Type) == contentPartTypeImage {
				return false, "image_content_part"
			}
		}
	}
	// Tools 的 JSON schema 虽然在同一 provider 内可由适配器序列化，
	// 但跨 provider 时格式差异（如 Anthropic computer_use、input_schema vs parameters 等）
	// 无法在无目标 provider schema 规范的情况下安全证明等价投影。
	// 遵循保守原则：宁可拒绝不安全跨 provider，不可静默发送语义不等价的请求。
	if len(req.Tools) > 0 {
		return false, "tools"
	}
	_ = from
	_ = to
	return true, ""
}

// fallbackExtractAttemptsUsed 从错误中推断本次渠道消耗的 attempt 数，用于共享预算扣减。
// 若无法从错误中获取精确值，以 allocated 保守估算（不超过已分配上限）。
func fallbackExtractAttemptsUsed(err error, allocated int) int {
	if err == nil {
		return 1
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil && httpErr.Attempt > 0 {
		return httpErr.Attempt
	}
	return allocated
}

// fallbackAttemptRecord 是一条 fallback 渠道尝试的可观测输入。
type fallbackAttemptRecord struct {
	requestID         string
	modelCallID       string
	channelAttempt    int
	channelID         string
	previousChannelID string
	reason            string
	fallbackTo        string
	suppressionReason string
	success           bool
	budget            *FallbackRetryBudget
	allocation        int
	retryDelay        time.Duration
}

// recordFallbackAttempt 记录 fallback 链每个渠道尝试的可观测事件。
// 同一 model_call_id，channelAttempt 从 1 开始区分各渠道；不记录凭证、响应正文或完整 URL query。
// fallbackTo 非空时表示本次失败后将切换到的下一渠道 ID（已通过全部安全门禁）。
// WaitBudgetBlocked 时仍可切换，此时 fallback_to 与 fallback_suppressed_reason=wait_budget_exhausted 同时存在。
func recordFallbackAttempt(ctx context.Context, rec fallbackAttemptRecord) {
	sink := observability.ProcessSink()
	if sink == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	correlation := observability.CorrelationFromContext(ctx)
	if correlation.CursorRequestID == "" {
		correlation.CursorRequestID = strings.TrimSpace(rec.requestID)
	}
	if correlation.ModelCallID == "" {
		correlation.ModelCallID = strings.TrimSpace(rec.modelCallID)
	}
	ctx = observability.WithCorrelation(ctx, correlation)
	status := "completed"
	semanticOutcome := observability.OutcomeSucceeded
	if !rec.success {
		status = "error"
		semanticOutcome = observability.OutcomeFailed
	}
	fields := make(map[string]any, 16)
	fields["channel_attempt"] = rec.channelAttempt
	if strings.TrimSpace(rec.channelID) != "" {
		fields["channel_id"] = audit.SanitizeMetadataText(rec.channelID)
	}
	if strings.TrimSpace(rec.previousChannelID) != "" {
		fields["fallback_from"] = audit.SanitizeMetadataText(rec.previousChannelID)
	}
	if strings.TrimSpace(rec.fallbackTo) != "" {
		fields["fallback_to"] = audit.SanitizeMetadataText(rec.fallbackTo)
	}
	if strings.TrimSpace(rec.reason) != "" {
		fields["fallback_reason"] = rec.reason
	}
	if strings.TrimSpace(rec.suppressionReason) != "" {
		fields["fallback_suppressed_reason"] = rec.suppressionReason
	}
	if rec.allocation > 0 {
		fields["channel_allocation_max_attempts"] = rec.allocation
	}
	fields["retry_delay_ms"] = rec.retryDelay.Milliseconds()
	if rec.budget != nil {
		maxAttempts, usedAttempts, remainingAttempts, maxWait, usedWait, remainingWait := rec.budget.Snapshot()
		fields["chain_max_attempts"] = maxAttempts
		fields["chain_max_wait_ms"] = maxWait.Milliseconds()
		fields["chain_attempts_used"] = usedAttempts
		fields["chain_attempts_remaining"] = remainingAttempts
		fields["chain_wait_used_ms"] = usedWait.Milliseconds()
		fields["chain_wait_remaining_ms"] = remainingWait.Milliseconds()
	}
	event := observability.Event{
		CursorRequestID:     strings.TrimSpace(rec.requestID),
		ModelCallID:         strings.TrimSpace(rec.modelCallID),
		Layer:               "provider",
		Event:               "provider_fallback_attempt",
		Capability:          "provider_fallback",
		Operation:           "provider.fallback",
		Direction:           observability.DirectionProxyToProvider,
		ExecutionTarget:     "provider",
		Protocol:            "http_stream",
		Status:              status,
		SemanticOutcome:     semanticOutcome,
		ImplementationState: observability.ImplementationImplemented,
		Fields:              fields,
	}
	defer func() { _ = recover() }()
	_ = sink.Record(ctx, observability.Capture{Event: event})
}

// recordFallbackIncompatible 记录跨 provider 不兼容跳过事件。
// reason 为不兼容原因字符串（如 request_body_override），不含凭证或原始消息内容。
func recordFallbackIncompatible(
	ctx context.Context,
	requestID, modelCallID string,
	channelAttempt int,
	channelID, previousChannelID, reason string,
) {
	sink := observability.ProcessSink()
	if sink == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	correlation := observability.CorrelationFromContext(ctx)
	if correlation.CursorRequestID == "" {
		correlation.CursorRequestID = strings.TrimSpace(requestID)
	}
	if correlation.ModelCallID == "" {
		correlation.ModelCallID = strings.TrimSpace(modelCallID)
	}
	ctx = observability.WithCorrelation(ctx, correlation)
	fields := make(map[string]any, 4)
	fields["channel_attempt"] = channelAttempt
	if strings.TrimSpace(channelID) != "" {
		fields["channel_id"] = audit.SanitizeMetadataText(channelID)
	}
	if strings.TrimSpace(previousChannelID) != "" {
		fields["fallback_from"] = audit.SanitizeMetadataText(previousChannelID)
	}
	if strings.TrimSpace(reason) != "" {
		fields["fallback_suppressed_reason"] = reason
	}
	event := observability.Event{
		CursorRequestID:     strings.TrimSpace(requestID),
		ModelCallID:         strings.TrimSpace(modelCallID),
		Layer:               "provider",
		Event:               "provider_fallback_incompatible",
		Capability:          "provider_fallback",
		Operation:           "provider.fallback",
		Direction:           observability.DirectionProxyToProvider,
		ExecutionTarget:     "provider",
		Protocol:            "http_stream",
		Status:              "skipped",
		SemanticOutcome:     observability.OutcomeDegraded,
		ImplementationState: observability.ImplementationImplemented,
		Fields:              fields,
	}
	defer func() { _ = recover() }()
	_ = sink.Record(ctx, observability.Capture{Event: event})
}
