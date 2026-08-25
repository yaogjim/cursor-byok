# Cursor CLI 主 Agent 与 Subagent 路由配置

本文件记录在 Docker 化 Cursor CLI 中，将主 Agent 与自定义 subagent 分配到不同模型路由的通用步骤。文档不包含服务器地址、登录密码、访问令牌、`auth.json`、API Key 或 HTTP Basic 凭据。

## 目标路由

本次配置使用两个独立的模型路由：

| 用途 | 显示名称 | 模型 ID |
|---|---|---|
| 主 Agent | `gpt-HA` | `69237f4cf0197d85` |
| `subagent-pool` | `grok-HA` | `543fe17c50d81660` |

`grok-HA` 是已经在 BYOK 后端配置好的逻辑路由。它的 fallback 链由后端维护；subagent 文件只固定逻辑路由 ID，不在 Docker 容器内复制渠道密钥或 fallback 配置。

## 前置条件

容器需要满足：

- Cursor CLI 已安装，并且 `agent --version` 可以运行；
- `CURSOR_API_ENDPOINT` 指向实际的 BYOK 后端；
- `HOME` 为 `/home/bun`；
- `/home/bun` 是持久化 bind mount；
- 目标模型可以通过 `agent models` 列出。

先确认模型：

```bash
docker exec cursor-cli agent models
```

输出中应包含：

```text
543fe17c50d81660 - grok-HA
69237f4cf0197d85 - gpt-HA
```

## 配置用户级 subagent

Cursor CLI 的用户级 custom subagent 放在：

```text
~/.cursor/agents/
```

在容器中对应：

```text
/home/bun/.cursor/agents/
```

创建 `/home/bun/.cursor/agents/subagent-pool.md`：

```markdown
---
name: subagent-pool
description: Use proactively for research, code exploration, verification, and implementation tasks that must use the dedicated grok-HA fallback pool. Never use gpt-HA for this subagent.
model: 543fe17c50d81660
readonly: false
is_background: false
---

You are the dedicated subagent for this Cursor CLI environment.

Use the model selected by this file. Do not inherit the parent agent model.

The parent Agent uses the dedicated gpt-HA route. This subagent must use the dedicated grok-HA route and must not switch to gpt-HA or another model.
```

文件权限建议为目录 `755`、文件 `644`，所有者应是实际运行 CLI 的用户。

## 固定主 Agent 路由

为了避免主 Agent 依赖当前会话继承的模型，在持久化的 CLI bin 目录创建包装器：

```sh
#!/bin/sh
set -eu
exec /home/bun/.local/bin/agent --model 69237f4cf0197d85 "$@"
```

例如保存为：

```text
/home/bun/.local/bin/agent-main
```

并设置可执行权限：

```bash
chmod 755 /home/bun/.local/bin/agent-main
```

之后主 Agent 使用：

```bash
agent-main
```

非交互调用也使用：

```bash
agent-main --print --output-format json "请检查当前项目并报告状态，不要修改文件。"
```

不要把系统自带的 `agent` 可执行文件替换成包装器；保留原命令便于诊断和升级。

## 调用 subagent

启动主 Agent 后，可以显式调用 custom subagent：

```text
/subagent-pool 检查当前项目的 Docker 配置并报告风险，不要修改 Cursor 凭据或网络配置。
```

也可以自然语言调用：

```text
使用 subagent-pool 检查当前项目的测试状态，并返回验证结果。
```

`subagent-pool.md` 中的 `model` 字段必须使用 `grok-HA` 的逻辑路由 ID，不要使用 `inherit`。否则 subagent 可能继承主 Agent 的 `gpt-HA`。

## 验证

验证文件和权限：

```bash
docker exec cursor-cli sh -c '
  ls -l /home/bun/.cursor/agents/subagent-pool.md
  ls -l /home/bun/.local/bin/agent-main
  sed -n "1,12p" /home/bun/.cursor/agents/subagent-pool.md
  cat /home/bun/.local/bin/agent-main
'
```

验证主 Agent 包装器接受固定模型参数：

```bash
docker exec cursor-cli agent-main --help
```

验证两个模型仍由同一个 BYOK 后端提供：

```bash
docker exec cursor-cli agent models
```

## 运行边界

- 代理池配置属于 BYOK 后端的逻辑路由配置；Docker 容器只引用逻辑路由 ID。
- 主 Agent 和 subagent 的模型分配由各自的 CLI 参数或 custom subagent frontmatter 控制。
- 不要把 `auth.json`、API Key、渠道密钥或真实密码写入镜像、仓库或本文档。
- 不要为了修改上述文件停止、重启或重建正在运行的 CLI 容器。
- 如果逻辑路由在 BYOK 后端发生改名、删除或重算 ID，应重新执行 `agent models`，并同步修改 `model` 字段和 `agent-main` 包装器。