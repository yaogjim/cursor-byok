# project.md — cursor-byok 项目适配声明

```yaml
project: cursor-byok
based-on-master: v0.7.0
onboarded: 2026-03-14
```

## 项目画像

- **定位**：为 Cursor 等开发工具提供可自托管的 BYOK 模型接入、协议适配与本地客户端能力。
- **技术栈**：Go 1.25、Wails 3、Vue 3/Vite、Taskfile；根 module 与 `cursor-tab-server`、`tools/log-analyzer` 两个独立 Go module 分开治理。

## 声明开关

- **任务真值源**：`task/todo.md`（详细实施目标、阶段、步骤、范围、验收与执行结果）
- **进展归档**：`docs/process.md`（当前进展、版本完成情况与时间线；阶段收尾时与 `task/todo.md` 同步）
- **Context7 MCP**：未接入；本项目明确禁用
- **浏览器验证工具**：无固定工具；任务需要 UI 验证时按当前环境能力选择并记录降级边界
- **仓库 README**：`noad` 与 `main` 均使用中文版，唯一入口为根目录 `README.md`；不保留英文版，不使用 `README-CN.md` 双语分流

## 文档路径映射

| 内容类型 | 目标文档 |
|---------|---------|
| 产品与工作决策基线 | `docs/prd_cursor_byok_工作决策基线.md` |
| 当前功能与上游差异 | `docs/prd_cursor_byok_当前功能与上游差异.md` |
| 系统 Design | `docs/prd_cursor_byok_系统架构与核心业务数据流.md` |
| 活动实施计划与执行状态 | `task/todo.md` |
| 总体进展、版本归档与时间线 | `docs/process.md` |
| 已批准的专项实施计划 | `.cursor/plans/*.plan.md` |
| 上游同步规则 | `docs/cursor_byok_upstream_merge_requirements.md` |
| 上游同步→发版 Runbook | `docs/cursor_byok_upstream_sync_release_runbook.md` |
| 198 远程 CLI 会话复用、本机 18090 接入与浏览器 WeTTY | `docs/ops_198_cursor_cli_session_reuse.md` |

## 项目上下文与权威事实源入口

只登记稳定入口和权威范围；每次设计与实现仍须读取当前内容，不得仅凭本表推断现状。

| 类型 | 路径 / 命令 / 入口 | 权威范围与备注 |
|------|--------------------|----------------|
| 项目定位与业务规则 | `README.md`；`docs/prd_cursor_byok_工作决策基线.md` | 产品定位、长期边界与已确认工作决策；仓库入口 README 仅中文 |
| PRD / 验收标准 | `docs/prd_cursor_byok_工作决策基线.md`；`docs/prd_cursor_byok_当前功能与上游差异.md` | 产品约束、当前功能事实、上游差异及验收口径 |
| 活动执行与进展 | `task/todo.md`；`docs/process.md` | 前者是详细实施与执行状态真值源，后者是进展、版本和时间线归档；阶段完成时必须同步更新 |
| Design / ADR | `docs/prd_cursor_byok_系统架构与核心业务数据流.md`；ADR 未登记 | 系统架构、模块职责和核心数据流；重大新决策需补稳定 Design/ADR 锚点 |
| 代码 / 配置入口 | `main.go`；`internal/`；`frontend/`；`Taskfile.yml`；`build/config.yml` | 客户端主流程、后端模块、前端、构建与运行配置 |
| schema / API / 契约 | `proto/`；`internal/backend/server/config/types.go`；`internal/observability/contract.go`；`tools/log-analyzer/internal/contract/contract.go` | RPC、配置、观测事件和离线分析器数据契约 |
| 测试与验证命令 | `go test ./internal/...`；`go vet ./internal/...`；`node frontend/scripts/test-config-projection.mjs`；`npm run build --prefix frontend`；在 `cursor-tab-server`、`tools/log-analyzer` 分别运行 `go test ./...`、`go test -race ./...`、`go vet ./...`；`task release:verify:analyzer-isolation` | 按改动范围选择相关子集；完成声明须匹配实际运行证据 |
| 运行 / 部署 / 可观测入口 | `task dev`；`task run`；`Taskfile.yml`；`build/`；`internal/observability/`；`cursor-tab-server/README.md`；`tools/log-analyzer/cmd/log-analyzer/`；`docs/ops_198_cursor_cli_session_reuse.md` | 本地开发、客户端构建发布、运行时观测、Tab relay 与离线分析入口；198 Docker CLI 会话复用、方案 A 反向隧道、WeTTY 浏览器终端为环境专项操作手册，不含真实凭据 |
| 外部依赖与参考方案 | `docs/cursor_byok_upstream_sync_release_runbook.md`；`docs/cursor_byok_upstream_merge_requirements.md`；根及独立 module 的 `go.mod`；`frontend/package.json` | 端到端同步发版、冲突边界、依赖版本与独立 module 隔离 |

## 项目覆盖规则

- **覆盖母版 AGENTS.md §4「子代理策略」**：未经用户当次明确要求，不使用 subagent。原因：本项目要求主对话保持单 owner、范围可控，避免并行探索扩大修改边界。
- **仓库 README 内容边界**：`noad` 与 `main` 的 README 只写本仓库产品事实。禁止写入上游项目信息、讨论组 / 交流群，以及其他在 `noad` README 中不存在的内容。同步 `main` 时若上游 README 带回英文版、上游主页、Trendshift、Telegram、QQ 群等内容，必须在合入前删除，不得保留为对照页。
