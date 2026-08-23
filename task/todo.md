# 活动任务

> 本文件是项目活动任务的唯一真值源。`.cursor/plans/*.plan.md` 的 frontmatter todo 仅作阶段索引，不代表任务已满足 Definition of Ready。

## Active Work Package

WORK_PACKAGE_ID: agent-governance-completion-20260822
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved (compatible replay aggregation + single-retry evidence gate)
DELIVERY_STATUS: accepted (implemented, wired, independently reviewed, fully regression-tested, documented; not committed or released)

### CONTEXT

- 用户已批准 `.cursor/plans/agent_治理补全主控执行计划_6bb10903.plan.md`，要求主控串行管理 subagent 完成 replay 聚合、执行证据账本、最终完成门禁、提示约束与 metadata-only 诊断。
- 基线固定为 `v0.0.49.2` 发布提交 `487856170b29380671477e843d7fec15250323ae`；隔离分支/worktree 为 `agent-governance-0.0.49.2` / `cursor-byok-governance-0.0.49.2`。
- `v0.0.49.1` 必须保持 `716b436ca0d79e34e52ea02a8ecc07f6579b5cfe`，不得移动、覆盖或复用。
- 当前主工作树的无关 WIP 不纳入本包；stash `bd8bf912...`（protocol recorder）与 `ab88a93a...`（fixture exporter）禁止 apply/drop。
- 用户已确认兼容式 history 双读和证据不足时最多一次提醒续跑；不修改公共 proto，不自动提交、推送或发布。

### GOAL

- 同一 `model_call_id` 的 reasoning 在内部 replay 只恢复一次，同时保持 canonical history、signature、provider metadata、工具顺序/参数/result 关联。
- 只以结构化 ToolCall 与成功终态 tool result 建立 metadata-only mutation/verification 证据；pending、失败、取消、未知 MCP 和 assistant 自述不得成为成功证据。
- mutation 后旧 verification 自动 stale；最终完成前最多提醒并续跑一次，第二次仍不足时保守结束并记录诊断。
- 公共 `agent-transcripts` 继续零 reasoning 泄漏，普通日志和账本不复制 reasoning、stdout、文件正文、凭据、路径或工具参数正文。

### ACTIVE SLICES

- [completed] `gov-stage-0`：已核对发布/标签/stash 指针，保护当前 WIP，并从 `4878561` 创建干净隔离 worktree；`git diff --check` 与保护路径检查通过。
- [completed] `gov-stage-1`：系统 Design §14.8 已冻结兼容 history/replay/ledger/gate、恢复与隐私合同；replay RED 测试按 TDD 落地。
- [completed] `gov-stage-2`：已贯通 `HistoryEntry.ModelCallID` 并按模型调用聚合 reasoning；只读复审发现的“弱 tuple 先占槽导致完整 tuple 丢失”P1 已修复。主控复验 `go test ./internal/backend/forwarder -count=1 -timeout 600s`、同包 race、vet、`git diff --check` 全部通过。
- [completed] `gov-stage-3`：已实现 metadata-only `execution_evidence`、typed terminal、mutation/verification 分类、最终 entry sequence、同一追加批次写入、跨 turn 幂等、restart/subagent rebuild 和 stale 判定。复审发现 AwaitShell 无 exit 误落失败及脚本前缀假阳性，均已修复；主控专项/全包/race/vet/diff 通过。保守边界：重启后不持久化后台命令正文，无法分类时保持 unknown；异步 subagent recovery 的 live 索引可能滞后，门禁必须从最新持久 history 重建。
- [completed] `gov-stage-4`：已接入单次提醒续跑、restart/重复 complete 幂等和第二次不足保守收口。独立复审发现编辑+解释请求被错误绕过（P1）及中文建议问句误触发（P2），均已按 TDD 修复并复核关闭；主控专项、forwarder 全包、race、vet、gofmt、`git diff --check` 全部通过。服务切换后的首次 520 按 20 秒退避恢复成功。
- [completed] `gov-stage-5`：已为 agent/subagent 静态 prompt 与动态 reminder 加入真实工具成功终态、mutation 后晚验证和失败缺口合同；Ask/Plan 不强制编辑且工具 allowlist 不变。`turn_completed` 与 debug recorder 已接入逐字段 metadata-only 诊断：reasoning SHA-256/absent、公共 transcript reasoning emit count、mutation/verification/stale/evidence 和 gate 状态。复审发现公共 transcript 合法 tool path 被隐私测试误报及投影错误 fail-open，均已修复；主控 prompt/专项/全包/race/vet/diff 通过。
- [completed] `gov-stage-6`：治理故障矩阵、forwarder 全包/race/vet、`go test ./internal/...`、`go vet ./internal/...`、根模块 `go test .`/`go test ./...`/`go vet .`、prompt、前端配置投影与生产构建、根客户端二进制、`cursor-tab-server` test/race/vet、`tools/log-analyzer` test/race/vet 均通过。初次根全量并发链接因磁盘 `ENOSPC` 失败，系统回收后以 `-p 1` 相同覆盖范围重跑通过；生成 symlink/dist/临时二进制已删除，禁止路径与 `git diff --check` 无差异。
- [completed] `gov-stage-7`：三路独立终审和主控反向审计已闭合。replay 终审发现并修复两个 P1：禁止 orphan reasoning 跨 `model_call_id` rehome，以及把 provider signature 与 exact reasoning content 绑定，避免“新正文 + 旧签名”；最终复核无新 P0/P1。账本/门禁及 prompt/诊断/隐私两路终审无可复现 P0/P1/P2。新增集成回归证明 completion gate 的内部 prompt reminder 与 metadata 保留在 canonical history、不会进入公共 transcript，同时用户可见回答和合法结构化 `tool_use.path` 均保留。
- [completed] `gov-stage-8`：在 replay 终审修复后重新运行公共 transcript 专项、forwarder 全包/race/vet、根模块串行全量 test/vet、gofmt 与 `git diff --check`，全部通过；更新 `task/todo.md`、`docs/process.md` 与系统 Design。根测试首次因缺少真实 `frontend/dist` 而在 `go:embed` setup 阶段停止；只读复用主工作树依赖生成隔离临时 dist 后重跑通过，临时 dist、依赖 symlink、bindings symlink、`gen` symlink 均已删除。公开 proto、MITM/证书、发布资产、主工作树 WIP 与两个专用 stash 无漂移。
- [completed] `post-governance-interrupted-work`：已只读分析指定 transcript 的全部用户请求、回合终态和结构化工具时间线；分析快照为 820 条 JSONL、SHA-256 `2f638d5571a0d3dc313033408d0700417d2dda6d2c76440575596f3b7aa3cf99`（该 transcript 会随当前会话继续增长，仅用于复算本次结论）。原会话中被停止的 Stage 4 在用户切换 LLM 服务后已恢复并闭合；Stage 6 的四条 `Superseded by newer request` 终态只是同一根测试命令被后续请求替代，之后已用 `-p 1` 串行重跑闭合，不是四个独立遗留任务。治理计划五项能力均已实现并验收。分析发现原建议中唯一可直接补齐的规格遗漏是“15K reasoning canary”；现已把三工具共享 reasoning 回归提升为显式 15 KiB start/end canary，并通过专项、forwarder 全包/race/vet 和 diff 检查。

### FINAL VERIFICATION EVIDENCE

- `go test ./internal/backend/forwarder -run 'TestCompleteSuccessfulTurnFirstInsufficientDoesNotProjectGateIntoPublicTranscript|TestPublicTranscriptReasoningEmitCountMustBeZero|TestCursorTranscript' -count=1 -timeout 600s`：通过。
- `go test ./internal/backend/forwarder -run 'TestProjectCursorTranscriptMultipleToolCallsOmitShared15KReasoning|TestCompleteSuccessfulTurnFirstInsufficientDoesNotProjectGateIntoPublicTranscript|TestPublicTranscriptReasoningEmitCountMustBeZero' -count=1 -timeout 600s`：transcript 分析后新增的 15 KiB canary 专项通过。
- `go test ./internal/backend/forwarder -count=1 -timeout 600s`：通过。
- `go test -race ./internal/backend/forwarder -count=1 -timeout 600s`：通过，无 race。
- `go vet ./internal/backend/forwarder`：通过。
- `go test -p 1 ./... -count=1 -timeout 900s`：通过；覆盖根模块全部 package。
- `go vet -p 1 ./...`：通过。
- 改动 Go 文件 `gofmt -d` 无输出；`git diff --check` 通过。
- Stage 6 已通过且本轮未受 replay 修复影响的既有证据继续有效：`go test ./internal/...` / `go vet ./internal/...`、根客户端构建、prompt 测试、前端配置投影与生产构建，以及 `cursor-tab-server`、`tools/log-analyzer` 两个独立模块的 test/race/vet。

### CONSERVATIVE COMPATIBILITY BOUNDARIES

- 裸 `modeladapter.Message` 二次 normalize 已丢失 model-call 身份时，不执行 orphan reasoning rehome；该保守行为避免跨调用误合并。
- 旧 history 的连续工具批次若双方 `model_call_id` 都为空，继续保留旧合并行为；这只是旧数据兼容，不作为新 history 身份隔离能力宣传。
- 真实 Cursor success/error/background/resume 六场景 fixture 仍是独立证据缺口；当前治理验收不证明 child 从中断执行点自动续跑。
- 原始建议中的“逐项比对 assistant 声称文件与 Git diff”和“reasoning 超阈值时中途打断”没有作为本治理包运行语义实现：前者与 metadata-only、禁止持久路径/完整参数的隐私合同冲突，后者会改变流式模型行为。当前已批准实现是在完成点检查结构化 mutation 与晚于 mutation 的 verification，并最多提醒一次；若要增加更强门禁，必须另做 Design 与产品决策。
- recorder/exporter 两个专用 stash 仍是独立工作包，未被读取、应用或混入治理；继续实现和真实 fixture 采集需要新的明确授权。

### STOP CONDITIONS

- 需要修改公共 proto、MITM/CA/证书/路由、发布元数据，或移动 `v0.0.49.1`。
- 发现需要破坏性 history 迁移、无法避免重复工具副作用、或完成门禁会误伤纯问答且不能在既定语义内修复。
- subagent 服务连续 5 次无法恢复；按 20/40/80/160/320 秒退避后保存 agent ID、错误、diff 和测试证据并停止等待用户。
- 需要应用/删除 recorder 或 exporter 专用 stash，或把当前主工作树其他 WIP 整体复制进本分支。

### ACCEPTANCE

- 计划 §12 全部总体验收条件闭合；实现、接线、精确/全量/race/vet/build、隐私扫描、旧 history 双读、故障注入和独立复审均有实际成功证据。
- `proto/`、`internal/mitm/`、证书、发布资产和两个专用 stash 无变化；不 commit/push/tag/release。
- 完成后同步本文件、`docs/process.md` 和系统 Design；必要验证未运行时只能标 `verified-partial`，不得标 `accepted`。

## Previous Work Package: P1 subagent handoff 与 Provider fallback

WORK_PACKAGE_ID: p1-subagent-handoff-provider-fallback-20260821
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved (durable-handoff + ordered-allowlist)
DELIVERY_STATUS: core implementation and verification completed; real Cursor protocol fixture remains pending evidence

### CONTEXT

- 用户已确认继续实施除 MITM、证书安装、系统代理接管和透明代理扩展外的 P1。
- Subagent 恢复承诺为 `durable-handoff`：持久化已生成终态并在重启后幂等交接给 parent；不承诺 child 执行点自动续跑。
- Provider fallback 为默认关闭的 `ordered-allowlist`：只尝试显式有序渠道；任意原始流字节、model event 或副作用后禁止切换；retry/fallback 共享 attempts 与等待预算。
- 不修改 proto；child prompt/result 正文和 Provider 原始敏感响应不得进入观测日志，凭证和 Provider 原始敏感响应不得落盘。
- 设计锚点：`docs/prd_cursor_byok_工作决策基线.md#10-p1-subagent-恢复与-provider-fallback-决策基线`、`docs/prd_cursor_byok_系统架构与核心业务数据流.md#147-design-p1-subagent-fallbacksubagent-恢复与-provider-fallback`。

### GOAL

- 建立版本化 `SubagentRunStore`，按 `terminal_prepared → parent_committed → acknowledged` 顺序完成本地 parent history/tool-result 的幂等唯一交接。
- Backend 启动扫描持久化 run；已生成终态可重放，未终结 child 转为 `awaiting_client_resume`，不得自动重派。
- 贯通 root/parent/tool/subagent/child/agent/model-call/attempt 的可选关联字段；缺失值保持空值，不按时间伪造。
- 实现默认关闭、显式有序且安全窗口严格受限的 Provider fallback，并提供后端校验、前端编辑和受控观测。

### NON_GOALS

- 不承诺 Backend 崩溃后 child 从原执行点自动续跑，不自行重派 Task 或重放工具副作用。
- 不实现已有输出后的 Provider 切换、续传、答案拼接或整轮重放。
- 不修改公共 proto、MITM whitelist、CONNECT/直通、CA/证书、系统代理或透明代理行为。
- 不提交、不推送、不发布，不停止或替换当前运行中的唯一代理实例。

### ACTIVE SLICES

- [completed] `p1-parent-correlation`：扩展并接线 parent/root/tool/subagent/child/agent/model-call/attempt correlation；缺失值保持 unknown/空值。
- [completed] `p1-terminal-contract`：实现 typed terminal 分类、首个持久化终态胜出和冲突保护。
- [completed] `p1-durable-handoff`：实现版本化 run/result、原子替换、checksum、启动恢复、parent 幂等提交及 crash-window/重复 replay/并发 CAS 测试。
- [completed] `p1-provider-fallback`：实现默认关闭的显式有序配置、resolver/router、raw-byte/model-event/兼容性门禁、共享预算、工件隔离和前端入口。
- [completed] `p1-integration-verification`：全仓 Go、race、vet、前端配置投影/生产构建、差异与敏感信息审查均通过；禁止范围零变更。
- [pending-evidence] `p1-protocol-fixture`：未采集新的真实 Cursor 协议 fixture；本轮仅使用并测试已有生成字段，不修改 proto。该缺口不得被表述为 child 自动续跑已验证。

### LATEST REVIEW

- 修复缺失 parent 持久会话时恢复逻辑静默创建孤立 conversation 的问题：online/recovery handoff 现在只追加到已存在 parent；缺失或已删除时保持 durable result 并进入 `awaiting_parent_resume`。
- 修复合法 checksum 但未知 run status 被启动扫描静默忽略的问题：未知状态现在隔离到 `_corrupt/`，不会被误判为无需处理。
- 修复 fallback 链末尾候选均不兼容时，最后实际尝试渠道仍使用临时候缀且 `fallback_to` 指向被跳过渠道的问题：现在按下一实际兼容候选计算工件终点和切换观测。
- 已补 online/recovery missing-parent、unknown-status isolation、跳过不兼容候选与实际最终渠道工件标识回归测试。

### ACCEPTANCE

- `gofmt`、`git diff --check`、`go test ./... -count=1` 通过。
- 相关 Go 包 `go vet` 与 `go test -race` 通过；前端配置投影测试和生产构建通过。
- 故障注入覆盖 result/run 原子写窗口、prepare/parent commit/run CAS 窗口、重复 replay、并发 CAS、非法 JSON/raw-byte、输出后禁止 fallback、预算和兼容门禁。
- `git diff` 不包含 `proto/`、`internal/mitm/`、证书、透明代理或系统代理范围变更。
- 记录单写者约束：同一 `historyRoot` 同时只能由一个活跃 Backend 写入；进程内 CAS 不宣传为跨进程 CAS。

### STOP CONDITIONS

- 需要扩大 MITM/证书/透明代理范围、修改公共 proto，或要求 child 崩溃后自动续跑。
- 发现无法在零原始字节、零 model event、零副作用窗口内证明 fallback 安全。
- 全仓回归需要扩大到未批准业务范围。

## Previous Work Package (P0)

WORK_PACKAGE_ID: provider-disconnect-recovery-20260821
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved (P0)
DELIVERY_STATUS: completed (P0); P1 deferred to roadmap

### CONTEXT

- 用户已确认按 P0/P1/P2 优先级实施 Provider 断连改进，并授权主控使用 subagent 执行编码与验证。
- 重点 trace `20260820T104843.414858000Z-5f93b537521a` 重新解析 241,852 条事件：21 个 `provider_stream_finished=error` 全部先出现 `llm_summary succeeded`；4 个失败调用已有 chunk，3 个存在 tool-call 关联元数据。
- 已确认根因之一：basic artifact 使用 `payload_summary`，归一化只读取 `payload`，导致失败摘要被默认投影为成功。
- 当前 HTTP retry 包围 `client.Do` 与 status；OpenAI/Anthropic 已校验 completion marker，但 2xx 后首个 model event 前的截断未进入请求重试。
- 设计锚点：`docs/prd_cursor_byok_工作决策基线.md#62-agent工具链与-provider-断连`、`docs/prd_cursor_byok_系统架构与核心业务数据流.md#145-design-provider-disconnect-001provider-断连终态与安全恢复`。
- 2026-08-21 P1 复核结论：当前 P0 已满足 provider 断连正确性门禁，父子关联、独立 checkpoint、结构化失败和原子结果提交只在承诺 subagent 崩溃恢复 / exactly-once 结果交付时成为必做；provider fallback 与 MITM 行为变更均非必做，全部暂缓到 `.cursor/plans/cursor_能力路线图_13d772bc.plan.md#暂缓provider-断连-p1-与进入条件`。

### GOAL

- 修复 basic/full 的 model-call 成功/失败语义冲突，建立可审计的 provider 协议终态和唯一业务终态。
- 仅在首个 model event、下游发布、工具和 checkpoint 均未发生时，对可重试的流前截断执行有限重试。
- 保持已有输出幂等保存、工具副作用保护、RunSSE 结构化失败和日志隐私边界。

### NON_GOALS

- 不实现已有输出后的 SSE 续传或答案拼接。
- 不实现跨 provider fallback、熔断或自动模型切换。
- 不在本工作包内临场设计 subagent checkpoint/result/parent commit 的原子持久化协议；该项进入后续 P1 独立设计与实施。
- 不修改 MITM whitelist、CONNECT/直通决策、proto 公共契约、前端、发布资产，不提交、不推送、不发布。

### BASELINE

- branch/head：`noad` / `08509589d75cee408552f8cb80a3a9470a94be51`。
- P0 已作为单个原子提交纳入仓库；本次仅做 P1 必要性复核和路线图收口。
- 运行中唯一代理实例受保护；测试不得停止或替换 `127.0.0.1:18080` / `127.0.0.1:18090` 的服务。
- `task/todo.md` 仅由主控修改；subagent 不得修改活动任务真值源。

### SLICES

#### provider-terminal-contract

- priority: P0
- status: completed
- owner: coding-subagent
- size: S
- depends_on: none
- scope: basic/full artifact error 投影、`llm_summary` 非业务终态语义、回归测试。
- expected paths: `internal/backend/forwarder/debug_recorder.go`、同包测试、必要的 `internal/observability/` 契约测试。
- acceptance: basic/full 的失败摘要均为 failed；同一 model call 不再因 basic omission 被错误标为 succeeded。

#### provider-stream-final

- priority: P0
- status: completed
- owner: coding-subagent
- size: M
- depends_on: provider-terminal-contract contract
- scope: pass 进度状态、`provider_stream_finished` 结构化字段、唯一 `model_call_final`、RunSSE 一致性、脱敏错误。
- expected paths: `internal/backend/forwarder/types.go`、`actor.go`、`service.go`、`provider.go`、相关测试；可选扩展 `internal/observability/contract.go`。
- acceptance: success/error/cancel/truncated 均有唯一一致 final；已有输出幂等；partial/completed/dispatched tool 状态可审计且不重复执行。

#### provider-stream-safe-retry

- priority: P0
- status: completed
- owner: coding-subagent
- size: M
- depends_on: provider-terminal-contract contract
- scope: OpenAI/Anthropic 2xx 后首 model event 前的可重试截断；保持同一 model call、重建 request、关闭 body、复用 attempts/退避预算。
- expected paths: `internal/backend/agent/model/retry.go`、`openai.go`、`anthropic.go`、相关测试；如需共享 helper 仅限同包。
- acceptance: 首事件前 EOF/transport 按预算重试；任意 model event/tool progress 后零重试；context cancel 不重试。

#### provider-p0-integration-verification

- priority: P0
- status: completed
- owner: orchestrator + readonly-verifier
- size: M
- depends_on: three P0 coding slices
- scope: 集成、差异审查、测试/race/vet、真实或近真实 basic trace fixture 验证、文档收口。
- acceptance: 相关 package tests/race/vet 与 `git diff --check` 通过；无 secret；无 whitelist/provider fallback 行为变化；残余 gap 分类记录。

#### provider-parent-correlation

- priority: P1
- status: deferred-to-roadmap
- owner: roadmap
- size: M
- depends_on: provider-p0-integration-verification
- scope: root/parent conversation、parent tool、subagent task/run、model call、attempt 的可选关联字段与入口接线；不含原子结果提交。
- acceptance: 父子调用可从入口关联到 terminal，缺失字段明确为 unknown。
- reentry_condition: 需要跨父子审计、定位 child 失败归属，或开始设计 subagent 崩溃恢复。
- roadmap: `.cursor/plans/cursor_能力路线图_13d772bc.plan.md#暂缓provider-断连-p1-与进入条件`

#### subagent-atomic-result

- priority: P1
- status: deferred-to-roadmap
- owner: roadmap
- size: L，必须继续拆分
- depends_on: provider-parent-correlation
- block_reason: 需要独立实施级 Design 固化 child checkpoint/result、parent tool-result、结构化失败、事务/CAS、恢复和兼容合同。
- reentry_condition: 产品明确承诺 subagent 进程重启恢复或 child result 到 parent tool-result 的 exactly-once 交付，或真实故障证明存在完成结果丢失/重复提交。
- roadmap: `.cursor/plans/cursor_能力路线图_13d772bc.plan.md#暂缓provider-断连-p1-与进入条件`

### ALLOWED_ROOTS

- `internal/backend/agent/model/`
- `internal/backend/forwarder/`
- `internal/observability/`
- `internal/audit/`（仅必要的 typed/sanitized 字段）
- `docs/`
- `task/`

### FORBIDDEN_PATHS

- `proto/`
- `internal/mitm/`
- `tools/log-analyzer/`
- `cursor-tab-server/`
- `frontend/`
- `bin/release/`
- `.git/`
- 用户凭据、真实日志内容和运行中代理配置。

### ALLOWED_DECISIONS

- 同包私有 helper、结构体内部字段、事件 fields 的向后兼容可选扩展、测试夹具和错误类型组合。
- 保持既有最大 attempts、退避预算和 provider/model 选择不变。
- 对当前已确认的 basic 假成功、completion marker、幂等输出和副作用门禁做根因修复。

### REQUIRES_PARENT_APPROVAL

- 修改 public proto/API、持久化 schema 或 subagent 原子提交语义。
- 新增跨 provider fallback、熔断、模型切换或已有输出后的自动续传。
- 修改 MITM whitelist/CA/路由、引入依赖、停止运行中代理、提交、推送或发布。

### ACCEPTANCE

- `go test ./internal/backend/agent/model ./internal/backend/forwarder ./internal/observability -count=1` 通过。
- `go test -race ./internal/backend/agent/model ./internal/backend/forwarder ./internal/observability -count=1` 在可接受时间内通过；若环境超时，只能记录 env-gap，不宣称通过。
- `go vet ./internal/backend/agent/model ./internal/backend/forwarder ./internal/observability` 通过。
- `git diff --check` 通过。
- basic/full summary 失败语义一致；每个 model call 最多一个业务 final；首事件前可安全重试，已有事件/工具/checkpoint 后禁止重试；错误输出完成脱敏。

### STOP_CONDITIONS

- 需要修改公共协议、持久化 schema、MITM 路由或 provider fallback 语义。
- 无法在不重复下游输出/工具副作用的情况下实现流前重试。
- 测试发现已有 history replay、tool result、compaction 或 commit-message 链路回归且需要扩大范围才能修复。
- 发现用户已有未归属改动或需要停止运行中的唯一代理实例。

### MONITOR_POLICY

- mode: event-driven；三个 P0 编码切片可在冻结合同后并行，主控负责集成；随后启动独立只读 verifier。

### VERIFICATION_POLICY

- verifier: required。
- 先相关测试，再 package tests/race/vet，最后差异、安全、文档和任务状态反向审计。

## 编排任务：upstream-sync-release-0.0.47

- 用户请求：`upstream/main` → `main` → `noad` → 窄范围测试 → push origin → 三平台构建（无 Linux）→ 仅发布 `yaogjim/cursor-byok`；不提 PR。
- 用户确认：`noad` 本地领先 2 提交（`ad285dd`、`8e0f335`）纳入发布；当前 5 个本地脏文件保留为 BASELINE/FORBIDDEN；版本升到 `0.0.47`。
- 权威流程：`docs/cursor_byok_upstream_sync_release_runbook.md` + `docs/cursor_byok_upstream_merge_requirements.md`。

## Active StageSpec

STAGE_ID: none
STATUS: none
OWNER: orchestrator
NOTE: `cleanup-release-worktrees-01` 已通过 verifier 门禁；当前无辅助 worktree、无进行中 Active Stage。

### cleanup-release-worktrees-01

- STATUS: verified
- executor: be054fa2-3f41-4ce0-b294-cee6d231520a
- verifier: 6aab12fd-36bb-49e3-b4a3-15c3208c5b91
- removed:
  - `/Users/yaogj/Downloads/works/yaogjim/cursor-byok/cursor-byok-main-sync-wt`
  - `/Users/yaogj/Downloads/works/yaogjim/cursor-byok/cursor-byok-noad-sync-wt`
- preserved refs: `main@d306c30`、`noad@d013961`、`sync/main-into-noad-20260810-193647@d013961`、`v0.0.47@d013961`
- release assets: `bin/release/0.0.47` preserved


## Orchestration Result (upstream-sync-release-0.0.47)

- overall: completed（在已验证范围内）
- verified_stages:
  - upstream-main-mirror-01
  - noad-acceptance-bindings-tests-01（含 merge/stabilize 产物）
  - push-origin-noad-0.0.47-01
  - release-0.0.47-desktop-github-01
- aborted_or_rework_history:
  - noad-merge-main-0.0.47-01 aborted-partial → 稳定化为 c4edab0/d013961
  - noad-merge-stabilize-verify-01 rework-required → bindings/tests 返工通过
- key SHAs:
  - upstream/main: da5fa34a4d501bcc8548f1140fe91e05e0da9c77
  - main/origin/main: d306c3019db5fa7891af508cd2ea992f198cbc3e
  - noad/origin/noad/tag v0.0.47: d0139611d33e441ac366cd3e20704c8d0ebae2d4
- release: https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.47
- platforms: macos-arm64, macos-amd64, windows-amd64（无 linux）
- not done: BASELINE 本地脏文档未提交；差异 PRD 未强制追加本节；worktree 可后续清理

## Stage History

### release-0.0.47-desktop-github-01

- STATUS: verified
- VERDICT: pass（verifier b826a27c-ece1-4ed1-9b9b-e07b4e1eea76）
- executor: 071c5b03-b8f5-427f-976c-e86bbc48f3c2
- release: https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.47
- sha: d0139611d33e441ac366cd3e20704c8d0ebae2d4


### push-origin-noad-0.0.47-01

- STATUS: verified
- VERDICT: pass（verifier bb867ed9-f7fe-400b-8080-53aa4e358a31）
- executor: 9e64948b-861d-46f9-9cd0-d8d731407e17
- origin/noad: d0139611d33e441ac366cd3e20704c8d0ebae2d4

### noad-acceptance-bindings-tests-01

- STATUS: verified
- VERDICT: pass（verifier d0e18dd9-ed45-4b4b-8197-5888c72395ac）
- executor: ef42c04f-79c4-4ad4-bbad-8024ce7f2e35
- tip: d013961；version 0.0.47；buildinfo yaogjim
- residual_risk: verifier 未重跑全量 go test/frontend build，采用抽样 + executor 证据

### noad-merge-stabilize-verify-01

- STATUS: rework-required (partial)
- executor: f710311d-73f1-4e9d-b1ae-4e730502bfa9
- cleanup: pass；i18n commit d013961；主工作区仅 BASELINE keep
- acceptance: go full internal hang/timeout；frontend build missing bindings in worktree
- root_cause_followup: bindings gitignored；copy/generate in worktree；retest with -timeout

### noad-merge-main-0.0.47-01

- STATUS: aborted-partial
- note: executor 被用户中止；merge commit 已存在
- merge: c4edab0798283e887f9e00c213d18d5916f5509e (8e0f335 + d306c30)
- backup: backup/noad-before-upstream-20260810-193647 @ 8e0f335
- sync/worktree: sync/main-into-noad-20260810-193647 / cursor-byok-noad-sync-wt
- residual: 主工作区 index reverse-diff；worktree i18n 5 文件脏
- version/buildinfo: 0.0.47 / yaogjim OK

### upstream-main-mirror-01

- STATUS: verified
- VERDICT: pass（verifier agent 899f0b73-e806-4a40-817c-f67eb154c9ae）
- executor: 14f4a309-0404-4cb2-8a77-6b84b18a0fa7
- main/origin/main: d306c3019db5fa7891af508cd2ea992f198cbc3e
- upstream/main: da5fa34a4d501bcc8548f1140fe91e05e0da9c77
- backup: backup/main-before-upstream-20260810-192846 @ ab53e21
- deviation: README.md 冲突在 main 镜像阶段取 upstream；允许
- noad tip 未变: 8e0f335

## 当前执行队列

- [completed] `semantic-design-contract`：更新 PRD、系统 Design 与任务真值源，冻结日志 v2、项目隐私、语义枚举、案例和 AI 包合同。
- [completed] `analysis-project-engine`：抽取可复用 analysis project 生命周期，并保持现有 CLI 参数与报告语义兼容。
- [completed] `semantic-capture-v2`：实现 v1/v2 兼容及客户端 project/turn/capability/operation/outcome/build 指纹采集。
- [completed] `interactive-query-source`：实现 SQLite 分页、组合检索 DSL、保存查询、App 日志索引及安全 payload 按需访问。
- [completed] `enhanced-diagnostics`：增加能力状态机、compat/partial、证据缺口、百分位和多维 baseline 诊断。
- [pending] `investigation-case-library`：实现持久调查案例库、脱敏证据快照、状态机、版本关联和修复后复验。
- [pending] `ai-evidence-bundle`：实现外部 AI 调查包导出、结构化分析结果导入和不可信日志数据边界。
- [completed] `standalone-analyzer-gui`：完成首版独立 Wails/Vue GUI，接通项目生命周期、检索与分页、Finding/指标/App 日志/payload/保存查询/既有报告导出及安全 session 管理；案例、AI 包和复验入口保持明确未启用，归属后续独立切片。首版 macOS arm64 `.app` 已通过原生启动/退出 smoke、ad-hoc 签名、DMG/tar.gz 结构及 SHA-256 校验。
- [completed] `analyzer-default-log-autoload`：GUI 启动后由后端按稳定路径合同解析并异步自动加载客户端默认日志目录；目录不存在、无可分析日志或加载失败时预填默认路径并保留手动选择。打开事务支持取消和最新请求发布，关闭项目/应用不会等待大目录分析结束；不建立对客户端运行时的模块依赖。
- [pending] `client-analyzer-launcher`：在客户端日志采集区接入跨平台分析器检测、启动按钮和未安装引导。
- [pending] `distribution-verification`：建立分析器独立发布、客户端归档隔离及跨版本/跨模块/跨平台验证证据；补充真实大默认日志根的完成耗时门禁（当前 `1.7 GB` 数据集 14 分钟内未完成，已验证可取消但尚未通过全量性能验收）。

## 已完成支撑任务

- [completed] `analyzer-sqlite-workspace`：建立分析器临时 SQLite workspace、schema、权限与生命周期。
- [completed] `analyzer-stream-ingest`：将输入发现与 JSONL 解析改为批量流式导入。
- [completed] `analyzer-stream-analysis`：将分析规则改为 SQLite 游标驱动的有界 Go trace reducer。
- [completed] `analyzer-stream-reports`：将 JSON、HTML 和诊断 ZIP 改为数据库游标流式输出并接入 CLI。
- [completed] `analyzer-sqlite-verification`：完成兼容性、大规模内存、故障注入、跨平台构建与发布隔离验证。
  - 旧 `contract.Dataset.Events` / `load.Dataset` 全量生产链路已移除；CLI 已接入临时 SQLite workspace → 流式导入 → 有界 reducer → 流式 report staging/publish。
  - 已验证：`GOPROXY=off GOSUMDB=off go test ./...`、`go test -race ./...`、`go vet ./...`、CLI smoke、20,000 events / 1,000 traces under `GOMEMLIMIT=96MiB`、`task release:verify:analyzer-isolation`、`CGO_ENABLED=0` darwin/linux/windows 构建、`git diff --check`。
- [completed] `agents-init-locate`：定位并校验 AGENTS 全局母版与项目接入状态。
- [completed] `agents-init-interview`：收集并确认项目上下文、权威入口与覆盖规则。
- [completed] `agents-init-onboard`：生成或升级项目 AGENTS 接入文件并完成本机登记。
- [completed] `enhance-sqlite-plan`：依据项目规范增强 SQLite 内存优化计划。
- [completed] `analyzer-sqlite-design-gate`：将 SQLite 工作区与流式管线合同写入系统 Design，并完成 Design Gate。

## 当前范围约束

- 按 `semantic-design-contract → analysis-project-engine → semantic-capture-v2 → query/diagnostics → case/bundle → GUI/launcher → distribution` 的依赖顺序增量实施；每个切片保持可测试、可回退。
- 客户端只采集并通过受限启动器打开独立分析器；不读取历史日志、不分析、不生成报告。
- 第一版分析器不调用 AI、不修改仓库、不运行外部命令；只导出脱敏调查包并导入结构化分析结果。
- `basic` 不记录正文或项目路径；full payload 默认不索引、不复制、不导出。调查案例默认只保存脱敏证据快照。
- 分析器保持独立 module、独立进程和独立发布，不进入客户端二进制或更新归档。
- 未经用户明确要求，不使用 subagent，不提交、不推送。