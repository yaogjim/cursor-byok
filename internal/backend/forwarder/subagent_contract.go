// subagent_contract.go 定义 subagent 生命周期的稳定合同：终态类别、handoff 状态、
// 运行状态和持久化记录结构。
// 设计约束：
//   - 不存储 prompt/result 正文到观测日志；result 持久化有大小上限
//   - 终态 CAS 胜出策略：首个 terminal 写入胜出，冲突产生 observable 事件
//   - 重启后非终结 run 转为 awaiting_client_resume，禁止自动重派
package forwarder

import (
	"time"
)

const subagentContractSchemaVersion = 1

// ──────────────────────────────────────────────────────────────────────────────
// 终态类别
// ──────────────────────────────────────────────────────────────────────────────

// SubagentTerminalCategory 是 subagent run 的稳定机器可判定终态。
// 首个合法 terminal CAS 胜出；后续冲突记录 subagent_terminal_conflict 观测事件。
type SubagentTerminalCategory string

const (
	// SubagentTerminalSucceeded: 子代理正常完成，有最终消息。
	SubagentTerminalSucceeded SubagentTerminalCategory = "succeeded"
	// SubagentTerminalCanceled: 被父侧或 Cursor 显式取消。
	SubagentTerminalCanceled SubagentTerminalCategory = "canceled"
	// SubagentTerminalTimeout: 超出运行时间预算。
	SubagentTerminalTimeout SubagentTerminalCategory = "timeout"
	// SubagentTerminalProviderError: 子侧 provider 返回不可恢复错误。
	SubagentTerminalProviderError SubagentTerminalCategory = "provider_error"
	// SubagentTerminalToolError: exec-bridge Throw 或不可恢复 tool 失败。
	SubagentTerminalToolError SubagentTerminalCategory = "tool_error"
	// SubagentTerminalParentUnavailable: parent stream 关闭，result 无法提交。
	SubagentTerminalParentUnavailable SubagentTerminalCategory = "parent_unavailable"
	// SubagentTerminalTruncated: 子代理耗尽上下文被强制停止。
	SubagentTerminalTruncated SubagentTerminalCategory = "truncated"
	// SubagentTerminalProtocolError: 未知结果形态或不符合预期的协议状态。
	SubagentTerminalProtocolError SubagentTerminalCategory = "protocol_error"
	// SubagentTerminalUnknown: 零值哨兵，已提交状态中不应出现。
	SubagentTerminalUnknown SubagentTerminalCategory = "unknown"
)

// ──────────────────────────────────────────────────────────────────────────────
// Run 状态机
// ──────────────────────────────────────────────────────────────────────────────

// SubagentRunStatus 跟踪 subagent 派发的生命周期阶段。
type SubagentRunStatus string

const (
	// SubagentRunDispatched: Task exec 已发给 Cursor，尚未绑定 child。
	SubagentRunDispatched SubagentRunStatus = "dispatched"
	// SubagentRunRunning: child conversation 正在活跃运行。
	SubagentRunRunning SubagentRunStatus = "running"
	// SubagentRunBackgrounded: child 已后台化。
	SubagentRunBackgrounded SubagentRunStatus = "backgrounded"
	// SubagentRunTerminalPrepared: result.json 已持久化；等待 parent commit。
	SubagentRunTerminalPrepared SubagentRunStatus = "terminal_prepared"
	// SubagentRunParentCommitted: parent history entry 已原子追加。
	SubagentRunParentCommitted SubagentRunStatus = "parent_committed"
	// SubagentRunAwaitingClientResume: Backend 重启前 run 未终结；等待 Cursor resume。
	SubagentRunAwaitingClientResume SubagentRunStatus = "awaiting_client_resume"
	// SubagentRunAwaitingParentResume: result.json 已持久化但 parent stream 不活跃；等待 parent 恢复重试 commit。
	SubagentRunAwaitingParentResume SubagentRunStatus = "awaiting_parent_resume"
	// SubagentRunAcknowledged: 完整 commit + checkpoint 周期已完成。
	SubagentRunAcknowledged SubagentRunStatus = "acknowledged"
)

// isTerminalRunStatus 返回 run 是否已到达已提交终态。
func isTerminalRunStatus(s SubagentRunStatus) bool {
	switch s {
	case SubagentRunParentCommitted, SubagentRunAcknowledged:
		return true
	default:
		return false
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Handoff 状态
// ──────────────────────────────────────────────────────────────────────────────

// SubagentHandoffState 是 parent tool-result 提交事务的持久状态。
type SubagentHandoffState string

const (
	SubagentHandoffNone                SubagentHandoffState = ""
	SubagentHandoffPrepared            SubagentHandoffState = "prepared"
	SubagentHandoffParentCommitted     SubagentHandoffState = "parent_committed"
	SubagentHandoffAwaitingResume  SubagentHandoffState = "awaiting_parent_resume"
	SubagentHandoffAcknowledged   SubagentHandoffState = "acknowledged"
)

// ──────────────────────────────────────────────────────────────────────────────
// 身份与关联字段
// ──────────────────────────────────────────────────────────────────────────────

// SubagentIdentity 保存一次 subagent run 的全部稳定关联 ID。
// 缺失值保持空字符串（保持 unknown），不按时间推断。
type SubagentIdentity struct {
	SubagentRunID        string `json:"subagent_run_id"`
	ParentConversationID string `json:"parent_conversation_id,omitempty"`
	RootConversationID   string `json:"root_conversation_id,omitempty"`
	ParentToolCallID     string `json:"parent_tool_call_id,omitempty"`
	ParentRequestID      string `json:"parent_request_id,omitempty"`
	ParentModelCallID    string `json:"parent_model_call_id,omitempty"`
	ParentTurnSeq        int64  `json:"parent_turn_seq,omitempty"`
	ChildConversationID  string `json:"child_conversation_id,omitempty"`
	AgentID              string `json:"agent_id,omitempty"`
	SubagentType         string `json:"subagent_type,omitempty"`
	ModelID              string `json:"model_id,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// 持久化记录
// ──────────────────────────────────────────────────────────────────────────────

// SubagentRunRecord 是持久化在 SubagentRunStore 的运行身份 + 生命周期记录。
// 写入路径：historyRoot/_subagents/<run_id>/run.json
// 文件权限：0600；原子替换（temp + rename）；带 checksum 校验。
type SubagentRunRecord struct {
	SchemaVersion int               `json:"schema_version"`
	Identity      SubagentIdentity  `json:"identity"`
	Status        SubagentRunStatus `json:"status"`
	// Version 是单调递增的乐观并发版本，用于 CAS 更新。
	Version int64 `json:"version"`
	// 终态字段 — 仅 terminal_prepared 后设置
	TerminalCategory SubagentTerminalCategory `json:"terminal_category,omitempty"`
	TerminalAt       *time.Time               `json:"terminal_at,omitempty"`
	// Handoff 字段
	HandoffState      SubagentHandoffState `json:"handoff_state,omitempty"`
	ParentCommitKey   string               `json:"parent_commit_key,omitempty"`
	RecoveryDegraded  bool                 `json:"recovery_degraded,omitempty"`
	LastRecoveryError string               `json:"last_recovery_error,omitempty"`
	// 时间戳
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Checksum 是本记录（Checksum 字段置零后）的 sha256 十六进制，用于损坏检测。
	Checksum string `json:"checksum,omitempty"`
}

// SubagentResultEnvelope 是 terminal_prepared 后写入 result.json 的持久化结果信封。
// 写入路径：historyRoot/_subagents/<run_id>/result.json
// 设计约束：
//   - 不记录完整 prompt/result 正文（隐私保护）
//   - ToolResultPayload 有大小上限（subagentResultPayloadLimit）
//   - ToolCallEncoded 有大小上限（subagentResultToolCallLimit）
//   - ResultSummary 有大小上限（subagentResultSummaryLimit）
type SubagentResultEnvelope struct {
	SchemaVersion    int                      `json:"schema_version"`
	SubagentRunID    string                   `json:"subagent_run_id"`
	TerminalCategory SubagentTerminalCategory `json:"terminal_category"`
	TerminalAt       time.Time                `json:"terminal_at"`
	// ResultDigest 是规范化 result text 的 sha256，用于幂等性验证。
	ResultDigest string `json:"result_digest"`
	// ResultSummary 是安全的简短非敏感摘要（最多 subagentResultSummaryLimit 字节）。
	ResultSummary string `json:"result_summary,omitempty"`
	// ToolName 是触发本 subagent run 的 parent tool call 名称（如 "Task"）。
	// 恢复时使用；空值兼容旧记录，默认回退为 "Task"。
	ToolName string `json:"tool_name,omitempty"`
	// ToolResultPayload 是追加到 parent history 的文本（最多 subagentResultPayloadLimit 字节）。
	ToolResultPayload string `json:"tool_result_payload,omitempty"`
	// ToolCallEncoded 是 protojson 编码的 ToolCall（最多 subagentResultToolCallLimit 字节）。
	ToolCallEncoded []byte `json:"tool_call_encoded,omitempty"`
	// ArgsJSON 保存 Task args 用于 history 重建。
	ArgsJSON []byte `json:"args_json,omitempty"`
	// ParentCommitKey 是 parent history 追加的幂等键。
	ParentCommitKey string `json:"parent_commit_key"`
	// Checksum 是本信封的 sha256 十六进制。
	Checksum string `json:"checksum,omitempty"`
}

const (
	// subagentResultPayloadLimit: result.json 中 tool_result_payload 最大 64 KiB。
	subagentResultPayloadLimit = 64 * 1024
	// subagentResultToolCallLimit: result.json 中 tool_call_encoded 最大 256 KiB。
	subagentResultToolCallLimit = 256 * 1024
	// subagentResultSummaryLimit: result_summary 最大 512 字节（安全摘要，无敏感内容）。
	subagentResultSummaryLimit = 512
)
