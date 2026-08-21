# Cursor BYOK noad

> 让 Cursor 使用你自己的模型 API 的本地 BYOK 客户端。`noad` 分支默认浅色、默认关闭广告、默认不启动检查更新，更新按“检查 / 下载 / 安装”分阶段确认，发布与更新资源固定使用 `yaogjim/cursor-byok`。

[下载最新版](https://github.com/yaogjim/cursor-byok/releases/latest) · [问题反馈](https://github.com/yaogjim/cursor-byok/issues)

![Cursor BYOK noad 主界面](./docs/images/main.png)

## 核心能力

- **自带 BYOK 代理能力**：把 OpenAI、Anthropic 或兼容接口接入 Cursor，让已有模型额度直接用于 Chat、Agent 和开发辅助任务。
- **本地优先、可自托管**：核心服务运行在本机，默认使用本地代理和后端入口，配置保存在本机。
- **协议适配更灵活**：支持协议端点、模型 ID、上下文窗口、推理 / thinking 参数、自定义 Header 和额外请求参数。
- **保留 Agent 能力**：保留工具调用、Skills、MCP、检查点、压缩、Shell / 工具流式输出等工作流能力。
- **可观测与统计**：展示 Token 消耗、缓存命中率、对话摘要、模型测试和日志采集状态。

## noad 分支默认策略

| 功能 | 默认行为 | 用户可控点 |
| --- | --- | --- |
| 广告 | 默认关闭；不拉取广告包，不展示顶部广告位和广告弹窗 | 在设置中显式开启后才允许请求和展示 |
| 更新 | 默认不启动检查；检查、下载、安装分阶段确认 | 可手动检查；下载和安装仍需再次确认 |
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