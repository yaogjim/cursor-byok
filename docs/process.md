# 项目实施进展

> 本文档记录项目当前状态、待完成事项、版本完成情况和时间线，是面向维护者的进展摘要。
> 详细实施阶段、逐步要求、路径范围和验收标准以 `[task/todo.md](../task/todo.md)` 为准。
> 每个阶段通过验收后，必须在同一次收尾中更新 `task/todo.md` 的执行结果和本文档的进展归档；没有验证证据的事项不得标为完成。
> 当前状态依据：仓库、Git 历史、项目计划及 2026-08-22 / 2026-08-24 / 2026-08-25 会话运行记录。

## 一、待完成的内容

### 0.1 198 远程 CLI 使用本机 18090（方案 A）

已完成（2026-08-24）。用户确认远端 `cursor-cli` 容器用 Docker `--network host` 消费本机 `127.0.0.1:18090`。本机用公钥登录 `jandar@172.16.23.198`，LaunchAgent `com.yaogj.cursor198-18090-tunnel` 维持 `ssh -N -R 127.0.0.1:18090:127.0.0.1:18090`；未开本机远程登录，未改绑 18080/18090。验证：远端与容器内 `/healthz` 均为 `ok`；`agent models` 与本机 `CURSOR_API_ENDPOINT=http://127.0.0.1:18090` 的 21 个模型 ID 完全一致；本机 `127.0.0.1:18080`/`18090` 仍由原 Cursor 进程监听。未转发 18080。Mac 休眠后隧道会断，唤醒后 LaunchAgent KeepAlive 会重连。逐步操作、非官方 `auth.json` 会话复用、安装与代理细节见 [`docs/ops_198_cursor_cli_session_reuse.md`](ops_198_cursor_cli_session_reuse.md)。

### 0.2 198 浏览器终端（WeTTY + 宿主机 nginx HTTPS/Basic）

已完成（2026-08-24）。ttyd 交互差，已换成 [WeTTY](https://github.com/butlerx/wetty)（xterm.js）。构建上下文在仓库 `cursor-cli-docker/`，运行镜像 `cursor-cli-runtime:wetty`。容器 `--network host`，WeTTY 以 root 听 `127.0.0.1:7681`，PTY 经 `cursor-cli-shell` 降权为 `bun` 的 `/bin/sh`。宿主机 nginx 443：自签 TLS + HTTP Basic，反代 loopback 7681；隐藏 WeTTY 误发的 `ws://` CSP 并允许 `wss:`（否则 WebSocket 被拦，终端约 10 秒断开）；`X-Frame-Options` 为 `SAMEORIGIN` 且 CSP 带 `frame-ancestors 'self'`（`DENY` 会拦 WeTTY 同源 iframe `/assets/xterm_config/index.html`，刷新后抛 `SecurityError`）。验证：无凭据/错凭据 401，正确凭据 200；配置 iframe 200 且头为 `SAMEORIGIN`；经 nginx 的 socket.io 可维持 ≥20s 且出现 `$ ` 提示；`:80` 仍 200；本机 18080/18090 未改。口令与 `auth.json` 正文不入库。操作细节见同一 ops 手册 §13。

### 0.3 三层模型路由与 CLI 模型池（实现完成，证据部分闭合）

2026-08-24 已完成三层实现、自动化门禁和两路独立终审，当前 `delivery_status=verified-partial`，未提交、未推送、未发布。第一层真实 IDE metadata-only probe 证明新建 Explore/generalPurpose child 可使用不同于父会话的模型，并且 child `requested_model.model_id` 与 runtime ModelID 一致；这只证明新 spawn 的模型传播，不证明运行中 child 热切换。

第二层 Provider fallback 已支持全链 `maxHttpAttempts` 默认 5/可配 2–9、`maxWaitSeconds` 默认 8/可配 1–30，单渠道固定最多 3。共享预算覆盖 `3+2`、`1+3+1`、wait=0、超预算 `Retry-After`、HTTP 500 只同渠道重试、取消、raw bytes/model event 后零切换和兼容性跳过。配置保存改为用后端派生 ID 的完整 adapter 集合先校验、再剥 ID 序列化；逻辑 adapter 不得嵌套成为另一条 fallback 链的物理成员。UI 增加预算、逻辑路由、费用/隐私/模型语义/工具兼容提示，逻辑 alias 不再直接发送 endpoint 测试请求。

第三层新增独立 `tools/cursor-cli-model-pool` Go module，按有序物理模型各启动一次。它用 `agent models` 与 BYOK 配置精确交叉核验并拒绝 fallback-enabled alias；prompt 只走 stdin；未知错误 fail closed；任意 thinking/assistant/tool/未知事件或 Cursor worktree mutation 后禁止跨模型重放。write 模式不执行 `git worktree add`，只监视 Cursor 的真实 `~/.cursor/worktrees/<repo>/<name>` 路径；fsnotify 事件与前后 snapshot 共同形成 sticky mutation 门禁。metadata-only journal 权限为 `0600`，不保存 prompt、NDJSON 正文、工具参数或凭据。使用说明见 `tools/cursor-cli-model-pool/README.md`，198 可选部署边界见运维手册 §14。

验证已通过：根 module `go test ./...`、全量 race、vet；Provider 定向 test/race/vet；前端配置投影与生产构建；CLI module test/race/vet/build；日志分析器 test/race/vet；临时 HOME 的 CLI `validate`/`dry-run`；发布隔离、禁止路径与 `git diff --check`。独立终审首轮发现并修复三类承重问题：前端重复实现渠道 SHA-256；CLI 监视错误 worktree 路径；终审阶段逻辑 alias 可嵌套入 Provider 链、短暂 worktree mutation 可漏检以及成功 HTTP 200 被错误分类。返工后两路复审均无剩余 P0/P1。

2026-08-25 用户本机真实观测（未提交、未改 18080/18090 PID）：逻辑路由 `grok-HA`/`543fe17c50d81660` 启用 fallback，primary 为故障物理渠道 `Grokeeror`/`d5ab6805830e5baa`（`127.0.0.1`，`modelID=dd`），candidates 为 `grok-hongai`/`3d2b0ff4a6be3e42` 与 `grok`/`378723ba5535e672`，全链 5 attempts / 8s。`provider_fallback_attempt` 共 54 条、28 个 `model_call_id`：13 次 Grokeeror `transport` 后 hongai 成功（used 4/5）；13 次 hongai 随后 `server_5xx` 且 `attempt_budget_exhausted`（used 5 remain 0），第三候选未启动，符合 primary 吃满 3 后只剩 3+2。预算字段 used+remaining 恒等于 5，wait 未耗尽。CLI 池在临时 HOME（复制 `config.yaml`，Library 软链以通过 `agent login`）对用户指定的 `3d2b0ff4a6be3e42`/`45719971585b2646`/`506d30d8e14b7b5e` `validate`/`dry-run` 通过；引用 `grok-HA` 返回「禁止引用 providerFallback.enabled=true 的逻辑适配器」。仓库 cwd 的 Ask `run` 首模型成功（`system/init → user → thinking* → assistant → result/success`），因此未切到第二/第三物理模型；从 `/tmp` 启动时 Cursor Workspace Trust 产生非结构化错误，控制器记 `unknown` 并 fail-closed、不换模型。journal `0600`、无 prompt/凭据。副作用：运行中 18090 把 `lastAgentModelHash` 从 `grok-HA` 写成 `grok-hongai`；23 个 adapter 与 fallback 链字段未变，未创建真实 `cli-model-pool.yaml`。

证据缺口：本机没有 `wails3`，独立 Vite 页面因 `/wails/runtime` 404 无法挂载，因此浏览器视觉验收未通过；真实 CLI 两模型 pre-output 故障切换仍未发生（首物理模型已成功）。故本工作包标 `verified-partial`，不得宣传为 CLI 故障切换和 Wails 浏览器验收均完成。

### 0.4 配置写竞态与物理上游容量（能力验收完成，在线未启用）

已完成（2026-08-25；随本次汇总提交交付，未推送、未发布）。普通 UI 保存现在会在 Store 锁内读取最新 YAML，以 UI payload 更新用户字段并保留最新 `lastAgentModelHash`；运行时 hash 更新只 patch 该字段，相同值不写盘、不通知 listener、不触发 Host rebuild；完整导入继续通过显式 replace 全量替换并允许替换 hash。Manager 写事务锁串行提交磁盘、current 和 snapshot，写盘失败不推进内存。确定性交错、并发 race、失败回滚、hash no-op 和 import replace 均有自动化覆盖。

物理 adapter 新增可选 `maxConcurrentRequests`：缺失/0 保持无限并发，非零允许 1–16；逻辑 fallback alias 必须为 0，同 provider、规范化 Base URL 和 API Key 的物理渠道必须配置一致。resolver 将限制和只在内存存在的 SHA-256 上游组身份投影到真实请求；进程级 limiter 固定最多等待 2 秒，槽覆盖完整 Stream 及同渠道 retry。容量超时为 typed `capacity_unavailable`，不消耗 HTTP attempt 或 fallback retry/backoff wait，只在零 HTTP、零原始字节、零 model event、零副作用窗口切到不同上游组；同组候选跳过，父 context 取消不 fallback，release 不泄漏。API Key、Base URL 和组 hash 不进入日志、事件或错误。

主控集成复查发现并修复了一个跨切片 P1：resolver 虽已产生容量值，`applyChannelToRequest` 原先没有用 `ResolvedChannel` 覆盖 `StreamRequest`，导致真实配置无法生效而仅测试手工请求字段有效。修复后补了请求投影、真实 HTTP 峰值、Manager legacy snapshot 和上游组规范化回归；独立复审确认无剩余 P0/P1。

验证通过：容量与配置定向 test；根 module `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`；前端投影测试和生产 build；CLI 独立 module test/race/vet/build；日志分析器 test/race/vet；`git diff --check` 与 proto/MITM/certs 保护路径检查。Apple Silicon / macOS 14.6.1 使用隔离临时工具链运行 `task build`，生成版本 `0.0.49.5`、约 23 MiB 的 `bin/macos-arm64.dmg`（SHA-256 `8ad056b49386841ba2cd1a8a3cff7ae3489948f38e9be47373da693090d93841`）；`hdiutil verify`、只读挂载、Info.plist/buildinfo 版本、Mach-O arm64 与 adhoc codesign 校验通过。DMG 受 `bin/` ignore 规则保护，不纳入 Git，且未做 Developer ID 签名或 notarization。详细合同见工作决策基线 §10.9、系统 Design §14.10，详细任务证据见 `task/todo.md` 的 `config-race-upstream-capacity-20260825`。

用户明确选择不修改当前真实 `grok-HA` 配置，本次也未读取后改写该文件、未占用或重启 18080/18090。因此可声明“容量能力已实现并经非零 fixture 验证”，当前在线 `grok-HA` 仍为无限并发，不能声明在线容量风险已启用关闭。

### 0.5 `v0.0.49.6` 发布

本次发布版本使用 `0.0.49.6`。版本事实源 `build/config.yml`、Wails macOS/Windows/Linux 构建元数据、`release-notes.md` 与归档 `releaselog/0.0.49.6.md` 已按该版本对齐；发布说明包含 Provider fallback 覆盖优先预算分配、五物理渠道配置与测试交互修正，以及此前已验证的多层模型路由、配置写事务和可选物理上游容量能力。2026-08-25 用户最新要求已取代此前“仅生成本地资产”的安排：`cli` 变更合入 `noad` 后，README 的“当前稳定发布”更新到 `v0.0.49.6`，发布资产上传到 `yaogjim/cursor-byok` GitHub Release，标签必须指向 `noad` 的发布提交。

### 0.6 Fallback 配置交互修正（自动化验收完成，视觉待补）

2026-08-25 已完成本轮配置交互调整，并纳入本次提交及 `0.0.49.6` 本地发布候选。“测试全部”继续只调度物理 adapter，但现在静默跳过逻辑 alias，不再弹出“已保存/需实际运行验证”的误导提示；单独点击逻辑 alias 测试仍保留运行验证提示。通用 Select 的视口可用高度现在施加到真实 `ul` 滚动容器，并保留键盘聚焦时的就近滚动，修复长列表只能用方向键看到后续 adapter 的问题。

Fallback 链上限从 1 primary + 2 candidates 扩展为 1 primary + 4 candidates，总共最多 5 个物理渠道。前端候选槽改为数据驱动的连续 4 槽，后一槽只在前一槽已选时显示，清空中间槽会截断后续槽；每个下拉继续排除 primary、逻辑 alias 和其他槽已选渠道。前后端共享上限校验，第 5 candidate 被拒绝；resolver 保持原循环并经测试证明五通道顺序完整投影。全链预算合同未改变：默认 5 attempts / 8 seconds，范围 2–9 / 1–30，单渠道最多 3，渠道增多不保证链尾一定获得 HTTP attempt。

验证通过：`node frontend/scripts/test-config-projection.mjs`；后端 config/client/model 定向测试；`go test ./internal/... -count=1`；`npm run build --prefix frontend`；受改文件 lint、gofmt 与 `git diff --check`。测试/build 只有既有 macOS deployment target、`--localstorage-file` 和 chunk-size warning。构建生成的 i18n 文件已恢复，不纳入变更。本机仍缺少 `wails3` 和 `task`，没有可用开发 UI 端口；为避免扰动运行中 Cursor 的 18080/18090，未做真实窗口视觉点击，因此当前状态为 `verified-partial`，缺口仅为 UI 视觉交互证据。

### 0.7 Multi-Client Chat Gateway 阶段 0/1（实现完成，证据部分闭合）

2026-08-25/26 已按已批准计划交付最小纵向切片，`delivery_status=verified-partial`，未提交、未推送。合同见工作决策基线 §10.10、系统 Design §14.11 和 `task/todo.md` 的 `multi-client-chat-gateway-phase0-1-20260825`。独立 `internal/gateway` 监听默认 `127.0.0.1:18091`，默认关闭、loopback + Bearer；复用现有 Provider Gateway/Router；token 不进普通投影/导出/localStorage/日志；默认导出剥离 token 后再导入会 overlay 现有 token。自动门禁（定向五包、`internal/...`、相关 race、`go vet ./internal/...`、前端投影/build、`git diff --check`）通过；`proto/`、MITM、certs 无 diff。真实 Cherry/OpenAI SDK、Wails 视觉验收和 Cursor 全量回归仍是证据缺口，不得标 accepted。本包不扰动运行中 18080/18090。

- **阶段 1 补充验证（2026-08-26）**：修复 OpenAI Chat 纯文本 `content` 数组兼容性，补充流式首块 `assistant` 角色，并严格拒绝非 `null` 的空 `tools` 数组。新增真实 TCP listener smoke 在隔离 `127.0.0.1:18091` 上依次验证 `/v1/models`、非流式 Chat 和流式 Chat；Provider 使用内存 fixture，未连接真实上游。`18080`、`18090` 未被启动、停止或替换。


已完成（2026-08-23，未提交、未发布）。治理实现位于隔离分支/worktree `agent-governance-0.0.49.2` / `cursor-byok-governance-0.0.49.2`，基线仍为 `v0.0.49.2` 发布提交 `487856170b29380671477e843d7fec15250323ae`；当前主工作树的无关 WIP 与两个 recorder/exporter 专用 stash 均保持隔离。

本治理包已接通五项能力：按 `model_call_id` 聚合内部 reasoning replay；metadata-only `execution_evidence` 账本；mutation 后 verification stale 判定；证据不足时最多一次提醒续跑的完成门禁；agent/subagent prompt、`turn_completed` 和 debug recorder 的白名单诊断。账本只接受结构化 ToolCall 与终态 result，成功 mutation/verification 使用最终持久 sequence，tool result 与 evidence 同一追加批次写入；重复 result、跨 turn、restart、subagent recovery、unknown MCP、pending/failed/canceled 均按保守规则处理。完成门禁从最新持久 canonical history 重建，不依赖可能滞后的 live 索引；纯问答与 Ask/Plan 不受编辑门禁影响。

三路独立终审均已闭合。replay 方向修复了两个 P1：orphan reasoning 不再跨 `model_call_id` rehome；provider signature 与 exact reasoning content 绑定，不再形成“新正文 + 旧签名”。此前阶段已关闭弱 tuple 丢失、AwaitShell 无 exit、脚本前缀假阳性、编辑+解释绕过、中文建议问句误触发、公共 transcript 合法 tool path 误报和诊断投影 fail-open。最终复核未发现新 P0/P1；账本/门禁和 prompt/诊断/隐私方向无可复现 P0/P1/P2。

最终新增集成回归证明：completion gate 的结构化 prompt reminder、`execution_evidence` 与 `completion_gate` metadata 会保留在 canonical history，但不会进入公共 transcript；用户可见 assistant 文本和合法结构化 `tool_use.path` 仍被保留，reasoning 与内部诊断继续零投影。最终实跑通过公共 transcript 专项、forwarder 全包/race/vet、根模块 `go test -p 1 ./... -count=1 -timeout 900s`、`go vet -p 1 ./...`、gofmt 和 `git diff --check`。Stage 6 已通过的 `internal/...`、根客户端构建、prompt、前端配置投影/生产构建及两个独立 Go module 的 test/race/vet 证据继续有效。

环境记录：初次根全量并发链接曾因磁盘 `ENOSPC` 失败，串行 `-p 1` 后通过；本轮根测试首次因隔离 worktree 缺少真实 `frontend/dist` 在 `go:embed` setup 阶段停止，随后只读复用主工作树 `node_modules`/bindings 生成隔离临时 dist，根测试通过。临时 dist、依赖 symlink、bindings symlink、`gen` symlink 和验证进程均已清理。公开 proto、MITM/证书、发布资产、标签和两个专用 stash 无漂移。

保守兼容边界：裸 `modeladapter.Message` 二次 normalize 缺少 model-call 身份时不做 orphan rehome；旧 history 连续工具批次双方身份都为空时继续保留旧合并行为。这两项只用于避免旧数据破坏，不宣传为新 history 的身份隔离能力。真实 Cursor success/error/background/resume 六场景 fixture 仍待独立补证；本治理包不证明 child 从中断执行点自动续跑。

指定 transcript 最终复盘结论：原会话中因服务中断停止的 Stage 4 已在用户切换 LLM 服务后恢复并闭合；Stage 6 四条 `Superseded by newer request` 只是同一根测试命令被后续请求替代，后续串行全量测试已闭合，并无可直接 resume 的独立遗留任务。原始建议中唯一未逐字覆盖且可在当前合同内补齐的是显式 15K reasoning canary；现已把三工具共享 reasoning 回归升级为 15 KiB start/end canary，并通过专项、forwarder 全包/race/vet 和 diff 检查。更强的“assistant 文件声明逐项对照 Git diff”和“reasoning 超阈值时中途打断”不属于已批准 metadata-only 完成门禁语义，若要实施需另行设计；recorder/exporter stash 与真实 Cursor 六场景仍是独立工作包，未混入本治理包。

阶段状态、命令和缺口以 [`task/todo.md`](../task/todo.md) 的 `agent-governance-completion-20260822` 为准；治理包当前 `DELIVERY_STATUS=accepted`，但尚未 commit、push、tag 或发布，后续发布必须等待单独批准。

### 1. 版本发布与交付

#### `v0.0.49.2` transcript 热修复

已完成（2026-08-22）。修复公开 Agent transcript 泄漏并重复输出 reasoning/thinking 的问题；内部 provider replay/signature 数据保持不变。发布提交为 `487856170b29380671477e843d7fec15250323ae`，三平台 GitHub Release：<https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.49.2>。

`v0.0.49.1` 标签仍固定指向 `716b436ca0d79e34e52ea02a8ecc07f6579b5cfe`，旧标签和归档未覆盖。未完成 exporter 与独立 subagent recorder 已分别隔离在专用 stash，未进入本补丁版。

#### `v0.0.49.1` 发布收尾

已完成（2026-08-22）。最终交付范围为 macOS arm64 与 macOS amd64，不含 Windows/Linux。

### 2. Provider、子代理与 MITM 证据



#### 真实 Cursor 子代理协议补证

本地合同与 synthetic 回归已完成（2026-08-22；**不归档为完成**，synthetic 不等于真实）：

- [x] 系统 Design §14.7.10 已冻结六场景。
- [x] 落地 `source=synthetic`、`closes_stage_2=false` 的 `internal/backend/forwarder/testdata/subagent_contract_scenarios.json` 与 `subagent_fixture_test.go`；独立审查无 P0/P1。
- [x] 主控验证已实际运行并通过（2026-08-22）：`go test ./internal/backend/forwarder ./internal/observability -count=1 -timeout 90s`（forwarder 与 observability ok）；`go test -race ./internal/backend/forwarder ./internal/observability -count=1 -timeout 120s`（两包 ok，无 race）；`go vet ./internal/backend/forwarder ./internal/observability` 通过；`gofmt -d internal/backend/forwarder/subagent_fixture_test.go internal/backend/forwarder/subagent_real_observation_test.go` 无输出；fixture JSON 值级隐私扫描无用户绝对路径、credential-like token 或 raw UUID；`git diff --check` 通过；`internal/mitm`、`internal/certs`、`proto` 保护路径 diff 为空。本轮验证覆盖 synthetic 回归与 partial real observation，**不是** `source=real` 六场景通过。

partial real observation 已完成（2026-08-22；`source=real`、`closes_stage_2=false`，**不能**关闭 Stage 2）：

- [x] `internal/backend/forwarder/testdata/subagent_real_success_observation.json` + `internal/backend/forwarder/subagent_real_observation_test.go`。
- 14 个 run 均 `acknowledged` / handoff acknowledged / terminal succeeded / result file present；identity presence root/parent/tool/parent_request/parent_model_call/agent 为 14，`child_conversation` 为 0；`protocol_envelope_presence_available=false`。
- 采集只读取 `run.json`；`result.json` 只检查存在，不打开/复制正文；不保存 raw IDs。排除 1 个 dispatched run，但该排除数不是 JSON 可复算字段。
- 只能证明匿名成功交接聚合，不能证明 typed message / 五字段 presence、error/cancel/restart/resume/missing-parent。

系统 Design §14.7.11/12 已完成并经独立终审（实现尚未开始）：

- [x] §14.7.11 metadata-only `subagent_protocol_observed` 与 §14.7.12 离线 exporter 已写入系统 Design；终审 P0/P1 无，P2 已闭合。
- 明确只是 Design approved：未添加生产事件，未实现 `cmd/subagent-fixture-exporter`，未启动/停止第二 Backend，未修改 Cursor/MITM/CA/系统代理/proto。

真实证据仍阻塞（Stage 2 保持 `in_progress`；`source=real` 六场景未完成，绝不能标 completed）：

- [ ] 后续须先实现已批准的 metadata 事件与 exporter（必须再次获得明确实施授权），再在隔离环境采集六场景。真实运行只能来自另一 OS 用户、VM、独立机器或未来另批批准 harness；本批 exporter 只读用户提供副本，不承诺本机第二实例/独立 Backend。
- [ ] 采集新的真实 Cursor 子代理协议 fixture，覆盖父子会话、工具调用、RunSSE、取消、重连和终态交接。
- [ ] 用真实 fixture 验证 `terminal_prepared → parent_committed → acknowledged` 的持久交接和关联字段。
- [ ] 补真实 Success/Error envelope、取消消息类型、`parent_committed` 未 ack 后重连、未终态 resume/`run_id`、父会话缺失后 resume。
- [ ] 明确验证边界：当前实现只保证已生成终态的持久、幂等交接；未完成 child 进入 `awaiting_client_resume`，不承诺从中断执行点自动续跑。



#### MITM/TLS 决策证据

已完成（2026-08-22）。基线报告见 [`docs/mitm-tls-baseline-report.md`](mitm-tls-baseline-report.md)。输入为 9 个 closed `v0.0.49.1` session，结论保持当前路由/CA/白名单；证据闭合前仍不修改 MITM 白名单、CONNECT/直通策略、CA/证书、系统代理、透明代理或协议行为。未闭合项摘要见下文已完成归档。

### 3. 独立日志分析器产品能力



#### 调查案例库

- [ ] 实现持久调查案例、状态机、版本关联和修复后复验。
- [ ] 案例默认只保存脱敏证据快照，不复制 full payload、凭据或项目路径。



#### 外部 AI 调查包

- [ ] 实现脱敏证据包导出和结构化分析结果导入。
- [ ] 保持日志内容为不可信输入；分析器第一版不直接调用 AI、不修改仓库、不执行外部命令。



#### 客户端启动器

- [ ] 在客户端日志采集区增加独立分析器检测、启动按钮和未安装引导。
- [ ] 保持客户端只负责采集与受限启动，不在客户端进程内读取历史日志、运行分析或生成报告。



### 4. 分析器发布隔离与性能验收

- [ ] 建立分析器独立发布、客户端归档隔离、跨版本、跨模块和跨平台验证证据。
- [ ] 补充真实大日志目录性能门禁：当前约 `1.7 GB` 数据集在 14 分钟内未完成；已验证可取消，但尚未通过全量性能验收。
- [ ] 明确性能验收指标、测试机环境、完成耗时、峰值内存和取消清理结果，避免只记录“能运行”。



### 5. 后续路线图（尚未进入当前实施包）

- [ ] 建立统一 Capability Registry，用于路由能力分类、工具名单和前端执行目标说明。
- [ ] 补齐 Bidi、RunSSE、工具循环、取消与重连的协议基线测试。
- [ ] 继续治理 actor 状态机、持久化格式版本与迁移、Keychain、日志安全和架构拆分。
- [ ] 在独立设计批准后推进本地检索和高价值 Cursor 能力补齐；不得与已完成的 `v0.0.49.1` 发布收尾混做。



## 二、已经完成的内容



### 1. 按版本号归档



#### `v0.0.49.2` — 2026-08-22（已发布）

- [x] 修复 `assistant_text` 把 `ReasoningContent` 拼入公开文本的问题。
- [x] 修复多个 `tool_call` 各自重复附带同一 reasoning 的问题；工具调用仍以结构化 `tool_use` 输出。
- [x] 导入 `model_message` 时不再向公开 JSONL 投影 reasoning；内部 history/context 与 provider replay 数据未删除。
- [x] reasoning canary、多个工具调用、导入消息、ConversationFileStore 与 agent-transcripts 回归测试通过。
- [x] 根模块、forwarder、race、vet、客户端构建、前端构建及两个独立 Go module 的完整发布前门禁通过。
- [x] macOS arm64、macOS amd64、Windows amd64 归档和三平台 `update.json` 已构建；架构、版本、签名结构、分析器隔离、URL、size 与 SHA-256 已复算验证。
- [x] 发布提交 `487856170b29380671477e843d7fec15250323ae`、标签 `v0.0.49.2` 与 GitHub Release 已完成；远端重新下载资产逐字节等于本地产物，`latest/download/update.json` 指向 `0.0.49.2`。

Release：<https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.49.2>

#### `v0.0.49.1` — 2026-08-19 至 2026-08-22（发布已收尾）

版本代码已经提交并推送；2026-08-22 发布收尾完成。最终交付范围为 macOS arm64 与 macOS amd64（`bin/release/0.0.49.1/` 下对应 tar.gz 与 `update.json`），不含 Windows/Linux。

- [x] Provider 错误与流终态：补齐 typed HTTP error、脱敏错误摘要、attempt/response 审计、截断 EOF 识别和唯一业务终态。
- [x] Provider 安全重试：只在首个 model event、原始流字节和副作用均未发生时有限重试；覆盖 transport、429、500、502、503、504、`Retry-After`、取消和等待预算。
- [x] Provider 与 MITM trace：修复 attempt 事件只进入默认关闭审计链的问题，补齐 request/response/retry/final、CONNECT 决策和 TLS 失败的正式 trace 落盘及关联字段。
- [x] 日志隐私：不记录请求/响应正文、header、token、cookie、API key、child prompt/result 或 Provider 原始敏感响应。
- [x] 子代理关联与终态：贯通 root、parent、tool、subagent、child、agent、model-call 和 attempt；实现 typed terminal、首个持久终态胜出和冲突保护。
- [x] Durable handoff：实现版本化 `SubagentRunStore`、原子 run/result、checksum、损坏隔离、启动恢复扫描和 parent tool-result 幂等提交。
- [x] 缺失父会话保护：父会话不存在或已删除时保留 durable result 并进入 `awaiting_parent_resume`，不创建孤立 conversation。
- [x] Provider fallback：实现默认关闭的显式有序 allowlist；retry/fallback 共享 attempt 与等待预算；任意输出、model event 或副作用后禁止切换。
- [x] Fallback 兼容门禁与前端：覆盖 Provider family、工具、图片、上下文和原始请求体兼容性；完成配置投影、导入导出、候选编辑和多语言入口。
- [x] 集成验证：相关及全仓 Go test、race、vet、前端配置投影与生产构建、故障注入、敏感信息审查和禁止路径反向审查均已通过。
- [x] 发布元数据：将版本更新为 `0.0.49.1`，补齐四段版本比较测试，并把内外发布说明收敛为简版。
- [x] 关键提交：`9e6936f`、`eed4ca6`、`0850958`、`c6a462d`、`716b436`；当前 `noad` 已与 `origin/noad` 对齐。
- [x] 发布收尾：2026-08-22 完成 macOS arm64 与 macOS amd64 资产交付；本地标签 `v0.0.49.1` 已存在。Windows/Linux 不在本轮范围。
- [x] MITM/TLS 基线：2026-08-22 基于 9 个 closed `v0.0.49.1` session 生成 [`docs/mitm-tls-baseline-report.md`](mitm-tls-baseline-report.md)；分析器合计 `events=128456 traces=14338 findings=11468`；`cd tools/log-analyzer && go test ./...` 通过。TLS 失败 906 次全部为 `cursor_to_proxy` + `client_unknown_ca`（api3 592、metrics 282、api2 32）；无 upstream TLS 失败。结论保持当前路由/CA/白名单；未改 `internal/mitm`、证书或 `proto`。open session 未计入。

已知限制：Stage 2 保持 `in_progress`。本地合同与 synthetic 回归已完成；partial real observation 已落盘（`source=real`、`closes_stage_2=false`），只能证明匿名成功交接聚合；系统 Design §14.7.11/12 已终审但实现尚未开始。真实 Cursor 子代理协议六场景 fixture 仍待补证，synthetic 与 partial observation 都不能关闭 Stage 2；不承诺 child 从中断执行点自动续跑。MITM 未闭合证据：api3/metrics 100% `client_unknown_ca` 的机制未知；Dashboard/MCP `client_error` 与 TLS 无 ID 级因果；provider attempt 层 `trace_id`/`http_request_id` 完整率 49.2%；`bidi.raw decode_error=true` 在 basic 模式下的语义未闭合。

#### `v0.0.49` — 2026-08-19 至 2026-08-20

- [x] 将 `upstream/main@564f2bd` 经 `main@305b108` 合入 `noad`，形成 `f969ca0`。
- [x] 保留并整合 `noad` 上已有的 Provider 首包前安全重试与 MITM 只读诊断开发线。
- [x] 清理对外发布说明中的非产品联系信息。
- [x] 上游 `v0.0.49` 标签已存在；本地后续 P0/P1 改进归入 `v0.0.49.1`，不回写为 `v0.0.49` 已发布能力。



#### `v0.0.48` — 2026-08-12 至 2026-08-17

- [x] 完成上游同步、合并、版本记录与 `v0.0.48` 标签。
- [x] 支持会话统计重置和已关闭日志的安全清理，并补齐相关前端、后端、i18n 和测试。
- [x] 对齐上游同步发版流程，并把 `v0.0.47` 编排结果写回任务记录。



#### `v0.0.47` — 2026-07-27 至 2026-08-10

- [x] 完成 `upstream/main → main → noad` 同步、冲突处理、窄范围验收、推送和 `v0.0.47` 标签。
- [x] 发布 macOS arm64、macOS amd64、Windows amd64 三个平台资产，不包含 Linux；GitHub Release 已完成。
- [x] 清理辅助 worktree，同时保留已验证分支、标签和发布资产。
- [x] 接入全局 Agent 规范并确立 `task/todo.md` 为任务唯一真值源。
- [x] 建立分析器临时 SQLite workspace、批量流式导入、有界 reducer 和流式 JSON/HTML/ZIP 报告。
- [x] 完成 SQLite 兼容性、大规模内存、故障注入、race/vet、离线跨平台构建和客户端发布隔离验证。
- [x] 完成日志语义 v2、项目生命周期、组合检索 DSL、保存查询、增强诊断、独立 Wails/Vue GUI 和默认日志目录异步自动加载。
- [x] 独立分析器 GUI 通过 macOS arm64 原生启动/退出 smoke、ad-hoc 签名、DMG/tar.gz 结构与 SHA-256 校验。



### 2. 按时间索引

- **2026-08-24**：198 远程 CLI 方案 A 隧道落地；浏览器终端由 ttyd 换成 WeTTY。随后修复 HTTPS 反代下 WeTTY helmet 只放行 `ws://`、浏览器 `wss://` 被 CSP 拦导致终端约 10 秒断开的问题；再将 `X-Frame-Options` 从 `DENY` 改为 `SAMEORIGIN`（并加 `frame-ancestors 'self'`），避免 WeTTY 同源 xterm 配置 iframe 被拦。`https://172.16.23.198/` 仍为 HTTPS + HTTP Basic，7681 仅 loopback。
- **2026-08-22（`v0.0.49.2`）**：完成 Agent transcript reasoning/thinking 公开投影修复、全套验证、三平台资产构建与 GitHub Release。发布提交和标签均指向 `487856170b29380671477e843d7fec15250323ae`；远端资产与本地 SHA-256 一致；`v0.0.49.1` 保持不可变；未完成 exporter 与 subagent recorder 未纳入补丁版。

- **2026-08-22**：`v0.0.49.1` 发布收尾完成；最终交付范围为 macOS arm64 与 macOS amd64，不含 Windows/Linux。Stage 3 MITM/TLS 基线完成：报告 `docs/mitm-tls-baseline-report.md`，9 个 closed `v0.0.49.1` session，分析器合计 `events=128456 traces=14338 findings=11468`，结论保持当前路由/CA/白名单。Stage 2 仍 `in_progress`：本地合同与 synthetic 回归已完成（`closes_stage_2=false`）；partial real observation 已落盘（14 个 acknowledged/succeeded run，`protocol_envelope_presence_available=false`，`closes_stage_2=false`）；系统 Design §14.7.11/12 已终审但实现尚未开始。本轮主控已实际运行 test/race/vet/gofmt/隐私扫描/`git diff --check`/保护路径检查并通过，**不是** `source=real` 六场景通过。`source=real` 六场景仍阻塞；当前阶段仍为 Stage 2 真实 Cursor 子代理协议补证。
- **2026-08-21**：完成 `task/todo.md` 实施计划与 `docs/process.md` 进展归档的职责拆分和同步合同；完成 `v0.0.49.1` 子代理 durable handoff、Provider fallback、前端配置、集成验证、版本配置和简版发布说明；源码提交并推送。发布构建第一次因磁盘不足失败，清理缓存后开始第二次构建并生成 macOS arm64 资产。
- **2026-08-20**：完成 Provider/MITM 正式 trace 落盘、日志级别与关联字段修复；完成 `v0.0.49` 上游合并和发布说明清理。
- **2026-08-19**：完成 Provider 首包前安全重试、流截断正确性和 MITM metadata-only 诊断；提交 `9e6936f`。
- **2026-08-17**：完成 `v0.0.48` 同步记录、会话统计重置和已关闭日志安全清理。
- **2026-08-10**：完成 `v0.0.47` 三平台发布和 GitHub Release。
- **2026-08-02**：完成日志观测语义、分析项目、查询、诊断和独立 GUI 的主要能力；提交 `17659ec`。
- **2026-07-27**：完成分析器临时 SQLite 工作区与流式管线基础；提交 `a1b1cf2`。

历史完成项只在本文档归档，不再与详细执行步骤混排。实施步骤以 `task/todo.md` 为准，产品决策与设计细节以相应 PRD、系统 Design、`.cursor/plans/` 和 Git 提交为准。