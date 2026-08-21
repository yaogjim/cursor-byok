# 活动任务

> 本文件是项目活动任务的唯一真值源。`.cursor/plans/*.plan.md` 的 frontmatter todo 仅作阶段索引，不代表任务已满足 Definition of Ready。

## Active Work Package

WORK_PACKAGE_ID: provider-disconnect-recovery-20260821
STATUS: in_progress
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved (P0)
DELIVERY_STATUS: implemented (P0)

### CONTEXT

- 用户已确认按 P0/P1/P2 优先级实施 Provider 断连改进，并授权主控使用 subagent 执行编码与验证。
- 重点 trace `20260820T104843.414858000Z-5f93b537521a` 重新解析 241,852 条事件：21 个 `provider_stream_finished=error` 全部先出现 `llm_summary succeeded`；4 个失败调用已有 chunk，3 个存在 tool-call 关联元数据。
- 已确认根因之一：basic artifact 使用 `payload_summary`，归一化只读取 `payload`，导致失败摘要被默认投影为成功。
- 当前 HTTP retry 包围 `client.Do` 与 status；OpenAI/Anthropic 已校验 completion marker，但 2xx 后首个 model event 前的截断未进入请求重试。
- 设计锚点：`docs/prd_cursor_byok_工作决策基线.md#62-agent工具链与-provider-断连`、`docs/prd_cursor_byok_系统架构与核心业务数据流.md#145-design-provider-disconnect-001provider-断连终态与安全恢复`。

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

- branch/head：`noad` / `eed4ca6898a3a6a5222ab188c72b209f1534175c`。
- 开工时工作区干净；本工作包首先产生的修改仅为已确认的 PRD/Design/Todo 同步。
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
- status: pending
- owner: orchestrator
- size: M
- depends_on: provider-p0-integration-verification
- scope: root/parent conversation、parent tool、subagent task/run、model call、attempt 的可选关联字段与入口接线；不含原子结果提交。
- acceptance: 父子调用可从入口关联到 terminal，缺失字段明确为 unknown。

#### subagent-atomic-result

- priority: P1
- status: blocked
- owner: orchestrator
- size: L，必须继续拆分
- depends_on: provider-parent-correlation
- block_reason: 需要独立实施级 Design 固化 child checkpoint/result、parent tool-result、事务/CAS、恢复和兼容合同。

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