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
- 不描述具体的 pull、cherry-pick、merge 命令和冲突处理步骤；这些内容见 [`cursor_byok_upstream_merge_requirements.md`](cursor_byok_upstream_merge_requirements.md)。
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

### 5.3 独立离线分析

日志分析属于独立子项目 `tools/log-analyzer`，不得导入客户端运行时包、调用 Wails bridge、参与客户端构建、归档或发布。客户端和分析器只通过版本化日志文件协议耦合。

分析器只读消费用户明确选择的日志目录，报告写入用户显式指定的输出目录。JSON/HTML 报告和脱敏诊断包只能由用户主动生成，不允许客户端定时分析、自动报告或自动上传。对外诊断包必须移除 full payload、凭据、Prompt、源码/diff、完整路径、UUID 和完整 URL，只保留事件形态、统计、错误分类和版本信息。

## 6. 功能语义决策

### 6.1 业务成功与兼容成功分离

RPC 返回 success 不自动代表真实业务完成。后续能力报告必须区分：

1. **必要兼容成功**：用于启动、能力 gate、避免重试或保持 UI 可用；保留，但不计入真实业务覆盖率。
2. **部分支持**：存在本地副作用，但没有完整抓取、分块、embedding、索引或语义检索；保留已工作的副作用，并明确标注能力边界。
3. **真实业务完成**：只有实际数据、状态和后续检索效果均有证据时才能使用。

Repository/Docs/Upload 的 success 语义在未完成客户端影响实验前，不直接改成 failure；后续修复必须先定义不会错误推进 Cursor 状态机的诚实语义。

### 6.2 Agent 与工具链

Agent 的 `context.json`、`state.json`、tool replay、reasoning signature、RunSSE 终态、取消和重试行为属于受保护主链路。`ForceBackgroundShell` 的无 reasoning replay 只允许在明确专项条件下生效，不得泛化为所有孤立 `tool_result` 放行。

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