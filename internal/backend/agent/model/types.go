// types.go 定义模型适配层的统一请求、事件与路由接口。
package modeladapter

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	// ReasoningSignatureSourceAnthropic 表示 signature 来自 Anthropic thinking signature。
	ReasoningSignatureSourceAnthropic = "anthropic"
	// ReasoningSignatureSourceOpenAIResponses 表示 signature 来自 OpenAI Responses encrypted reasoning content。
	ReasoningSignatureSourceOpenAIResponses = "openai_responses"
)

// Message 表示模型适配层统一使用的消息结构。
type Message struct {
	// Role 表示消息角色。
	Role string `json:"role"`
	// Content 表示消息文本内容。
	Content string `json:"content"`
	// ContentParts 表示消息中的结构化内容块，例如文本或图片。
	ContentParts []ContentPart `json:"content_parts,omitempty"`
	// ReasoningContent 表示推理内容（用于支持 reasoning 的模型）。
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ReasoningSignature 表示 provider 对推理内容签发的签名（如 Anthropic thinking signature）。
	ReasoningSignature string `json:"reasoning_signature,omitempty"`
	// ReasoningSignatureSource 表示 reasoning signature 的 provider 语义来源。
	ReasoningSignatureSource string `json:"reasoning_signature_source,omitempty"`
	// OpenAIResponsesReasoningID 保存 Responses reasoning output item 的原始 id。
	OpenAIResponsesReasoningID string `json:"openai_responses_reasoning_id,omitempty"`
	// OpenAIResponsesReasoningStatus 保存 Responses reasoning output item 的原始 status。
	OpenAIResponsesReasoningStatus string `json:"openai_responses_reasoning_status,omitempty"`
	// OpenAIResponsesReasoningSummary 保存 Responses reasoning output item 的原始 summary。
	OpenAIResponsesReasoningSummary json.RawMessage `json:"openai_responses_reasoning_summary,omitempty"`
	// ToolCalls 表示 assistant 发起的函数调用。
	ToolCalls []ToolCallDescriptor `json:"tool_calls,omitempty"`
	// ToolCallID 表示 tool role 关联的调用 id。
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name 表示 tool role 的工具名。
	Name string `json:"name,omitempty"`
}

type ToolCallDescriptor struct {
	ID                    string                `json:"id"`
	Index                 int                   `json:"index,omitempty"`
	Type                  string                `json:"type"`
	Function              ToolCallFunctionShape `json:"function"`
	OpenAIResponsesID     string                `json:"openai_responses_id,omitempty"`
	OpenAIResponsesCallID string                `json:"openai_responses_call_id,omitempty"`
	OpenAIResponsesStatus string                `json:"openai_responses_status,omitempty"`
}

type ToolCallFunctionShape struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// CodexAffinity 是仅内存的 managed Codex 缓存亲和字段，禁止记录值或可关联摘要。
type CodexAffinity struct {
	PromptCacheKey  string
	SessionID       string
	ThreadID        string
	ClientRequestID string
}

// StreamRequest 表示一次统一的模型流请求。
type StreamRequest struct {
	// RequestID 表示当前模型调用所属 request。
	RequestID string
	// RunID 表示当前模型调用所属 run。
	RunID string
	// ModelCallID 表示当前模型调用标识。
	ModelCallID string
	// ConversationID 表示当前模型调用所属会话，用于稳定 provider 侧 prompt cache 路由。
	ConversationID string
	// Mode 表示当前运行模式。
	Mode agentv1.AgentMode
	// ModelID 表示当前模型标识。
	ModelID string
	// ThinkingEffort 表示客户端在本轮运行时选择的思考强度覆盖。
	ThinkingEffort string
	// Provider 表示目标 provider 类型，例如 openai 或 anthropic。
	Provider string
	// BaseURL 表示请求应发送到的 provider 基础地址。
	BaseURL string
	// APIKey 表示 provider 鉴权凭据。
	APIKey string
	// CredentialSource 表示渠道凭据来源：static、codex 或 grok。
	CredentialSource string
	// CredentialID 表示本次请求实际使用的订阅账号 ID。
	CredentialID string
	// ChatGPTAccountID 表示 Codex 请求使用的 ChatGPT 账号 ID。
	ChatGPTAccountID string
	// StableAccountID 表示 CredentialID 来自持久账号记录，而非 token fingerprint fallback。
	StableAccountID bool
	// CodexAffinity 是仅内存的 managed Codex 缓存亲和结果，禁止写入配置、日志或导出。
	CodexAffinity CodexAffinity
	// MaxConcurrentRequests 是物理上游组的可选并发上限。
	// 0 表示不限流；非零合法范围 1–16。路由解析后以 ResolvedChannel 的值为准。
	MaxConcurrentRequests int
	// UpstreamCapacityGroupKey 是仅内存的上游组身份（SHA-256 hex）。
	// 按 provider type + NormalizeBaseURL(baseURL) + API key 计算；
	// 不得写入日志、YAML 或错误文本。空则在 acquire 时按当前渠道身份计算。
	UpstreamCapacityGroupKey string
	// ProviderModelID 表示 provider 侧真实模型标识。
	ProviderModelID string
	// ResolvedChannelID 表示本次请求实际命中的 adapter 渠道 ID。
	ResolvedChannelID string
	// ResolvedChannelName 表示本次请求实际命中的 adapter 展示名。
	ResolvedChannelName string
	// ResolvedContextWindowTokens 表示本次请求实际命中的 adapter 上下文窗口。
	ResolvedContextWindowTokens int
	// ReasoningEffort 表示 OpenAI 兼容 provider 的推理强度。
	ReasoningEffort string
	// OpenAIEndpoint 表示 OpenAI 兼容 provider 使用的 API 端点。
	OpenAIEndpoint string
	// OpenAIExtraParamsEnabled 表示是否启用 OpenAI 额外请求参数。
	OpenAIExtraParamsEnabled bool
	// OpenAIExtraParamsJSON 表示 OpenAI 额外请求参数 JSON 对象。
	OpenAIExtraParamsJSON string
	// CustomHeadersEnabled 表示是否启用自定义请求头。
	CustomHeadersEnabled bool
	// CustomHeadersJSON 表示自定义请求头 JSON 对象。
	CustomHeadersJSON string
	// AnthropicExtraParamsEnabled 表示是否启用 Anthropic 额外请求参数。
	AnthropicExtraParamsEnabled bool
	// AnthropicExtraParamsJSON 表示 Anthropic 额外请求参数 JSON 对象。
	AnthropicExtraParamsJSON string
	// AnthropicMaxTokens 表示 Anthropic 兼容 provider 的 max_tokens。
	AnthropicMaxTokens int
	// AnthropicThinkingEffort 表示 Anthropic adaptive thinking 的 output_config.effort。
	AnthropicThinkingEffort string
	// ThinkingBudgetTokens 表示 Anthropic thinking 预算。
	ThinkingBudgetTokens int
	// Messages 表示按顺序排列的消息列表。
	Messages []Message
	// StableMessageCount 表示 messages 中可作为稳定缓存前缀的 provider-visible 消息数量。
	StableMessageCount int
	// Tools 表示原始工具描述 JSON 列表。
	Tools []json.RawMessage
	// MaxTokens 表示本轮最大输出 token 数。
	MaxTokens int
	// Stream 表示当前请求必须使用流式。
	Stream bool
	// RequestKnobs 保存本轮请求的附加参数摘要。
	RequestKnobs map[string]any
	// CompileSummary 保存当前 prompt 编译摘要。
	CompileSummary string
	// Observer 负责写入 request-scoped LLM 工件。
	Observer LLMArtifactObserver
	// ArtifactPaths 用于由 adapter 回填工件路径。
	ArtifactPaths *LLMArtifactPaths
	// RequestBodyOverride 表示直接复用的 provider 原始请求体；设置后由 adapter 原样发送。
	RequestBodyOverride map[string]any
	// ProviderStreamIdleTimeout 表示 provider 流式响应无有效内容时的空闲超时。
	ProviderStreamIdleTimeout time.Duration
	// StreamDiagnostics 收集本次 HTTP 流的可选 header/body 时间线与 close_cause。
	// nil 表示调用方不需要回读；适配器仍可分配局部实例。
	StreamDiagnostics *StreamDiagnostics
	// StreamRecoveryAttempt 是适配器级零模型事件同渠道恢复次数；仅内部递归调用使用。
	StreamRecoveryAttempt int
	// FallbackMaxAttempts 表示 FallbackAwareRouter 分配给当前渠道的最大尝试次数（共享预算）。
	// 0 表示使用适配器默认值（providerRequestMaxAttempts）。非零值将覆盖适配器默认 maxAttempts，
	// 以确保所有渠道共用同一总 attempt 预算，防止 fallback 链无限放大重试次数。
	FallbackMaxAttempts int
	// FallbackRemainingWait 表示 FallbackAwareRouter 分配给当前渠道的共享 sleep/backoff 预算剩余量。
	// 哨兵是 FallbackBudget != nil，不是本字段 > 0。fallback 路径在 normalize 之后把
	// maxTotalWait 直接覆盖为该值（包括 0）；0 表示禁止后续 sleep，不得回落到 4s。
	// 注意：不得将此值用于 context.WithTimeout，仅作为 providerRetry 的 maxTotalWait 上限。
	FallbackRemainingWait time.Duration
	// FallbackArtifactSuffix 表示 FallbackAwareRouter 为非最后渠道注入的工件写入后缀。
	// 设置后，适配器将 req.ModelCallID+req.FallbackArtifactSuffix 作为工件标识写入，
	// 防止多渠道 fallback 链中各渠道失败摘要与最终成功摘要共用同一 model_call_id 产生语义冲突。
	// 空字符串时行为与原单渠道路径完全一致。
	FallbackArtifactSuffix string
	// FallbackSafety 由 fallback router 为每个渠道尝试创建，适配器与 retry 层
	// 只通过 typed 方法更新。nil 表示普通单渠道路径，不改变既有行为。
	FallbackSafety *FallbackSafetyInfo
	// FallbackBudget 是整条 fallback chain 共享的 HTTP attempt 与退避等待预算。
	// nil 表示普通单渠道路径，仍使用原有 providerRetry 本地预算。
	FallbackBudget *FallbackRetryBudget
}

// FallbackSafetyInfo 是单个渠道尝试的 typed 安全状态。Router 不根据错误
// 文本推断是否允许切换，只读取这些由 adapter/retry 层设置的事实。
type FallbackSafetyInfo struct {
	mu                 sync.Mutex
	rawBytesObserved   bool
	modelEventObserved bool
	requestBuildFailed bool
	httpAttempts       int
	waited             time.Duration
	waitBudgetBlocked  bool
	lastRetryDelay     time.Duration
}

type FallbackSafetySnapshot struct {
	RawBytesObserved   bool
	ModelEventObserved bool
	RequestBuildFailed bool
	HTTPAttempts       int
	Waited             time.Duration
	WaitBudgetBlocked  bool
	LastRetryDelay     time.Duration
}

func (s *FallbackSafetyInfo) MarkRawBytesObserved() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.rawBytesObserved = true
	s.mu.Unlock()
}

func (s *FallbackSafetyInfo) MarkModelEventObserved() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.modelEventObserved = true
	s.mu.Unlock()
}

func (s *FallbackSafetyInfo) MarkRequestBuildFailed() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.requestBuildFailed = true
	s.mu.Unlock()
}

func (s *FallbackSafetyInfo) markHTTPAttempt() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.httpAttempts++
	s.mu.Unlock()
}

func (s *FallbackSafetyInfo) markWaited(delay time.Duration) {
	if s == nil || delay <= 0 {
		return
	}
	s.mu.Lock()
	s.waited += delay
	s.lastRetryDelay = delay
	s.mu.Unlock()
}

func (s *FallbackSafetyInfo) markWaitBudgetBlocked() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.waitBudgetBlocked = true
	s.mu.Unlock()
}

func (s *FallbackSafetyInfo) Snapshot() FallbackSafetySnapshot {
	if s == nil {
		return FallbackSafetySnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return FallbackSafetySnapshot{
		RawBytesObserved:   s.rawBytesObserved,
		ModelEventObserved: s.modelEventObserved,
		RequestBuildFailed: s.requestBuildFailed,
		HTTPAttempts:       s.httpAttempts,
		Waited:             s.waited,
		WaitBudgetBlocked:  s.waitBudgetBlocked,
		LastRetryDelay:     s.lastRetryDelay,
	}
}

// FallbackRetryBudget 对同一 fallback chain 的所有渠道共享计数。每次真正
// client.Do 前消费一个 attempt；每次 retry sleep 前预留等待预算。
type FallbackRetryBudget struct {
	mu                sync.Mutex
	maxAttempts       int
	maxWait           time.Duration
	remainingAttempts int
	remainingWait     time.Duration
}

func NewFallbackRetryBudget(maxAttempts int, maxWait time.Duration) *FallbackRetryBudget {
	if maxAttempts < 0 {
		maxAttempts = 0
	}
	if maxWait < 0 {
		maxWait = 0
	}
	return &FallbackRetryBudget{
		maxAttempts:       maxAttempts,
		maxWait:           maxWait,
		remainingAttempts: maxAttempts,
		remainingWait:     maxWait,
	}
}

func (b *FallbackRetryBudget) TryConsumeAttempt() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.remainingAttempts <= 0 {
		return false
	}
	b.remainingAttempts--
	return true
}

func (b *FallbackRetryBudget) TryReserveWait(delay time.Duration) bool {
	if b == nil || delay <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if delay > b.remainingWait {
		return false
	}
	b.remainingWait -= delay
	return true
}

func (b *FallbackRetryBudget) Remaining() (int, time.Duration) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remainingAttempts, b.remainingWait
}

func (b *FallbackRetryBudget) Snapshot() (maxAttempts, usedAttempts, remainingAttempts int, maxWait, usedWait, remainingWait time.Duration) {
	if b == nil {
		return 0, 0, 0, 0, 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	usedAttempts = b.maxAttempts - b.remainingAttempts
	if usedAttempts < 0 {
		usedAttempts = 0
	}
	usedWait = b.maxWait - b.remainingWait
	if usedWait < 0 {
		usedWait = 0
	}
	return b.maxAttempts, usedAttempts, b.remainingAttempts, b.maxWait, usedWait, b.remainingWait
}

// LLMArtifactPaths 表示一次模型调用相关工件路径。
type LLMArtifactPaths struct {
	RequestPath  string
	ResponsePath string
	SummaryPath  string
}

// LLMArtifactObserver 定义模型调用原始工件写入接口。
type LLMArtifactObserver interface {
	RecordLLMRequest(requestID string, runID string, modelCallID string, payload map[string]any) (string, error)
	AppendLLMResponseChunk(requestID string, runID string, modelCallID string, chunk string) (string, error)
	RecordLLMSummary(requestID string, runID string, modelCallID string, payload map[string]any) (string, error)
}

// ModelEventKind 表示统一模型事件类型。
type ModelEventKind string

const (
	// ModelEventKindTextDelta 表示文本增量事件。
	ModelEventKindTextDelta ModelEventKind = "text_delta"
	// ModelEventKindThinkingDelta 表示思考增量事件。
	ModelEventKindThinkingDelta ModelEventKind = "thinking_delta"
	// ModelEventKindThinkingCompleted 表示思考结束事件。
	ModelEventKindThinkingCompleted ModelEventKind = "thinking_completed"
	// ModelEventKindPartialToolCall 表示工具调用已开始，但参数仍在流式生成中。
	ModelEventKindPartialToolCall ModelEventKind = "partial_tool_call"
	// ModelEventKindToolCallDelta 表示工具调用参数或输出的流式增量。
	ModelEventKindToolCallDelta ModelEventKind = "tool_call_delta"
	// ModelEventKindToolLikeCompleted 表示工具意图已完整收口。
	ModelEventKindToolLikeCompleted ModelEventKind = "tool_like_completed"
	// ModelEventKindTurnFinished 表示当前模型回合结束。
	ModelEventKindTurnFinished ModelEventKind = "turn_finished"
	// ModelEventKindProviderError 表示 provider 侧返回错误。
	ModelEventKindProviderError ModelEventKind = "provider_error"
)

// ModelEvent 表示一条统一模型事件。
type ModelEvent struct {
	// Kind 表示事件类型。
	Kind ModelEventKind
	// OccurredAt 表示当前 provider 事件发生时间。
	OccurredAt time.Time
	// Provider 表示当前事件所属 provider。
	Provider string
	// Model 表示当前事件所属模型标识。
	Model string
	// Text 表示文本增量。
	Text string
	// ThinkingStyle 表示思考样式。
	ThinkingStyle agentv1.ThinkingStyle
	// ThinkingDurationMS 表示思考持续时长。
	ThinkingDurationMS int32
	// ThinkingSignature 表示 provider 返回的思考签名（如 Anthropic signature_delta）。
	ThinkingSignature string
	// ThinkingSignatureSource 表示思考签名的 provider 语义来源。
	ThinkingSignatureSource string
	// ProviderItemID 保存 provider 原始 output item id，用于 stateless Responses replay。
	ProviderItemID string
	// ProviderStatus 保存 provider 原始 output item status，用于 stateless Responses replay。
	ProviderStatus string
	// ProviderSummary 保存 provider 原始 output item summary，用于 stateless Responses replay。
	ProviderSummary json.RawMessage
	// ProviderCallID 保存 provider 原始 tool/function call id，用于 stateless Responses replay。
	ProviderCallID string
	// ToolCallID 表示当前 partial/delta 对应的工具调用标识。
	ToolCallID string
	// ToolCall 保存 partial tool call 当前可公开的结构化快照。
	ToolCall *agentv1.ToolCall
	// ToolCallDelta 保存与当前工具调用相关的流式增量。
	ToolCallDelta *agentv1.ToolCallDelta
	// ArgsTextDelta 保存原始工具参数文本增量，供兼容层透传。
	ArgsTextDelta string
	// InputTokens 表示当前已知的输入 token 数。
	InputTokens int64
	// OutputTokens 表示当前已知的输出 token 数。
	OutputTokens int64
	// CacheReadTokens 表示当前已知的 cache read token 数。
	CacheReadTokens int64
	// CacheWriteTokens 表示当前已知的 cache write token 数。
	CacheWriteTokens int64
	// UsagePresent 表示 provider 本次流里实际返回过 usage 对象。
	UsagePresent bool
	// CacheReadPresent 表示 provider 明确返回了 cache read token 字段。
	CacheReadPresent bool
	// CacheWritePresent 表示 provider 明确返回了 cache write token 字段。
	CacheWritePresent bool
	// ToolInvocation 表示完成收口的工具调用意图。
	ToolInvocation *runtimecore.ToolInvocation
	// FinishReason 表示回合结束原因。
	FinishReason string
	// Err 表示 provider 错误。
	Err error
}

// ModelAdapter 定义具体 provider 适配器接口。
type ModelAdapter interface {
	// Stream 按流式方式发送请求，并持续产出统一模型事件。
	Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error
}

// ModelAdapterRouter 定义 provider 路由接口。
type ModelAdapterRouter interface {
	// Stream 根据模型标识选择底层 provider 适配器。
	Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error
}
