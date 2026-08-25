# Cursor BYOK noad

> 让 Cursor 使用你自己的模型 API 的本地 BYOK 客户端。`noad` 分支默认浅色、默认关闭广告、默认不启动检查更新，更新按“检查 / 下载 / 安装”分阶段确认，发布与更新资源固定使用 `yaogjim/cursor-byok`。当前发布版本为 [v0.0.49.6](https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.49.6)。

[下载最新版](https://github.com/yaogjim/cursor-byok/releases/latest) · [问题反馈](https://github.com/yaogjim/cursor-byok/issues)

![Cursor BYOK noad 主界面](./docs/images/main.png)

## 核心能力

- **自带 BYOK 代理能力**：把 OpenAI、Anthropic 或兼容接口接入 Cursor，让已有模型额度直接用于 Chat、Agent 和开发辅助任务。
- **本地优先、可自托管**：核心服务运行在本机，默认使用本地代理和后端入口，配置保存在本机。
- **协议适配更灵活**：支持协议端点、模型 ID、上下文窗口、推理 / thinking 参数、自定义 Header 和额外请求参数。
- **保留 Agent 能力**：保留工具调用、Skills、MCP、检查点、压缩、Shell / 工具流式输出，以及子代理终态恢复。
- **Provider 备用渠道**：默认关闭；仅在当前渠道无输出、无副作用时，按你配置的顺序切换。
- **完成更可核对**：编辑类任务必须同时有成功修改，以及晚于该修改的成功验证；口头声明不能替代实际改动和验证。
- **可观测与统计**：展示 Token 消耗、缓存命中率、对话摘要、模型测试和日志采集状态。
- **公开记录更克制**：公共 Agent transcript 只保留用户可见文本和结构化工具事件，不写入 reasoning、signature、内部回放、凭据或门禁提醒。

## 当前版本

当前稳定发布为 **v0.0.49.6**（2026-08-25）。完整说明见 [GitHub Release](https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.49.6) 与仓库 [`release-notes.md`](./release-notes.md)。

### v0.0.49.6

- Provider fallback 采用“保证渠道覆盖，再用剩余预算重试”，最多支持 1 个主渠道和 4 个有序物理候选渠道。
- 普通配置保存会保留最新运行时模型选择；物理 adapter 支持可选的并发容量保护，逻辑 fallback alias 不参与容量限制。
- “测试全部”静默跳过逻辑 alias，长下拉列表使用真实滚动容器并保留键盘访问。
- 独立 CLI 模型池保持独立 module，不进入客户端发布包；真实 CLI 故障切换和 Wails 浏览器视觉验收仍是独立证据缺口。

### v0.0.49.3

- 用结构化工具证据判断编辑是否真正完成：必须同时有成功修改，以及晚于该修改的成功验证；修改之后已有验证会失效，需要重新验证。
- 证据不足时最多自动提醒并续跑一次，重启后保持幂等；再次不足则保守收口，避免无限循环。
- 按同一模型调用聚合推理回放，并把 provider 签名与原始 reasoning 精确绑定；旧 history 继续兼容读取。
- 公共 transcript 不投影 reasoning、signature、内部 reminder 或诊断 metadata。

### v0.0.49.2 / v0.0.49.1

- 修复公开 transcript 把 reasoning / thinking 当普通文本输出，以及同一段推理在多个工具调用前重复出现的问题。
- 子代理终态会持久化，重启后幂等交回父会话；未完成任务进入待恢复，不会自动重跑，也不承诺从中断点续跑。
- 收紧 Provider 断连终态与日志隐私：请求 / 响应正文、密钥和子代理内容不会写入日志。
- 更新比较支持四段版本号（如 `0.0.49.3`），避免 `0.0.49` 客户端检测不到后续补丁。

## noad 分支默认策略

| 功能 | 默认行为 | 用户可控点 |
| --- | --- | --- |
| 广告 | 默认关闭；不拉取广告包，不展示顶部广告位和广告弹窗 | 在设置中显式开启后才允许请求和展示 |
| 更新 | 默认不启动检查；检查、下载、安装分阶段确认；版本比较支持四段号 | 可手动检查；下载和安装仍需再次确认 |
| 主题 | 默认浅色，减少启动闪黑和深色硬编码体验 | 可切换浅色 / 深色 |
| 发布身份 | 发布仓库和更新资源使用 `yaogjim/cursor-byok` | 仅使用本仓库发布资源 |

核心原则：**关闭就是关闭**。广告关闭时不请求远端广告内容；更新不会在用户未确认时自动下载或安装。

## 快速开始

1. 从 [yaogjim/cursor-byok Releases](https://github.com/yaogjim/cursor-byok/releases/latest) 下载对应平台版本。
2. 启动 Cursor BYOK noad，打开“模型配置”，填写接口地址、API Key 和模型标识。
3. 测试模型配置；测试通过后返回主界面启动本地服务。
4. 打开 Cursor，选择已配置的模型并开始使用 Chat 或 Agent。

## 系统截图

![模型配置](./docs/images/model.png)

![客户端设置](./docs/images/cursor%20model.png)

## 工作原理

```text
Cursor 客户端
    │ Agent 请求与工具结果
    ▼
Cursor BYOK noad 本地服务
    │ OpenAI / Anthropic 兼容请求
    ▼
你配置的模型 API
```

## 许可证

本项目基于 [MIT License](./LICENSE) 开源。