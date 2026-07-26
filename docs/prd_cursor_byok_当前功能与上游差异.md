# Cursor BYOK 当前功能与上游差异 PRD

- **文档类型**：功能现状与版本差异 PRD
- **适用项目**：Cursor BYOK 本地分支 `noad`
- **当前已提交基线**：`noad@e9b6d701d63f3cc315676afffaddc3128c7db7cc`
- **原始仓库**：<https://github.com/leookun/cursor-byok>
- **原始主线基线**：`main@799dbda7e0ca30ab5d0bfe965fd1ab3c5da5c588`
- **当前工作树**：代码已提交；治理文档和路线图正在作为独立文档提交
- **状态**：功能记录与差异基线；运行时窗口和网络零请求验收仍需按同步说明执行

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

- 原始仓库 `https://github.com/leookun/cursor-byok.git` 的 `refs/heads/main` 仍以同步时实际获取的 commit 为准；当前历史记录的原始主线基线为 `799dbda7e0ca30ab5d0bfe965fd1ab3c5da5c588`。
- 本地 `noad` 的客户端体验治理代码基线为 `e9b6d701d63f3cc315676afffaddc3128c7db7cc`。
- `origin` 是本地 Fork `https://github.com/yaogjim/cursor-byok`；原始上游应通过 `upstream` 远程获取，不使用 `origin/main` 代替上游。
- 本次治理文档和客户端体验路线图与代码基线分开提交，后续以 Git 提交记录和本 PRD 的版本记录为准。

相对 `799dbda...b7cd64f` 的已提交差异统计为 **23 个文件，1978 行新增、19 行删除**；客户端体验治理提交相对 `b7cd64f` 另有 **36 个文件，1502 行新增、285 行删除**。统计用于识别同步风险，不代表所有行都是独立产品功能。

### 2.2 已提交工作

| 提交 | 工作内容 | 当前状态 |
| --- | --- | --- |
| `9b31024` 及其祖先差异 | 调整 OpenAI 默认 Endpoint 为 Chat Completions，保留显式协议选择；补充本地忽略及 provider/model 兼容调整 | 已提交；合并时必须保留用户显式 Endpoint 选择 |
| `9728326` | 增加默认关闭的专用隐私审计、字段摘要、canary 门控、权限/过期/事件上限 | 已提交；相关 Go 测试曾通过 |
| `0d78c09` | 修复 `ForceBackgroundShell` 在无 reasoning payload 时工具结果无法历史 replay 的阶段断裂，并加入功能特征测试 | 已提交；不得泛化为所有孤立结果放行 |
| `045b60b` | 增加精确路由优先于 `/AiService/*` 通配路由的测试 | 已提交；只保护路由优先级，不改变目标 |
| `b7cd64f` | 保存验证路线图与工作决策 | 已提交；属于决策记录，不是运行时功能 |

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

原始版本以 `leookun/cursor-byok` 的 `main@799dbda` 为准。下表把“代码已经改变”“已经验证”和“仅作决策”分开记录。

| 功能域 | 原始主线行为 | 本地已提交差异 | 当前工作树或决策 | 状态与合并口径 |
| --- | --- | --- | --- | --- |
| Agent BYOK provider | 已有本地 provider 主链路及 OpenAI/Anthropic 适配 | 调整 OpenAI 默认协议；增加 retry/replay 相关保护 | 继续以用户 Endpoint 为主要推理出口 | 已提交；必须回归输入输出、重试和恢复 |
| OpenAI 协议选择 | 原始默认行为以 Responses 为主 | 默认调整为 Chat Completions，同时保留显式 Responses/Custom | 不得静默覆盖用户已有模型配置 | 已提交；配置冲突需人工审查 |
| Tab/Cpp/FileSync/Git 路由 | 相关 RPC 经 `tab.leokun.cn` relay | 增加脱敏审计接入，转发目标和 body 语义未改变 | 计划支持 `local_official/external_relay` 双模式，但 token 导入和官方直连尚未实现 | 已提交审计；路由变更待验证和确认 |
| 隐私审计 | 无本项目专用字段摘要观察器 | 默认关闭，仅输出 metadata/字段 presence/大小/事件类型/host 分类 | 仅允许 synthetic canary，禁止正文和凭据落盘 | 已提交并验证；不得恢复 raw dump |
| 原始 observability | 存在写入完整 Bidi/provider 工件的调试能力 | 本地决策维持 `log:false`，尚未完成全面 raw observability 治理 | 后续单独治理权限、保留期和显式导出 | 决策已定；治理待完成 |
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
- 用户 Cursor token 的 Keychain、刷新、身份隔离和 `local_official/external_relay` 实现。
- relay、官方直连、本地实现、no-op 和禁用之间的逐 RPC 功能影响。
- Repository/Docs/Upload 改为诚实失败或完整持久化后的 Cursor UI/状态机影响。
- 客户端体验治理的网络零请求、旧配置迁移、窗口首屏和发布资源实机验收。

未观察到请求、未命中 canary 或收到 RPC success，都不能单独推出“未发送”“未泄露”或“真实业务完成”。

## 6. 当前发布前验收缺口

将提交 `e9b6d701d63f3cc315676afffaddc3128c7db7cc` 视为可发布或可供上游合并前，仍需要：

1. `git diff --check`、`go test ./...`、`go build ./...`。
2. `cd frontend && yarn test:config-projection && yarn build`。
3. 验证旧 `config.yaml` 迁移不丢失模型、路由和监听地址。
4. 默认广告关闭时不访问 `ads.leokun.cn`；默认更新设置不请求 manifest 或资源。
5. 更新资源 URL、大小、checksum、取消和退出清理测试通过。
6. 审计默认关闭；审计文件不含正文、凭据、canary 原值或完整 header。
7. 保留 `tab.leokun.cn` 现状路由，确认没有新增自动 fallback。
8. Git diff 不包含 `.env`、Token、API Key、真实 Prompt、真实 workspace 或 synthetic 原始产物。

## 7. 后续版本记录格式

上游更新或本地功能落地后，在本 PRD 中追加一条版本记录，至少包含：

- 上游仓库与目标 commit、同步时间；
- 本地基线 commit；
- 实际保留、移植、重做、拒绝的功能；
- 受影响的外部目标、凭据、审计、配置或状态机；
- 测试与运行时证据；
- 新增的未验证边界和用户确认项。