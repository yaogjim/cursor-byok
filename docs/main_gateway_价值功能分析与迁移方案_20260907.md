# 最新 `main` 对 `gateway` 的价值功能分析与借鉴迁移方案

- **分析日期**：2026-09-07
- **修订日期**：2026-09-07；依据代码复核及已确认的恢复范围、代理兼容策略修订。
- **上游仓库**：<https://github.com/leookun/cursor-byok>
- **上游基线**：`main@1268e99b5a693dda4e59051d7bf77bdc856e54e9`，标签 `v0.1.7`
- **本地目标基线**：`gateway@2fc04e7823056f17923310aacdaf41399a3cc9a6`
- **共同祖先**：`564f2bdcaec790863aca86403cedbfc77191bd43`
- **分支独有提交数**：`gateway` 84 个，`main` 208 个
- **文档目标**：保留已确认的 P0/P1 范围，按现有 Go 架构补齐行为合同、实施顺序、验收和回退；P2 仅作独立产品候选。
- **本次修订范围**：仅修订本文，不修改生产代码、不创建第二份方案、不授权自动提交。

> 行数口径：现有文件行数按固定 Git 基线执行 `wc -l`，包含空行、注释和文件内测试。行数只说明责任集中度，不是实现规模或质量指标；新增文件仅在职责确需拆分时创建，不按预估行数建设模块。

### 已确认的实施边界

1. **恢复按当前失败调用判断**：当前调用尚未产生模型输出或执行工具时，允许压缩后继续；此前已完成的工具结果保留，不由恢复流程重新执行。每个 Run（一次逻辑运行）最多进行一次溢出恢复。
2. **WebFetch 保留代理兼容性**：直连执行 DNS 校验和目标地址固定；显式代理模式沿用现有出站链路，拒绝 URL 凭据和字面量非公网目标，不额外阻断域名抓取。代理端解析风险仍存在，不宣称与直连具有同等保护。
3. **最小实施**：复用现有 actor（流事件串行处理器）、压缩、checkpoint（恢复检查点）、配置解析和出站代理机制；不新增审批、通用安全框架、开关矩阵、账号系统或持久化恢复服务。
4. **实施入口**：后续收到实施指令可从 §9 的 P0 工作包开始；§3～§4 是行为依据，§11 是验证依据。已确认的范围不反复审批；如果运行证据推翻接口或数据前提，只暂停受影响工作包，不用文档措辞替代验证。P2 不在本次实施授权范围内。

## 1. 结论

### 1.1 总体判断

`main` 和 `gateway` 已经不是可以进行源码级合并的同一实现：

```text
564f2bd（共同祖先，Go/Wails）
├── main@1268e99
│   └── Rust/Tauri：server/ + crates/ + apps/desktop/
└── gateway@2fc04e7
    └── Go/Wails：internal/ + frontend/ + 多个独立工具
```

在上述固定基线上执行 `git diff --shortstat gateway main`，结果为 **1,248 个文件、138,014 行新增、241,595 行删除**（方向为 gateway → main，使用当前 Git 默认 rename 检测）。这主要来自整栈重写，不能把文件差异数量当作应迁移功能数量。正确策略是：

1. **不 merge、cherry-pick 或翻译整棵 Rust/Tauri 代码。**
2. **只迁移可观察行为、状态不变量、失败用例和安全约束。**
3. **保留 `gateway` 已建立的 MITM、OpenAI 兼容 Gateway、订阅多账号、fallback、流恢复和观测体系。**
4. **每个能力先用 Go 失败测试证明缺口，再实施最小 Go 修改。**

### 1.2 最新 `main` 的真实增量

`main@1268e99` 是一个双父 merge：

```text
1268e99
├── 03b25be  # 桌面广告滚动文本分支
└── 99d527d  # gateway 上一轮已经审阅的上游基线
```

`1268e99` 的文件树与第二父 `99d527d` 完全相同。因此：

- 相对 `gateway` 上次记录的 `main@99d527d`，**没有额外未审源码**；最新提交主要收敛了上游历史。
- 上一轮唯一仍明确标记为 pending 的关键能力仍然是 `fdae9c4`：provider 报上下文溢出后，压缩并在同一 Run 中重试。
- 本文仍以最新完整文件树重新核对，因此也发现了上一轮不能只按提交标题判断的两个真实缺口：
  - CLI 模型目录会把真实 provider API Key 和 Base URL 投影给客户端，而 `main` 已改为本地哨兵凭据。
  - `GetUsableModels`、`GetDefaultModelForCli` 只有旧的 `aiserver.v1` 路径，缺少对应的 `agent.v1` 别名；这不代表 gateway 完全没有 `agent.v1`，其 `RunSSE` 已使用该协议路径。

### 1.3 优先级结论

必须迁移的 P0：

1. **provider context overflow → 压缩 → 同一 Run 重试一次。**
2. **CLI 本地路由的凭据隔离，并补齐 `agent.v1` / `aiserver.v1` 双协议入口。**
3. **`Bash` / `bash` 完整映射为 `Shell`，覆盖目录、事件、执行、证据和回放。**
4. **WebFetch 直连模式的 DNS 校验和地址固定，以及所有模式的 URL 凭据拒绝；代理模式保持兼容。**

`gateway` 已经有，但应借鉴改进的 P1：

1. 保留摘要请求已具备的 user 结尾，补齐摘要输入预算和按既有轮次结构裁剪；恢复必需部分纳入 P0-1。
2. 工具结果截断收敛为共享预算规则，保留展示与回放的必要差异。
3. CLI 模型目录和实际运行路由共享模型标识、参数与能力解析规则，凭据继续在运行时解析。
4. 配置变化确需重启 Cursor 时，由用户确认后正常退出并重启，首版不增加强制终止流程。

有产品价值、但应作为独立产品增量的 P2：

1. Google Antigravity 订阅认证、账号轮转和模型目录。
2. 通用 OAuth 回环回调模块，供 Codex、Grok、Antigravity 复用。

已经对齐或 `gateway` 更强，不应重复迁移：

- OpenAI Chat `content_filter`、`length`、`stop + tools` 终态矩阵。
- 空工具参数规范化为 `{}`。
- usage 字段 presence-aware 累计。
- MCP `ListMcpResources` 资源数量截断提示。
- checkpoint Blob 哈希、缺失 Blob 失败和客户端 ACK 后发布。
- Task/subagent 恢复、幂等、重调度。
- provider fallback、四阶段 liveness、截断流续写。
- Commit Message 本地生成。

## 2. 当前架构与主执行路径

### 2.1 `main` 当前结构

`server/` 下 Rust 文件共 52,324 个物理行，包含空行、注释和测试代码；不包含 TypeScript、SQL、前端和其他目录。

```text
main@1268e99
├── apps/desktop/                                  # Tauri 桌面壳和 React 管理界面
├── crates/semble-core/                            # 对话/模型基础类型
└── server/src/                                    # Rust 本地服务器
    ├── app.rs                                     # 197 行；组装 Store、Provider、插件、搜索和本地代理
    ├── api/cursor/handlers.rs                     # 392 行；Cursor RPC 路由入口
    ├── local_app/
    │   ├── mod.rs                                 # Cursor 接管、代理生命周期、进程处理
    │   └── proxy.rs                               # 本地/上游路由判定
    ├── cursor/
    │   ├── conversation/
    │   │   ├── runtime.rs                         # 847 行；每个会话的运行时和取消生命周期
    │   │   └── output.rs                          # 1,139 行；工具结果和输出投影
    │   ├── checkpoint/recovery.rs                 # 49 行；预取 Blob 校验和消息恢复
    │   ├── services/model_catalog.rs              # 862 行；桌面/CLI 模型目录和本地哨兵凭据
    │   └── tools/tool_call_result/gate.rs         # 897 行；所有工具结果的统一预算门禁
    ├── run/
    │   ├── engine.rs                              # 1,066 行；完整 Run 循环、重试、压缩、工具轮
    │   ├── compaction.rs                          # 508 行；压缩预算、历史裁剪、overflow 识别
    │   └── model_cycle.rs                         # 554 行；单次模型调用状态机
    ├── provider/
    │   ├── router.rs                              # 421 行；provider 选择、请求超时、流空闲超时
    │   └── openai_chat.rs                         # 469 行；OpenAI Chat 事件与终态映射
    ├── plugin/
    │   ├── registry.rs                            # 1,374 行；插件注册、账号、OAuth 和模型目录
    │   └── oauth_callback.rs                      # 347 行；回环 OAuth callback
    └── search/
        ├── fetch.rs                               # 328 行；WebFetch、DNS 校验、重定向、正文解析
        └── cache.rs                               # 155 行；抓取结果本地缓存和读取路由
```

主执行路径：

```text
Cursor IDE / Cursor CLI
  │
  ▼
local_app::proxy
  ├── 判定 local / upstream
  └── 路由 Cursor RPC
        │
        ▼
api::cursor::handlers
  │
  ▼
ConversationRuntime::spawn
  │  会话持有、取消、checkpoint
  ▼
RunEngine::run_claimed
  │
  ├── 读取 checkpoint/history
  ├── 估算上下文并按需压缩
  ├── ProviderRouter::stream
  │     └── OpenAI Chat / OpenAI Responses / Anthropic / plugin provider
  ├── 工具调用 → tool result gate → 下一模型轮
  └── 持久化 checkpoint、usage、输出
```

### 2.2 `gateway` 当前结构

`internal/` 下 Go 文件共 118,470 个物理行；`frontend/` 下 Vue/TypeScript/JavaScript 共 13,918 行。两项都包含空行、注释和目录内测试，不包含生成的协议代码目录。

```text
gateway@2fc04e7
├── main.go                                             # 29 行；Wails 入口
├── internal/
│   ├── app/runner.go                                   # 505 行；桌面生命周期、CA、MITM、更新器
│   ├── client/lifecycle.go                             # 343 行；启动/停止本地代理和 Cursor 设置
│   ├── mitm/                                           # HTTPS MITM，Cursor 流量接管
│   ├── backend/
│   │   ├── host.go                                     # 1,240 行；本地 HTTP/Connect 路由装配
│   │   ├── server/policy.go                            # 15 行；local/upstream 策略类型
│   │   ├── forwarder/
│   │   │   ├── service.go                              # 4,227 行；BidiAppend、RunSSE、provider pass
│   │   │   ├── actor.go                                # 1,783 行；mailbox 状态推进和 provider 终态
│   │   │   ├── compaction.go                           # 1,931 行；预算压缩和摘要持久化
│   │   │   ├── file_store.go                           # 1,146 行；state.json/context.json
│   │   │   ├── checkpoint_blobs.go                     # 370 行；Blob ACK 和 checkpoint 发布
│   │   │   ├── token_usage.go                          # 454 行；会话导入、token anchor 和快照
│   │   │   ├── tool_result_replay_truncation.go        # 394 行；回放预算
│   │   │   └── tool_catalog.go                         # 314 行；模式工具目录
│   │   └── agent/
│   │       ├── model/
│   │       │   ├── router.go                           # 856 行；OpenAI/Anthropic 和凭据解析
│   │       │   ├── openai.go                           # 2,502 行；Chat/Responses 流协议
│   │       │   └── fallback_router.go                  # 648 行；渠道 fallback 和恢复预算
│   │       └── bridge/
│   │           ├── exec/bridge.go                      # 3,143 行；本地执行工具桥
│   │           └── interaction/bridge.go               # 985 行；交互工具、WebSearch、WebFetch
│   ├── gateway/server.go                               # 461 行；OpenAI 兼容 Gateway 入口和鉴权
│   ├── subscriptionauth/
│   │   ├── types.go                                    # 220 行；订阅账号接口和 ProviderKind
│   │   ├── codex.go                                    # 781 行；Codex OAuth、刷新和凭据
│   │   └── grok.go                                     # 395 行；Grok OAuth 和账号操作
│   └── netproxy/                                       # 统一出站 HTTP transport
├── cursor-cli-docker/                                  # Cursor CLI Docker/WeTTY 产品入口
├── tools/cursor-cli-model-pool/                        # CLI 模型池独立模块
├── tools/log-analyzer/                                 # 脱敏日志分析器
└── frontend/                                           # Vue/Wails 管理界面
```

完整主路径：

```text
Cursor IDE
  │ HTTPS
  ▼
internal/mitm
  │
  ▼
backend.Host + PolicyMiddleware
  │
  ├── BidiAppend
  │     └── ConversationFileStore → state.json/context.json
  │
  └── RunSSE
        └── StreamBroker / ActiveStream mailbox
              │
              ▼
            driveProvider
              ├── snapshot checkpoint
              ├── compile conversation
              ├── maybeCompactBeforeProvider
              ├── Router / FallbackAwareRouter
              │     ├── OpenAI Chat
              │     ├── OpenAI Responses
              │     ├── Anthropic
              │     └── Codex/Grok subscription credentials
              ├── actor 处理流事件、工具和 terminal
              ├── checkpoint Blob ACK
              └── history、usage、观测输出

其他本地客户端
  │ OpenAI Chat/Responses + Bearer
  ▼
internal/gateway.Server（只允许 loopback）
  └── 复用 ProviderGateway
```

### 2.3 所有权差异

`main` 把一次模型循环、压缩和 checkpoint 收敛在 `RunEngine`；`gateway` 把同一职责分散在 `service.go`、`actor.go`、`compaction.go` 和 `ActiveStream`。因此迁移 P0 时最危险的不是算法翻译，而是把新状态放错所有权位置。

迁移原则：

- provider 错误分类属于 `internal/backend/agent/model`。
- 恢复次数属于整个 `ActiveStream`/Run；恢复许可所需的模型输出和工具状态属于当前失败调用，按调用生命周期重置。
- 压缩执行和压缩历史生成属于 `internal/backend/forwarder/compaction.go`。
- provider 终态之前是否改为恢复动作属于 `actor.go`。
- `service.go` 只继续负责重新编译并发起下一次 provider pass。

## 3. 必须借鉴的 P0 能力

## 3.1 P0-1：provider 上下文溢出后自动压缩并重试

### 已核实的上游行为与本地事实

`fdae9c4` 修改 6 个文件，新增 663 行、删除 23 行。`main` 在 `server/src/run/engine.rs` 中用 `overflow_compacted` 限制一次恢复，调用 `auto_compact` 后继续模型循环；`server/src/run/compaction.rs` 提供溢出识别、摘要历史裁剪和预算检查。其实现是参考，不自动等同于 gateway 的安全或生命周期合同。

`gateway` 的实际基础为：

- `maybeCompactBeforeProvider` 只在调用前按预算触发压缩；actor 的失败路径尝试流续写后关闭流，没有 provider 拒绝后的压缩恢复入口。
- `buildCompactionSummaryMessages` 已生成 `system + user` 两条消息，摘要请求以 user 结束是已有行为，不需要新增另一套历史准备器。
- `beginPendingCompaction` → pre-compact hook → `startPendingCompactionSummary` → `handleCompactionEvent` 已是异步流程。
- `applyCompactionPlan` 先在候选会话上重编译、验证预算，再 append-only 写入历史；已有原始历史保留、当前轮保护和 token anchor 清理。
- `buildFallbackCompactionSummary` 当前用于摘要成功但为空的情况；摘要 provider 返回错误会失败退出，不能把“摘要 overflow 后使用 fallback”写成已有能力。
- `publishCheckpoint` 可能仅排队等待 Blob ACK；当前自动压缩完成后会请求 `providerActionResume`，并不普遍等待客户端 ACK 才发起下一调用。保留该时序，不另加全局 ACK 等待条件。

### REC-1：恢复范围与错误合同（本次确认）

**恢复许可按当前失败的模型调用判断，恢复次数按整个 Run 计算。**

- 允许：可信 provider 溢出错误，当前调用未产生文本、thinking、工具参数/占位输出或工具执行，当前没有未完成工具/交互，本 Run 尚未尝试溢出恢复。
- 此前调用已经输出内容或执行工具，不构成拒绝理由。已完成工具的调用 ID、结果和证据仍在历史中；恢复流程不得把这些调用重新派发。模型重新读取历史不等于重新执行工具。
- 当前调用已有任一模型生成输出、处于截断流 continuation、尚有未完成执行/交互，或流已取消/终结时，不启动溢出恢复。握手、usage 和普通协议元数据不计为模型生成输出。
- 不用已清零的 `ToolInvocationCount` 或当前文本缓冲推断过去是否有输出；在本次 provider done 清理之前捕获当前调用观察状态，缺失的标记只加到现有每调用状态，不创建通用副作用追踪系统。

错误分类位于 `internal/backend/agent/model`：

1. 复用实际类型 `HTTPStatusError` 和 `ProviderTerminalStatusError`。HTTP 400/413 必须同时具有溢出 code 或已知溢出 message；状态码自身不是溢出证据。401/403/429/5xx、网络错误和取消不因 body 文本而变成可恢复错误。
2. 识别 `context_length_exceeded`、`model_context_window_exceeded`，以及 `prompt is too long`、`context window exceeded`、同时包含 `maximum context length` 与 `token` 的 provider 错误消息。只读错误结构，不扫描用户文本、工具内容或完整日志。
3. HTTP 错误在摘要化前提取必要的 code；HTTP 200 内的协议失败事件可按明确的溢出 code 分类。只含一般 prose 且没有可信状态/code 的流内错误维持原有失败处理。
4. 必要时最小补充 adapter 的错误字段映射，不重写 OpenAI/Anthropic 流解码器。原有错误类别、脱敏和渠道 fallback 规则保持兼容；溢出识别是恢复条件，不把一般 4xx 变成自动重试。

### REC-2：状态推进与所有权

```text
当前 provider done（尚未对客户端终结 Run）
  → 捕获当前调用输出/工具状态，识别可信 overflow
  → actor 接受恢复，标记本 Run 已尝试，并结束失败调用的观测/usage
  → 强制规划压缩，不再要求旧估算先超过阈值
  → 复用异步 pre-compact hook、摘要生成及候选预算检查
  → 追加压缩记录，更新本地 checkpoint/token anchor
  → 按现有 Blob ACK 协议发布 checkpoint，并请求 providerActionResume
  → 重新读取已提交历史、重编译，开始新的普通模型调用
  → 成功继续；若再次 overflow，则一次性失败终结
```

实施约束：

- `ActiveStream` 增加一次性恢复标记，只在新 Run 初始化时清零，不在 provider pass、hook、摘要结束时清零；不新增磁盘恢复标志。进程重启/新的显式运行仍按原有恢复合同处理，不承诺跨 Run 共用这一次额度。
- 入口返回值若使用 bool，其含义必须是 `accepted`（已接管该错误并启动异步流程），不是“压缩已经成功”。actor 不同步等待 hook、模型或 ACK，避免阻塞回复和取消处理。
- 恢复只复用一条压缩执行链。forced trigger 绕过触发阈值，但不绕过当前输入保留、候选编译和预算检查。没有可压缩内容时直接终止，不原样重发。
- 同一时刻仅有一个 provider/摘要活动任务。复用现有 token、mailbox 和取消逻辑忽略旧调用迟到事件；下一调用调度前检查终态，已接受取消后不得启动新调用。已有在途调用按现有取消机制结束，不承诺撤销已经发出的 HTTP 请求。
- `request_id / conversation_id / turn_seq` 不变；失败调用、摘要调用、恢复调用分别使用独立 `model_call_id`。`ProviderPassCount` 沿用 `driveProvider` 的现有计数，不把摘要额外计作普通 pass，也不把 pass 数当作 HTTP 请求数。
- 压缩成功的本地历史提交是重编译依据；Blob ACK 仍负责客户端 checkpoint 发布。ACK 的错误和超时沿用现有失败策略，不加入新的全局等待屏障。
- 每 Run 一次恢复仅约束这条“错误→压缩→继续”分支，不改变正常工具循环的自动压缩，也不重置或扩大现有单调用 HTTP/fallback 预算。恢复所需摘要及新模型调用是额外的独立调用，其 usage 和失败调用已知 usage 均按现有模型调用记录累计，缺失字段不按零覆盖。

### REC-3：摘要失败、终态与数据保留

摘要预算、输入裁剪和 user 结尾由 §4.1 定义，与本能力同批完成。

- 摘要成功且非空：使用该摘要；成功但为空：继续使用现有 fallback summary。
- 摘要请求本身明确 overflow：本次新增为使用一次本地 fallback summary，再执行同一候选编译/预算检查，不再次请求摘要模型。其他摘要错误、取消、hook 错误及持久化错误仍按现有错误路径停止，不扩展为“任意失败都降级”。
- 无可压缩内容、当前必要输入仍超窗、fallback 后仍超窗或恢复调用再次 overflow：返回 `context_overflow_after_compaction`，该溢出终态为非 retryable。其他故障保留原错误语义，不一律改写为 overflow。
- 候选验证或持久化前失败，不替换有效历史；提交后发生 ACK/连接失败，保留已提交的追加记录，不覆盖回旧 `state.json/context.json`。原始 canonical 历史保持可读，不引入新 schema 或批量重写。
- 恢复本身不重派发已有工具。后续模型因新决策发起的新工具调用仍遵循原工具规则，不新增命令去重或禁止用户重跑工具的机制。

### 实施落点与验收

优先修改 `model/http_error.go`、`forwarder/types.go`、`actor.go`、`compaction.go`、`service.go`，必要的错误字段映射改动落在现有 adapter。恢复辅助逻辑确需隔离时再新增 `overflow_recovery.go`，不预设文件行数；测试优先扩展现有错误、压缩和流生命周期测试。

最低验收：

1. 本地估算未超限，provider 首次 overflow：实际进入压缩，重编译后同一 Run 成功。
2. 前一个调用已执行工具并保存结果，当前调用零输出时 overflow：可以恢复，已有工具执行次数不增加，结果关联不丢失。
3. 当前调用已有文本/thinking/工具输出或执行：不恢复；普通 400、401、429、超时和截断不误触发。
4. 摘要 overflow：只尝试一次本地 fallback；候选仍超限、必要输入自身超窗、无可压缩内容时停止，原始历史仍在。
5. 恢复后再次 overflow：一个非 retryable 溢出 terminal，无重复恢复；客户端仍只经历一个 RunSSE 生命周期。
6. 取消、旧 provider done、旧摘要回复和 Blob ACK 交叉：终态不复活、不并发启动模型、不重复提交工具结果；针对该路径运行 race。
7. 摘要请求以 user 结束，保留/裁剪完整语义单元；普通工具续跑请求不强制改成 user 结尾。
8. 失败调用、摘要及恢复调用分别记录已知 usage；append-only、当前轮保留、fallback channel 和 checkpoint 协议回归通过。

## 3.2 P0-2：CLI 本地模型路由与凭据隔离

### 已核实的现状

`main@1268e99` 的 `server/src/api/cursor/handlers.rs` 同时注册下面四条路径，`model_catalog.rs` 使用 `cursor-byok-local` 和省略的 Base URL。`gateway` 的 `host.go` 只注册其中两条旧路径：

```text
/agent.v1.AgentService/GetUsableModels             # 本地待增加
/aiserver.v1.AiService/GetUsableModels              # 本地已有
/agent.v1.AgentService/GetDefaultModelForCli       # 本地待增加
/aiserver.v1.AiService/GetDefaultModelForCli        # 本地已有
```

`buildCLIModelDetails` 目前把 `adapter.APIKey`、`adapter.BaseURL` 写入 `apiKeyCredentials`；`mocks_test.go` 的元数据和 protobuf 测试还在断言这一行为。迁移应修改旧测试契约，不能把旧测试通过理解为凭据边界安全。现状证明密钥进入客户端，不证明已经发生外泄。

### CLI-1：接口与凭据合同

1. 四条路径均接入既有本地路由策略；同一方法的两条路径共享对应 builder。`GetUsableModels` 返回模型列表，`GetDefaultModelForCli` 返回默认模型，二者并非相同响应结构。保持现有默认选择顺序和无模型时的合法空响应。
2. `apiKeyCredentials.apiKey` 固定为 `cursor-byok-local`；JSON 省略 `baseUrl`，protobuf 不设置其 optional 字段。既不投影真实地址，也不把本地 backend/Gateway URL 硬编码到该字段。
3. 保留现有 channel ID、显示名、能力和参数投影；ID 仍是服务器可解析的渠道标识，不改成 provider model name，不改变依赖该 ID 的 CLI 模型池合同。
4. 请求继续通过现有 CLI endpoint 进入本地 `RunSSE`，由配置 manager、model router 和 `subscriptionauth` 在运行时解析渠道及凭据。哨兵只满足客户端字段要求，不是新的鉴权 token，不传给真实 provider。
5. 不更改 Docker、WeTTY、反向隧道、官方登录或本地 Gateway 鉴权。local 路由失败不隐式转官方；未知模型继续使用现有明确错误，不新增静默默认模型回退。

### 实施与验收

优先修改 `host.go`、`server/upstream/mocks.go` 及既有路由/编码测试；P0 不要求创建 `cli_model_catalog.go` 或新的跨层类型。

- 方法别名一致：四条路径分别验证列表/默认模型语义，覆盖 JSON 和 protobuf 编码及 local/upstream 策略。
- 响应使用合成秘密进行断言：没有配置 API Key、上游 Base URL；Base URL optional 字段确实未设置，不只是 getter 返回空串。
- 合成 provider 验证选定 channel ID 能解析到真实服务器端配置；覆盖静态 BYOK、订阅凭据及 fallback 既有路径，不扩展账号功能。
- 使用目标版本的真实 CLI 验证 `models`、默认模型和选定模型的一次请求确实回到本地，provider 收到的是服务器凭据而非哨兵。远程 Docker/WeTTY 接入按现有 endpoint/隧道验证，不把容器 loopback 当成本机地址。
- 若真实 CLI 不支持省略 Base URL 的行为，暂停该工作包并记录版本/请求路径反证，不以重新暴露真实凭据或改写部署入口掩盖兼容问题。单测完成不代替这项运行验收。

## 3.3 P0-3：`Bash` / `bash` 完整 Shell 别名

### 上游能力

`main` 的 `9e2d224` 在 codec 的 render、request、response 三侧补齐 `Bash`：

- 流式占位能识别 Bash。
- 请求解码能识别 Bash。
- 完成结果能映射到 Shell result。
- 大小写不敏感。

### `gateway` 当前缺口

源码核对显示：

- `internal/backend/agent/bridge/exec/bridge.go` 只有 `case "Shell"`。
- `internal/backend/forwarder/events.go`、`execution_evidence.go`、`service.go`、`tool_result_replay_truncation.go` 也只覆盖 `Shell`。
- 工具目录只声明 `Shell`。

模型或旧会话返回 `Bash` 时，可能在执行、事件投影、证据或回放任一阶段失配。只改执行 switch 不足够。

### TOOL-1：名称规范化合同

增加一个最小纯函数 `CanonicalToolName`，优先放入 `internal/backend/agent/core` 既有合适文件；无合适文件时再新建 `tool_name.go`，不新增注册框架。

```text
Shell, shell, Bash, bash → Shell
其他名称保持原值
```

- 在 `service.go` 工具入口和流式事件投影入口规范化，再交给现有 bridge、execution evidence 和 replay。bridge 的独立入口及存量历史读取也使用同一函数，不在多个 switch 中复制别名列表。
- 工具目录仍只发布 `Shell`；补别名不应绕过 Agent 模式、审批、sandbox 或既有工具可用性判断。
- 只改变工具名称，不改变调用 ID、参数、权限、reasoning 元数据、执行结果关联和已有幂等键。
- 新记录使用规范名；旧历史在读取/投影时兼容，不批量重写磁盘历史。

### 验收标准

同一组 fixture 以四种名称经过 provider tool event → 流式占位 → bridge 请求 → 完成编码 → execution evidence → history replay → truncation budget，得到同一 Shell 行为；确认调用 ID 不变、权限不变、原始历史未被迁移。四种名称只各执行对应请求，不因回放再次执行。

## 3.4 P0-4：WebFetch 直连 DNS 防护与代理兼容

### 已核实的现状

`gateway` 已有 HTTP/HTTPS、localhost/字面量私网拒绝、重定向 URL 复查、最多 10 次重定向、约 2 MiB 正文读取上限、32 KiB Markdown 返回及 UTF-8 安全截断。`isBlockedWebFetchHost` 不解析域名；域名解析到内网仍可能连接，URL userinfo 也尚未拒绝。

`main` 的 `server/src/search/fetch.rs` 在每次请求前解析地址并用 `resolve_to_addrs` 固定解析结果，拒绝 URL 凭据、最多跟随 5 次重定向。但 `safe_resolution` 对**域名**解析到 `198.18.0.0/15` 有代理兼容例外，并非无条件拒绝所有非公网地址。HTTP 客户端的解析固定也不能证明显式代理最终使用了该地址。

### FETCH-1：按实际出站路径处理（本次确认）

共同行为：

- 所有模式拒绝 URL userinfo；只允许 HTTP/HTTPS，保持 localhost 拒绝。
- 字面量目标先规范化 IP（包括 IPv4-mapped IPv6），使用同一个公网地址判定。覆盖 loopback、私网、链路本地、unspecified、multicast、共享地址、文档/基准测试及保留范围；优先复用标准库和一份固定前缀表，不引入在线地址库或配置白名单。
- 每个请求及 redirect 都调用现有 `netproxy.ProxyForRequest`，以本次请求最终是否有显式代理为准，不用全局 `NetProxyActive` 推断。`NO_PROXY` 命中的请求按直连处理。该跳的代理选择固定到对应 transport，避免先按直连校验、发送时又换成代理。

**无显式代理（直连）**：

1. 使用可注入的解析函数查询 A/AAAA；解析失败、空结果、任一非公网结果则拒绝。
2. 使用已通过验证的地址集合拨号；地址切换仅在该集合内，不再次解析原域名。保留 URL 域名、Host 和 TLS ServerName，不把 URL 改写成 IP。
3. 使用 WebFetch 专用的 transport/客户端配置，不修改共享 provider 或 WebSearch transport；可以复用已验证公网连接，不引入额外连接审计或禁用全局连接池。
4. DNS、拨号、重定向和正文读取共同受现有 15 秒请求超时约束；不能把新增 DNS 放在无超时的前置步骤中。

**使用显式 HTTP/HTTPS/SOCKS 代理**：

- 沿用 `netproxy` 的代理连接方式，保留域名交给代理处理；不强制本地 DNS 校验或 IP 固定，不新增代理认证、信任探测或域名抓取禁用开关。
- 本地仍执行共用 URL/字面量目标检查。代理本身可以在 loopback；检查对象是请求目标，不是禁止本机代理地址。
- **接受的限制**：代理可能将域名解析到非公网目标，本次不解决该风险。完成定义只承诺代理模式的 URL/字面量检查及兼容性，不承诺最终目标地址受控。

**透明代理/Fake-IP**：透明接管不一定表现为显式代理，无法从当前接口可靠识别。若走直连分支却解析到 `198.18.0.0/15`，按非公网地址拒绝，不复制上游的广泛例外、不自动识别或信任 TUN。此类环境可按既有能力使用显式代理；具体环境是否需要调整以运行验收为准，不声称所有 Fake-IP 环境零影响。

### 实施与验收

从 `interaction/bridge.go` 提取 WebFetch 网络实现到 `webfetch.go`；公网判定可先留在同文件，只有复用需要时再分文件。通过 resolver/dialer/代理替身测试，不依赖真实公网；复用现有大小限制、格式转换、10 次重定向和错误结果协议。DNS/redirect 地址限制只应用于 WebFetch，不搭车修改 WebSearch。

验收覆盖：公网直连成功；域名解析到私网或混合公网/私网时拒绝；实际拨号使用已验证 IP；重定向重新判断代理路径并重新校验；URL 凭据和字面量非公网拒绝；显式代理域名请求保持可用、无需本地解析；`NO_PROXY` 和 Fake-IP 限制符合上述合同；超时仍有界。真实部署另做一次当前代理方式的抓取验证。

WebCache、端口白名单、代理强制禁用、全局 transport 改造均不在本次范围内。

## 4. 已有能力，但应借鉴改进

## 4.1 P1-1：摘要输入预算与轮次裁剪（恢复必需部分纳入 P0）

### 已有行为与真实差异

`buildCompactionSummaryMessages` 已将旧摘要、`CompactedTurns`、hook 和手动指令组织为 `system + user`，不是上游的原始消息数组重放。现有 `buildContextCompactionCandidates`、`buildCurrentTurnCompactionCandidate`、`autoCompactionPreservedEntryIndexes` 已有轮次及当前工具结果/推理载体保护。

需要补的是**摘要请求自身的完整预算和按现有语义单元裁剪**，不是再增加一套 user-terminated 函数。正常/手动压缩和 provider overflow 压缩共用该输入准备逻辑。

### CMP-1：预算与裁剪合同

- 窗口 `W` 沿用 `compactionContextWindowSize` 的来源：会话已有窗口值，否则使用现有默认值；不新增模型窗口目录或从 provider 自由文本猜测窗口。配置/会话窗口与实际 provider 不一致的剩余误差由有界 overflow 恢复兜底，不承诺一次压缩必成功。
- 本版将 reserve 定为 `R = max(10000, floor(W / 10))`，保留现有 10,000 token 下限；此为本地选择，不是已验证对所有模型最优的参数。统一普通压缩触发和候选检查使用 `B = W - R`，不在不同入口叠加 reserve；`B <= 0` 按现有超窗终态停止，不偷偷扩大窗口。
- 摘要输出仍为 `O = 4096`；发送前要求**完整摘要请求估算 + O <= B**。估算包括 system prompt、旧摘要、轮次文本、hook、手动指令和最终摘要指令，不能只计算轮次文本。普通模型请求的输出参数策略不因本项变更。
- 超预算时从摘要请求视图中的最旧完整 `CompactedTurns` 单元开始移除，每次重建并估算，保留最近的完整单元及既有摘要/指导信息；只裁剪送给摘要模型的副本，不修改 `PendingCompaction` 的归档范围和完整计划，不把 tool call/result 拆成孤立消息，不修改 canonical 原始历史。
- 若最后一个可保留单元连同固定输入仍不适合摘要请求，不发出注定超预算的摘要请求、不发送空历史；改用一次现有本地 fallback summary 构造候选，再验证恢复后的完整上下文。候选仍超限则停止。
- 当前用户必要输入、最新工具调用/结果及其 reasoning 载体仍由既有保护规则保留。此前已完成的旧工具结果可按既有规则转成摘要/预算内投影，但原始调用和结果不删除、不重新派发。
- `user` 结尾只约束摘要请求；压缩后的普通 provider 请求由既有 compiler/adapter 按实际工具历史生成，禁止为满足字面测试追加伪造用户消息。

验证覆盖不同窗口、全部固定输入计入预算、完整单元裁剪、最新必要内容自身超窗、user 结尾和原始历史追加保留。200K/1M 窗口应体现比例 reserve，小窗口不出现负预算放行。测试若发现窗口来源不适用，应明确修正事实，而不是增加隐式例外。

## 4.2 P1-2：工具结果共享预算规则

### 已有行为与目标

`exec/bridge.go` 管理 MCP 结构和资源数量，`interaction/bridge.go` 管理 WebSearch/WebFetch 文本，`tool_result_replay_truncation.go` 管理历史回放。上游 `tool_call_result/gate.rs` 值得借鉴的是统一预算、UTF-8 和有界截断，不是其整个类型体系。

**RESULT-1：共享规则，保留必要投影差异。**

- 先冻结当前每类工具、每种用途的预算和输出样本，不在结构重构中顺带修改额度或删除信息。
- 共享工具名称/用途到预算的解析、文本截断、字节/数量单位和提示构造；结构化 proto/JSON 的转换仍留在现有使用方。可以提取小型 `internal/backend/agent/toolresult` 包，但不要求建立 `Payload any → Payload any` 的通用转换框架。
- 用途明确区分客户端展示与模型历史回放，调用方显式选择。图片原始数据可用于客户端展示，回放继续省略 Base64；Shell 继续保留 stdout/stderr/interleaved 的分字段回放预算；历史编辑错误可继续压缩冗余说明。
- 所有截断提示自身计入对应预算，区分 bytes/items，保持 UTF-8；对已截断结果重复应用相同规则不再增长提示或重复缩短内容。必要元数据如调用 ID、退出码和错误类别保持完整。
- 迁移按 MCP、Shell/Grep、WebFetch 等工具逐类进行：迁入共享规则后删除旧重复计算，保留真正承担展示/回放转换的薄调用层。最终是预算规则单一来源，不是只允许一个函数或所有 payload 必须相同。

验收比较每种用途迁移前后样本，包括图片省略、Shell 分字段、结构化资源列表、超短预算、中文/UTF-8、提示字节数及重复应用。规则提取不应改变执行证据、历史格式或工具权限。

## 4.3 P1-3：CLI 模型目录与运行路由一致性

### MODEL-1：共享配置规则，不固定运行时凭据

`serverSystemSettings.ResolveModelAdapters` 已从配置 manager 的 `LegacyRuntimeSnapshot` 读取目录数据，`Router.Stream` 通过 `SelectChannelForModel` 选择渠道并调用 `applyRuntimeCredentials`。这不是两份独立配置，而是同源配置的不同投影。

最小改进：

1. 保留既有 manager 和 channel ID 作为事实源，提取实际重复的 ID、thinking 参数和能力转换纯函数；没有重复就先增加一致性契约测试，不为了 `ServerModelCatalog` 名称新建服务。
2. 静态模型标识/参数/能力规则供目录和运行解析复用。目录加载不预选账号、不刷新 token、不把带凭据的运行计划缓存给客户端。
3. 订阅账号轮转、短期凭据和 fallback 计划仍在请求到达时解析。配置变更后，旧 CLI 选择的 ID 无法解析时返回现有错误，允许用户刷新目录，不静默换模型。
4. 同一组静态 BYOK、Codex/Grok 和 fallback fixture 同时断言目录 ID、能力、thinking 参数及实际请求目标一致；逻辑 fallback ID 不误变为 CLI 模型池允许的物理 ID。

本项在 §3.2 完成后开展，不重写 provider adapter、账号系统或 CLI 模型池。

## 4.4 P1-4：Cursor 接管前的正常重启

### 已核实的现状与采用范围

上游会在代理设置不匹配时终止 Cursor；本地 `StartProxy` 目前启动 backend/MITM 后注入设置，没有等价的“运行中 Cursor 重启确认”流程。首版只增加配置变化时的一次确认和正常退出/重启，不新增强制终止、进程恢复服务或启动时普遍弹窗。

### CURSOR-1：交互与失败状态

```text
用户启用/改变代理
  ├── 设置无需变化 → 沿用启动流程，不要求重启
  ├── Cursor 未运行 → 服务就绪后应用设置，不擅自启动 Cursor
  └── Cursor 运行且设置需变更
        → 一次确认“保存工作后重启以应用配置”
        → 用户拒绝：保持旧设置和运行状态
        → 用户同意：请求正常退出，确认退出后完成设置切换并启动 Cursor
```

- 检测和启动只针对已识别的 Cursor 主应用，使用现有安装路径/平台能力，不按模糊进程名批量 kill。
- 首版正常退出等待上限为 30 秒；遇到未保存文件阻止退出、权限拒绝或超时即停止切换、保持旧设置，并提示用户手动退出后重试。没有第二次强制终止确认，也不调用强制 kill。
- backend/MITM 必须先确认就绪，再写入代理设置；只在本次调用中保存被改设置的旧值和服务启动前状态，不新增备份系统。不要停止切换前已经运行的共享 backend/远程 CLI 服务。
- 设置写入失败：恢复本次改动的旧值，仅清理本次新启动且无既有共享用途的服务；Cursor 若已正常退出，可用旧配置尝试重新启动一次。
- 设置已成功应用但 Cursor 启动失败：保留工作正常的代理服务与新设置，明确显示“服务已就绪，Cursor 启动失败，请手动启动”，不误报整体成功、不反复自动启动。
- 更新操作进行中禁用重复触发，结束后恢复按钮；复用现有状态/错误展示，仅补必要的“等待退出/启动失败”文案，不重做管理 UI。

实现入口为现有启用/设置动作和 `internal/client/lifecycle.go`；平台实现、前端实际绑定和用户退出行为在本阶段开始时做定点核查。最先覆盖用户拒绝、退出超时、服务失败、设置失败和正常切换，再做一次真实 UI/应用验证。本阶段独立推进，不阻塞 P0。

## 5. 有价值但应独立实施的 P2 产品能力

## 5.1 Google Antigravity 订阅认证

### 上游能力

`d7578bc` 涉及 30 个文件，新增 3,326 行、删除 90 行，包含：

- Google OAuth。
- Antigravity 模型目录。
- 多账号和自动轮转。
- 多轮请求支持。
- 账号状态和资源操作。

### 迁移判断

此能力对希望使用 Google 订阅模型的用户很有价值，`gateway` 当前只有 Codex 和 Grok，确实未提供。但上游实现依赖 Deno 插件运行时和 Tauri 插件 UI，直接搬入会与 `gateway` 的 `subscriptionauth` 重复并扩大攻击面。

正确落点：

```text
internal/subscriptionauth/
├── antigravity.go                       # 新增，≈900 行；OAuth、刷新、模型请求凭据
├── antigravity_usage.go                 # 新增，≈250 行；配额状态
├── antigravity_test.go                  # 新增，≈700 行；合成 OAuth/刷新/轮转
├── oauth_callback.go                    # 新增，≈300 行；通用 loopback callback
└── types.go                             # ProviderAntigravity

frontend/
└── 复用现有订阅账号卡片，不引入插件管理页
```

实施前提：

- 使用合成 OAuth server 测试，不使用真实账号自动化。
- token 持久化权限、前端脱敏和日志脱敏沿用 Codex/Grok 规则。
- 轮转复用现有账号 affinity 与 quota 状态，不再建第二套账号系统。
- Provider adapter 只接受 `subscriptionauth` 输出的短生命周期凭据。

## 5.2 通用 OAuth 回环回调

`main` 的 `plugin/oauth_callback.rs` 把端口绑定、state 校验、callback 页面、code/error 解析和超时集中管理。`gateway` 的 Codex/Grok 流程已有 OAuth，但逻辑按 provider 分散。

当且仅当 Antigravity 开始实施时，提取：

```go
type AuthorizationResult struct {
    Code  string
    State string
}

func AwaitAuthorization(ctx context.Context, request AuthorizationRequest) (AuthorizationResult, error)
```

这是“两种以上 adapter 已真实需要”的 seam；不提前建设通用插件系统。

## 6. 已经对齐：冻结行为，不重复迁移

以下能力在 `gateway@2fc04e7` 已有源码和测试证据。后续只保留回归测试，不因 `main` 存在另一份实现而重写。

### 6.1 OpenAI Chat 终态矩阵

已覆盖：

- `stop + tools` → `tool_calls`。
- 空 finish reason + tools → `tool_calls`。
- `content_filter + tools` → 不完成、不执行观察到的工具。
- `length + tools` → 不执行不完整工具。
- 显式 `tool_calls` / `function_call`。

落点：`internal/backend/agent/model/openai.go` 和 `openai_stream_test.go`。

### 6.2 空工具参数

OpenAI/Anthropic 的空 arguments 已规范化为 `{}`。不再迁移 `main` 的同名修复。

### 6.3 Usage 稀疏字段累计

`turnUsageSnapshot` 已有：

- `UsagePresent`
- `CacheReadPresent`
- `CacheWritePresent`

`usage_store.go` 会保留 presence，已有测试覆盖后续调用省略字段的场景。保持现状。

### 6.4 Checkpoint 与 Blob 完整性

`gateway` 已有：

- SHA-256 Blob ID 校验。
- 错误长度拒绝。
- 缺失 prefetched Blob 失败。
- 客户端确认全部 Blob 后才发布 checkpoint。
- 5 秒 Blob 写入超时。
- checkpoint terminal action 与 subagent ACK 合并。

这部分在客户端 ACK 协议上比 `main` 的 49 行 recovery 模块更完整，不迁移 SQLite/CAS 存储。

### 6.5 Provider 韧性

`gateway` 已有且应保留：

- `FallbackAwareRouter` 多渠道 fallback。
- 首包前安全重试。
- 四阶段 liveness：建连、首个有效事件、流空闲、整段逻辑调用。
- 截断流识别和受限 continuation。
- partial output 融合和重复输出抑制。
- 每个 model call 独立观测。

`main` 的单一 stream idle timeout 不比该体系更深，不迁移。

### 6.6 Tool call ID 与 Task/subagent

`gateway` 已有跨调用 ID 测试、subagent 持久化、checksum、幂等结果、恢复和重调度。`main` 的 `completed.clear()` 是 Rust output dispatcher 的局部修复，不能机械翻译到 Go。

## 7. `gateway` 独有能力：迁移时必须保留

任何 P0/P1 改动都必须通过这些回归门禁：

1. **MITM 与路由策略**
   - `internal/mitm`
   - `ModeLocal` / `ModeUpstream`
   - 精确路由优先级
   - local 失败不隐式切官方

2. **OpenAI 兼容 Gateway**
   - Chat Completions
   - Responses
   - loopback-only
   - Bearer 鉴权

3. **订阅多账号**
   - Codex / Grok
   - usage 刷新
   - quota exhausted
   - 账号 affinity
   - sub2api 导入

4. **Provider 渠道治理**
   - fallback channel
   - capacity limiter
   - managed Codex 请求体边界
   - provider 400 脱敏摘要

5. **流和运行时治理**
   - retrying stream body
   - stream continuation
   - terminal exactly-once
   - checkpoint ACK
   - tool side-effect 门禁

6. **观测与隐私**
   - schema v1
   - 默认关闭的审计正文
   - `tools/log-analyzer`
   - 真实凭据和 prompt 不进入普通日志

7. **CLI 产品**
   - 198 Docker CLI
   - WeTTY
   - cursor-cli-model-pool

8. **本地产品决策**
   - 广告默认关闭
   - 更新策略
   - 浅色主题
   - JSON 历史目录保持现状

## 8. 明确不迁移

### 8.1 整体代码和运行时

- `server/` Rust 服务器整树。
- `apps/desktop/` Tauri/React 桌面。
- `crates/semble-core/`。
- Deno plugin runtime。
- SQLite migrations 和上游 Store。
- Rust `RunEngine` 整体。

理由：这些模块会替换 `gateway` 已运行的 Go/Wails、MITM、JSON 历史和订阅系统，不是增量能力。

### 8.2 产品决策冲突

- 广告 endpoint、广告菜单、广告缓存和跑马灯。
- 仅用于上游 Tauri 桌面的 compact modal、tooltip、window console 行为。
- 上游自动更新布局和 UI 风格。

理由：`gateway` 已有明确的 no-ad 和桌面体验决策。

### 8.3 当前没有迁移收益

- 上游 SQLite 替换 JSON 历史：风险大，且 checkpoint/blob 行为已覆盖。
- WebCache：先完成 WebFetch 安全加固；没有大正文必须本地 URL 化的运行证据。
- `required_permissions`：先确认 Cursor 当前 wire 是否实际发送、`gateway` sandbox 是否消费；没有证据前只允许协议解码兼容，不改变权限执行。
- 通用插件市场：已有两个订阅 provider 不足以证明需要 Deno 插件运行时。
- 上游 resource limit：Tauri 子进程场景专用，不能直接套到 Wails/Go 主进程。

## 9. 分阶段实施与当前状态

### 9.1 实施入口与依赖

本节划分的是**工作包，不是自动提交指令**。后续收到实施指令后直接从对应工作包推进，结构性提取与行为修改分开验证；没有明确 commit 指令时全部保留在工作区，不合入未通过的失败测试。

当前 P0/P1 功能改动均**待实现**；本次仅修订文档。后续功能进度沿用项目已有 `task/todo.md` 和 `docs/process.md`，本文维护行为合同及索引，不新增任务文件或第二份设计。

- 默认先阶段 1，再阶段 2～4；**阶段 1 必须包含 §4.1 的摘要输入预算/裁剪**，不留到 P1 补救。
- 阶段 2（CLI）、3（别名）、4（WebFetch）彼此独立，也不依赖 overflow 完成；阶段 1 被特定环境阻塞时，可先交付这些独立能力。
- 阶段 5 的目录一致性依赖阶段 2，共享工具预算在别名规范化后推进；阶段 6 独立。
- P2 不属于本次已确认实施范围，不是 P0/P1 发布前提。

### 阶段 0：各工作包开始时校准现状

只检查该工作包涉及的基线、入口、测试和外部版本；复用 §3～§4 的已核实事实，不重新开展整仓对比。

每个工作包按 RED → GREEN → REFACTOR 推进：先写能证明缺口的一个行为测试，再实现、复测；不先堆全套红测试，不把失败测试单独当作可合入成果。若已有行为已满足，例如摘要 user 结尾，保留回归断言而不制造生产改动。

### 阶段 1：overflow 恢复及必要压缩输入完善

合同：§3.1 `REC-1`、`REC-2`、`REC-3`，以及 §4.1 `CMP-1`。

1. **1A 错误分类**：覆盖 HTTP 400/413 和带明确 code 的协议失败，以及 401/429/一般 400 反例；只补必要错误信息。
2. **1B 摘要预算**：在现有摘要入口接入完整预算和轮次裁剪，保留 user 结尾、当前轮、hook 和本地 fallback；覆盖摘要输入不适合发送及候选仍超窗。
3. **1C 同 Run 恢复**：先贯通“本地低估→provider overflow→异步压缩→新模型调用成功”，再覆盖“此前工具已完成、当前调用零输出”的恢复场景。
4. **1D 失败与并发**：补齐当前调用已有输出禁止恢复、重复 overflow、取消/迟到事件/ACK、usage 和历史保留测试；在实际修改包运行 race。

阶段验收：§3.1 八组场景通过、普通自动/手动压缩无回归，并用合成 provider 通过实际 RunSSE 入口观察一个逻辑 Run。任何输入/历史保护失败先修复，不用扩大恢复次数规避。回退依据 §10.3。

### 阶段 2：CLI 四路径与服务器端凭据

合同：§3.2 `CLI-1`。

1. **2A 双协议**：补 `GetUsableModels`、`GetDefaultModelForCli` 两个新别名，验证每个方法的两条路径及编码。
2. **2B 凭据投影**：更新旧的凭据外发测试；使用固定哨兵、省略 Base URL，验证 channel ID 不变和服务端真实凭据解析。
3. **2C 实际入口**：真实 CLI 验证模型列表、默认模型及选定模型请求；现有远程 CLI 用户链路另验证 endpoint/隧道可用，不改部署默认值。

阶段验收：四路径和同源响应通过，真实 CLI 请求进入本地 provider 链；未验证的 CLI 版本或部署环境单独列出，不能借编码测试声明已兼容。

### 阶段 3：Shell 别名

合同：§3.3 `TOOL-1`。一个工作包完成共享名称规范化、执行/事件/证据/历史接入及四名称全链测试；目录只保留 `Shell`。验收确认权限、调用 ID、执行次数和存量历史不变，不新增历史迁移。

### 阶段 4：WebFetch 直连防护与代理兼容

合同：§3.4 `FETCH-1`。

1. **4A 保持行为的提取**：提取 WebFetch 网络实现，冻结现有内容格式、大小、超时和重定向上限。
2. **4B 网络行为**：加入共用 URL/字面量校验，按实际代理选择实现直连 DNS 固定和显式代理透传；保持其他 HTTP 客户端不变。
3. **4C 验证边界**：用本地替身覆盖 DNS/redirect/代理/NO_PROXY/Fake-IP 及超时，再验证当前实际代理方式。

阶段验收按模式区分，代理端解析风险是已确认的保留限制，不增加关闭代理、可信代理配置或全模式目标追踪。

### 阶段 5：P1 共享规则

- **5A CLI 一致性**（§4.3 `MODEL-1`）：复用既有配置解析，提取重复的 ID/参数/能力转换；同一模型 fixture 同时验证目录和运行目标，不提前固定账号凭据。
- **5B 工具预算**（§4.2 `RESULT-1`）：逐类提取共享预算/截断规则，保持展示与回放差异；每迁完一类删除对应重复规则，验收包含重复应用、UTF-8、结构和必要元数据。

两项分开实施和回退。§4.1 由阶段 1 交付，此处不再次建设摘要输入路径。

### 阶段 6：P1 Cursor 正常重启

合同：§4.4 `CURSOR-1`。先定点核查目标平台的安装定位、正常退出/启动方式及现有前端绑定，再按既定交互接线；不重新讨论已确定的一次确认和无强制终止范围。

验收覆盖不需重启、用户拒绝、退出超时、服务/设置/启动失败及一次真实正常切换，确认旧设置回退和既有共享服务不被意外停止。若平台缺少正常退出能力，只暂停该平台自动重启并提示手动操作，不静默换成 kill。

### 阶段 7：P2 独立产品候选

Antigravity 和通用 OAuth callback 保留 §5 的方向，等待单独产品确认和设计；不随本次 P0/P1 顺带实现。

## 10. 修改热点、范围与回退

### 10.1 热点

```text
internal/backend/forwarder/service.go       # provider pass、普通恢复调度、工具输入
internal/backend/forwarder/actor.go         # terminal、当前调用状态、取消和异步事件
internal/backend/forwarder/compaction.go    # hook、摘要预算、历史候选与提交
internal/backend/agent/model/http_error.go  # HTTP/协议错误分类信息
internal/backend/agent/model/openai.go      # 必要的协议错误字段映射，不重写解码器
internal/backend/host.go                    # CLI 精确路由及既有 local/upstream 策略
internal/backend/agent/bridge/interaction/bridge.go  # WebFetch 专用网络路径
```

### 10.2 最小修改规则

1. 恢复、工具别名和预算复用现有生命周期；必要的每 Run/每调用字段放在现有状态中，不新建框架。
2. overflow 可以补齐 adapter 错误字段，但不改变已对齐的 finish reason、工具完成和 usage 解码语义。
3. CLI 只调整模型目录投影、路由及共享配置规则，不改 provider 请求协议、部署默认值或认证体系。
4. WebFetch 的网络检查不进入 WebSearch 或 provider 的共享出站 transport。
5. 共享预算删除重复规则，保留必要投影调用层；现有业务 fallback 与新旧重复实现不是同一概念，不能一并删除。
6. 不按预计代码行数新建文件，不为本次迁移增加通用开关、审计服务、代理信任配置或额外审批。

### 10.3 最小回退合同

- **恢复/压缩**：候选验证失败不提交；提交后仍保持原始 canonical 历史和既有 append-only 格式。代码回退前用旧 reader 读取迁移后的合成历史，确认没有新增必需 schema。运行已写入的新历史不能靠覆盖旧目录回退；数据完整性反例出现时停止该工作包发布，保留现场，不自动重放工具或重置用户会话。
- **CLI**：路由别名、投影和测试按独立工作包回退，不改账号数据或隧道。若兼容有反证，停止发布该变更或暂停受影响 CLI 用法；不以恢复真实密钥外发作为自动降级。
- **Bash/共享预算**：只回退对应函数接线，旧历史无需迁移；保留 ID 和结果格式兼容，不丢弃原始证据。
- **WebFetch**：失败请求返回既有工具错误格式；直连校验失败不自动切代理绕过。兼容问题按已确认的显式代理路径处理或撤回该工作包，不在新路径内回退到无校验抓取。
- **Cursor**：按 §4.4 保留本次设置旧值和服务原状态；设置失败时恢复，Cursor 启动失败时显示部分成功并允许手动启动。只恢复本次修改，不停止既有共享服务，不建设通用恢复系统。

### 10.4 修订核查与证据边界

- **已核实事实**：固定 Git 基线、四条 CLI 上游路径/本地两条旧路径、真实凭据投影、摘要 `system + user`、异步压缩与候选先验证后追加、上游保留网段例外，均已从代码核对。
- **已确认选择**：按当前失败调用恢复、每 Run 一次；显式代理保持兼容并接受代理端解析限制。其余实施默认值写在各合同中，不作为现有能力冒充。
- **待运行验证**：实际 CLI 版本的哨兵行为、当前代理/TUN 环境、Cursor 平台退出/重启及新增并发恢复。验证入口和失败分支已列明；只影响相应工作包的验收，不另加全项目安全审批。
- **反向走查**：正常恢复从 done→hook→摘要→本地历史→新调用；最高风险路径是取消/迟到回复与历史提交交叉；回退路径按 §10.3。文档不再要求 actor 同步等待 ACK，不再把之前已完成工具视为恢复禁区。
- **就绪范围**：P0 及 §4.1 可据本文开始实现和测试，功能状态仍为待实现；P1 共享规则按已列合同推进，Cursor 阶段先校准平台事实；P2 未批准。本次为主对话自审，未进行独立外部评审，不将设计就绪等同于运行验收通过。

## 11. 验证方案

### 11.1 开发迭代：只验证受影响范围

每个小改只运行相应包和用例，不机械重复所有包：

- 错误分类：`go test -count=1 ./internal/backend/agent/model -run '<相关用例>'`。
- 压缩/恢复：`go test -count=1 ./internal/backend/forwarder -run '<相关用例>'`。
- CLI：`go test -count=1 ./internal/backend/server/upstream ./internal/backend -run '<相关用例>'`。
- 工具桥：按改动选择 `./internal/backend/agent/bridge/exec`、`./internal/backend/agent/bridge/interaction` 和 forwarder；共享包创建后再加入该包。
- 前端/客户端：按实际修改选择 `./internal/client/...`、`./internal/cursor/...` 及 `frontend/package.json` 已存在的测试/构建脚本。

上面 `<相关用例>` 在执行时替换为真实测试名，不是可原样执行的测试表达式。Go 修改使用 `gofmt`，有代码修改时运行对应包 `go vet`；文本改动检查 `git diff --check`。新增未跟踪文档还需单独检查内容，因为普通 diff 不包含它。

### 11.2 工作包与 P0 收口

- 每个工作包结束跑受影响包完整测试；恢复相关并发修改在 model/forwarder 的相应用例及包运行 `go test -race -count=1`。纯名字投影和文档修改不要求全项目 race。
- P0 收口运行一次根 module 的 `go test -count=1 ./...`、`go build ./...`，确认包含 backend、Gateway、subscriptionauth、MITM 的既有回归。外部 CLI 安装或其他环境导致失败时，报告具体失败及已通过子集，不声称全量通过，也不改产品代码绕过环境。
- 根命令不覆盖独立 module：`cursor-tab-server`、`tools/log-analyzer`、`tools/cursor-cli-model-pool`。只有该模块代码或其共享契约受影响时，进入对应目录运行相关测试；CLI 渠道 ID/选择规则变动时应覆盖模型池。
- 前端有实际修改时运行现有测试和生产构建；没有前端改动不跑无关 UI 构建。不要求真实账号自动化或生产故障注入。

### 11.3 运行链与验收对象

1. **恢复近真实链**：合成 provider + 实际 RunSSE，验证首次 overflow 后成功、再次 overflow 停止、之前工具不重复执行、当前输出禁止恢复、取消和已知 usage 累计。
2. **CLI 链**：四路径编码/路由测试 + 服务端凭据解析 + 目标版本真实 CLI 请求。真实远程部署只用现有 endpoint 和隧道验证，不能借本机 fixture 证明全部远端可用。
3. **Shell 链**：四名称从模型事件贯通到执行、结果、证据和回放，权限与执行次数不变。
4. **WebFetch 链**：直连 DNS/固定 IP/redirect 测试与显式代理兼容测试分开报告；代理端最终解析不属于本次完整防护承诺，Fake-IP 限制必须准确展示。
5. **P1 链**：共享预算验证不同用途的样本及重复应用；目录一致性验证运行目标；Cursor 重启使用真实 UI/应用验证确认、取消及错误展示。

fixture 使用合成 prompt、地址和凭据；普通日志不新增完整错误 body、摘要正文或真实密钥。不新建全仓 secret 扫描流程，仅检查本次新增响应、日志和测试产物。

### 11.4 最小可观测信息

复用现有 request/model call/usage/terminal 记录。恢复事件只补足原因、尝试次数、失败调用和恢复调用关联、压缩结果；必要输出/工具状态来自当前失败调用，不能把前序调用计数误写为零。测试断言 terminal 与工具执行次数，无需为每个断言新增生产指标。

示例语义：`recovery_kind=context_overflow_compaction`、`recovery_attempt=1`、失败/摘要/恢复 `model_call_id`、`compaction_result=success|failed|still_over_limit`。不要把 provider pass 计数硬编码为真实 HTTP 请求数。

## 12. 完成定义

### P0 完成

- §3.1 八组恢复场景通过，包括相关 race；§4.1 摘要预算和裁剪同批完成，user 结尾作为已有行为保留。
- CLI 两个方法共四条路径、哨兵/省略 Base URL、服务端凭据解析及目标 CLI 运行链通过。
- Bash 四名称全链通过，目录仍只发布 Shell，历史和权限不变。
- WebFetch 直连 DNS/固定地址、各跳重查、全部模式 URL/字面量检查通过；显式代理保持可用，保留风险和 Fake-IP 限制按 §3.4 说明。
- §11 所需回归和根 module 收口结果有记录；无法运行的对象按对象列缺口，不升级为全量验收完成。
- 没有引入新运行时、存储体系、全局代理限制、通用开关或重复恢复框架。

### P1 完成

- 工具预算规则单一来源，真正重复逻辑已删除；展示/回放的必要差异、图片省略和证据保留通过。
- CLI 目录与运行路由共享静态解析规则，凭据和账号选择仍在运行时完成。
- Cursor 只在必要时确认正常重启，拒绝、超时及失败状态符合 §4.4，无自动强制终止。

### P2 完成

在独立产品设计批准后，才按其 OAuth、账号、配额、轮转和人工登录验收定义完成；本文的候选文件清单不构成可直接实施合同。

## 13. 最终迁移清单

### 已具备，保留回归，不重复实现

- 摘要请求 `system + user`，以 user 结束。
- 现有 append-only 历史、候选预算检查、当前轮/推理载体保护、token anchor。
- §6 的终态、稀疏 usage、checkpoint/Blob、渠道 fallback 和任务恢复能力。

### P0 待实现

- [ ] `REC-1`：可信 provider overflow 分类与当前失败调用的恢复许可。
- [ ] `REC-2`、`REC-3`：同一 Run 最多一次异步压缩恢复、历史/usage 保留及有界终止。
- [ ] `CMP-1`：摘要请求完整预算、比例 reserve 和既有轮次结构裁剪，与恢复同批。
- [ ] `CLI-1`：`GetUsableModels`、`GetDefaultModelForCli` 各自补齐新协议路径。
- [ ] `CLI-1`：目录使用哨兵、省略 Base URL，真实 CLI 仍回到服务器解析渠道。
- [ ] `TOOL-1`：四种 Shell/Bash 名称兼容，不改权限和调用 ID。
- [ ] `FETCH-1`：URL/字面量检查、直连 DNS 固定、重定向复查及显式代理兼容。

### P1 待实现

- [ ] `MODEL-1`：共享静态模型解析规则，不新增独立目录系统。
- [ ] `RESULT-1`：共享工具预算规则，保留展示/回放差异。
- [ ] `CURSOR-1`：配置变化时的一次确认与正常重启、失败状态和设置回退。

### 独立候选，尚未授权实施

- 通用 OAuth 回环 callback。
- Antigravity provider、账号、usage 和轮转。

### 明确不做

- Rust/Tauri 整树合并、Deno plugin runtime、SQLite 替换 JSON 历史。
- 上游广告和桌面 UI 迁移。
- 重写已有终态、usage、checkpoint、fallback 或任务恢复。
- WebFetch 全模式强制直连、可信代理配置、自动 TUN 信任、额外端口白名单。
- 为本次迁移新增审批、通用安全框架、配置开关矩阵或强制终止 Cursor。

---

**默认执行顺序**：overflow 恢复（包含摘要预算/裁剪）→ CLI 凭据与四路径 → Bash 别名 → WebFetch 直连防护/代理兼容 → CLI 解析与工具预算各自收敛 → Cursor 正常重启。CLI/Bash/WebFetch 可独立于 overflow 提前交付；Antigravity 等待独立确认。后续可以据此启动 P0 实施，本文修订完成不代表任何新增功能已经实现或运行验收通过。
