# 活动任务

> 本文件是项目活动任务的唯一真值源。`.cursor/plans/*.plan.md` 的 frontmatter todo 仅作阶段索引，不代表任务已满足 Definition of Ready。

## 当前焦点

### 🎯 Active Work Package

当前无进行中的工作包；下一项工作需由 Pending Work 队列重新确认后启动。

### ⏸️ 暂停工作包

**multi-client-acp-phase5-20260826** (blocked)
- 状态：等待真实 ACP Client/编辑器到位
- 风险：high
- 条件：提供能连接本地 stdio bridge 的真实 ACP Client/编辑器，或明确授权并提供其版本、启动方式和临时 HOME/workspace 验收边界

### ✅ 最近完成

**macos-build-0.0.52.4-20260829** (completed, verified)
- 用户要求汇总更新情况作为 git 提交说明（版本号 `0.0.52.4`）、提交并 push，然后编译 Apple Silicon macOS 版本。
- 已将 `build/config.yml`、darwin Info.plist/Info.dev.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.4.md` 对齐到 `0.0.52.4`；保留构建前已有的工作树改动。
- 本版本新增 Gateway 可用性测试（启动后自动本机 HTTP 探测 `/v1/models`，卡片展示入口状态、模型数量与延迟）、token 复制/轮换改用 Wails 原生剪贴板、极简使用引导；managed Codex 请求体白名单 fail-closed 剥离 `previous_response_id`，static OpenAI 保留原请求体。
- 使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成 `task build`，产物归档为 `bin/release/0.0.52.4/cursor-byok-0.0.52.4-macos-arm64.dmg`（24,015,566 bytes）。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.4`；最低 macOS `10.15.0`；Mach-O arm64；codesign 有效（`codesign --verify --deep --strict` OK）；`hdiutil verify` VALID；SHA-256 `ba3321f551a74127dd760a9a40703e22492a5c6a93b5887eaf64ac41185c6a62`；`SHA256SUMS` 校验通过。
- 已提交并 push（提交 `9c4db4f`）；未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release。

**subscription-account-list-modal-fix-20260829** (completed, verified-partial)
- 修复 Codex/Grok 多账号列表在固定高度卡片中被裁切的问题：账号列表成为独立纵向滚动区，卡片标题和底部操作栏保持可见。
- 修复 sub2api 确认导入后选择弹窗未关闭的问题：成功路径不再调用受 `busy` 状态保护的手动关闭函数，而是直接重置并关闭弹窗状态。
- 验证：前端配置投影门禁覆盖滚动容器和成功关闭路径，生产构建与 `git diff --check` 通过；未做真实 Wails 窗口点击，未重新打包 DMG。

**macos-build-0.0.52.3-20260829** (completed, verified)
- 用户要求编译 Apple Silicon macOS 版本，版本号 `0.0.52.3`。
- 已将 `build/config.yml`、darwin Info.plist/Info.dev.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.3.md` 对齐到 `0.0.52.3`；保留构建前已有的工作树改动。
- 使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成 `task build`，产物归档为 `bin/release/0.0.52.3/cursor-byok-0.0.52.3-macos-arm64.dmg`（24,019,575 bytes）。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.3`；最低 macOS `10.15.0`；Mach-O arm64；codesign 有效；`hdiutil verify` VALID；SHA-256 `1b63be249e0bafb6955d2deedf1e29d81d17309123b4597d489ddbcd9c11f635`；`SHA256SUMS` 校验通过。
- 本版本包含 Codex 多账户池、sub2api JSON 多选导入和逐账户用量刷新桥接修复。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未创建提交。

**codex-previous-response-stateless-boundary-20260829** (completed, verified)
- Managed Codex ChatGPT Responses 在请求体白名单边界一律剥离 `previous_response_id`，无法验证引用所属账号时 fail-closed；static OpenAI Responses 保持原请求体不变。
- 当前正常 Codex 请求发送完整 `input`，生产路径目前不生成 `RequestBodyOverride` / `previous_response_id`；回归覆盖 managed Codex 删除与 static Responses 保留，未创建提交。

**subscription-sub2api-import-20260829** (completed, verified-partial)
- 修复 Codex 单账户“刷新用量”调用了未绑定 Wails 方法的问题：桥接层补齐 `RefreshSubscriptionAccountUsage` 转发。
- Codex 与 Grok 接入页增加 sub2api JSON 导入入口；后端先按当前页面类型过滤 OAuth 账号，前端弹窗展示候选账号并允许多选后导入。Codex 仅接受 `openai/codex + oauth`，Grok 仅接受 `grok/xai/x.ai + oauth`，其他平台、认证类型或缺少 access/refresh token 的项目跳过。
- 导入只读取源文件并写入本应用私有凭据池；重复账号更新原账号，Codex/Grok 都不因后续导入抢占当前激活账号。
- 验证：订阅认证 Go 测试（包含用户提供 sub2api 文件的只读解析、provider 过滤、选择导入和源文件不变）、Wails bindings 生成、前端配置投影和生产构建通过；桥接包完整测试因大型生成文件编译超过两分钟后终止，未使用真实外部订阅请求，未做 Wails 窗口点击。

**codex-multi-account-rotation-20260829** (completed, verified-partial)
- Codex 已升级为兼容旧单账户文件的多账户池；模型配置仍只保存 `credentialSource=codex`，第一个账户自动激活，后续账户作为备用，重复导入更新原账户。
- 已实现逐账户激活、删除、双窗口用量、401 定向刷新和零输出明确配额错误下的幂等轮换；并发重复失败不会连续跳过备用账户。
- 验证通过：`go test ./internal/subscriptionauth -count=1`、`go test ./internal/backend/agent/model -count=1`、`go test ./internal/backend/server/config -count=1`、前端配置投影测试和生产构建。
- 已重新生成并核验包含本功能的 Apple Silicon macOS `0.0.52.2` DMG：23,070,403 bytes，SHA-256 `467bb52b1a04ef421f7df6ae1cf09e2e15cf5cf8346e594a6f38336c956d7f7d`；未使用真实 Codex token，未做 Wails 窗口点击，未创建提交。

**macos-build-0.0.52.2-20260829** (completed, superseded)
- 用户要求编译 Apple Silicon macOS `0.0.52.2`。
- 已将 `build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.2.md` 对齐到 `0.0.52.2`，发布说明补充订阅模型测试端点修复；保留构建前已有的工作树改动。
- 已重新生成包含当前 Codex 多账户功能的安装包，旧产物校验已被新产物取代：`bin/release/0.0.52.2/cursor-byok-0.0.52.2-macos-arm64.dmg`（23,070,403 bytes），SHA-256 `467bb52b1a04ef421f7df6ae1cf09e2e15cf5cf8346e594a6f38336c956d7f7d`。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.2`；最低 macOS `10.15.0`；Mach-O arm64；codesign 校验有效；`hdiutil verify` VALID；`SHA256SUMS` 校验通过。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未创建提交。

**subscription-model-endpoint-normalization-20260829** (completed, verified-partial)
- 根因：模型测试只把订阅 token 注入请求，却继续使用模型配置中的接口地址；当 Codex 配置填写本应用 Gateway 入站地址 `127.0.0.1:18091/v1` 时，请求带 ChatGPT token 回到本地 Gateway，而不是发往 Codex Responses 上游。
- 修复：OpenAI 类型的 Codex 订阅固定归一化为 `https://chatgpt.com/backend-api/codex/responses` + `/v1/responses`；Grok 订阅固定归一化为 `https://api.x.ai/v1` + `/v1/chat/completions`。前后端保存、重载和测试使用同一规则，订阅模式下接口地址和端点不再允许编辑；静态 API key 配置保持自定义地址行为。
- 验证：后端 Codex/Grok 端点归一化测试、Codex Responses 请求头/请求体测试、客户端订阅模型测试链路、前端配置投影和生产构建通过；未使用用户真实 token 请求外部上游，未重新打包 macOS 安装包。

**macos-build-0.0.52.1-20260829** (completed, verified)
- 用户要求编译 Apple Silicon macOS `0.0.52.1`，并直接在 `bin/release` 下创建版本目录和带版本号的产物。
- 已将 `build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.1.md` 对齐到 `0.0.52.1`；保留构建前已有的工作树改动。
- 使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成构建，产物为 `bin/release/0.0.52.1/cursor-byok-0.0.52.1-macos-arm64.dmg`（24,001,624 bytes）。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.1`；最低 macOS `10.15.0`；Mach-O arm64；adhoc codesign；`hdiutil verify` VALID；SHA-256 `6de50cd36b295e7a31d6c58af4d7bfd93dfd3d18e80981874d286aae7ccb8271`。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未创建提交。

**subscription-auth-access-layout-20260829** (completed, verified-partial)
- 按用户确认截图移除接入页底部混合的 Codex/Grok 全局授权区，把 Codex `auth.json` 导入、设备码授权、用量与清除操作全部放入 Codex 接入详情，把 Grok 设备码、账号切换、删除与用量全部放入 Grok 接入详情。
- 接入中心顺序调整为共享入口、Cursor、Codex、Grok、Anthropic；Anthropic 保留计划中占位，旧 `client=claude` 查询兼容映射到 Anthropic。
- 更新模型页提示、Grok/Anthropic 品牌样式、四套语言目录和前端结构门禁；未修改认证协议、凭据存储或请求链路。
- 验证：前端生产构建、配置投影测试、接入路由测试 5/5 和全局补丁格式检查通过；已重新打包并核验包含本次布局修复的 macOS DMG，未做真实 Wails 窗口点击。

**macos-build-0.0.52.0-20260829** (completed, verified)
- 用户要求编译 Apple Silicon macOS 版本，版本号 `0.0.52.0`。
- 已将 `build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.0.md` 对齐到 `0.0.52.0`，并保留构建前已有的跨平台元数据改动。
- 本机使用项目现有 Go 1.25.0 工具链和低并发参数完成 `task build`；布局修复后重新打包的 `bin/macos-arm64.dmg` 为 24,002,569 bytes。
- 校验：Info.plist 版本为 `0.0.52.0`；最低 macOS `12.0.0`；Mach-O arm64；adhoc codesign；`hdiutil verify` VALID；SHA-256 `8cfa4d7c6797d8bae83603e24012b24513288d23c536d016388e493b0981511a`。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未更新 README 当前稳定发布。

**gateway-subscription-auth-20260828** (completed, verified-partial)
- 完成 Codex 设备码与 `auth.json` 双入口、Grok 设备码与账号管理、私有凭据存储、managed credential 请求链路、usage/模型发现/轮换、Wails/Vue 独立订阅区和 token 脱敏。
- 修复 Codex usage 与会话窗口未持久化、Grok 耗尽账号仍可解析、设备授权终态清理、轮询失败等待态和 `pollToken` 脱敏缺口。
- Go 定向回归、除 OpenCode 外的完整 internal 回归、vet、前端构建/配置投影/路由测试及 `git diff --check` 通过；race 已通过 `subscriptionauth` 和 `model`，`forwarder` 因 Go 1.26.3 对大型生成文件持续编译超过 12 分钟而终止，未得到结果。
- 完整 internal 回归唯一失败是本机 OpenCode CLI 数据库缺少 `name` 列，属于外部安装环境问题；未修改产品代码绕过。
- 已确认 `/Applications/Gateway-byok.app` 正在运行并监听 `127.0.0.1:18080`、`127.0.0.1:18090`，因此未重复启动 `task dev` 或替换现有实例。

**macos-build-0.0.50.3-20260828** (completed, verified-partial)
- 用户要求编译 macOS 版本，版本号 `0.0.50.3`
- 已将 `build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.50.3.md` 对齐到 `0.0.50.3`
- 本机 Apple Silicon 执行 `PATH=/Users/yaogj/go/bin:$PATH task build`，产出 `bin/macos-arm64.dmg`（约 22 MiB）
- 校验：Info.plist `CFBundleShortVersionString`/`CFBundleVersion` = `0.0.50.3`；二进制注入版本 `0.0.50.3`；Mach-O arm64；adhoc codesign；`hdiutil verify` VALID；SHA-256 `ecf330bd61fbcac3133028dfb518df56722d57f6d075c573f22608889ba89ef9`
- 未做 Developer ID 签名或 notarization；未发布 GitHub Release；未更新 README 当前稳定发布（仍为已发布的 v0.0.50.1）

**ui-brand-icons-light-default-20260828** (completed, verified-partial)
- 用户确认三张附件区域的 UI 设计，并批准应用到真实系统；范围仅限 Codex 接入详情、Claude Code 接入详情和模型管理主体
- Codex 与 Claude Code 接入详情已应用授权管理布局；因真实授权 API 尚未接通，使用 0 账号空状态并禁用授权、同步、测试与保存操作，不写入演示账号
- 模型管理已应用提供商筛选、彩色提供商标识、清除全部确认和精简操作菜单；沿用真实模型数据与既有导入、导出、测试、编辑、保存能力
- 接入中心的 Codex 与 Claude Code 使用对应公司品牌图标；模型配置导入、导出按钮不显示图标
- 浅色保持为新配置默认主题，深色与跟随系统仍可选；未修改主导航、总览、Gateway、Cursor、设置及其他区域
- 2026-08-28 续验证：`npm run build --prefix frontend` 通过；`npm run test:config-projection --prefix frontend` 通过；`node --test frontend/src/router/access.test.js` 通过；相关文件 `git diff --check` 通过；`frontend/src` 无截图演示邮箱。配置投影测试已改为允许禁用的「添加授权」空态，并覆盖完整提供商 Tab。本机无 Wails 窗口视觉点击。

**model-save-identity-remap-20260828** (completed, verified-partial)
- 修复模型页保存身份变更与类型切换问题
- 渠道 ID 精确 remap、删除/新增不自动配对、类型切换保留 modelID
- 定向测试通过；未做 Wails 窗口点击

**interrupt-recovery-error-propagation-20260827** (completed, verified-partial)
- HTTP 524 零输出 allowlist、automatic-continuation、typed error propagation、stream diagnostics
- 实现与定向验证完成；真实 Cursor 多段 continuation/TCP-TLS 故障注入仍是证据缺口
- Design Gate: DESIGN-INTERRUPT-RECOVERY-001 approved

### 📋 Pending Work (当前执行队列)

- [pending] `investigation-case-library`：持久调查案例库、脱敏证据快照、状态机、版本关联和修复后复验
- [pending] `ai-evidence-bundle`：外部 AI 调查包导出、结构化分析结果导入和不可信日志数据边界
- [pending] `client-analyzer-launcher`：客户端日志采集区接入跨平台分析器检测、启动按钮和未安装引导
- [pending] `distribution-verification`：分析器独立发布、客户端归档隔离及跨版本/跨模块/跨平台验证证据

---

## Active Work Package

WORK_PACKAGE_ID: gateway-subscription-auth-20260828
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（以 `.cursor/plans/gateway_subscription_auth_6eff22f5.plan.md` 为实施锚点；参考协议以 `/Users/yaogj/Downloads/works/gitprojects/cursor-byok` 的只读核对结果为准）
DELIVERY_STATUS: verified-partial

### CONTEXT

- 本地 `gateway` 已快进到 `89524e3`，包含 subscriptionauth 领域、managed credential 基础接线和订阅授权 UI 的 WIP。
- 已核实参考项目没有 refresh-token 刷新实现或独立 OAuth fixture；Codex refresh、401 单次安全重试必须先由 fake HTTP fixture 固定协议，再进入运行时。
- 当前工作树原有的跨平台构建元数据改动与本工作包无关，继续保留且不修改。
- 已运行的 `/Applications/Gateway-byok.app` 监听 `18080/18090`；实施和测试期间不替换、不重启，最终通过后再启动仓库 `task dev`。

### ACTIVE SLICES

- [completed] `auth-baseline`：冻结 Codex/Grok 协议 fixture、核对 WIP 行为和错误分类。
- [completed] `auth-domain`：补齐私有存储、刷新、账号状态和 resolver 合同。
- [completed] `credential-integration`：完成 ChatGPT headers/body、401 安全刷新、Grok quota 轮换和 fake upstream 测试。
- [completed] `subscription-auth-ui`：完成独立订阅区、多语言、Wails 脱敏和配置投影测试。
- [completed] `usage-routing-verification`：完成 usage、模型发现和同 provider 轮换验证。
- [completed] `auth-security-release`：完成 token 清洗、Go/前端/vet 回归、review 修复和运行状态确认；race 的 `forwarder` 包保留环境性验证缺口。

### DEFINITION OF DONE

- Codex 设备码与 `auth.json` 导入/恢复、Grok 设备码与账号管理闭环可用；源凭据文件不被修改。
- Cursor backend 与独立 Gateway 共用 resolver；static/OpenAI/Anthropic 现有路径无回归。
- 401 最多刷新重试一次；quota 仅在零输出窗口内同 provider 轮换；两者共享既有 attempt budget。
- token/JWT/auth.json 原文不进入配置、日志、观测、artifact 或 Wails/Vue 状态。
- 计划要求的 Go、race、vet、前端构建、配置投影和必要桌面验证均有本次运行证据。

## Paused Work Package

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

## Completed Work Packages (Historical)
