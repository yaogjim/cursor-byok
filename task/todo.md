# 活动任务

> 本文件是项目活动任务的唯一真值源。`.cursor/plans/*.plan.md` 的 frontmatter todo 仅作阶段索引，不代表任务已满足 Definition of Ready。

## Active Work Package

WORK_PACKAGE_ID: multi-client-acp-phase5-20260826
STATUS: blocked
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: blocked（本机仅发现 ACP Agent 服务端，缺少可端到端验收的 ACP Client/编辑器）
DELIVERY_STATUS: blocked

### CONTEXT

- 阶段 2 Chat tools 已通过 OpenCode 1.2.25 隔离真实工具循环；阶段 3 `/v1/responses` 已通过 Codex 0.144.4 隔离真实 `exec_command` 工具循环；阶段 4 独立生命周期与 metadata-only 入站观测已实现并自动验证。
- 本机可用的 `agent acp`/`opencode acp` 是 ACP Agent 服务端，不能冒充 ACP Client；未读取用户凭据或 prompt，也没有可验证 initialize/session/prompt/cancel/update 的真实编辑器。
- 在真实 ACP Client 到位前，禁止声称 ACP v1 完成，禁止把服务端自测当成端到端验收，且不提前抽取共享 Agent Core。

### ACTIVE SLICES

- [blocked] `gateway-acp-v1`：等待一个真实 ACP Client/编辑器及其允许的隔离验收环境。

### UNBLOCK CONDITION

- 提供能连接本地 stdio bridge 的真实 ACP Client/编辑器，或明确授权并提供其版本、启动方式和临时 HOME/workspace 验收边界。届时先作 metadata-only 探针，再冻结协议与权限合同。

## Previous Work Package: 双集成导航与数据概览

WORK_PACKAGE_ID: dual-nav-overview-20260826
STATUS: completed（代码与决策/架构文档已落地；Wails 视觉验收未做）
RISK_LEVEL: medium
OWNER: orchestrator
DESIGN_READINESS: approved（`.cursor/plans/双集成导航与数据概览改造计划_2622a26e.plan.md` + 工作决策基线 §7.4/§10.13 + 系统 Design §4.1/§10.1/§14.15）
DELIVERY_STATUS: verified-partial（实现已对照源码落入五页导航、section 保存与 daily 报告；本轮只改文档，未重跑 Go/前端命令，也未做 Wails 窗口视觉点击）

### CONTEXT

- 已批准方案二：主窗口改为「数据概览 / Cursor 集成 / 网关集成 / 上游模型 / 系统设置」五页同层导航。Cursor 与 Gateway 并列，不把 Gateway 做成根节点。
- 同时修复 token 复制、启用/启动语义、Gateway 保存范围，以及首页统计口径；趋势和日历热力图只使用 `usage.json` 的 `daily[]`。
- 不修改 Cursor 18080/18090、MITM、工具桥；不新增远程监听或 Gateway 协议；不用 `recent_events` 伪造完整热力图。

### ACTIVE SLICES

- [completed] `rebuild-dual-navigation`：主窗口 `1100×720`（最小 `980×640`）、侧栏、五条路由与旧 `/config`→`/settings`、`/model-config`→`/models` 重定向。
- [completed] `build-five-pages`：数据概览、Cursor 集成、网关集成、上游模型、系统设置搬入主窗口；模型配置不再独立开窗。
- [completed] `scope-config-persistence`：`SaveCursorConfig` / `SaveGatewayConfig` / `SaveModelAdapters` / `SaveSystemSettings` 在存储锁内合并对应 section；前端每页 dirty，离开未保存页需确认。
- [completed] `fix-gateway-workflows`：复制 token 走 `copy-text-to-clipboard` 并区分未生成/WebView 拒绝/成功；「配置为启用」与「启动/停止 Gateway」分离；dirty 时禁止静默启动。
- [completed] `build-overview-analytics`：`GetHomeMetricsReport` 按 `7d`/`30d`/`all` 过滤 `daily[]` 并聚合 KPI；趋势图与日历热力图时区 UTC；无小时热力图、无自定义/近 1 小时/今日范围。
- [completed] `document-sync`：已把菜单层级、保存范围、运行隔离和 daily 报告合同写入工作决策基线、系统 Design、本文件与 `docs/process.md`。
- [pending-evidence] `wails-visual-verify`：五页导航、窗口尺寸、复制 token、无 token、WebView 剪贴板失败、启用但未运行、独立启停、趋势范围和窄窗口布局的 Wails 手工点击。未执行，不得标完成。

### ACCEPTANCE AND ROLLBACK

- 五页可切换；保存一页不得覆盖另一页最新字段或 Gateway token。
- Gateway 启停只影响 18091；失败不阻止 Cursor。概览 KPI 与图表使用同一 `range`，重置只清零 usage 聚合。
- 本包文档收口不声称 Go/前端命令已在本轮重跑，也不声称 Wails 视觉验证已完成。
- 回滚导航不得恢复整文件 `persistUserConfig` 作为五页保存路径。

## Previous Work Package: Responses API 与 Codex

WORK_PACKAGE_ID: multi-client-responses-gateway-phase3-20260826
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（已批准计划阶段 3 + Codex 0.144.4 本机 metadata-only 校准 + 工作决策基线 §10.12 + 系统 Design §14.13）
DELIVERY_STATUS: verified（自动矩阵与隔离 Codex 真实工具循环通过；Provider 为内存 fixture，未连接外部 API）

### CONTEXT

- 本机 Codex `0.144.4` 自定义 Provider 使用 `wire_api="responses"` 和 HTTP SSE，要求 `response.completed`；普通 function 的完整 `response.output_item.done` 是工具循环关键事件。
- Codex 同时携带 `namespace`/`web_search`/`tool_search` 本地能力描述与可执行 function；Gateway 跳过前者，只转发 function，绝不展平或执行任何客户端工具。
- 实现覆盖 `instructions`、typed text input、function call/output、reasoning replay、usage、created/text/tool/completed/failed SSE 生命周期和不支持参数 4xx。

### ACTIVE SLICES

- [completed] `gateway-phase3-design`：冻结实际 Codex 子集与 namespace/web_search 跳过边界。
- [completed] `gateway-phase3-code`：实现 `/v1/responses` typed input、工具和 SSE 终态。
- [completed] `gateway-phase3-verify`：隔离 `CODEX_HOME` + 临时 Gateway + 内存 Provider fixture 完成 Codex `exec_command` read 工具循环；Gateway 不执行工具。

### FINAL VERIFICATION EVIDENCE

- Responses：`go test ./internal/gateway -run '^TestCodex' -count=1 -v -timeout=120s` 与工具循环 `-race` 通过；Codex 0.144.4 在隔离 `CODEX_HOME` 中先以 read-only 自己执行 `exec_command` 读取临时文件，再以 workspace-write 自己创建临时文件，两轮均回传 tool output 并收到唯一 `response.completed`。
- 生命周期/观测：`go test ./internal/gateway ./internal/client ./internal/backend/server/config -count=1` 通过；独立 Start/Stop 测试确认 backend/MITM/Cursor 设置均未运行。ProcessSink 测试确认只落 `client_protocol`、`public_model_id`、method/path/status/字节数且不含 token/正文。
- 根模块：`go test ./internal/... -count=1 -timeout=300s` 通过；组合 `go test -race ./internal/gateway ./internal/backend/agent/model ./internal/backend/server/config ./internal/client ./internal/backend -count=1` 通过；相关 `go vet` 通过。
- 前端：`node frontend/scripts/test-config-projection.mjs` 与 `npm run build --prefix frontend` 通过（122 modules）；仅有既有 localstorage/chunk warning。
- 分析器：`tools/log-analyzer` 全模块 `go test ./...` 与 `go vet ./...` 通过。
- 最终门禁：`gofmt`、`git diff --check` 通过；`proto/`、`internal/mitm/`、`internal/certs/` 无 diff。分支 `gateway`、HEAD `eb1702a` 未变；未 commit、未 push。Cursor PID 2407 仍独占 127.0.0.1:18080/18090，最终无 18091 listener。

### ACCEPTANCE AND ROLLBACK

- `response.completed` 唯一，失败只发 `response.failed`；普通 function 的 call_id、item id、参数和 tool output 无损回放。
- 回滚只移除 `/v1/responses`，不得回退 Chat、配置、容量、独立生命周期或 Cursor 合同。

## Previous Work Package: 独立 Gateway 生命周期与观测

WORK_PACKAGE_ID: multi-client-gateway-runtime-phase4-20260826
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（批准计划阶段 4 + 系统 Design §14.14）
DELIVERY_STATUS: verified

### ACTIVE SLICES

- [completed] 独立 `StartGateway`/`StopGateway` API 和 Gateway card 控件只操作 `18091`；Cursor `StopProxy` 不再停止 Gateway，进程退出仍有界关闭。
- [completed] 独立 lifecycle 测试证明 Gateway 可在 backend/MITM/Cursor 设置均未运行时启停。
- [completed] metadata-only 入站 `request_started`/`request_finished` 观测与 analyzer allowlist；不记录 token、正文、工具参数或 Provider 凭据。

### ACCEPTANCE AND ROLLBACK

- 自动验证覆盖独立启停、Cursor 状态隔离、Chat/Responses、ProcessSink metadata 脱敏和 analyzer allowlist。
- 回滚恢复 Cursor StopProxy 对 Gateway 的停止耦合及移除独立入口；不触碰 `18080`/`18090`、MITM 或 Provider Router。

### POST-REVIEW CLOSEOUT（2026-08-26）

- [completed] `responses-mixed-output-order`：修复混合文本/工具输出的 SSE 与最终 `response.output` `output_index` 不一致；首个文本增量预留文本槽位，工具项使用后续索引，并新增混合顺序回归测试。
- [completed] `gateway-runtime-protection`：独立 Gateway 热更新先绑定新 listener，绑定失败保留旧 listener；配置导入在 Gateway 独立运行时拒绝；异常 listener 退出的错误投影到 Gateway 状态。
- [completed] `gateway-usage-projection`：Responses `total_tokens` 按输入与输出 token 计算，缓存 token 仅投影到 `input_tokens_details.cached_tokens`。
- 验证：定向 test/race/vet、根模块全量 `go test ./...` 与 `go vet ./...`、前端配置投影/build、`tools/log-analyzer` test/vet 均通过；protected `proto/`、`internal/mitm/`、`internal/certs/` 无 diff；现有 Cursor `18080`/`18090` listener 未受影响，未残留 `18091` listener。


WORK_PACKAGE_ID: multi-client-chat-gateway-phase2-20260826
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（已批准计划阶段 2 + OpenCode 1.2.25 本机 metadata-only 校准 + 工作决策基线 §10.11 + 系统 Design §14.12）
DELIVERY_STATUS: verified（实现、真实 OpenCode 隔离工具循环、共享容量、test/race/vet 通过；未连接真实外部 Provider）

### CONTEXT

- 本机 OpenCode `1.2.25` 使用 OpenAI-compatible `POST {baseURL}/chat/completions`，最小工具循环包含 `tools`、assistant `tool_calls`、`role=tool`、`tool_call_id`、`stream=true` 与 `stream_options.include_usage`。
- Gateway 只转发工具描述、模型 tool call 和客户端返回的 tool result；OpenCode 执行 read/shell/edit。禁止调用 Cursor 工具桥或在 Gateway 内执行工具。
- 复用现有 `ProviderRequest.Tools`、`Message.ToolCalls`、`Message.ToolCallID` 和 `ModelEvent.ToolInvocation`。任意 ModelEvent 已由 fallback router 标记为输出，禁止跨渠道切换；SSE 已写后错误只能在当前流内终结，禁止拼接另一个 Provider。
- 本阶段不加入多模态、reasoning 扩展、Responses、ACP、远程监听或客户端专属鉴权；不修改 proto、MITM、certs，不扰动 18080/18090。

### ACTIVE SLICES

- [completed] `gateway-phase2-recon`：核验 OpenCode 版本、请求形态、现有 OpenAI/Anthropic 工具事件与 fallback 安全窗口。
- [completed] `gateway-phase2-design`：冻结 tools/tool_calls/tool result、多调用 ID、`finish_reason=tool_calls` 和流式终态合同。
- [completed] `gateway-phase2-code`：实现 Chat 工具请求、历史回放、非流/流式 tool calls；Gateway 不执行工具。
- [completed] `gateway-phase2-verify`：OpenCode 1.2.25 通过隔离 18091 完成真实 read 工具循环；共享容量 fixture 峰值为 1；Gateway test/race/vet/diff 通过。

### ACCEPTANCE AND ROLLBACK

- 单/多工具 ID、参数 JSON、assistant tool_calls 与 tool result 必须无损关联；Anthropic `tool_use` 对外规范化为 OpenAI `tool_calls`。
- 非流响应返回完整 `message.tool_calls` 和 `finish_reason=tool_calls`；SSE 返回可累加的 `delta.tool_calls`、唯一终态和 `[DONE]`。
- 任意模型事件或 SSE 输出后 provider 错误不得跨渠道拼流；Gateway 工具执行次数必须恒为 0，`lastAgentModelHash` 不变。
- 回滚恢复阶段 1 对 tools 的 400，并保持文本 Chat、配置和 Cursor 合同不变。

## Previous Work Package: Multi-Client Chat Gateway 阶段 0/1

WORK_PACKAGE_ID: multi-client-chat-gateway-phase0-1-20260825
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（工作决策基线 §10.10 + 系统 Design §14.11 + `.cursor/plans/placeholder_6b5e8b5a.plan.md`）
DELIVERY_STATUS: verified-partial（实现与自动门禁通过；真实 OpenAI SDK/Cherry 一轮聊天、Wails 视觉验收和 Cursor 全量回归仍是证据缺口）

### CONTEXT

- 已批准计划改为纵向切片：先复用现有 Router 交付最小 Chat Gateway，再补工具、Responses、独立生命周期；ACP 另设后续设计门。Cursor 始终是不可回归的最高优先级。
- 阶段 0/1 范围：冻结最小合同；独立 `127.0.0.1:18091`、默认关闭、loopback-only、Bearer；`GET /v1/models` 与纯文本 `POST /v1/chat/completions`；复用 DefaultProviderGateway/Router/fallback/retry/capacity；禁止复制 limiter、禁止写 `lastAgentModelHash`；最小 gateway 配置与 token 红线；config/temp `0600` 与首次 schema 备份；Gateway 生命周期随当前服务但失败隔离；最小 UI 卡片。
- 不触碰 proto、MITM、certs；不扰动真实 18080/18090；不做阶段 2+。

### ACTIVE SLICES

- [completed] `gateway-design-baseline`：工作决策基线 §10.10、系统 Design §14.11 与本工作包已冻结合同、阶段边界、回滚、导入 token overlay 和 Cursor 承重 characterization 范围。
- [completed] `gateway-chat-mvp`：独立 `internal/gateway` HTTP server、配置/token/0600、生命周期隔离、最小 UI 与计划自动验收测试。
- [pending] `gateway-chat-tools`：阶段 2，不在本包。
- [pending] `gateway-responses`：阶段 3，不在本包。
- [pending] `gateway-independent-runtime`：阶段 4，不在本包。
- [pending] `gateway-acp-v1`：阶段 5，不在本包。

### FINAL VERIFICATION EVIDENCE

- 定向：`go test ./internal/backend/server/config ./internal/gateway ./internal/client ./internal/backend ./internal/backend/server/upstream -count=1 -timeout=180s` 通过；覆盖 401/403 loopback、404 别名、模型列表无 token/API Key/Base URL/内部 hash、非流聚合与 SSE、tools/multimodal/reasoning 400、stale mapping 400、取消、fallback 前置失败不写 hash、0600+`.bak-pre-gateway`、旧 struct 再保存丢字段、普通保存/剥离导出后再导入 overlay token、显式 YAML token 才替换、Gateway 启动失败不写 Cursor lastError。
- 根相关：`go test ./internal/... -count=1 -timeout=300s` 通过；`go test -race` 对上述五包通过；`go vet ./internal/...` 通过。仅有既有 macOS deployment target linker warning。
- 前端：`node frontend/scripts/test-config-projection.mjs` 通过；`npm run build --prefix frontend` 通过（122 modules）。Gateway 文案已同步四语种 locale 与生成的 `frontend/src/i18n/generated/catalog.json`；构建产生的 `dist/` 未纳入 Git。仅有既有 `--localstorage-file` 与 chunk-size warning。
- 保护路径：`git diff --check` 通过；`proto/`、`internal/mitm/`、`internal/certs/` 无 diff。最终收口复核确认分支仍为 `gateway`、HEAD 仍为 `eb1702a`，未 commit、未 push，未扰动运行中 18080/18090。
- 真实缺口：未跑真实 OpenAI SDK / Cherry Studio 一轮聊天；本机无 `wails3`/`task`，未做 Wails 窗口视觉点击；未做 Cursor 全量回归。阶段 1 已完成隔离 TCP listener smoke，但其 Provider 是内存 fixture，不替代真实上游客户端验收。故不得标 `accepted`。

### ACCEPTANCE AND ROLLBACK

- 自动：401/404、模型列表无敏感字段、流/非流文本、取消、fallback 前置失败、不写 hash、token 不进 JSON/导出/localStorage/日志、默认导出后再导入 overlay 现有 token、0600+备份、相关 test/race/vet、前端投影/build、`git diff --check`；proto/MITM/certs 无非预期 diff。
- 真实：OpenAI SDK 或 Cherry 一轮聊天、Cursor 全量回归。本包缺少真实客户端/Wails 视觉证据，只能 `verified-partial`。
- 回滚：`gateway.enabled=false`；保留 `.bak-pre-gateway` 以防旧版本保存丢字段。

## Previous Work Package: Fallback 覆盖优先预算

WORK_PACKAGE_ID: fallback-coverage-first-budget-20260825
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（用户确认“保证渠道覆盖，再用剩余预算重试”；工作决策基线 §10.5 + 系统 Design §14.9.4）
DELIVERY_STATUS: accepted（实现、定向测试、race、vet、前端投影检查和 diff 门禁通过；已纳入 0.0.49.6 提交）

### CONTEXT

- 运行证据 `f4c6e4d2-5bd4-4265-98ec-0aa9bfa99b2b` 证明旧分配把默认 5 attempts 贪婪分成 `3+2+0`：前两个 transport 渠道耗尽预算，第三个已配置且可用渠道从未发送 HTTP。
- 用户已确认根因级修复：启用 fallback 时先为每个后续兼容渠道预留 1 次 attempt，再把剩余预算用于前序渠道重试；全链 attempt/wait、单渠道最多 3、输出/副作用/context/错误 allowlist 和兼容性门禁保持不变。
- 最多 5 个物理渠道；默认 5 attempts 必须形成全覆盖，9 attempts 对 5 渠道形成 `3+3+1+1+1`。若显式预算小于渠道数，则按配置顺序各 1 次直到预算耗尽，不突破共享预算。
- 不修改真实 BYOK 配置、公共 proto、MITM/CA/证书、系统代理、18080/18090、前端交互和发布版本；本工作包已按用户要求提交并推送，发布构建另行生成且不上传 GitHub。

### ACTIVE SLICES

- [completed] `coverage-contract`：工作决策基线与系统 Design 已替换旧 `min(remaining,3)`/`3+2+0` 合同，冻结覆盖优先算法、预算不足边界和验收矩阵。
- [completed] `coverage-tdd`：真实 HTTP fixture 覆盖三渠道、五渠道、transport、503、预算 5/9/2 以及不兼容候选预留计算。
- [completed] `coverage-runtime`：router 按后续兼容渠道数预留 attempt，当前渠道只使用剩余额度且继续受单渠道 3 次上限与共享预算约束。
- [in_progress] `coverage-verification-closeout`：运行相关/全量测试、race/vet、diff/lint/review，随后同步证据和交付状态。

### ACCEPTANCE AND ROLLBACK

- 前两个 transport 或 503 渠道失败时，第三个兼容健康渠道必须收到 HTTP 并可成功终止；五渠道默认预算必须到达第五渠道。
- 任意配置下总 HTTP attempt 不超过共享预算，单渠道不超过 3；等待预算、安全门禁、兼容性与同上游组跳过语义不放宽。
- 回滚仅需恢复旧分配器与对应测试/文档；配置 schema 和持久化数据无迁移。

## Previous Work Package: Fallback 配置交互

WORK_PACKAGE_ID: fallback-config-interaction-20260825
STATUS: completed
RISK_LEVEL: medium
OWNER: orchestrator
DESIGN_READINESS: approved（工作决策基线 §10.5 + 系统 Design §14.9.4/14.9.5）
DELIVERY_STATUS: verified-partial（实现、定向/全量自动化和生产构建通过；本机缺少 Wails v3/task，未执行真实窗口视觉交互）

### CONTEXT

- 用户确认：“测试全部”静默跳过逻辑 alias；单独测试逻辑 alias 保留实际运行验证提示。
- fallback 链上限调整为总共 5 个物理渠道，即 1 个 primary + 1–4 个 candidates；默认全链预算继续为 5 attempts / 8 seconds，范围继续为 2–9 / 1–30，渠道增多不扩大全链预算。
- 修复通用 Select 长列表的视口高度和滚动容器错位，使鼠标与键盘都能访问全部可用物理 adapter。
- 不修改公共 proto、MITM/CA/证书、系统代理、18080/18090、真实 BYOK 配置、发布版本；本工作包此前已提交，当前发布版本为 0.0.49.6，并按 2026-08-25 用户最新要求随 `noad` 分支发布到 GitHub。

### ACTIVE SLICES

- [completed] `fallback-contract-tests`：需求/设计合同已补充 5 通道、批量/单独测试语义和预算边界；前端投影、后端校验与 resolver 顺序回归已添加。
- [completed] `fallback-ui-bugs`：批量测试移除逻辑 alias 保存提示；Select 将 Floating UI 可用高度施加到真实 `ul` 滚动容器，并保留键盘聚焦滚动。
- [completed] `fallback-five-channels`：前端候选槽改为 4 槽数据驱动，后端权威上限改为 4 candidates；清空中间槽截断后续，候选排重和逻辑 alias 排除保持。
- [completed] `fallback-verify-closeout`：前端投影、后端定向、`internal/...` 全量、生产构建、lint/diff 门禁通过；本机缺少 Wails v3/task，未扰动运行中 18080/18090，视觉交互记录为环境缺口。

### FINAL VERIFICATION EVIDENCE

- `node frontend/scripts/test-config-projection.mjs`：通过；覆盖 4 candidates 保存/顺序、第 5 candidate 拒绝、批量静默跳过、单独提示保留和 Select 真实滚动容器合同。
- `go test ./internal/backend/server/config ./internal/client ./internal/backend/agent/model -count=1`：三包通过；仅有既有 macOS deployment target linker warning。
- `go test ./internal/... -count=1`：全部 internal package 通过；同样仅有既有 linker warning。
- `npm run build --prefix frontend`：Vite production build 通过（121 modules）；仅有既有 `--localstorage-file` 与 chunk-size warning。构建自动改写的 i18n 生成文件已恢复，未纳入本包。
- `ReadLints`：受改源文件无诊断；`git diff --check` 与 gofmt 检查在收口时通过。
- 浏览器边界：本机无 `wails3` 与 `task`，且没有可用浏览器 tab/开发 UI 端口；为避免占用运行中 Cursor 的 18080/18090，本轮不启动替代服务，视觉交互保持 `env/test gap`。

### ACCEPTANCE AND ROLLBACK

- 1 primary + 4 candidates 可保存并按序投影，第 5 candidate 被前后端拒绝；默认预算与运行算法不变。
- “测试全部”只调度物理 adapter 且不弹逻辑 alias 保存提示；单独逻辑 alias 测试仍提示运行验证。
- 长下拉可滚动并选择末项；浏览器不可用时必须记录环境缺口，不用源码断言冒充视觉证据。
- 回退到旧版前须先把新配置缩回最多 2 candidates；否则旧版严格校验会拒绝配置。

## Previous Work Package: 配置写竞态与物理上游容量

WORK_PACKAGE_ID: config-race-upstream-capacity-20260825
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（精简三写语义 + 物理上游共享槽）
DELIVERY_STATUS: accepted（能力实现、受控非零 fixture、全量门禁和独立复审通过；当前真实 `grok-HA` 按用户决定保持不限流，在线容量保护未启用）

### CONTEXT

- 用户已批准 `.cursor/plans/配置竞态与上游容量风险精简计划_ff017475.plan.md`，目标是关闭普通 UI 保存与运行时 `lastAgentModelHash` 更新互相覆盖的进程内竞态，并提供默认关闭的物理上游共享容量限制。
- 三种配置写语义固定为：普通保存保留最新 hash；hash-only 更新只 patch 该字段且相同值 no-op；完整导入显式全量替换并允许替换 hash。
- `maxConcurrentRequests` 只挂物理 adapter：缺失/0 不限流，1–16 有效；逻辑 alias 必须为 0，同 provider + 规范化 Base URL + API Key 的物理 adapter 必须值一致。
- 用户已确认本轮发布版本使用 `0.0.49.6`；`build/config.yml`、Wails 平台元数据、`release-notes.md` 和 `releaselog/0.0.49.6.md` 必须保持一致。2026-08-25 最新要求为：合并 `cli` 到 `noad`，更新 README 的“当前稳定发布”，推送 `noad`，并将构建资产发布到 GitHub；该要求取代此前仅生成本地资产的安排。
- 不修改真实 `~/.cursor-local-assistant-v2/config.yaml`，不占用或重启 18080/18090；不修改公共 proto、MITM/CA/证书、系统代理或发布隔离。

### COMPLETED SLICES

- [completed] `config-write-transaction`：Store 锁内读取最新配置并分别实现 `SaveUserConfig`、`SaveLastAgentModelHash` 和完整 `Save`；Manager 写锁串行提交 disk/current/snapshot，hash-only 不通知 listener。Host/客户端普通保存与 import replace 已分流；确定性交错、并发 race、失败回滚、no-op 和完整替换测试已覆盖。
- [completed] `capacity-schema-ui`：后端 schema、权威校验、resolver/runtime、前端状态/投影、ModelEditor 和四语种文案已接入 `maxConcurrentRequests`；逻辑 alias 非零、同组不一致和越界均拒绝，默认/边界/复制/roundtrip 已覆盖。
- [completed] `provider-capacity-runtime`：进程级 limiter 按只在内存存在的 SHA-256 上游组共享槽，固定最多等待 2 秒，槽覆盖完整 Stream 和同渠道 retry；typed `capacity_unavailable` 只在零输出安全窗口切不同组，同组跳过，取消及时退出且 release 幂等。
- [completed] `orchestrator-integration-review`：主控发现并修复 resolver 容量未覆盖到实际 `StreamRequest` 的跨切片 P1，同时补齐 Manager legacy snapshot 投影、runtime/前端上游组规范化和承重回归。独立复审确认无剩余 P0/P1。
- [completed] `verification-closeout`：根 module、前端、CLI 独立 module、日志分析器、保护路径和 diff 门禁均通过；工作决策基线 §10.9 与系统 Design §14.10 已冻结合同。

### FINAL VERIFICATION EVIDENCE

- 定向后端：`go test ./internal/backend/agent/model ./internal/backend/server/config ./internal/runtime ./internal/client ./internal/backend -count=1 -timeout=120s` 通过；容量专项覆盖配置投影、limit=0、同组峰值、异组隔离、retry 持槽、2 秒 timeout fallback、同组跳过、取消无泄漏和 attempt/wait 预算零消耗。
- 根 module：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...` 全部通过。
- 前端：`node scripts/test-config-projection.mjs` 和 `npm run build` 通过；仅有既有 chunk-size 与 Node localstorage 参数 warning。
- CLI module：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`go build ./...` 通过；日志分析器 module 的 test/race/vet 通过。
- macOS 客户端：在 Apple Silicon / macOS 14.6.1 的既有历史验证曾生成版本 `0.0.49.5` 的 `bin/macos-arm64.dmg`（约 23 MiB，SHA-256 `8ad056b49386841ba2cd1a8a3cff7ae3489948f38e9be47373da693090d93841`）；`hdiutil verify`、DMG 只读挂载、Info.plist/buildinfo 版本、Mach-O arm64 检查及 app adhoc `codesign --verify --deep --strict` 均通过。产物受 `bin/` ignore 规则保护，不纳入 Git；未执行 Developer ID 签名或 notarization。
- 最终检查：`git diff --check` 通过；`proto/`、`internal/mitm/`、`internal/certs/` 无 diff；本工作包未执行真实 BYOK 配置写入，也未占用或重启运行中端口。

### ACCEPTANCE BOUNDARY AND ROLLBACK

- 本工作包可声明“配置竞态修复和可选容量能力已实现并经非零受控 fixture 验证”。当前真实 `grok-HA` 未设置 `maxConcurrentRequests`，仍为无限并发，不得声明在线容量风险已经启用关闭。
- 删除 `maxConcurrentRequests` 或设 0 可回滚 limiter；普通保存/hash patch/完整 replace 为持久化合同，回退时不得恢复整文件 hash RMW。
- 三层模型路由工作包的 Wails 视觉验收与 CLI 两模型 pre-output 切换证据缺口保持独立，不由本包测试替代。

## Previous Work Package: 三层模型路由与 CLI 模型池

WORK_PACKAGE_ID: three-layer-subagent-provider-cli-pool-20260824
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（双入口 probe + 独立两轮实现模拟通过）
DELIVERY_STATUS: verified-partial（实现、自动化门禁和独立终审通过；真实 grok-HA Provider fallback 与 CLI 三物理模型只读预检/Ask 已观测；CLI 两模型 pre-output 故障切换与 Wails 浏览器视觉验收仍是证据缺口）

### CONTEXT

- 用户已批准 `.cursor/plans/逻辑子代理、可配置_provider_fallback_与_cli_模型池计划_ff730446.plan.md`，要求在 `cli` 分支由主控协调 subagent 完整执行。
- 三层职责固定为：IDE 新 spawn 的软专用逻辑子代理模型；BYOK Provider 层安全 fallback；独立 Cursor CLI 物理模型池控制器。CLI 池不得引用启用 `providerFallback` 的逻辑 adapter，避免外层进程切换与内层渠道链预算乘法放大。
- Provider 全链 `maxHttpAttempts` 默认 5、可配置 2–9；`maxWaitSeconds` 默认 8、可配置 1–30；单渠道固定最多 3，链级预算跨渠道共享。
- 自动切换只允许在零 assistant、零 tool call、零 mutation 的窗口；真实 CLI probe 额外观察到 `thinking` 事件，当前设计采用保守门禁：任意 model-generated event（包括 thinking）均关闭自动切换窗口。
- 不修改公共 proto、MITM、CA、系统代理、18080/18090 监听；不把 CLI module 并入客户端配置、Wails 构建或发布归档；不提交、不推送、不发布。

### GOAL

- 让逻辑子代理模型通过可配置但安全的 Provider fallback 获得确定的全链 HTTP/等待预算、保存/回显 UI 和 metadata-only 链级观测。
- 新增独立 `tools/cursor-cli-model-pool` Go module，按有序物理模型各启动一次，并在任何模型输出、工具或 worktree mutation 后停止自动重放、转人工复核。
- 以真实 IDE metadata、真实 CLI NDJSON、httptest/fake-agent、前端浏览器与各 module 门禁分别证明三条链，不跨对象借证据。

### ACTIVE SLICES

- [completed] `three-layer-probe-ide`（owner: orchestrator + readonly explorer；size: S）：真实父会话模型 A 与新 Explore/generalPurpose child 模型 B 不同；父 `subagent_model_override_resolved.effective_model_id`、child `requested_model.model_id` 与 child `stream_state_updated.model_id` 一致。证据只保留匿名会话哈希和模型 ID metadata，未读取 prompt/header/凭据；18080/18090 PID 未变。
- [completed] `three-layer-probe-cli`（owner: orchestrator + readonly explorer；size: S）：核验 Agent CLI `2026.08.11-e8db854`；本机 18090 `agent models` 返回 21 个配置模型且均为未启用 fallback 的物理 adapter；受控 Ask probe 依次产生 `system/init → user → thinking* → assistant → result/success`，stdin prompt 未进入 argv，探针后工作树干净、healthz 正常、监听 PID 未变。
- [completed] `three-layer-design-gate`（owner: orchestrator + readonly reviewer；size: M；depends_on: two probes）：工作决策基线 §10.5/§10.7/§10.8 与系统 Design §14.9 已冻结配置、状态、错误、隐私、防叠加、回滚与验证合同。独立首轮模拟发现 wait=0 哨兵、模型 identity/secret、typed 错误、Retry-After、write argv/worktree、HTTP 500 和取消存在自由裁量；修订后第二轮确认无 P0/阻塞 P1，`Design Readiness=approved`。
- [completed] `provider-config-observability-red-green`（owner: provider-coding-subagent；size: M；depends_on: design gate）：配置默认/边界/roundtrip、resolver、真实 `httptest` HTTP 3+2/1+3+1、等待/Retry-After、安全门禁、同一 model-call 关联和 metadata 观测已按 RED→GREEN 落地；逻辑 adapter 不能嵌套成为 fallback 链成员。
- [completed] `provider-ui-save-red-green`（owner: frontend-coding-subagent；size: M；depends_on: design gate）：已修复“剥离派生 id 后再校验”的悬空引用根因，接入预算 UI、物理渠道筛选、逻辑路由标记、alias 禁止直接测试、风险提示和多语言文案。Node 投影测试与生产构建通过；Wails 浏览器视觉验收受本机缺少 Wails v3 CLI 阻塞。
- [completed] `cli-pool-red-green`（owner: cli-coding-subagent；size: M；depends_on: design gate）：独立 module 以 fake agent 覆盖 preflight、stdin/argv、NDJSON 状态机、typed 错误分类、进程组取消、Cursor 真实 worktree 路径与 sticky mutation 监视、metadata-only journal、物理模型限制和发布隔离；首轮 P0 worktree 路径问题及终审 P1 mutation/status 问题均已修复。
- [completed-partial] `three-layer-runtime-closeout`（owner: orchestrator；size: M；depends_on: all implementations）：根 module 全量 test/race/vet、Provider 定向门禁、前端投影/生产构建、CLI module test/race/vet/build、分析器 test/race/vet、发布隔离、敏感路径与 diff 检查均通过；两路独立终审无剩余 P0/P1。2026-08-25 用户本机补证：逻辑路由 `grok-HA`（`543fe17c50d81660`）primary=`Grokeeror`/`d5ab6805830e5baa`，candidates=`grok-hongai`/`3d2b0ff4a6be3e42` 然后 `grok`/`378723ba5535e672`，预算 5/8s；traces 共 54 条 `provider_fallback_attempt`、28 个 `model_call_id`，前期 13 次 transport→hongai 成功（3+1），后期 13 次 hongai `server_5xx` 在 remain=0 时 `attempt_budget_exhausted`，第三候选 `grok` 因 3+2 预算未启动。CLI 临时 HOME 对 `grok-hongai`/`grok-aigo`/`gpt-honga` 的 `validate`/`dry-run` 通过，引用 `grok-HA` 被拒绝；仓库 cwd 的 Ask `run` 首模型 `3d2b0ff4a6be3e42` 成功故未切后续模型；`/tmp` cwd 因 Workspace Trust 得到 `unknown` 并 fail-closed。18080/18090 仍为 Cursor PID 96155。副作用：运行中 18090 把 `lastAgentModelHash` 从 `543fe17c50d81660` 写成 `3d2b0ff4a6be3e42`，adapter/fallback 链未改。Wails 浏览器视觉验收和 CLI 两模型 pre-output 故障切换仍未闭合，交付保持 `verified-partial`。

### ACCEPTANCE

- Provider：配置 2–9 严格控制同一 `model_call_id` 的真实 HTTP 总 attempt；单渠道不超过 3；等待不超过配置；任意 raw byte/model event/副作用后候选调用为 0；整链唯一业务终态。
- CLI：有序物理模型各最多启动一次；只在 `preflight/launching/pre_output` 且无 model-generated event/tool/mutation 时切换；write 强制独立 worktree；prompt、NDJSON、工具参数和凭据不入 journal。
- IDE/UI：新 spawn child 使用逻辑模型覆盖；配置可保存、导入导出和回显；逻辑 alias 不直接发送测试请求；费用、隐私、模型语义和工具兼容风险可见。
- 根 module、`tools/cursor-cli-model-pool`、`tools/log-analyzer` 分别通过适用 test/race/vet；前端投影测试和生产构建通过；浏览器视觉验收因缺少 Wails runtime 记录为证据缺口；`git diff --check` 和敏感信息/发布隔离/禁止路径检查通过。

### FINAL VERIFICATION EVIDENCE

- 根 module：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...` 通过。
- Provider 定向：`go test ./internal/backend/server/config ./internal/backend/agent/model ./internal/runtime -count=1`、同范围 race 与 vet 通过；覆盖 3+2、1+3+1、2/7/9 封顶、等待预算归零、超预算 `Retry-After`、HTTP 500 不切换、取消、raw bytes/model event 和跨 Provider 兼容门禁。
- 前端：`node scripts/test-config-projection.mjs` 与 `npm run build` 通过；逻辑链成员拒绝、先校验后剥 ID、预算边界和 alias 跳过直接测试有自动化回归。独立 Vite 浏览器只能加载空壳：`/wails/runtime` 404，且本机 `wails3` 不可用，所以视觉验收未通过、不是代码失败证据。
- CLI module：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`go build` 通过；假 agent/fixture 的 `validate`/`dry-run` 以及 2026-08-25 真实三物理模型 `validate`/`dry-run` 通过。真实 Ask `run` 在仓库 cwd 由首模型成功结束；未写入真实 `cli-model-pool.yaml`/journal。adapter 身份未变，但 18090 将 `lastAgentModelHash` 更新为本次 Ask 使用的 `3d2b0ff4a6be3e42`。
- 日志分析器：独立 module test/race/vet 通过；macOS 链接器仅报告既有 deployment target warning。
- 发布与安全：根 `go list ./...` 不包含 CLI module；Taskfile 的 release prepare/verify 路径均调用 analyzer/CLI-pool 归档隔离检查；`proto/`、`internal/mitm/`、`internal/certs/`、`cursor-cli-docker/`、`bin/release/` 无本工作包 diff；`git diff --check` 通过。
- 独立终审：Provider/frontend 与 CLI/security 两路复审在返工后均确认无剩余 P0/P1。残余 P2 包括 fallback 失败工件后缀/逻辑 alias 输出上限的既有语义、fsnotify 祖先目录事件可能导致额外 fail-closed、真实 `agent models` 输出 fixture 与 root identity golden 尚未固定；均不放宽安全窗口或重试预算。

### ROLLBACK

- Provider：关闭 `providerFallback.enabled` 恢复单渠道 P0；删除新预算字段回到默认 5 attempts/8 秒。配置现在拒绝逻辑 alias 嵌套在渠道链内。
- CLI：停止使用或删除 `~/.cursor-local-assistant-v2/cli-model-pool.yaml` 即可；独立二进制/module 不进入客户端发布包，不修改 18090、IDE 配置或真实 BYOK `config.yaml`。
- 前端：预算/UI/保存修复只投影现有配置 schema；回退相关前端文件不会迁移或重写已有配置。

### STOP CONDITIONS

- 需要修改公共 proto、MITM/CA/证书/系统代理、18080/18090 监听，或替换运行中唯一 Cursor 进程。
- 无法可靠区分 CLI pre-output 与 model/tool/mutation，或无法终止整个子进程组。
- 需要把 fallback-enabled 逻辑 adapter 放入 CLI 模型池，或发生完整 Agent runs × Provider fallback chain 的预算乘法。
- subagent LLM 暂时失败时按 20/40/80/160/320 秒重试；连续五次仍失败则记录 agent ID、错误、diff/测试证据并停止等待用户。

## Previous Work Package: Agent 治理补全

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