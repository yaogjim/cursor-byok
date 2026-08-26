# Cursor BYOK 工作决策基线 PRD

- **文档类型**：产品与工程决策 PRD
- **适用项目**：Cursor BYOK 本地分支 `noad`
- **本地决策基线**：`noad@e9b6d701d63f3cc315676afffaddc3128c7db7cc`
- **原始仓库**：<https://github.com/leookun/cursor-byok>
- **原始主线基线**：`main@799dbda7e0ca30ab5d0bfe965fd1ab3c5da5c588`
- **记录范围**：截至双集成导航与数据概览合同；运行时窗口、Wails 视觉点击和网络零请求验收仍按功能差异 PRD 与上游同步说明执行
- **状态**：决策基线；不等同于全部决策已经实现

## 1. 文档职责

本 PRD 只记录本项目必须遵循的产品目标、工程原则、隐私边界、路由决策、客户端体验决策和停止条件。

它不承担以下职责：

- 不记录完整的当前代码差异；这些内容见 [`prd_cursor_byok_当前功能与上游差异.md`](prd_cursor_byok_当前功能与上游差异.md)。
- 不描述具体的 pull、cherry-pick、merge、构建与发版命令；端到端步骤见 [`cursor_byok_upstream_sync_release_runbook.md`](cursor_byok_upstream_sync_release_runbook.md)，冲突与停止条件见 [`cursor_byok_upstream_merge_requirements.md`](cursor_byok_upstream_merge_requirements.md)。
- 不把“已决定”表述为“已实现”，每项实现状态以功能差异 PRD 和验证证据为准。

决策来源：

- [`Cursor BYOK 功能可用、隐私与稳定性验证路线图`](../.cursor/plans/cursor_byok_功能可用、隐私与稳定性验证路线图_22a7548b.plan.md)
- [`客户端体验治理`](../.cursor/plans/客户端体验治理_64db96aa.plan.md)
- [`双集成导航与数据概览改造计划`](../.cursor/plans/双集成导航与数据概览改造计划_2622a26e.plan.md)

## 2. 产品目标

1. 以用户配置的模型 Endpoint 承担主要 Agent/BYOK 推理能力。
2. 优先保持 Agent、工具、上下文持久化、取消、重试和恢复可用。
3. 对外部请求建立清晰的出口分类和最小化隐私观察边界。
4. 让主题、广告和更新行为由本地配置控制，并采用保守默认值。
5. 让未来上游同步能够区分“可接受的功能更新”和“会覆盖本地决策的高风险变更”。

## 3. 工程优先级与变更原则

### 3.1 优先级

1. **功能可用优先**：任何路由、协议响应、鉴权或持久化修改前，先建立现状基线。
2. **稳定优先于结构优雅**：不为文件拆分、抽象统一或状态机重写单独立项；只有已复现缺陷被现有结构阻碍且有回归保护时才局部重构。
3. **隐私边界不能被功能便利绕过**：任何模式都不得落盘凭据；只有用户明确启用的本机 `full` 采集可以保存经过凭据清洗的业务原文。
4. **一次只改变一个变量**：验证和修复都应有独立的输入、结果、测试和回滚点。
5. **运行时证据优先**：当源码、注释、历史计划和运行时行为冲突时，依次以运行时行为、脱敏网络/事件元数据、当前构建产物、进程配置、持久化状态、已提交源码、注释和死代码为判断依据。

### 3.2 运行中实例保护

当前会话依赖 `127.0.0.1:18080` 和 `127.0.0.1:18090`。没有明确维护窗口时：

- 不停止、替换或切换唯一代理实例。
- 如必须交接，采用“回复发送后延迟交接、等待旧请求稳定、启动候选实例、健康检查、失败自动回退”的顺序。
- 任何实验必须能恢复原代理、Cursor 设置、模型配置和账号状态。

### 3.3 远程 CLI 使用本机 backend

用户已确认：远端 Linux Docker 中的 Cursor CLI 要使用本机 BYOK backend 时，走方案 A。

- 由本机发起 SSH 反向隧道 `-R 127.0.0.1:18090:127.0.0.1:18090`，把本机 loopback 的 18090 接到远端 loopback。不从远端 SSH 登录本机，不使用本机用户名密码，不把 `18080`/`18090` 改绑到非 loopback。
- 远端容器使用 Docker `--network host`，并设置 `CURSOR_API_ENDPOINT=http://127.0.0.1:18090`；本地 BYOK 不设 CLI API key。
- 本机 `127.0.0.1:18080` / `127.0.0.1:18090` 的运行中实例保护仍然有效。未确认前不附带转发 18080。
- 完整操作记录（非官方会话复用、Linux Docker 安装、代理、方案 A 隧道与验收）见 [`docs/ops_198_cursor_cli_session_reuse.md`](ops_198_cursor_cli_session_reuse.md)。该手册不含真实 token、SSH 密码或 `auth.json` 正文。
- 浏览器打开远端终端时：用 WeTTY（xterm.js），不用 ttyd。WeTTY 以 root 只监听远端 `127.0.0.1:7681`，交互 PTY 降权为非 root 用户 `bun` 的 `/bin/sh`；由 198 宿主机已有 nginx 提供 HTTPS + HTTP Basic，反代到该 loopback。不把 7681 发布到 `0.0.0.0`，不另起一套 nginx 抢占已有 `:80`。构建文件在仓库 `cursor-cli-docker/`。

## 4. 路由与信任决策

### 4.1 默认允许的目标

以下目标属于默认允许访问的信任区，但“允许访问”不代表允许携带任意字段：

- 用户显式配置的模型 Endpoint。
- Cursor 官方 upstream：`api2.cursor.sh`、`api3.cursor.sh`、`api4.cursor.sh`。

所有外发仍应采用最小 headers、凭据和 payload 策略。

### 4.2 Tab 双模式

Tab 目标采用两个明确、互斥的业务模式：

- `local_official`：使用用户自己的 Cursor token，主要目标为 Cursor 官方 upstream。
- `external_relay`：仅在用户明确启用后使用第三方 relay。

两个模式之间禁止自动 fallback。local 失败不能静默转发到官方 upstream 或第三方 relay；是否切换必须由明确配置和用户决策控制。

### 4.3 `tab.leokun.cn` 的当前决策

`https://tab.leokun.cn` 当前不属于默认信任区，但现状暂不阻断、不替换。原因是：

- 它承载的真实 RPC 能力、线上部署行为和请求数据边界仍需验证。
- 本地模拟 Cursor 身份与用户原始 Cursor token 的兼容性尚未完成验证。
- 直接改为官方直连可能导致现有 Tab 功能因鉴权失败中断。

任何保留、替换、禁用、脱敏或转官方直连的方案，都必须先完成逐 RPC 影响验证并取得用户确认。不得新增未显式配置的外部域名或隐藏 relay。

## 5. 隐私、日志与审计决策

### 5.1 客户端日志采集

客户端代理只负责采集，不负责读取历史日志、执行分析、生成报告或导出诊断包。采集配置以 `config.yaml` 中的 `observability` 为唯一事实源：

```yaml
observability:
  mode: basic
  retentionDays: 7
  maxDiskMB: 1024
```

- `basic` 为默认模式，只记录生命周期、路由、执行目标、协议、状态、错误类别、耗时、字节数和关联 ID，不记录正文或完整 headers。
- `full` 必须由用户明确启用，可在本机保存经过凭据清洗的 Cursor 业务语义、Prompt、源码/diff、工具参数与结果、provider 请求和流式响应。
- `Authorization`、Cookie、API Key、token/secret/credential 字段、自定义敏感 header 和敏感 query 参数在任何模式下都禁止落盘；清洗必须发生在序列化和写盘之前。
- 无法安全解析和清洗的未知二进制 body 只记录长度、协议和 `decode_error`，不得回退为原始字节抓包。
- `full` 必须受保留天数和磁盘配额约束；达到上限时先清理最旧的已关闭采集 session，仍不足则停止 payload 采集，但不得阻断代理主链路或删除活动 session、`context.json`、`state.json` 等业务事实。
- 旧 `log: false/true` 只作为迁移输入，分别规范化为 `basic/full`；规范化保存后不得继续写回旧字段。

### 5.2 专用隐私审计

专用观察器默认关闭，只允许覆盖明确标记的 17 条 relay RPC 和 provider 观察点。它是临时隐私验证能力，不与 `basic/full` 日志合并，也不改变既有 canary、TTL 和事件上限语义。允许记录的内容仅包括：

- 事件类型、状态、错误类别、耗时。
- 目标 host 的净化分类和 endpoint 类型。
- 请求/响应字节数。
- 字段 presence、字段长度、repeated 数量、oneof/event 类型。
- 凭据类别是否存在。
- synthetic canary 是否匹配的布尔值。

禁止记录：

- Prompt、源码、diff、文件名、路径、UUID 原值。
- Token、API Key、Authorization、Cookie。
- body hash、完整 headers、完整请求/响应或任意内容 preview。

审计必须具备测试构建开关、自动过期、事件上限、单次 session 隔离和 `0600` 文件权限。canary 只在内存中匹配，持久化不得写入 canary 原值或其 hash。解析失败只能记录 `decode_error`，不能回退到原始 body dump。

### 5.3 独立日志分析与客户端启动边界

日志分析属于独立子项目 `tools/log-analyzer`，不得导入客户端运行时包或参与客户端二进制、更新归档。分析器可以拥有自己的 Wails/Vue GUI、独立安装包和独立发布周期；客户端和分析器只通过版本化日志文件协议及受限启动参数耦合。

客户端代理仍只负责采集，不读取历史日志、不执行分析、不生成报告。客户端配置页可以检测固定应用标识并启动已安装的分析器，只允许传入客户端解析出的可信日志根目录；不得接受用户提供的任意 executable、shell 命令或额外环境注入。分析器未安装时只能显示安装引导，不能自动下载或静默安装。

分析器只读消费用户明确选择的日志目录。活动 session 采用快照读取和手动刷新；报告、调查案例、诊断包与 AI 调查包均只能由用户主动生成，不允许客户端定时分析、自动上传或后台触发。删除操作只能针对分析器再次核实为 `closed` 且位于可信 `traces` 根下的 session。

### 5.4 调查案例与外部 AI 证据闭环

分析器可以把筛选结果保存为持久调查案例，用于追踪“发现异常 → 外部分析 → 人工修改 → 新日志复验”。案例默认只保存脱敏事件快照、查询、用户标注、版本指纹和复验结果；full payload 必须逐项显式附加并标记为敏感。删除原始 session 不自动删除案例证据。

第一版 AI 闭环只允许：

1. 用户主动导出版本化、最小化、脱敏的调查包；
2. 外部 AI 编码代理基于证据 ID 生成结构化假设、建议测试和改进方案；
3. 用户把经 schema 校验的分析结果导回案例；
4. 用户在分析器之外审核并实施修改，再以新日志执行复验。

分析器不配置模型、不调用 AI、不修改仓库、不运行外部命令。日志、payload、Markdown 和导入结果都视为不可信数据，不能触发工具、命令、网络请求或代码修改。默认调查包不得包含 full payload、凭据、Prompt、源码/diff、完整路径、UUID 或完整 URL；敏感附件必须逐项确认并在 manifest 中列明。

## 6. 功能语义决策

### 6.1 业务成功与兼容成功分离

RPC 返回 success 不自动代表真实业务完成。后续能力报告必须区分：

1. **必要兼容成功**：用于启动、能力 gate、避免重试或保持 UI 可用；保留，但不计入真实业务覆盖率。
2. **部分支持**：存在本地副作用，但没有完整抓取、分块、embedding、索引或语义检索；保留已工作的副作用，并明确标注能力边界。
3. **真实业务完成**：只有实际数据、状态和后续检索效果均有证据时才能使用。

Repository/Docs/Upload 的 success 语义在未完成客户端影响实验前，不直接改成 failure；后续修复必须先定义不会错误推进 Cursor 状态机的诚实语义。

### 6.2 Agent、工具链与 Provider 断连

Agent 的 `context.json`、`state.json`、tool replay、reasoning signature、RunSSE 终态、取消和重试行为属于受保护主链路。`ForceBackgroundShell` 的无 reasoning replay 只允许在明确专项条件下生效，不得泛化为所有孤立 `tool_result` 放行。

Provider 断连与重试遵循以下已确认规则：

1. HTTP transport/状态成功、provider 协议完整结束、model call 业务成功是三种不同事实，任何指标和终态不得相互代替。
2. `llm_summary` 只作为调用摘要工件，不是业务成功事实源；每个 `model_call_id` 必须由唯一、幂等的最终事件表达业务结果。
3. 自动重试只允许发生在尚未产生 model event、尚未向下游发布内容、尚未形成或派发工具、尚未提交 checkpoint 的安全窗口；已有文本、reasoning、partial/completed tool 或副作用不明时禁止自动重放。
4. 缺失 provider 明确完成标记的 EOF、scanner/decoder 错误和半帧必须归类为截断失败，不得记录协议成功或业务成功。
5. 部分输出失败时保留已产生内容并明确终结当前 turn；本阶段不实现 SSE 断点续传，也不把新请求伪装为原流续传。
6. provider 原始错误与响应只能形成脱敏诊断摘要；Authorization、Cookie、API key、完整 query、请求体和未经清洗的 provider 正文不得进入 basic 日志、history 或用户终态。
7. subagent 后续恢复必须使用稳定父子关联、独立 checkpoint 和原子结果提交；在该合同完成前，不以 provider fallback 或整轮自动重放替代局部恢复。

## 7. 客户端体验决策

以下配置统一持久化到 `~/.cursor-local-assistant-v2/config.yaml`；前端 `localStorage` 只能作为首屏缓存投影，不能成为第二份事实源：

```yaml
appearance:
  theme: light
advertising:
  enabled: false
updates:
  checkOnStartup: false
```

### 7.1 主题

- 默认主题为 `light`。
- 保留 `dark`。
- 使用语义化 CSS token，不继续扩散主题无关的暗色硬编码。
- 原生窗口背景与前端首屏保持一致，避免启动闪黑。

### 7.2 广告

广告采用“双重许可”：本地 `advertising.enabled` 与远端广告位均允许时才可使用。

本地开关关闭时：

- 不请求广告服务。
- 不展示广告入口、广告弹窗或旧缓存广告。
- 不因启动、窗口聚焦或定时刷新产生广告网络请求。

### 7.3 更新

更新默认完全手动：

- `checkOnStartup=false` 时启动不请求 manifest。
- 即使允许启动检查，也只能检查，不能自动下载。
- 状态机为 `check -> available -> 用户确认下载 -> downloading -> ready -> 用户确认安装`。
- `mandatory` 只能改变提示，不能跳过下载或安装确认。
- 资源必须是当前项目 GitHub Release 的 HTTPS 地址，必须有 SHA-256，限制包体大小，并在取消、错误和退出时清理临时包。

### 7.4 双集成主导航

主窗口采用五页同层导航，页面标题使用完整名称，侧栏可用短名。Cursor 与 Gateway 是并列集成，不是父子模块。完整合同见 §10.13。

| 页面标题 | 侧栏短名 | 路由 |
| --- | --- | --- |
| 数据概览 | 概览 | `/` |
| Cursor 集成 | Cursor | `/cursor` |
| 网关集成 | 网关 | `/gateway` |
| 上游模型 | 模型 | `/models` |
| 系统设置 | 设置 | `/settings` |

## 8. 非目标与待决策事项

当前不承诺：

- 纯本地或离线实现 Tab、FileSync、Repository、Docs、Git 全部能力。
- 已完成 Cursor token 的 Hobby/付费权限、额度、过期和刷新矩阵。
- 已完成用户 token 的 Keychain、刷新、身份隔离和双模式实现。
- 已完成所有 17 个 RPC 的触发条件、privacy mode 和逐 RPC 路由影响验证。

以下事项在证据和用户确认前不得自动决定：

- `tab.leokun.cn` 的保留、替换或禁用。
- local official 与 external relay 的逐 RPC 目标。
- Repository/Docs/Upload 的诚实失败语义或完整能力补齐方案。
- 任何新增外部目标、凭据转发和身份模拟策略。

## 9. 停止条件

出现以下任一情况，立即停止实验或修改：

- Agent、文件工具、模型 Endpoint、Cursor 启动或设置恢复回归。
- Cursor 卡死、入口消失、重试风暴或不可恢复状态。
- 请求进入未选择的新外部目标。
- 凭据在任何模式下落盘，或真实正文、源码、Prompt 在未明确启用的本机 `full` session 之外写入日志或 artifact。
- 无法判断 success 是否承担兼容 gate。
- 无法提供独立回滚路径。

## 10. P1 Subagent 恢复与 Provider Fallback 决策基线

本节记录计划 `p1_subagent_恢复与_provider_fallback_实施计划_5c8db987` 阶段 0 Design Gate 冻结的工程决策。决策来源：已批准的 P1 计划、已读源码与 proto fixture；真实 Cursor 运行 fixture 仍属未知（见系统架构 PRD §14.7.2）。

### 10.1 ID 合同

后续实现必须遵守以下 ID 来源规则，不得自行推断或以传输标识代替业务 ID：

| 字段 | 来源 / 生成规则 | 稳定性 | 当前状态 |
|------|-----------------|--------|---------|
| `root_conversation_id` | 父 `ConversationFile.RootConversationID`；若为空则取父 `ConversationID` 本身 | 跨 resume 稳定 | 已持久化 |
| `parent_conversation_id` | `openTask` 填入 `SubagentArgs`（field 9），来源为 `openContext.ConversationID` | 稳定 | 已填入 |
| `parent_tool_call_id` | `ToolInvocation.CallID`，由 Cursor 分配，传入 `openTask` | 稳定 | `ConversationFile` 已持久化 |
| `subagent_run_id` | Task dispatch 前本地生成 UUID v4，立即持久化到 run store | 稳定，本地生成 | **P1 新增，当前不存在** |
| `child_conversation_id` | `SubagentSuccess.agent_id` 或 Cursor checkpoint；仅在 child 绑定后可知 | 绑定后稳定，跨 resume 稳定性未知 | 到达 `SubagentResult` 时才可获取 |
| `agent_id` | `SubagentSuccess.agent_id`（proto 必填 string）；`SubagentError.agent_id`（optional） | 真实运行行为未知 | 仅存在于 result 中 |
| `exec_id` | `openTask` 内部生成 `exec-subagent-<UnixNano>` | 传输标识，每次重建均不同 | 不能作为业务 ID |
| `request_id` | Forwarder 请求级别 ID，已存在于 `ConversationRequestPrefix` | 请求级别 | 已持久化 |
| `model_call_id` | 每次 provider pass 生成，P0 已实现幂等 final | provider pass 级别 | 已实现（P0） |

**unknown 规则**：任何字段值未知或尚未到达时，记录为字符串 `unknown`，不按时间窗口推断，不以 `exec_id`、`ExecServerMessage.Id` 或任何传输 ID 代替。

### 10.2 Subagent Terminal 枚举与兼容投影

**本地 terminal category（机器可判定，仅存在于本地 run record / history metadata / observability）：**

| category | 触发条件 |
|----------|---------|
| `succeeded` | `SubagentResult.Success`，`background_reason = UNSPECIFIED` |
| `backgrounded` | `SubagentResult.Success`，`background_reason != UNSPECIFIED`；或 `ForceBackgroundSubagentResult.ACCEPTED` |
| `canceled` | `CancelSubagentAction`；`SubagentRunStatus.ABORTED` |
| `timeout` | transport close / 等待超时，无 `SubagentResult` 到达 |
| `provider_error` | `SubagentError`，typed 判断含 provider/model 失败语义（不解析自由文本分类） |
| `tool_error` | `SubagentError`，typed 判断含工具执行失败语义 |
| `parent_unavailable` | parent stream 消失且无 `SubagentResult` |
| `truncated` | `ExecClientControlMessage.throw` / `ExecClientStreamClose`，无 `SubagentResult` |
| `protocol_error` | 其他不可分类的协议层错误 |

**向公共 proto 的兼容投影规则：**

- success → `SubagentSuccess`（携带 `agent_id`、`final_message`、`tool_call_count`、`background_reason`、`transcript_path`）；`TaskResult.Success`。
- 非 success → `SubagentError`（`error` = 脱敏 safe summary text，不含凭据，不含完整 provider error）；`TaskResult.Error`（`text` = safe category + summary）。
- 机器可判定 category 不写入公共 proto，只存于本地 run record、history metadata 和 observability events。
- 同一 `subagent_run_id` 只接受一次 terminal CAS；重复 terminal 记录 `subagent_terminal_conflict` 事件，不覆盖首个已提交终态。

### 10.3 Durable Handoff 状态机与 Exactly-Once 范围

```
dispatched → running → terminal_prepared → parent_committed → acknowledged
running → backgrounded → terminal_prepared
terminal_prepared（parent identity 缺失或 parent 持久会话不存在）→ awaiting_parent_resume → parent_committed
dispatched / running / backgrounded（backend 重启，child 未终结）→ awaiting_client_resume
```

**Exactly-once 范围限定**：本地 parent history 和 tool-result 最多出现一次结果。

不变量：

1. child terminal 必须先写入 durable `result.json`（`terminal_prepared`），再写 parent history；禁止先删除 `PendingExec` 再尝试持久化。
2. parent `tool_result` 以 `subagent_run_id + result_digest` 作为幂等键；重启恢复可重复执行 commit 步骤，parent history 最多一次结果。
3. 网络 checkpoint 和 RunSSE update 允许重发，但内容和幂等标识必须一致。
4. parent identity 缺失，或对应 parent 持久会话不存在/已删除、无法安全追加时，标记 `awaiting_parent_resume`；恢复逻辑不得新建仅含 `tool_result` 的孤立 parent 会话。parent stream 不活跃本身不阻止本地文件级提交。在线 checkpoint 发布失败时保持 `parent_committed`，不回滚已提交结果。
5. 同一 `historyRoot` 采用单活 Backend 写入约束；进程内 CAS 不宣传为跨进程 CAS，本轮不引入 OS 文件锁。

### 10.4 Child Checkpoint 恢复边界

已确认的恢复承诺：

- Backend 重启后扫描 run store，恢复本地状态并等待 Cursor 重连/resume；**禁止 backend 自行重建或重启 Cursor 子代理进程**。
- `terminal_prepared / awaiting_parent_resume`：从 durable result 重试 parent 文件级幂等提交；`awaiting_parent_resume` 表示 parent identity 不足，或对应 parent 持久会话不存在/已删除。恢复逻辑不得为此静默新建孤立 parent 会话。
- `parent_committed / acknowledged`：不重新调用 child；本地 parent result 已唯一落盘。在线 checkpoint 发布失败时保持 `parent_committed`。
- `dispatched / running / backgrounded`：转为 `awaiting_client_resume`；本轮不自动重派，也不宣传为从 child 执行点自动续跑。
- 损坏、checksum 错误或版本不兼容：隔离文件，不删除 parent history，不自动重派 Task。
- 本轮未新增复制 child 历史的独立 checkpoint 文件；恢复保证限定为 durable handoff。真实 Cursor resume fixture 仍是待补证项。

明确禁止的恢复操作：

- 在没有 Cursor resume 的情况下自行重派 Task 或重放工具副作用。
- 以新请求伪装为原 child conversation 续传。

### 10.5 Provider Fallback 合同

**默认：关闭（`enabled: false`）**。关闭时行为与当前版本字节级一致。

**配置合同：**

```yaml
providerFallback:
  enabled: false               # 默认关闭
  primaryChannelID: ""         # 主渠道 ID
  candidateChannelIDs: []      # 有序候选列表，显式配置
  maxHttpAttempts: 5           # 全链共享 HTTP 尝试预算；允许 2–9
  maxWaitSeconds: 8            # 全链共享退避等待预算（秒）；允许 1–30
```

- 旧配置缺失字段或字段为 0 时规范化为 `5 attempts / 8 seconds`；合法非零值必须原样保存、导入导出和回显。
- 保存入口对非零越界值严格报错；运行时解析再做 `2–9 / 1–30` 防御性 clamp，避免绕过保存入口的旧文件或手工修改造成失控预算。
- 单渠道 HTTP attempt 上限固定为 3，不开放 UI 配置。启用 fallback 时采用“保证渠道覆盖，再用剩余预算重试”：先为当前请求下每个后续兼容渠道预留 1 次 attempt，当前渠道最多使用 `min(chain_remaining_attempts - reserved_attempts, 3)`；当预算不足以覆盖全部渠道时按配置顺序各执行 1 次，预算耗尽后的链尾不发送 HTTP。典型分配为：3 渠道/5 attempts → `3+1+1`，5 渠道/5 attempts → `1+1+1+1+1`，5 渠道/9 attempts → `3+3+1+1+1`。
- 全链 attempt 与退避等待预算按同一 `model_call_id` 共享，切换渠道时不重置。fallback budget 存在时，链剩余 wait 包括 0 都直接覆盖单渠道 4 秒默认值；0 表示禁止再 sleep。`Retry-After` 超过剩余预算时不等待、不做零延迟同渠道重试，只在错误类别和安全窗口仍允许时切换渠道。
- fallback 禁用时保留预算字段但不生效，运行路径继续使用单渠道 P0 合同。
- 每条显式 fallback 链最多包含 5 个物理渠道：1 个主渠道与 1–4 个有序候选。渠道上限与 HTTP attempt 预算彼此独立；默认 5 attempts 可保证 5 个兼容渠道各获得 1 次机会。若显式配置的 attempt 预算小于渠道数，则按配置顺序覆盖到预算耗尽，未获得预留的链尾不发送 HTTP；运行时不得突破共享预算。
- 模型配置的“测试全部”只测试物理 adapter，静默跳过逻辑 alias；单独点击逻辑 alias 的测试仍提示其虚拟 endpoint 不可直接测试，整链必须通过实际运行验证。

**允许切换的错误类别**（首 model event 前安全窗口，且无副作用）：

- transport 错误（DNS/TCP/TLS）
- HTTP 429
- 部分 5xx（502/503/504）
- 2xx 后首 model event 前的 transport EOF / stream 截断
- 零原始字节的 stream EOF / decode error

**禁止切换：**

- `context.Cancel` / deadline exceeded
- HTTP 400/401/403/404
- 请求构建错误 / 模型参数错误
- 任意原始响应字节已到达
- 任意 model event 已到达
- `partial_tool_seen / completed_tool_seen / tool_dispatched = true`
- `checkpoint_committed = true`
- `downstream_published = true`
- `RequestBodyOverride` 跨 Provider

**跨 Provider 兼容门禁**（fallback 到不同 Provider 时执行）：

- canonical messages 可投影到目标 Provider
- tool schema 可用
- 图片 / 附件需求可满足
- 目标 context window 足够
- provider-specific opaque state 可安全处理
- 不满足时跳过并记录 `fallback_incompatible`，不静默降级能力

**费用、隐私与兼容提示：**

- 配置 UI 必须明确提示：跨 Provider fallback 可能改变费用、隐私边界、模型语义和工具兼容性。
- 配置导入导出必须保留候选链；渠道 ID 变化时提示并阻止保存悬空引用。
- fallback 保持同一 `model_call_id`；每个渠道尝试使用独立 `channel_attempt`；整条链只有一个业务终态。

**与 P0 不冲突确认：**

- P0 安全重试在同一渠道同一 `model_call_id` 内进行，重试窗口规则不变。
- fallback 满足与 P0 相同的"无任何原始响应字节、无 model event"前提后才触发；共享 HTTP attempts 预算。
- 默认关闭时 P0 路径无任何变化。

### 10.6 MITM / CA / 透明代理不变量

以下内容在 P1 全程保持不变：

- `internal/mitm/`、`internal/certs/`、CONNECT 处理、白名单规则、系统代理配置、透明代理行为。
- 不新增外部目标、不修改拦截策略、不改变证书安装流程。
- P1 实现不得以任何方式影响现有 MITM 路由路径。

### 10.7 逻辑子代理模型与独立 CLI 模型池

三层能力必须同时保留且职责互斥：

1. **IDE 逻辑子代理模型**：Cursor IDE 只在启动新 subagent 时应用 `SubagentModelOverrides`。启用 fallback 的 adapter 是“逻辑路由（建议仅子代理）”；当前 Cursor 模型选择器仍可能让父 Agent 选择它，因此“软专用”是产品引导，不是技术强制隔离。已经运行的 subagent 不支持热切换模型。
2. **BYOK Provider fallback**：只重试同一 `model_call_id` 的 HTTP/channel 链，不重放完整 subagent，不续传或拼接已有输出。渠道切换必须满足零原始响应字节、零 model event、零工具或其他副作用。链内 primary/candidate 必须是 `providerFallback.enabled=false` 的物理渠道，逻辑 alias 不得嵌套成为另一逻辑链成员。
3. **独立 Cursor CLI 模型池**：作为高层进程控制器按配置顺序启动物理模型；每个模型在一次编排中最多启动一次。它不是 IDE 能力的替代，也不把新进程描述为恢复同一个 subagent。

CLI 模型池只允许引用 `providerFallback.enabled=false` 的物理 adapter。预检必须同时验证精确 16 位模型 ID 存在于 `agent models`，并在固定的本机 BYOK `config.yaml` 中唯一匹配；ID 复用现有渠道哈希合同，因此 API Key 仅可在内存中参与哈希，禁止输出、记录或持久化。零匹配、多匹配或逻辑路由 ID均拒绝。Provider 与 CLI 预算不跨进程共享，因此通过配置隔离防止“外层进程数 × 内层 fallback chain”的乘法重试。

CLI 自动切换的安全条件是：进程仍处于启动或输出前阶段，且尚未观察到 assistant、thinking、tool call 或 worktree mutation。`system/init` 与回显用户输入只表明进程已初始化，仍属于 pre-output；任意模型生成事件保守关闭自动切换窗口。只有 spawn 失败或 CLI 暴露的结构化错误类别明确为 transport、429、502/503/504 时才允许切换；不得解析 stderr 或自然语言猜测。认证、非法模型、HTTP 500、其他 4xx、取消、配置错误、未知非零退出或缺少 typed 分类均直接停止；输出或 mutation 后的失败进入 `needs_review`，禁止跨模型自动重放。

Prompt 只经 stdin 传入，不进入 argv。journal 只允许保存编排 ID、模型 ID/序号、阶段、可用的 session/request ID、退出码、受控错误类别、是否已观察输出/变更和 worktree 标识；不得保存 prompt、NDJSON 正文、tool 参数、API Key、Cursor token 或 provider 凭据。写任务必须显式启用并使用独立 worktree；Ask/Plan 为默认安全模式。

### 10.8 验收与回滚决策

- Provider fallback 可通过 `enabled: false` 回到单渠道 P0；删除或忽略新增预算字段回到默认 5/8。
- CLI 控制器使用独立配置 `~/.cursor-local-assistant-v2/cli-model-pool.yaml` 和独立 Go module；停止使用或移除该配置不得影响 IDE、18090 或客户端二进制。
- IDE、Provider 和 CLI 三条链必须分别提供同层运行证据。fake agent 不证明真实 CLI，单元测试不证明真实 IDE child 模型传播，CLI 成功也不证明 Provider 故障注入通过。
- 缺少真实 IDE 或 CLI 证据时只能标记 `delivery_status=verified-partial`，不得标记 accepted。

### 10.9 配置写事务与物理上游容量合同

本节冻结配置整文件读改写竞态和同一物理上游进程内容量保护的最小合同：

- 普通 UI 保存以调用方提交的用户字段为准，但必须在存储锁内读取最新配置并保留最新 `lastAgentModelHash`；运行时模型 hash 更新只 patch 该字段，相同值为 no-op，不写盘、不通知 listener、不触发 Host rebuild；完整导入是唯一允许全量替换配置和 hash 的入口。
- Manager 写事务串行覆盖“Store 原子写入 → 内存 current → 文件 snapshot → listener”。写盘失败时磁盘和内存均不前移；listener 在 Store 锁和 Manager 写锁释放后调用，避免重入死锁。
- 物理 adapter 可配置 `maxConcurrentRequests`：缺失或 `0` 表示不限流，非零只允许 `1–16`。启用 `providerFallback` 的逻辑 alias 必须为 `0`；按 provider type、规范化 Base URL 和 API Key 判定为同一物理上游的 adapter 必须配置相同值，禁止 `0` 旁路。
- 上游组身份只在进程内以 SHA-256 表示；API Key、Base URL 和组 hash 均不得进入日志、错误、事件或持久配置。槽覆盖一次物理渠道的完整 Stream，包含同渠道 HTTP retry；切换候选前释放旧槽。
- 有限容量下每个物理渠道最多等待固定 2 秒，不保证 FIFO。容量等待不消费真实 HTTP attempt，也不计入 Provider fallback 的 retry/backoff 等待预算。超时返回 typed `capacity_unavailable`；只有在零 HTTP、零原始响应字节、零 model event、零副作用窗口内才可切到不同上游组，同组候选直接跳过。父 context 取消立即返回原始取消错误并禁止 fallback。
- 默认 `0` 保持历史无限并发行为。删除字段或设为 `0` 即回滚容量能力；当前真实 `grok-HA` 配置按用户决定不设置非零限制，因此本合同的实现与受控测试不能表述为当前在线环境已经启用容量保护。

### 10.10 Multi-Client Chat Gateway 最小合同

本节冻结独立 Chat Gateway 的阶段 0/1 合同。ACP、Responses、工具调用和独立生命周期不在本阶段范围。

**不变量**

- Cursor IDE/CLI 继续独占 `127.0.0.1:18080` / `127.0.0.1:18090` 以及现有 Connect/Protobuf、MITM、history 和工具桥。Gateway 使用独立 `http.Server`，默认 `127.0.0.1:18091`，不注册到 Cursor Host mux。
- Gateway 默认关闭，只允许 loopback + Bearer。Gateway 启停或失败不得阻止 Cursor 启动、停止或恢复设置：Cursor 启动成功后再启动 Gateway；Gateway 启动失败只记录独立错误；Gateway 停止失败不能阻止 MITM、Cursor 设置和 Backend 清理。
- 外部请求不得写 `lastAgentModelHash`，不得暴露 Provider API Key/Base URL，不得调用 Cursor 工具桥。
- Gateway 与 Cursor 共用同一 config manager 和现有 `DefaultProviderGateway` / Router / fallback / retry / 包级容量 limiter。禁止复制第二套 Router 或 limiter。
- 公开模型别名只经 `gateway.publicModels[{id,targetAdapterID}]` 显式映射解析，不回落到 Provider `modelID` 或 16 位内部 hash。目标 adapter 变化后显示映射失效并要求重选，不自动迁移。

**配置、token 与权限**

- 最小字段：`gateway.enabled`、`gateway.listenAddr`、后端生成的 `gateway.token`、`gateway.publicModels`。不含 ACP 字段和逐协议权限。
- 普通配置保存必须保留 token；默认导出剥离 token 后的再导入，在导入文档未带 token 时 overlay 现有 token，避免无意识轮换。导入 YAML 若显式含 token 则全量替换。token 不进入普通 JSON/Wails 投影、默认导出、`localStorage` 或日志。只通过显式复制/轮换接口读取。
- `config.yaml` 与写入用临时文件权限为 `0600`。首次写入 Gateway schema 前创建同权限 `.bak-pre-gateway` 备份。旧版本二进制再次保存会丢弃未知的 `gateway` 字段，这是明确的降级边界。

**阶段 1 协议**

- `GET /v1/models` 与最小 `POST /v1/chat/completions`：纯文本 messages、`stream=false` 聚合、`stream=true` SSE、usage、取消和 OpenAI 风格错误。
- 请求含 tools、多模态 content 或 reasoning 扩展时返回明确 4xx。阶段 2 之前 Gateway 不执行工具。

**回滚**

- 关闭 `gateway.enabled` 即停止独立入口，不影响 Cursor 18080/18090。删除 `gateway` 块并恢复备份可回到旧 schema；旧版本保存会丢字段，需先保留 `.bak-pre-gateway`。

**验收与证据边界**

- 自动门禁：401/404、模型列表无敏感字段、流与非流文本、取消、fallback 前置失败、外部请求不写 hash、config 权限/备份/token 红线、根模块相关 test/race/vet、前端投影/build、`git diff --check`；`proto/`、`internal/mitm/`、`internal/certs/` 无非预期变化。
- 真实门禁：OpenAI SDK 或 Cherry Studio 完成一轮聊天，以及 Cursor 全量回归。自动化不能替代真实客户端 smoke；本机不得扰动运行中的 18080/18090。
- Cursor 承重 characterization 只覆盖：18090 `/healthz`、Bidi/RunSSE procedure 注册、Cursor 模型哈希投影、`/v1/traces`，以及现有 fallback/capacity 测试合同。不为全部 Cursor mock 路由制作大规模 golden。

### 10.11 Chat 工具调用与 OpenCode 合同

本节冻结阶段 2 的 OpenAI-compatible Chat 工具合同；阶段 1 的文本能力与安全边界继续有效。

- 入站支持 OpenAI `tools[].function`、`tool_choice` 缺省或 `auto`、`parallel_tool_calls` 布尔值、assistant `tool_calls[]`、`role=tool`、`tool_call_id` 和多工具 ID。旧 `functions/function_call`、多模态和 reasoning 扩展仍返回明确 4xx。
- Gateway 将工具描述原样交给现有 Provider Gateway；assistant 工具调用和 tool result 投影到现有 `Message.ToolCalls` / `ToolCallID`。Gateway 不执行工具、不调用 Cursor 工具桥、不写 `lastAgentModelHash`。
- Provider 的完成工具事件按原 `CallID`、工具名和完整参数 JSON 对外返回；不得重新生成调用 ID。Anthropic `finish_reason=tool_use` 对外规范化为 OpenAI `tool_calls`。
- 非流响应在 `message.tool_calls` 返回完整调用并以 `finish_reason=tool_calls` 结束。SSE 以可累加的 `delta.tool_calls` 返回完整调用，随后唯一终态与 `[DONE]`；通用工具允许在 Provider 收口后一次性发送完整参数，不要求 Gateway 伪造参数增量。
- 任意 ModelEvent 产生后禁止跨渠道切换；HTTP SSE 已写出后发生错误只能在当前流内发送错误终态，禁止拼接其他 Provider 输出。
- 真实验收使用本机 OpenCode `1.2.25` 自定义 Provider，由 OpenCode 执行 read/shell/edit；自动 fixture 不能替代真实客户端。共享容量验收必须证明 Cursor 与 Gateway 命中同一物理上游时共用进程级限制。
- 回滚仅恢复阶段 1 对工具字段的 4xx；不得回退文本 Chat、token、公开别名、配置权限或 Cursor 隔离合同。

### 10.12 Responses API 与 Codex 合同

- 本阶段以本机 Codex `0.144.4` 的 HTTP SSE 行为为准，新增 `POST /v1/responses`；自定义 Provider 的 `base_url` 指向 Gateway `/v1`，`wire_api` 固定为 `responses`。
- 入站支持 `model`、`instructions`、`input` 中的文本 message、`function_call`、`function_call_output`、function tools、`tool_choice=auto`、`parallel_tool_calls`、reasoning item 回放、`store=false` 和 `stream=true`。
- `store=true`、WebSocket、`previous_response_id`、hosted/custom/freeform tools、图片、压缩请求体及无法安全投影的 typed item 明确 4xx；Codex `namespace`、`web_search` 与 `tool_search` 描述仅作为客户端本地能力跳过，不能展平或由 Gateway 执行；不提前实现服务端状态或任意 Provider 私有事件。
- SSE 至少输出 `response.created`、文本 `response.output_text.delta`、完整工具 `response.output_item.done`、失败事件和唯一 `response.completed`。Codex 必须依靠 `response.completed` 收口，不以 `[DONE]` 代替业务终态。
- function call/output 的 `call_id` 原样回放；reasoning 的 encrypted content 只有能保持 Provider 身份和 fallback 兼容性时才转发，否则明确拒绝，不跨不兼容渠道降级。
- Gateway 不执行工具、不写 Cursor 当前模型；Codex 自行执行 shell/patch。真实验收使用隔离 `CODEX_HOME`、自定义 Provider 和 read-only/write 两轮任务，不读取用户登录凭据。
- 回滚只移除 `/v1/responses`；Chat、模型别名、token、容量和 Cursor 路径保持。

### 10.13 双集成导航与数据概览合同

本节冻结已批准计划 `.cursor/plans/双集成导航与数据概览改造计划_2622a26e.plan.md` 的产品结构、保存边界和概览报告接口。它补充 §7 客户端体验与 §10.10–§10.12 Gateway 合同，不改变 Cursor 18080/18090、MITM、工具桥或 Gateway 协议能力。

**产品结构**

- 五个页面同属主窗口，不再把模型配置做成独立窗口：数据概览 `/`、Cursor 集成 `/cursor`、网关集成 `/gateway`、上游模型 `/models`、系统设置 `/settings`。
- 侧栏短名固定为「概览 / Cursor / 网关 / 模型 / 设置」；页面标题使用完整名称。
- Cursor 与 Gateway 是同层集成：都是本工具接入外部客户端或开发工具的并列入口。禁止把 Gateway 做成产品根节点，也禁止把 Cursor 降为 Gateway 子项。
- 旧路由兼容：`/config` 重定向到 `/settings`，`/model-config` 重定向到 `/models`。
- 主窗口默认 `1100×720`，最小 `980×640`。底部版本、代理状态、教程、作者和语言入口是主窗口全局元素。
- 概览页只读展示 Cursor / Gateway 运行状态，不再复制「启用 Gateway」开关。

**Gateway 保存范围**

- 网关集成页保存只合并 `gateway.enabled`、`gateway.listenAddr`、`gateway.publicModels`。
- token 不由本页草稿提交；存储层在锁内读取最新配置后保留磁盘 token，启用时按现有规则生成。
- 对应入口为 `SaveGatewayConfig`。同层还有 `SaveCursorConfig`（routing、Cursor 监听/超时）、`SaveModelAdapters`（只合并 `modelAdapters` 并重校验 Gateway 映射）、`SaveSystemSettings`（observability、appearance、advertising、updates）。
- 所有 section 保存都在存储锁内读最新配置、合并本页字段、校验、原子写入并返回最新投影；不把 token 放入普通 JSON/Wails 返回。
- `gatewayEnabled` 是配置意图，`gatewayRunning` 是运行时事实，`dirty` 是前端本页草稿。启动/停止只使用已保存配置；本页 dirty 时必须提示「请先保存本页」，不得静默提交其他页草稿。

**Gateway 运行隔离**

- Gateway 继续使用独立 `http.Server` 与独立生命周期，默认 `127.0.0.1:18091`。
- 启动 Gateway 不启动 Cursor MITM，不改变 `127.0.0.1:18080` / `127.0.0.1:18090`，不注入 Cursor 设置，不写 `lastAgentModelHash`。
- Gateway 启停失败只进入独立 Gateway 错误；不得阻止 Cursor 启动、停止或恢复设置。
- 复制 token 必须区分「尚未生成」「复制被 WebView 拒绝」「复制成功」；失败不得显示成功。token 明文不写入页面常驻状态、配置投影或日志。

**数据概览 daily 报告接口**

- 概览趋势与日历热力图的权威接口是 Wails `MetricsService.GetHomeMetricsReport(range)`。
- 首期 `range` 只接受 `7d`、`30d`、`all`；未知值归一为 `all`。计划中的「近 1 小时 / 今日 / 自定义」尚未成为已交付合同。
- 返回体至少包含：`range`、`timezone`（当前固定 `UTC`）、按该范围聚合的 `summary`（`providerCallsTotal`、`turnsTotal`、有效/异常轮次、Token、缓存命中率）、以及 `daily[]`。
- `daily[]` 只来自已持久化的 `usage.json` 日桶；禁止用仅保留约 500 条的 `recent_events` 冒充完整热力图。首期不做小时级热力图。
- 无按日数据时 KPI 可为零并显示「暂无按日数据」；读取失败必须在页面显式展示错误和重试，不得只把 `error` 传给组件而不渲染。
- `providerCallsTotal` 与 `turnsTotal` 不得互相映射。重置统计只清零 usage 聚合，不删除会话历史。

**验收与证据边界**

- 自动门禁属于实现与回归范围：section 保存互不覆盖、token 保留、Gateway 独立启停、`GetHomeMetricsReport` 范围过滤、前端投影/build。
- Wails 手工视觉验收（五页导航、窗口尺寸、复制 token、启用但未运行、趋势范围、窄窗口布局）是独立证据；未实际点击前不得标为已完成。
- 回滚导航与 section 保存不得恢复「保存一页覆盖另一页」的整文件用户配置提交；关闭 `gateway.enabled` 仍只停止独立入口。