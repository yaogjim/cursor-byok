# Cursor BYOK 工作决策基线 PRD

- **文档类型**：产品与工程决策 PRD
- **适用项目**：Cursor BYOK 本地分支 `noad`
- **本地决策基线**：`noad@e9b6d701d63f3cc315676afffaddc3128c7db7cc`
- **原始仓库**：<https://github.com/leookun/cursor-byok>
- **原始主线基线**：`main@799dbda7e0ca30ab5d0bfe965fd1ab3c5da5c588`
- **记录范围**：截至客户端体验治理代码提交；运行时窗口和网络零请求验收仍按功能差异 PRD 与上游同步说明执行
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

待实现的配置结构（示意）：

```yaml
providerFallback:
  enabled: false               # 默认关闭
  primaryChannelID: ""         # 主渠道 ID
  candidateChannelIDs: []      # 有序候选列表，显式配置
  maxChannels: 3               # 主渠道 + 最多 2 个候选
  maxHttpAttempts: 5           # 共享 HTTP 尝试预算
  maxWaitSeconds: 8            # 共享等待时间预算（仅 fallback 启用时生效）
```

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