# Cursor CLI 模型池控制器

这是一个与 cursor-byok 客户端发布包隔离的独立命令。它按配置顺序启动 Cursor Agent CLI 的物理模型；只有当前进程仍处于零模型输出、零工具调用、零文件或外部副作用的安全窗口时，才会切换到下一个模型。

## 构建

```sh
cd tools/cursor-cli-model-pool
go build -o ./bin/cursor-cli-model-pool ./cmd/cursor-cli-model-pool
```

该 module 不加入根目录 `go.work`，也不会进入 Wails 客户端归档。可用 `task test:cursor-cli-model-pool` 运行本 module 的 test、race 和 vet 门禁；如果本机没有 Task CLI，可在本目录分别运行对应的 Go 命令。

## 配置

控制器只读取以下两个文件，不会改写它们：

- 模型池：`~/.cursor-local-assistant-v2/cli-model-pool.yaml`
- cursor-byok 现有配置：`~/.cursor-local-assistant-v2/config.yaml`

示例：

```yaml
schemaVersion: 1
agentPath: /absolute/path/to/agent
endpoint: http://127.0.0.1:18090
models:
  - 0123456789abcdef
  - fedcba9876543210
mode: ask
worktreeNamePrefix: cursor-pool
safety:
  allowWrite: false
```

字段合同：

- `schemaVersion`：当前固定为 `1`。
- `agentPath`：Cursor Agent CLI 的绝对路径。
- `endpoint`：当前只允许 `http://127.0.0.1:18090`。
- `models`：有序、不可重复的 16 位十六进制物理模型 ID。
- `mode`：只允许 `ask`、`plan` 或 `write`。
- `worktreeNamePrefix`：可选；默认 `cursor-pool`，只允许白名单字符且最长 32 字符。
- `safety.allowWrite`：`mode: write` 时必须显式设为 `true`；其他模式建议保持 `false`。

`validate` 会把每个池成员与 `agent --endpoint ... models` 以及现有 cursor-byok adapter 配置进行精确交叉核验。启用 `providerFallback` 的逻辑路由 adapter 会被拒绝；模型池只接受物理模型，避免外层进程切换与 Provider 内层渠道 fallback 形成乘法重试。API Key 仅在内存中参与既有渠道 ID 算法，不会写入模型池配置、journal 或错误输出。

## 命令

```sh
# 校验配置、Agent 模型列表和 physical-only 约束
cursor-cli-model-pool validate

# 显示将执行的 argv；执行完整 preflight，但不启动任务进程
cursor-cli-model-pool dry-run

# prompt 只从 stdin 输入，不进入 argv
printf '%s' '只回答 OK，不调用工具。' | cursor-cli-model-pool run
```

控制器固定使用 `--print --output-format stream-json --endpoint ... --model ...`。`ask`/`plan` 会增加对应 `--mode`；`write` 会增加随机隔离的 `--worktree` 名称。控制器不会传 `--force`、`--yolo`，也不会自动批准 MCP 工具。

`run` 会把 Agent 的 NDJSON 流传到当前 stdout，方便调用方实时消费，但不会把正文保存到 journal。未知或无法结构化分类的错误会保守停止，不会根据错误文本猜测并换模型。

## 安全停止与 write 隔离

可自动切换的白名单仅包括启动失败，以及确认发生在 `pre_output` 阶段的 transport、HTTP 429、502、503、504。认证错误、其他 4xx/5xx、取消、NDJSON 解析失败和未知错误都会停止。

任意 `thinking`、`assistant`、`tool_call`、未知事件，或 write worktree 中的 mutation 都会关闭切换窗口。失败后结果进入人工复核语义，控制器不会重放完整 Agent 任务。

write 模式由 Cursor 自己创建 worktree；控制器只监视 Cursor 的真实路径：

```text
~/.cursor/worktrees/<repository-basename>/<worktree-name>
```

控制器不会执行 `git worktree add`。如果该路径已存在、无法监视，或 mutation 状态无法确定，会保守停止。

## Journal、隐私与退出码

journal 路径：

```text
~/.cursor-local-assistant-v2/cli-model-pool-journal.jsonl
```

文件权限为 `0600`。每条记录只包含编排 ID、模型 ID/序号、phase、可用的 session/request ID、退出码、结构化错误类别、输出/变更标志和 worktree 名称。prompt、NDJSON 正文、工具参数、API Key、Cursor token 和 header 不会写入 journal。

退出码：

- `0`：`validate`/`dry-run` 成功，或 `run` 的某个物理模型成功。
- `1`：配置、预检或编排失败；也包括已有输出/工具/mutation 后需要人工复核的安全停止。
- `2`：命令缺失或命令名未知。

收到 `SIGINT`/`SIGTERM` 时，控制器会取消任务并终止所启动的整个进程组。当前进程组实现只支持 Unix；不支持的平台会在 preflight 阶段失败。