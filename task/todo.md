# 活动任务

> 本文件是项目活动任务的唯一真值源。`.cursor/plans/*.plan.md` 的 frontmatter todo 仅作阶段索引，不代表任务已满足 Definition of Ready。

## 当前焦点

### 🎯 Active Work Package

**multi-client-acp-phase5-20260826** (blocked)
- 状态：等待真实 ACP Client/编辑器到位
- 风险：high
- 条件：提供能连接本地 stdio bridge 的真实 ACP Client/编辑器，或明确授权并提供其版本、启动方式和临时 HOME/workspace 验收边界

### ✅ 最近完成

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
