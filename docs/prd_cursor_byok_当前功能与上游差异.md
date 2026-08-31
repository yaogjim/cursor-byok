# Cursor BYOK 当前功能与上游差异 PRD

- **文档类型**：功能现状与版本差异 PRD
- **适用项目**：Cursor BYOK 本地分支 `noad`
- **已提交 `noad` 基线 / 目标**：`9e6936f15fc45a351e9b53e2e99f321aa1b79ac1`
- **当前发布提交**：`93dbae694a1cb0c8d5d312496dea3a115ed0855a`（仍为 `v0.0.48`）
- **本次合并来源**：本地 `main@305b108e8cb44f68c15672809fc579acc65a9835`（含本地 `upstream/main@564f2bdcaec790863aca86403cedbfc77191bd43`）
- **共同祖先**：`cbf7cb4030e24ddfbd366c0e87bea969ba5e2421`
- **原始仓库**：<https://github.com/leookun/cursor-byok>
- **原始上游核对基线**：本地锁定 `upstream/main@564f2bdcaec790863aca86403cedbfc77191bd43`；最终 `git fetch upstream --prune --no-tags` 已通过，远端仍为此 SHA
- **原始主线历史基线**：`main@799dbda7e0ca30ab5d0bfe965fd1ab3c5da5c588`
- **当前工作树**：`0.0.49` 已在隔离 worktree 语义合入并完成可执行门禁；用户已确认创建本地 merge commit 并推进 `noad`；**仍未 push**
- **最新上游审阅**：已核对正式版 `v0.1.5@b807608bf3622ea08fdde9ace5840701a3d039a8` 与最新 `upstream/main@9120b90be7b3aa3e0a81fb4d056f1ad905fec23f`；同步本地 `main` 因真实冲突安全中止，未创建提交、未 push、未改 `gateway`
- **状态**：即将在 `sync/main-305b108-into-noad-20260820-004943` 创建本地 merge commit，并把本地 `noad` 推进到该提交；**仍未 push**；运行时未验证边界仍成立

## 1. 文档职责与证据口径

本 PRD 记录截至当前的实际工作、已验证能力、待验证边界，以及本地版本相对原始仓库主线的功能差异。它不重新定义产品决策；决策以 [`prd_cursor_byok_工作决策基线.md`](prd_cursor_byok_工作决策基线.md) 为准。

后续记录采用以下状态标签：

- **已提交并验证**：已进入 Git 提交，并有测试或运行时证据支持。
- **已实现待验收**：代码已在工作树中，但尚未作为稳定提交或尚未完成完整验收。
- **已观察未定性**：观察到现象，但因果链或实际 transport 尚未确认。
- **待验证**：证据不足，不得据此修改生产路由、鉴权或协议语义。
- **禁止自动合并**：必须人工审查，不能按文本冲突任意覆盖。

当证据冲突时，按运行时行为、脱敏网络/事件元数据、当前服务构建产物、进程配置、持久化状态、已提交源码、注释和死代码的顺序判断。

## 2. 当前版本快照

### 2.1 Git 关系

本次核对得到：

- 原始仓库 `https://github.com/leookun/cursor-byok.git` 已同步到 `main@c55c575a583694e8c25be73b08ac7b3ec496e1d5`。
- 上游目标先以真实双父 merge 合入本地 `main@d082e79019bdb5a472003db37f09e23d083de1f2`，再以真实双父 merge 合入 `noad@ad50039f5bf83b8df257c5375d6f24b3a16ae31e`。
- 本地同步前基线为 `noad@869f45f539e416b0fbe2c95c25b33f3c6350615e`；`ad50039` 的两个父提交分别为 `869f45f` 与 `d082e79`。
- `origin` 是本地 Fork `https://github.com/yaogjim/cursor-byok`；原始上游引用来自 `https://github.com/leookun/cursor-byok.git`，没有从 `origin/main` 推断上游状态。
- 本次仅更新本地分支，没有执行 `git push`。

本次 merge 相对 `noad@869f45f` 的差异为 **23 个文件，258 行新增、1889 行删除**。主要删除来自上游为修复会话消失而回退 blob/checkpoint 同步，不代表本地其他能力被整体移除。

### 2.2 已提交工作

| 提交 | 工作内容 | 当前状态 |
| --- | --- | --- |
| `9b31024` 及其祖先差异 | 调整 OpenAI 默认 Endpoint 为 Chat Completions，保留显式协议选择；补充本地忽略及 provider/model 兼容调整 | 已提交；合并时必须保留用户显式 Endpoint 选择 |
| `9728326` | 增加默认关闭的专用隐私审计、字段摘要、canary 门控、权限/过期/事件上限 | 已提交；相关 Go 测试曾通过 |
| `0d78c09` | 修复 `ForceBackgroundShell` 在无 reasoning payload 时工具结果无法历史 replay 的阶段断裂，并加入功能特征测试 | 已提交；不得泛化为所有孤立结果放行 |
| `045b60b` | 增加精确路由优先于 `/AiService/*` 通配路由的测试 | 已提交；只保护路由优先级，不改变目标 |
| `b7cd64f` | 保存验证路线图与工作决策 | 已提交；属于决策记录，不是运行时功能 |
| `74c1c38` | 以真实 merge 同步本地 `main@b438237`，整合 artifact 生命周期、Agent replay、静态 i18n 与俄语，并保留本地安全策略 | 已提交并通过完整自动化门禁；运行中实例与网络窗口验收待维护窗口完成 |
| `17659ec` | 完善日志观测语义、分析项目生命周期、查询/诊断能力与独立分析器 GUI | 已提交并通过主模块及独立分析器门禁 |
| `81be616` | 安全同步上游 `release/0.0.43`，整合 command/summarize/transcript、控制面账号和版本元数据，并收紧鉴权、凭据与文件写入边界 | 已提交并通过根模块、Tab relay、日志分析器和前端门禁；真实账号与发布资产验收待完成 |
| `d240ea3` | 安全同步上游 `0.0.44`，整合 Cursor CLI、blob/checkpoint 同步和独立协议调试器，并限制调试代理只能监听回环地址 | 已提交并通过根模块、全量 race、Tab relay 和前端门禁；发布资产与安装验收待完成 |
| `ad50039` | 安全同步上游 `0.0.45`，为修复会话消失回退 blob/checkpoint 同步，并增加显式环境变量门控的 Windows VDI 白屏规避 | 已提交并通过根模块、forwarder race、独立模块和前端门禁；运行时会话恢复、Windows VDI 与发布资产待验收 |

### 2.3 已有验证证据

- 代理、Agent、provider、主要工具、Cursor 设置和原账号恢复已完成可重复基线验证。
- `go test ./...`、`go build ./...`、Go 格式检查和编辑文件诊断曾通过；构建有既有 macOS 目标版本链接警告，但退出码为 0。
- synthetic Agent 请求已到达用户 OpenAI `responses` provider 并返回 HTTP 200。
- `FSUploadFile` 已实测携带 synthetic 文件完整内容，并在失败后重试。
- `StreamCpp` 已实测携带当前文件全文、相对 workspace 路径、光标、文件版本、additional files 和 diff history。
- 未在已审计的 17 条 relay RPC 中观察到该 synthetic Agent prompt，但这只说明该场景未观察到，不证明全局不存在其他链路。
- `FastRepoInitHandshakeV2`、`FastUpdateFileV2` 等 Repository/Docs/Upload 处理器已通过隔离特征测试复现 success 语义失真、跨重启状态错误和 KnowledgeBase 部分提交。
- Cursor 3.12.17 Commit Message 的静态调用链和精确路由优先级已定位；staged/unstaged 两次 UI 生成均成功，但没有命中已审计 relay/provider canary，真实 client-side transport 仍未解析，因此未修改路由。

## 3. 已提交的客户端体验治理功能

客户端体验治理已进入提交 `e9b6d701d63f3cc315676afffaddc3128c7db7cc`，自动化测试和构建已通过；网络零请求、旧配置迁移和窗口运行时验收仍按发布前门禁执行。

| 功能 | 主要变化 | 目标行为 | 当前状态 |
| --- | --- | --- | --- |
| 统一配置 | `types.go`、`store.go` 增加 `appearance`、`advertising`、`updates` 配置段和旧配置默认迁移 | `light / false / false`，保存模型或路由不丢失偏好 | 已提交；自动化验证通过，运行时验收待完成 |
| 主题 | 全局 CSS token、共享组件、首页、配置页、模型编辑器、图表、弹窗和滚动条迁移 | 默认浅色，保留深色，原生窗口与首屏一致 | 已提交；自动化验证通过，窗口验收待完成 |
| 广告 gate | `ads/service.go`、`app/runner.go`、前端增加本地总开关 | 本地关闭时不请求、不展示、不使用旧缓存 | 已提交；gate 测试通过，运行时零请求验收待完成 |
| 更新状态机 | updater、bridge、前端增加 `available` 和 `DownloadAvailableUpdate` | 检查、下载、安装分别需要明确阶段和用户确认 | 已提交；状态机测试通过，运行时验收待完成 |
| 更新安全 | 限制 GitHub Release HTTPS、必填 checksum、包体大小和退出清理 | 不接受任意资源、空 checksum 或遗留临时包 | 已提交；自动化验证通过，发布资源验收待完成 |

已完成验证：`git diff --check`、`go test ./...`、`go build ./...`、`yarn test:config-projection` 和 `yarn build`。
## 4. 相对原始仓库的功能差异

历史差异以 `leookun/cursor-byok main@799dbda` 为起点；上一同步记录核对到 `main@9e05739`，本次实际同步原始上游 `release/0.0.43@726f082`，并经本地 `main@14f20fe` 合入。下表把“代码已经改变”“已经验证”和“仅作决策”分开记录。

| 功能域 | 原始主线行为 | 本地已提交差异 | 当前工作树或决策 | 状态与合并口径 |
| --- | --- | --- | --- | --- |
| Agent BYOK provider | 已有本地 provider 主链路及 OpenAI/Anthropic 适配 | 调整 OpenAI 默认协议；增加 retry/replay 相关保护 | 继续以用户 Endpoint 为主要推理出口 | 已提交；必须回归输入输出、重试和恢复 |
| OpenAI 协议选择 | 原始默认行为以 Responses 为主 | 默认调整为 Chat Completions，同时保留显式 Responses/Custom | 不得静默覆盖用户已有模型配置 | 已提交；配置冲突需人工审查 |
| Tab/Cpp/FileSync/Git 路由 | 相关 RPC 经 `tab.leokun.cn` relay | 增加脱敏审计接入，转发目标和 body 语义未改变 | 计划支持 `local_official/external_relay` 双模式；`0.0.43` 的独立账号仅用于插件、Skills 和 MCP 控制面，不改变 Tab relay 身份 | 已提交审计；路由变更待验证和确认 |
| Cursor 控制面账号 | 原始 `0.0.43` 增加浏览器 PKCE 登录及 `api2.cursor.sh` 控制面转发 | 保留独立身份边界；凭据文件强制 `0600`，前端只接收脱敏状态；未登录时沿用本地兼容动作 | 仅允许插件、Skills、MCP 相关白名单路由使用真实账号，不改变 Cursor 客户端账号 | 已提交并通过单元/构建门禁；真实账号登录和权限行为待隔离验收 |
| 隐私审计 | 无本项目专用字段摘要观察器 | 默认关闭，仅输出 metadata/字段 presence/大小/事件类型/host 分类 | 仅允许 synthetic canary，禁止正文和凭据落盘 | 已提交并验证；不得恢复 raw dump |
| 结构化 observability | 原始 debug 工件分散且保留期/配额不足 | 已实现 schema v1 basic/full session、凭据清洗、轮转/配额和独立 CLI 分析 | schema v2 的 project/turn/capability/operation/outcome/build 指纹正在实施 | v1 已实现并验证；v2 Design 已批准、代码未完成 |
| 独立日志分析器 | 原始主线无本项目独立分析工作流 | 已实现临时 SQLite 流式 CLI、JSON/HTML/脱敏 ZIP | 规划独立 Wails GUI、多维检索、案例库、AI 证据包和客户端受限启动 | CLI 已验证；GUI 与闭环正在实施，独立于客户端发布 |
| 工具 replay | 未覆盖该阶段的专项保护 | 修复 `ForceBackgroundShell` 无 reasoning 时的历史 replay | 保持专项门控 | 已提交；不得泛化 |
| Commit Message | 存在精确 RPC 与 `/AiService/*` 通配 handler 的静态路由关系 | 增加精确路由优先级测试 | 真实 UI transport 未解析，不切换或删除路由 | 已调查未定性 |
| Repository/Docs/Upload | 多个本地处理器可返回 success | 增加隔离特征测试锁定能力缺失、错误状态和部分提交 | 暂不把 success 改成 failure，先验证客户端状态机 | 已确认缺陷；修复待设计 |
| 主题 | 深色硬编码较多，没有统一浅色偏好 | 已提交 `e9b6d70` 增加 light/dark token，默认浅色 | 已提交；窗口运行时验收待完成 |
| 广告 | 启动、聚焦和定时刷新可访问远端广告服务 | 已提交 `e9b6d70` 增加本地总开关，默认关闭 | 已提交；网络零请求验收待完成 |
| 更新 | 启动/定时检查，并可能直接下载更新 | 已提交 `e9b6d70` 改为手动检查、下载和安装确认，并收紧资源校验 | 已提交；发布资源验收待完成 |
| 文档与治理 | 原始仓库无本地治理基线 | 增加路线图、测试记录和决策文档 | 本 PRD、决策 PRD 和合并说明共同形成维护基线 | 文档变更正在提交 |

## 5. 明确不能宣称已完成的功能

以下事项目前仍不能写成“已实现”或“已验证”：

- 17 个低可见 RPC 的精确触发条件、失败重试和 privacy mode 差异。
- 合法隔离 Cursor token 的 Hobby/付费权限、额度、过期和刷新行为。
- 用户 Cursor token 的 Keychain 存储，以及把独立控制面身份扩展到插件、Skills、MCP 白名单之外的 `local_official/external_relay` 实现。
- relay、官方直连、本地实现、no-op 和禁用之间的逐 RPC 功能影响。
- Repository/Docs/Upload 改为诚实失败或完整持久化后的 Cursor UI/状态机影响。
- 客户端体验治理的网络零请求、旧配置迁移、窗口首屏和发布资源实机验收。
- v5 四页导航、`/access` 接入中心、系统主题、持久小时 usage 桶、Cursor 应用启动/有界重启，以及 Codex/Claude Code 真实授权。
- schema v2 的项目/轮次/能力语义采集、独立分析 GUI、多维检索、调查案例、AI 调查包和修复后复验闭环。
- 客户端“日志分析工具”按钮对独立安装应用的跨平台检测、启动与未安装引导。

未观察到请求、未命中 canary 或收到 RPC success，都不能单独推出“未发送”“未泄露”或“真实业务完成”。

## 6. 当前发布前验收缺口

`81be6166eca8b29ee0f51ce8446f2463d492477e` 的静态、单元、race、独立模块和前端构建门禁已通过。发布与运行时验收仍需要：

1. 验证旧 `config.yaml` 实机迁移不丢失模型、路由和监听地址。
2. 默认广告关闭时不访问 `ads.leokun.cn`；默认更新设置不请求 manifest 或资源。
3. 验证更新资源 URL、大小、checksum、取消和退出清理的真实发布资源流程。
4. 复核审计默认关闭；审计文件不含正文、凭据、canary 原值或完整 header。
5. 保留 `tab.leokun.cn` 现状路由，并以运行时元数据确认没有新增自动 fallback。
6. 完成窗口首屏、`18080/18090` 候选实例交接和 `/healthz` 验收。
7. 使用隔离账号验证 PKCE 登录、刷新、插件/Skills/MCP 白名单和断开清理，不读取或输出真实 token。
8. 构建并校验 `0.0.43` 跨平台资产、update manifest、GitHub Release 与安装结果。

## 7. `main@b438237` 同步记录

- **同步时间**：`2026-07-26T18:07:14-07:00`
- **原始上游核对点**：`leookun/cursor-byok main@9e057399690bff78cd7571dba2e7a14e12767585`
- **本地合并来源**：`main@b438237e08ed42f871dd7de2f05ee1dd9689a72a`
- **本地同步前基线**：`noad@841a49c0963a911b4c838157438e074ef930b33b`
- **merge commit**：`74c1c38ec3e68ed0f40fbefceaedd41c69616065`

### 7.1 实际保留与接受

- 保留 `noad` 的 Chat Completions 默认 Endpoint、无隐式 fallback、默认关闭审计、默认关闭广告和分阶段更新确认。
- 接受 provider artifact session 释放、请求摘要化、Agent replay 归并、俄语、静态 i18n、Linux 依赖、`0.0.41` 版本元数据与贡献文档。
- 广告策略保持本地显式开关为硬门禁；关闭时不展示，启用后允许所有 locale 展示。

### 7.2 重做与拒绝

- 重做三处冲突：`frontend/package.json`、`frontend/src/state/appState.js`、`frontend/src/views/Home.vue`，以本地配置事实源和状态机为准接入 scanner。
- 补齐 scanner 新增的 22 个源 key 在英、日、俄三种语言中的 66 个翻译；四个 locale 均为 246 个非空 key。
- 将 conversation/provider artifact 与 provider debug 文件创建权限收紧为 `0600`，并增加退出路径、payload 不驻留和 replay 专项测试。
- 拒绝 Windows 固定管理员/HKLM 安装，保留 user/machine 双 scope；拒绝发布说明中的 API Key 暴露引导和无法由实际 diff 证明的功能声明。

### 7.3 测试证据

- `git diff --cached --check`、冲突标记扫描；
- `go vet ./...`、`go test ./...`、`go test -race ./internal/backend/forwarder`、`go build ./...`；
- `yarn test:config-projection`；
- `yarn build` 连续两次且 catalog/locale 哈希稳定；
- i18n key、空值、中文 fallback 与 placeholder 集合校验通过。

既有 macOS linker 目标版本警告和 Vite chunk 大小警告不阻断退出码。

### 7.4 未验证边界

- 未停止或替换运行中的 `18080/18090` 代理，因此窗口首屏、候选实例交接、`/healthz` 与真实网络零请求验收仍待维护窗口。
- 广告关闭零请求、更新完整交互序列、旧配置实机迁移与发布资源安装不在本次自动化证据范围内。
- 本次不执行 `git push`，远程 `origin/noad` 不因本地快进自动更新。

## 8. `release/0.0.43` 同步记录

- **同步时间**：`2026-08-02T20:22:14-07:00`
- **原始上游目标**：`release/0.0.43@726f082e6b9570a6dbaf1c05cc93caf9e5e28c54`
- **本地合并来源**：`main@14f20fe654c9ad798510dad9e51f90d2d6a26b8f`
- **本地同步前基线**：`noad@17659ece3f283b4fa79af7963a347d508700fcea`
- **merge commit**：`81be6166eca8b29ee0f51ce8446f2463d492477e`

### 8.1 实际接受与整合

- 接受 Cursor command replay、`/summarize` 主动压缩、fork message transcript 同步和 `0.0.43` 版本元数据。
- 接受独立 Cursor 控制面账号：浏览器 PKCE 登录、access/refresh token 刷新，以及插件、Skills、MCP 白名单请求向 `api2.cursor.sh` 的账号鉴权转发。
- 接受账号卡片和 Wails API，但保留 noad 的隐私日志封装，并将组件颜色接入现有 light/dark 主题 token。
- 接受上游删除过时的“禁止写任何测试”技能规则；保留其他 `.agents` 调试与编码指导。

### 8.2 安全重做与边界

- 恢复 `policy.go`、`ModeLocal/ModeUpstream`、`PolicyMiddleware`、全局 fallback upstream 与显式 route precedence；local 处理失败不会自动切换到 official 或 relay。
- `cursor.sh` 本地模拟身份只在 `ModeLocal` 重写；`ModeUpstream` 保留原始官方身份，账号控制面请求再由专用 header patch 明确覆盖。
- 控制面凭据独立保存于应用数据目录的 `cursor-account.json`，新建、保存和加载均强制 `0600`；前端状态不含 access/refresh token，断开账号会删除文件。
- transcript 写入仅接受当前用户 `~/.cursor/projects/**/agent-transcripts`，拒绝相对路径、其他目录和缺少项目层级的路径，避免请求上下文扩大文件写入范围。
- `tab.leokun.cn` 目标和 17 条 relay 主链路保持原有显式动作；新增外部目标仅为账号登录所需的 `cursor.com` 和白名单控制面 `api2.cursor.sh`。

### 8.3 自动化证据

- `git diff --check`、`git diff --cached --check`、冲突标记和真实凭据特征扫描通过；merge commit 双父为 `17659ec` 与 `14f20fe`。
- 根模块：`go test ./...`、`go build ./...`、`go vet ./...` 通过。
- 路由与账号专项：覆盖 local/upstream、Policy middleware、fallback、显式 upstream precedence、请求关联、本地/官方身份隔离、控制面 header patch、凭据权限/脱敏/删除。
- `cursor-tab-server`：`go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- `tools/log-analyzer`：`go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- 前端：`npm run test:config-projection`、`npm run build` 通过；Wails bindings 已重新生成并确认 4 services、41 methods 后完成生产构建。

仅观察到既有非阻断告警：macOS linker 目标版本警告与 Vite chunk 大小告警。

### 8.4 未验证边界

- 未使用真实 Cursor 账号执行浏览器登录、token 刷新、插件市场、Skills 或 MCP 权限验收；自动化测试使用的均为合成身份。
- 未停止或替换运行中的 `18080/18090` 代理，因此候选实例交接、窗口首屏、`/healthz` 与真实网络目标序列未在本轮执行。
- 本记录生成时尚未构建、校验或安装 `0.0.43` 发布资产；发布产物、manifest、GitHub Release 和跨平台安装结果必须以随后 release 流程的实际输出为准。
- 账号凭据当前采用应用私有文件 `0600` 持久化，不宣称已接入 macOS Keychain、Windows Credential Manager 或 Linux Secret Service。

## 9. `0.0.44` 同步记录

- **同步时间**：`2026-08-03T20:44:15-07:00`
- **原始上游目标**：`main@639c452a0035de979edecc1ae2be654f5fd2383f`（最终文件树与 `release/0.0.44@0c9a07e` 一致）
- **本地合并来源**：`main@a31c7ea19834607f27cf634af1e14f6a2841ccf9`
- **本地同步前基线**：`noad@191f24c288272ed5232be80ef3e76046559ead90`
- **merge commit**：`d240ea3e76d5ccb2d73a4bca34ffc70e3c349b85`

### 9.1 实际接受与整合

- 接受 `0.0.44` 版本元数据、Cursor CLI 本地兼容路由及模型详情生成能力。
- 接受 Agent blob/checkpoint 预取、导入与同步，通过既有 broker 和客户端 blob store 保持会话状态一致。
- 接受独立 `cursor-proxy-debugger` 及其本地 Web UI，用于合成数据下的协议帧捕获、解码与调试。
- 接受 MiniMax 禁止 thinking 的模型兼容修复及对应专项测试。

### 9.2 安全重做与边界

- 保留 OpenAI 空配置默认使用 Chat Completions 的本地决策，没有被上游默认值覆盖。
- Cursor CLI 模型详情中的 provider API key 只由 `server.Local(...)` 本地兼容路由生成；upstream 模式不执行该 builder，未新增凭据外发目标。
- blob/checkpoint 预取内容仅在内存中处理，并通过既有 broker 写回客户端 blob store；未新增请求正文落盘路径。
- 调试代理与 Web UI 均强制使用 loopback 地址，拒绝 `0.0.0.0` 等非回环监听，避免形成开放代理。
- 调试指导明确禁止复制真实 Prompt、源码和凭据，验证只使用 synthetic 数据。
- 保留 `noad` 的默认浅色、广告关闭、启动更新检查关闭，以及 local/official/relay 无隐式 fallback 的既有状态机。

### 9.3 自动化证据

- `git diff --check`、`git diff --cached --check`、冲突标记和高置信度凭据特征扫描通过；merge commit 双父为 `191f24c` 与 `a31c7ea`。
- 根模块：`go test ./...`、`go test -race ./...`、`go build ./...`、`go vet ./...` 通过。
- `cursor-tab-server`：`go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- `tools/log-analyzer`：`go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- 前端：当前 `package.json` 不存在 `test:config-projection` 脚本；现有 `npm run build` 通过。
- 默认配置专项测试确认 `light / advertising=false / checkOnStartup=false` 保持不变。
- 已生成 macOS arm64、macOS amd64、Windows amd64 和 Linux amd64 四个平台资产；`release:verify:assets` 与分析器隔离检查通过。
- 面向 `yaogjim/cursor-byok` 生成 `update.json`，四个平台 URL、文件大小和 SHA-256 均与本地产物一致。

仅观察到既有非阻断告警：macOS linker 目标版本警告与 Vite chunk 大小告警。

### 9.4 未验证边界

- 未停止或替换运行中的 `18080/18090` 代理，因此候选实例交接、窗口首屏、`/healthz` 与真实网络目标序列未在本轮执行。
- 调试代理仅使用单元测试和 synthetic 边界验证，未捕获或保存真实 Prompt、源码、凭据或会话正文。
- GitHub Release 和 annotated tag 尚未创建；最终目标必须指向包含本记录的 `noad` 提交。
- 四个平台资产已完成静态结构、大小、SHA-256 与分析器隔离校验，但尚未在各目标系统执行安装和启动验收。

## 10. `0.0.45` 同步记录

- **同步时间**：`2026-08-05T02:41:06-07:00`
- **原始上游目标**：`main@c55c575a583694e8c25be73b08ac7b3ec496e1d5`
- **本地 `main` 合并提交**：`d082e79019bdb5a472003db37f09e23d083de1f2`
- **本地同步前基线**：`noad@869f45f539e416b0fbe2c95c25b33f3c6350615e`
- **`noad` merge commit**：`ad50039f5bf83b8df257c5375d6f24b3a16ae31e`
- **拓扑**：`ad50039` 的两个父提交分别为 `869f45f` 与 `d082e79`；`d082e79` 的两个父提交分别为旧 `main@a31c7ea` 与 `upstream/main@c55c575`。

### 10.1 实际接受与整合

- 接受 `0.0.45` 版本元数据。
- 接受上游为修复会话消失而执行的 blob/checkpoint 同步回退：删除 checkpoint blob 写入、prefetched blob 导入和相关缓存/超时状态，恢复 legacy checkpoint 发布路径。
- 接受 Windows VDI 白屏规避能力；只有显式将 `CURSOR_BYOK_DISABLE_WEBVIEW_SANDBOX` 设为可解析的真值时才向 WebView2 传入 `--no-sandbox`，默认仍启用 sandbox。
- `internal/app/runner.go` 的唯一内容冲突通过同时保留本地主题背景职责和上游 VDI 参数构造函数解决，没有覆盖本地广告、更新或启动状态机。

### 10.2 保留与拒绝

- 保留 `noad` 的默认浅色、广告关闭、启动更新检查关闭、审计边界和 local/official/relay 无隐式 fallback；本次上游差异没有新增外部目标、凭据转发或正文落盘路径。
- 保留本地删除 `release-notes.md` 的既有决策，没有重新引入群组信息或未经本地证据验证的宣传文案。
- 不再把 `0.0.44` 的 blob/checkpoint 同步描述为当前有效能力；该实现及其专项测试已随上游会话消失修复回退。

### 10.3 自动化证据

- 合并冲突全部解决，`git diff --cached --check` 通过；`ad50039` 为真实双父 merge。
- 根模块：`go test ./...`、`go test -race ./internal/backend/forwarder`、`go build ./...`、`go vet ./...` 通过。
- `cursor-tab-server`：`go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- `tools/log-analyzer`：`go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- 前端：`npm run build` 通过。
- 仅观察到既有非阻断告警：macOS linker 目标版本警告与 Vite chunk 大小告警。

### 10.4 未验证边界

- 本轮没有用可复现运行时场景验证“会话消失”缺陷已消除，也没有验证回退后跨会话 fork、prefetched blob 或 checkpoint 恢复的实际降级边界。
- 未在 Windows VDI 环境验证白屏规避；`--no-sandbox` 会降低 WebView2 隔离强度，只应在受影响环境中由用户显式启用。
- 未停止或替换运行中的 `18080/18090` 代理，因此候选实例交接、窗口首屏、`/healthz` 与真实网络目标序列不在本次证据范围内。
- 本轮没有生成或验证 `0.0.45` 发布资产，也没有执行 `git push`。

## 11. 后续版本记录格式

上游更新或本地功能落地后，在本 PRD 中追加一条版本记录，至少包含：

- 上游仓库与目标 commit、同步时间；
- 本地基线 commit；
- 实际保留、移植、重做、拒绝的功能；
- 受影响的外部目标、凭据、审计、配置或状态机；
- 测试与运行时证据；
- 新增的未验证边界和用户确认项。

## 12. 会话统计重置与已关闭日志清理

- **工作包**：`session-metrics-log-cleanup-20260817`
- **代码基线**：`noad@69177ead75734bbdfac38af1fac71ec874a5206b`
- **状态**：已实现待验收

### 12.1 精确边界

| 能力 | 允许删除/改写的范围 | 明确不改的范围 |
| --- | --- | --- |
| 首页会话统计重置 | 只清零 `~/.cursor-local-assistant-v2/history/usage.json` 的 `totals`、`daily`、`recent_events`、`event_index`，并更新 `updated_at`；新请求从 0 累计 | 不删除 `history/<conversation-id>/` 会话历史，不删除 `usage.json` 文件本身 |
| 配置页清理日志 | 只删除 `~/.cursor-local-assistant-v2/logs/traces/` 下 `manifest.status == "closed"` 的采集 session | 保留当前 open session、普通运行日志、坏 manifest、`open_failed`、symlink；空 root / `.` root 拒绝执行，避免把 `./traces` 当目标 |

两项操作都保持确认弹窗，取消不调用后端。统计重置由 `UsageFileStore.Reset()` 在既有 `usage.json.lock` 下原子写回 schema v2 空文档；bridge / 前端不拼 JSON。日志清理继续走既有 `CleanupClosedLogSessions` 链路，不对同进程重复清理重复计数。

### 12.2 当前实现位置

- 统计写入与重置：`internal/backend/forwarder/usage_store.go`
- 首页 Wails 接口：`internal/bridge/metrics.go` 的 `ResetHomeMetricsSummary`
- 首页确认与按钮：`frontend/src/views/Home.vue`、`frontend/src/components/HomeMetricsCard.vue`
- 日志清理硬化：`internal/observability/storage.go` 的 `CleanupAllClosedSessions`；配置页文案统一为“清理日志”

### 12.3 验证证据与未验收项

已实际跑过：

- `gofmt` 已作用于本轮编辑的 Go 文件。
- `go test ./internal/backend/forwarder ./internal/historymetrics ./internal/bridge ./internal/client ./internal/observability -count=1` 通过；`historymetrics` / `bridge` 无测试文件。
- `go test -race ./internal/observability -count=1` 通过。
- `go vet ./internal/backend/forwarder ./internal/historymetrics ./internal/bridge ./internal/client ./internal/observability` 通过。
- `node frontend/scripts/test-config-projection.mjs` 通过。
- `npm run build --prefix frontend` 通过；仅有既有 Vite chunk 大小告警。

未完成：

- 建议命令 `go test -race ./internal/backend/forwarder ./internal/observability` 在编译 `gen/aiserverv1/aiserverv1connect`（约 3 万行）时长时间未结束，不能写成已通过。

仍待运行时验收：

- 真实用户目录下点重置后，首页从 0 展示，且 `history/<conversation-id>/` 仍在。
- 真实配置页点“清理日志”后，只少已关闭 traces session，当前采集 session 与 `logs/app` 普通日志仍在。
- 多窗口同时点清理时，删除结果不重复计数。

## 13. `0.0.48` 同步记录

- **同步时间**：`2026-08-17T18:39:40-0700`
- **发布核对时间**：`2026-08-17T20:56:44-0700`
- **原始上游目标**：`upstream/main@a3ec2a0dfc9029863dab1fd802b0545c05151a67`（`release: 0.0.48`）
- **本地 `main` 合并提交**：`cbf7cb4030e24ddfbd366c0e87bea969ba5e2421`
- **本地同步前基线**：`noad@bf994d0503fab2b9355cecb9daed3d364a6ee3a1`
- **同步分支**：`sync/main-into-noad-20260817-183443`
- **旧备份**：`backup/noad-before-upstream-20260817-183443` @ `bf994d0`
- **新备份**：`backup/noad-before-main-20260817-205644` @ `bf994d0`（仅本地，未 push）
- **`noad` merge commit**：`54d967e766eb85641dd438913b865c34eac188ad`
- **发布提交**：`93dbae694a1cb0c8d5d312496dea3a115ed0855a`
- **拓扑**：`54d967e` 为真实双父 merge，父提交分别为 `bf994d0` 与 `cbf7cb4`；`cbf7cb4` 再以真实双父合入 `upstream/main@a3ec2a0`。
- **远端**：`origin/noad` 已与 `93dbae6` 对齐；`origin/main` 已与 `cbf7cb4` 对齐；未 push `upstream`。
- **Release**：<https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.48>，`targetCommitish=93dbae694a1cb0c8d5d312496dea3a115ed0855a`

### 13.1 实际接受与整合

- 接受 Read Image：工具读图、按内容哈希持久化 `.blobs/sha256` 图片 blob（`0600`），以及相关 projector / provider / exec bridge 测试。
- 接受可选推理强度：OpenAI `reasoningEffort` 允许空值，请求可不携带该参数；前端新增「不设置」选项。
- 接受每套安装独立根证书：删除仓库内共用 `ca.key`，启动时 `LoadOrCreateManager` 生成或复用本机 CA；证书修复需重启 Cursor。
- 接受上游 `0.0.48` 打包与 i18n：`build/config.yml` `info.version` 为 `0.0.48`；locale / catalog 经前端生产构建重生，见提交 `554a56a`。

### 13.2 保留与拒绝

- 保留 noad 的 Chat Completions 默认 Endpoint：空 `openAIEndpoint` 归一化为 `/v1/chat/completions`，不覆盖用户已显式选择的 Responses / Custom。
- 保留 local / official / relay **无隐式 fallback**；local 失败不会静默转官方或 relay。
- 保留默认 `light`、广告关闭、启动更新检查关闭，以及检查 / 下载 / 安装分阶段确认。
- 保留 fork 发布身份：`internal/buildinfo.ReleaseRepo=yaogjim/cursor-byok`，更新 URL 只指向该 fork Release。
- 保留隐私边界：审计默认关、不落敏感正文/凭据；本次差异未发现新增未知硬编码外发域名、广告关闭仍请求、更新器跳过确认，或把请求正文/凭据写入日志。
- 保留 noad 会话统计重置与已关闭日志安全清理（`bf994d0`），以及模型卡片按钮溢出修复。
- 拒绝把发布说明改成上游群组宣传主体；`release-notes.md` 按 noad 产品说明风格重写。

### 13.3 冲突裁决

| 文件 | 裁决 | 理由 |
| --- | --- | --- |
| `internal/app/runner.go` | 手工并集 | 用上游每机 CA `caCertPEM`，保留 noad `DefaultConfig()` 与后续广告/主题启动路径 |
| `frontend/src/i18n/locales/*` | 先并 key，再扫描 | 保留 noad 仍在用的「范围 1–90 天」；删掉源码已替换的旧推理强度文案 |
| `frontend/src/i18n/generated/catalog.json` | 前端 build 重生 | 与上次 `d013961` 相同，不手改 line 号 |
| `release-notes.md` | 按 noad 风格重写 | 写入 0.0.48 上游能力 + 保留策略，联系方式保留 |
| `frontend/src/state/appState.js`、`internal/backend/forwarder/service.go`、`internal/backend/server/config/types.go` | 自动合并后人工复核 | 只并入可选推理强度与读图 blob，未改广告/更新/fallback |

### 13.4 测试与构建证据

只记录已实际观察到的结果，不把缺失命令或未跑完的门禁写成通过。

**`main@cbf7cb4` 合入上游时已跑过：**

- `git diff --check`
- `go test ./internal/...`
- `go vet ./internal/...`
- `npm run build --prefix frontend`
- `cursor-tab-server`：`go test ./...`、`go test -race ./...`、`go vet ./...`

当时 `main` 树里 **没有** `frontend/scripts/test-config-projection.mjs`；`tools/log-analyzer` 在 `main` / `upstream/main` 都没有受 Git 管理的包，`go test ./...` 返回 “matched no packages” 并以退出码 1 结束。这两项 **不能** 记为通过。

**`noad` 发版线已核实：**

- 合并提交前已跑一次 `npm run build --prefix frontend`，用于 i18n catalog 同步；仅有既有 Vite chunk 大小告警。
- 发布后复查：`git diff --check` 通过；`go test ./internal/...` 通过（多数包为缓存结果）。
- 当前 `noad` 树里 `frontend/scripts/test-config-projection.mjs` 与 `tools/log-analyzer` 源码/测试文件都在，但本轮 **没有** 完整跑 `node frontend/scripts/test-config-projection.mjs`，也 **没有** 完整跑 `tools/log-analyzer` 的 `go test ./...` / race / vet。不得把“文件存在”写成“测试通过”。
- `go test -race ./internal/backend/forwarder ./internal/backend/agent/model ./internal/certs ./internal/runtime` 曾启动，编译时间长，**没有**拿到完成退出码，不能记为通过。

**发布资产（已核验，无 Linux）：**

| 资产 | size | SHA-256 |
| --- | --- | --- |
| `cursor-byok-0.0.48-macos-arm64.tar.gz` | `20596523` | `f96a985894a3bf979d7a3a24ec0885c177d41b33b037589b36c25cc9c7b2cbff` |
| `cursor-byok-0.0.48-macos-amd64.tar.gz` | `21591405` | `350a8070caa20464dfe807e1259368837bbf1c1aa779aae1beeb57e89a089048` |
| `cursor-byok-0.0.48-windows-amd64.zip` | `21447142` | `f84b8483b0da87794b218b3dcddfa3193190d94d1caa045618f7b87edd894588` |

`update.json` 三个平台 URL 均指向 `https://github.com/yaogjim/cursor-byok/releases/download/v0.0.48/...`，无 `linux-amd64`。本地文件 size/SHA-256 与 `update.json`、GitHub Release digest 一致。

### 13.5 未验证边界

- 未停止或替换运行中的 `18080/18090` 代理，因此窗口首屏、候选实例交接、`/healthz` 与真实网络零请求验收不在本次证据范围内。
- Read Image 实机读图、独立 CA 安装后 Cursor 信任、可选推理强度对真实模型请求的省略行为，均未做运行时验收。
- 三平台安装包已上传，但 **没有** 在 macOS / Windows 目标机执行安装和启动验收；Linux 资产按 fork 策略未构建。
- `frontend test-config-projection` 与 `tools/log-analyzer` 完整门禁、以及 targeted race 的完成结果，均不在本记录的通过范围内。

## 14. `0.0.49` 受控同步记录（用户已确认本地 merge）

- **同步时间**：`2026-08-20T02:40:22-0700`
- **原始上游目标**：本地锁定 `upstream/main@564f2bdcaec790863aca86403cedbfc77191bd43`（包含于 `main@305b108`）
- **本地 `main` 来源**：`305b108e8cb44f68c15672809fc579acc65a9835`
- **本地同步前基线 / 目标 `noad`**：`9e6936f15fc45a351e9b53e2e99f321aa1b79ac1`
- **共同祖先**：`cbf7cb4030e24ddfbd366c0e87bea969ba5e2421`
- **同步分支**：`sync/main-305b108-into-noad-20260820-004943`
- **备份分支**：`backup/noad-before-main-20260820-004943` @ `9e6936f`（仅本地，未 push）
- **隔离 worktree**：`/Users/yaogj/Downloads/works/yaogjim/cursor-byok/cursor-byok-sync-main-305b108-20260820-004943`
- **merge commit**：即将创建；用户已确认。当前仍为 `git merge --no-ff --no-commit main` 已解决 index；即将以 `9e6936f` 与 `305b108` 为双父创建本地 merge，不预填未知 commit SHA；**仍未 push**。
- **待提交 index**：`git diff --cached --name-only` 当前 **59** 个文件。
- **拓扑意图**：用户已确认创建以 `9e6936f` 与 `305b108` 为双父的本地 merge，并把本地 `noad` 推进到该提交。

### 14.1 实际接受与整合

- 接受 `IsConnected` schema：`proto/aiserver_v1.proto` 重建生成物，并在 `internal/backend/server/upstream/client.go`、`internal/backend/host.go`、`internal/cursor/state_db.go` 接入精确 mock 与 statsig gate。
- 接受 OpenAI Responses 孤儿 `output` 修复及专项测试。
- 接受交错 tool-call replay 与 projector replay trim。
- 接受 transcript 新语义；删除启动回填测试，**不恢复**已移除的启动回填行为。
- 接受配置导入导出（`config_transfer` 及平台原子替换）和 client 锁分层；导入后仍以后端 YAML 为事实源。
- 接受前端导入导出 composable / API / 配置页 UI，以及四个 locale 与 catalog 的扫描并集。
- 接受 main 的 `prompt/` 更新和 `0.0.49` 版本元数据。

### 14.2 保留与拒绝

- 保留 noad 首包前安全重试、流截断检测、审计默认关、日志脱敏、MITM 只读观测、observability basic 默认、`ForceBackgroundShell` 专项放行和路径沙箱。
- 保留 YAML 事实源与默认值：`light / advertising=false / checkOnStartup=false / observability=basic`。
- 保留广告关闭零请求、手动三阶段更新、local/official/relay **无隐式 fallback**。
- 保留 fork 发布身份：`yaogjim/cursor-byok`；更新资源只指向该 fork。
- 保留 Windows `WAILS_INSTALL_SCOPE` user/machine 双 scope，仅同步版本号到 `0.0.49`。
- 删除 prompt 中的 `leookun` 身份覆盖；既有作者署名与 updater 负向 fixture 不改。
- 拒绝含糊上游 release note；`release-notes.md` 按 noad 风格重写为 v0.0.49。
- 修复导入导出中的 duplicated `routing` YAML key，以及旧 `Config.Log` 测试断言。
- 本轮 merge 曾把 Windows 安装器覆盖成固定 admin/HKLM，已恢复 dual scope。
- 最终代码 review 发现并修复 P1：`frontend/src/services/clientApi.js` 里 noad 两参数 `withApiLogging(name, runner)` 被三参数导出调用误用，导出在打到后端前就会 TypeError；已改回两参数契约。review 其余范围无阻塞问题。

### 14.3 冲突裁决

MERGE_MSG 记录的 **8 个文本冲突**：

| 文件 | 裁决 | 理由 |
| --- | --- | --- |
| `frontend/src/i18n/generated/catalog.json` | 前端扫描构建重生 | 不手改；连续两次生产构建后 SHA-256 稳定 |
| `frontend/src/i18n/locales/en-US.json` | key/译文并集 | 保留 noad 文案，接入导入导出等新 key |
| `frontend/src/i18n/locales/ja-JP.json` | key/译文并集 | 同上 |
| `frontend/src/i18n/locales/ru-RU.json` | key/译文并集 | 同上 |
| `frontend/src/i18n/locales/zh-CN.json` | key/译文并集 | 同上 |
| `frontend/src/state/appState.js` | 以 noad 配置投影为底接入导入导出 | 后端 YAML 仍是事实源；默认偏好不变 |
| `internal/backend/forwarder/transcript_adapter_test.go` | 按最终契约改写 | 接受新 transcript 语义，删除启动回填断言 |
| `release-notes.md` | 按 noad 风格重写 | 写 0.0.49 能力 + 保留策略，拒绝含糊宣传 |

关键 **无文本冲突标记、但必须人工裁决** 的语义冲突：

| 主题 | 文件 | 裁决 |
| --- | --- | --- |
| Agent 主链路 | `openai.go`、`router.go`、`projector.go`、`transcript_adapter.go`、`service.go` | 并集：接受 orphan output / 交错 tool-call / replay trim / transcript 新语义；保留 retry、截断、审计、observability、`ForceBackgroundShell`、路径沙箱 |
| 配置导入导出与 routing | `config_transfer.go`、`config.go`、`lifecycle.go`、`service.go` | 接受导入导出与锁分层；以 noad schema 为准，去掉 legacy 重复 `routing` 键 |
| 前端导出 API 日志契约 | `frontend/src/services/clientApi.js` | 最终 review 修复：导出调用必须遵守 noad 两参数封装，不得沿用三参数写法 |
| Windows 安装 scope | `build/windows/nsis/wails_tools.nsh` | 拒绝固定 admin/HKLM；恢复 `WAILS_INSTALL_SCOPE` 双模式，只升版本 |
| prompt 身份 | `prompt/**` | 接受 prompt 更新与 `FAKE_MODEL_NAME` 渲染；删除 `@leookun` 身份覆盖 |
| 发布身份 | `internal/buildinfo/buildinfo.go`、`Taskfile.yml` | 保持 `yaogjim/cursor-byok`，不回写上游 Release |

### 14.4 测试与构建证据

只记录本轮实际跑过的结果。未生成 `0.0.49` 发布归档，因此 **没有** 运行 `task release:verify:analyzer-isolation`，不得记为通过。本地 `task build` 只产出 ignored DMG，不能代替发布归档。

- proto 生成成功：`gen/aiserverv1` 含 `IsConnectedRequest` / `IsConnectedResponse`。
- 官方 Wails bindings 生成成功：`frontend/bindings/{cursor,github.com,log}`。
- 协议 / Agent、配置、产品不变量专项通过。
- `node frontend/scripts/test-config-projection.mjs` PASS。
- `npm run build --prefix frontend` 连续两次 PASS；catalog SHA-256 `3aea2eec12cde08f5b791defffab23455545d3b85c7edbbd3bfb4cc4f9ffed31` 两次稳定。四个 locale 与 catalog 均由扫描生成，不手改。
- 新增 `frontend/scripts/test-client-api-logging.mjs` 静态契约检查：24 个调用点全部为两参数；该检查 PASS。P1 修复后 `test-config-projection` 与 frontend build 重新 PASS，catalog SHA-256 不变。
- 根模块：`GOPACKAGESDRIVER=off go test -p=1 ./internal/...` PASS；`GOPACKAGESDRIVER=off go vet -p=1 ./internal/...` PASS。
- `cursor-tab-server`：`go test -p=1 ./...`、`go test -p=1 -race ./...`、`go vet ./...` PASS。
- `tools/log-analyzer`：`go test -p=1 ./...`、`go test -p=1 -race ./...`、`go vet ./...` PASS。`cmd/log-analyzer-gui` 带 `//go:build gui`，默认 `./...` 不含该包；GUI 逻辑由 `internal/gui` 覆盖。
- macOS 正式构建 `task build` PASS，约 8m12s。产物仅本地 ignored `bin/macos-arm64.dmg`：`23577563` bytes，SHA-256 `6105a69a0d1ea48fa4b822a5ea90fc496e5c2d5e75efaed04939e9662e46cd7b`；Mach-O arm64、non-fat/非 universal；Info.plist 版本 `0.0.49`、Bundle ID `com.cursor.wuxianxubei`；adhoc 签名且 `codesign --verify --deep --strict` PASS。该 DMG 不进入 index。
- `git diff --check` 与 `git diff --cached --check` 通过；无 unmerged。

仅观察到既有非阻断告警：macOS linker 目标版本警告、Vite chunk 大小告警。

### 14.5 未验证边界

- 最终 `git fetch upstream --prune --no-tags` 已通过；`upstream/main` 仍为锁定 SHA `564f2bdcaec790863aca86403cedbfc77191bd43`。此前一次 fetch 曾因 `Could not resolve host: github.com` 失败，不得再写成待 fetch。
- 未做广告零请求、更新完整交互、Windows 安装/启动、Wails 实机运行时验收。
- 未停止或替换运行中的 `18080/18090` 代理，未影响这两个端口。
- 本地 `task build` 仅产出 ignored `bin/macos-arm64.dmg`，**未** notarize、**未** 生成 release 归档、**未** 做 Gatekeeper 首次启动实机验收；不得把该 DMG 写成已发布资产。`task release:verify:analyzer-isolation` 仍未运行。
- 仍缺 Wails runtime mock 下的真实导出调用单测；当前只有静态两参数契约检查。
- 非阻塞残余测试风险：缺少 `IsConnected` 精确路由测试；缺少 `ForceBackgroundShell` × replay trim 交叉测试。
- 未用 `-tags gui` 专项编译 `tools/log-analyzer/cmd/log-analyzer-gui`。
- 用户已确认创建本地 merge commit 并推进 `noad`。本记录仍不宣称已 push；运行时未验证边界仍成立。

## 15. v5 四页控制面（已批准 Design，未实现）

- **工作包**：`ui-v5-shell-20260826`
- **决策来源**：已批准计划「v5 界面重构复审后的完整实施计划」；原型 [`docs/cursor-assistant-v5.html`](cursor-assistant-v5.html)
- **权威冻结**：工作决策基线 §7.5 / §10.14；系统 Design §4.2 / §10.2 / §14.16
- **状态**：阶段 0 文档已冻结；**代码未开始**。不得写成已实现、已验证或已交付。

### 15.1 与当前已落地能力的关系

- 当前控制面仍是五页同层导航（`/`、`/cursor`、`/gateway`、`/models`、`/settings`）与 daily `GetHomeMetricsReport`（`7d`/`30d`/`all`）。该实现见双集成工作包，Wails 视觉仍未做。
- v5 将主导航改为总览 / 接入 / 模型 / 设置，并把 Cursor、Gateway 收进 `/access?client=...`。这是新产品 IA，不是对五页实现的事后描述。
- section 保存、Gateway token 红线、18080/18090 隔离和「不用 `recent_events` 冒充完整热力图」继续有效，并扩展为：小时曲线也不得从 `recent_events` 推断。

### 15.2 已冻结、尚未落地的合同

| 能力 | 冻结口径 | 当前代码 |
| --- | --- | --- |
| 四页 IA | 顶部标签：总览 `/`、接入 `/access`、模型 `/models`、设置 `/settings` | 仍为五页侧栏 |
| 旧路由 | `/cursor`→`/access?client=cursor`，`/gateway`→`/access?client=gateway`；`/config`、`/model-config` 保持 | 仅后两项重定向已落地 |
| 脏状态 | 按 client/scope 检查；接入标签脏点为 OR；禁止总保存 | 按整页 dirty |
| 计数 | 接入数、模型数来自真实状态 | 原型「3 / 14」不得进入产品 |
| 模型导入导出 | 文案必须标明完整配置，不是仅模型文件 | 现有能力是完整配置；文案未按 v5 改写 |
| 主题 | `light` / `dark` / `system`，默认 `light` | 仅 `light` / `dark` |
| 小时统计 | 持久 `usage.json` 小时桶；`24h` 用小时，`30d`/`all` 用日桶 | 仅 daily；无小时字段 |
| Cursor 重启/打开 | 无 `RestartProxy` 则不显示重启；打开应用必须有平台失败语义 | 无有界重启、无 LaunchCursor 合同 |
| Codex/Claude | 生产空态/`unsupported`；fixture 仅测试/DEV；真实授权另过 Design Gate | 无账号中心；原型 example.com 账号禁止进生产 |

### 15.3 明确不能宣称

- 未改前端/后端源码，未跑 Go/前端门禁，未做 Wails 视觉。
- 原型 HTML 不是产品实现。
- Codex HTTP `/v1/responses` 已存在，不等于 Codex 订阅账号授权中心已实现。
- 本记录不改变 ACP 工作包的 blocked 状态。

## 16. 上游 `v0.1.5` 深度审阅（2026-08-30）

### 16.1 基线与同步结果

- 此前审阅基线为 `76003a907a559dc385fea5d8b96c8e02f928a89f`；正式版 `v0.1.5` 为 `b807608bf3622ea08fdde9ace5840701a3d039a8`；最新 `upstream/main` 为 `9120b90be7b3aa3e0a81fb4d056f1ad905fec23f`。
- `76003a9..b807608` 的主增量是 Provider 流空闲超时、Deno 插件框架、Codex/Grok 内置插件、Rule 本地镜像与离线日志、模型分组及版本元数据。`b807608..9120b90` 只删除废弃的 `server_backup/**`，没有新增核心运行时能力。
- 本地 `main@305b108e8cb44f68c15672809fc579acc65a9835` 与 `upstream/main@9120b90` 均不是对方祖先，共同祖先为 `564f2bdcaec790863aca86403cedbfc77191bd43`，因此不能 fast-forward。
- 已在隔离 worktree 尝试显式 `--no-ff` 合并。冲突包括 `.gitignore` 内容冲突、多处“本地修改但上游删除”、`cursor-proxy-debugger` 目录迁移，以及 `internal/backend/agent/model/openai.go`、`frontend/src/components/ModelEditor.vue`、`frontend/src/state/appState.js` 等跨架构文件。按 Runbook 执行 `git merge --abort`，没有自动裁决。
- 中止后本地 `main` 仍为 `305b108`，`gateway` 仍为 `534ffc02e37fd3420286c7e060240355f7a06d64`；功能源码无残留改动，无 `MERGE_HEAD`，未创建提交，未 push；当前工作树只包含本审阅的三份项目文档更新。若以后要求推进 `main`，需要单独批准人工冲突裁决，不能把本次审阅写成同步成功。

### 16.2 能力对比与移植结论

| 功能簇 | 上游 `v0.1.5` | 当前 `gateway` | 结论 |
| --- | --- | --- | --- |
| 普通 OpenAI 缓存 | 请求携带稳定 cache key | 已按 `ConversationID` 为 GPT 请求生成 `prompt_cache_key`，Responses 与 Chat Completions 都覆盖 | 已等价；不重复移植 |
| Anthropic 缓存 | Provider 侧通用协议支持 | 已有 system、tools、稳定历史 frontier 与 `cache_control` 断点，并记录 cache read/write token | 当前更细；不回退 |
| managed Codex 缓存亲和 | 同一个 `cacheKey` 写入 `prompt_cache_key`、`session-id`、`thread-id`、`x-client-request-id` | 生成普通 OpenAI cache key 后，会在 managed Codex 白名单中删除；请求头只保留 `originator` 与 `ChatGPT-Account-Id` | **P1 独立移植** |
| `previous_response_id` | 插件请求采用完整输入与 `store=false` | managed Codex 因无法验证 Response ID 所属账号而 fail-closed 删除；static OpenAI 保留 | 保持当前安全边界，不随缓存改动放宽 |
| Provider 流空闲超时 | 统一事件消费看门狗和 typed timeout | 已有 typed `stream_idle_timeout`、OpenAI/Anthropic 统一 watchdog、观测、retry/fallback 抑制及测试 | 已等价或更强 |
| Rule 基础能力 | CRUD、本地 Markdown 镜像、Prompt 接入 | 已有 `KnowledgeBaseAdd/List/Update/Remove`、按内容哈希去重与非 Debug 会话 Prompt 注入 | 基础能力已具备 |
| Rule 离线/云同步 | upstream-first，失败写本地 journal，恢复后回放；完整列表写穿本地镜像 | 仅本地规则，没有 journal、镜像、冲突与恢复状态机 | 先 P0 加固，再 P1 设计移植 |
| 官方模型 / BYOK 切换 | 统一模型目录与新 transport | 已有显式路由、静态凭据、managed Codex/Grok、多渠道与无隐式 fallback 边界 | 已等价或更强 |
| 官方/BYOK 子代理 | 统一 conversation runtime 可选择模型 | 已有父子模型传播、独立子代理状态和安全恢复基础 | 不重写主链路 |
| Codex/Grok | 以内置插件提供 OAuth、模型与额度 | 已有 Go 原生设备授权、导入、多账号、刷新、用量、配额与安全轮换 | 已等价或更强 |
| 模型分组 | `group_name` 持久化并用于模型选择器 badge | 当前 UI 已有 provider 筛选，但官方/BYOK 展示分组仍可更清晰 | P1/P2 仅移植展示语义 |
| 通用插件 | Deno worker、manifest、资源、安装、状态、协议 SDK 与管理 UI | 当前 Go/Wails 没有对应运行时；Codex/Grok 已原生实现 | 拒绝整体移植 |

### 16.3 最小可移植单元

#### P0：先加固现有 Rule 持久化

当前 Rule 文件存储有以下已核实边界，不能在此基础上直接叠加云同步：

1. 规则目录与文件以 `0755/0644` 创建，规则正文可能对同机其他账号可读；应收紧为应用私有目录和 `0600` 文件，并验证旧文件权限迁移。
2. 写入使用固定 `<id>.md.tmp` 后直接 rename；应改为同目录唯一临时文件、写入后同步并原子替换，同时清理失败残留。
3. 扫描和读取会跟随 `.md` 符号链接；应拒绝 symlink、非普通文件和逃逸出规则根目录的目标，更新与删除也必须复核最终路径。
4. 没有显式单文件/总量上限；应加入有界读取和写入，避免规则正文无界进入内存及 system prompt。
5. Rule CRUD 与 docs index 更新当前是两个独立写操作，后者失败只记日志；需先明确“Rule 为事实源、索引可重建”的恢复合同和测试，不能把部分成功误报成完整同步成功。

P0 验收至少覆盖：权限、symlink、路径逃逸、并发写、崩溃残留、原子替换、损坏文件隔离、容量上限、CRUD 与索引恢复。该项是后续 Rule 离线日志和云同步的前置条件，不代表当前 Rule 功能不可用。

#### P1：managed Codex 缓存亲和

- 从当前稳定 `ConversationID` 派生不含 token、账号 ID 或 Prompt 的 cache affinity key；同一会话和同一已解析账号内保持稳定。
- 仅在 `credentialSource=codex` 且目标为 `chatgpt.com` 的 managed Codex 路径写入 `prompt_cache_key`、`session-id`、`thread-id` 与 `x-client-request-id`；static OpenAI、Grok 和其他兼容端点不受影响。
- 账号轮换后必须重新绑定账号隔离范围，避免把一个账号签发的缓存亲和信息跨账号沿用。日志只允许记录是否设置和短哈希，不记录完整键。
- 继续剥离 `previous_response_id`；缓存亲和键不能作为 Response ID 账号归属证明，也不能改变 `store=false`。
- 单元测试覆盖正常构造与 `RequestBodyOverride`、managed/static 分流、账号轮换、空会话、header/body 同源、敏感信息扫描；集成测试用 fake upstream 核对请求，不使用真实 token。
- 上线前后以 `cached_tokens` / cache read token 比例、首 token 延迟、总延迟和错误率做同负载对照。没有这些数据前，只能宣称“补齐亲和合同”，不能宣称命中率或性能提升幅度。

#### P1：Rule 离线日志与本地镜像语义

- P0 完成后，再设计本地事实源、离线 journal、远端镜像、临时 ID 提升、回放幂等、冲突处理、损坏恢复和完整列表覆盖条件。
- 云同步会把 Rule 正文发送到 Cursor 官方控制面，必须是可见的显式 opt-in，默认保持本地；UI 必须说明外发目标、数据类型、断开后的本地保留与删除语义。
- 不直接复制上游 `upstream-first` 行为。当前项目坚持无隐式外发和无隐式 fallback，离线可用不能成为自动上传 Rule 正文的授权。
- 需要补跨来源去重：本地共享 Rule 与 IDE 请求已经携带的规则按规范化内容去重，避免同一规则重复占用 Prompt 前缀。

#### P1/P2：模型展示分组

只移植展示合同：在模型选择器和模型管理页明确区分官方模型、静态 BYOK、Codex 订阅与 Grok 订阅，并允许可选自定义分组名。路由、凭据、模型 ID、fallback 与子代理选择继续使用现有事实源，不引入上游 Rust 模型目录。

### 16.4 拒绝、暂缓与无需重复移植

- **拒绝整体移植 Deno 插件框架**：本次上游相关核心范围新增约 9,344 行，耦合 Rust/Tauri、Deno worker、插件安装/状态/协议/资源和 UI。当前 Codex/Grok 用例已由 Go 原生实现，引入第二运行时的收益不足以覆盖供应链、沙箱、升级和发布成本。
- **暂缓 conversation runtime 整体迁移**：上游替换当前 run 时取消旧 run、结束时按 run ID 条件释放的行为值得作为测试参考，但其完整生命周期、队列与内存上限没有足够 benchmark 证明，不以架构语言差异推导性能优势。
- **无需重复移植**：官方/BYOK 切换、双向子代理模型使用、普通 OpenAI/Anthropic prompt cache、Provider 流空闲超时、Codex/Grok 授权主链路、订阅额度和账号轮换。
- **纯删除不移植**：`b807608..9120b90` 只删除 `server_backup/**`，与当前 Go `gateway` 无对应运行时能力。

### 16.5 证据强度与未验证宣传

| 结论 | 证据等级 |
| --- | --- |
| 上游存在 Codex 四项同源缓存亲和标识、Rule journal/镜像、模型分组和 Provider 空闲超时 | 源码与测试支持 |
| 当前 `gateway` 的普通 GPT cache key、Anthropic cache frontier、Rule CRUD/去重/Prompt 注入、Codex/Grok 多账号链路和 typed 空闲超时 | 当前源码与既有回归支持 |
| `gateway` 缺 managed Codex `prompt_cache_key` 与三个亲和 header | 当前源码与专项测试直接支持 |
| 上游“几乎不占内存”“性能极大提升”“40 轮约 99% 缓存命中”等幅度结论 | **未验证**；未发现可复现 benchmark、同负载基线或完整指标 |
| 移植亲和合同后一定提高命中率或降低成本 | **待本地 A/B 验证**；不能由字段存在直接推出收益幅度 |

### 16.6 当前实施状态（2026-08-30）

- Rule P0 已完成：私有权限、symlink/非普通文件拒绝、原子且有界持久化、Rule/docs index 来源隔离、损坏恢复与 post-rename 提交语义已通过定向、race、vet 和 Windows 交叉构建验证。Windows 实机 ACL/reparse-point 行为仍属于最终发布门禁。
- managed Codex affinity 基础阶段已完成：稳定账号身份、独立私密安装密钥、HMAC 域分离、Router 重试重派生、精确 ChatGPT Responses 注入和 `control | prompt_key | full` profile 已接线；`previous_response_id` 仍删除，static/Grok 不受影响。
- fake upstream、缓存观测和同负载 A/B 尚未完成，因此当前只能宣称“亲和合同已接线”，不能宣称缓存命中率、成本或延迟收益。
- Rule journal、remote snapshot 与手动 opt-in 同步尚未实现；默认仍为纯本地零上传。

本轮只完成同步尝试、差异审阅和文档决策，不向 `gateway` 写入功能代码。后续 P0/P1 必须拆成独立工作包实施、测试和验收。收尾验证确认 `git diff --check` 通过、改动范围仅为本 PRD、`task/todo.md` 与 `docs/process.md`，且 `gateway`、`main` 引用未移动、无 `MERGE_HEAD`。
