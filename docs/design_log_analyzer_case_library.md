# 独立日志分析器：调查案例库设计（2026-11-16）

> 状态：Design Readiness=review-required（待用户确认四项决策后实施）
> 需求来源：`docs/process.md`「独立日志分析器产品能力 / 调查案例库」；`task/todo.md` 的 `investigation-case-library`；差异 PRD §16。
> 本文只覆盖案例库（`investigation-case-library`）。证据包导出（`ai-evidence-bundle`）依赖本文的数据模型，另行设计；客户端启动器与发布验证不在本文范围。

## 1. 问题与目标

分析器当前只能查询"本次打开的数据集"：`workspace.Open` 在临时目录建 SQLite，`CloseProject` 后数据集与 findings 即销毁。调查结果只能靠人工截图或导出整份报告，无法回答"这个问题上次查到什么、修了没有、这次复现是否回归"。

目标：把"问题 → 证据 → 结论 → 修复 → 复验"固化为可持久、可复现、可交接的案例；跨次打开数据集时能对照同一案例的历史证据与复验结论。案例默认完全本地，不外发、不调用 AI。

## 2. 已核实事实（代码锚点）

- 持久化参照：`savedquery.Open`（`tools/log-analyzer/internal/savedquery/store.go`）使用 JSON 文档 + `schema_version`，路径为 `os.UserConfigDir()/Cursor Log Analyzer/saved-queries.json`（`internal/gui/service.go:104-108`）。
- 数据集是临时的：`workspace.Open` 创建 `os.MkdirTemp`，关闭即删除；`FindingRecord` 含 `DatasetID/TraceKey/Code/Count`，`ComparisonRecord` 提供 current/baseline 差异（`workspace.go:213-235`）。
- 脱敏能力：`internal/sanitize` 维护字段白名单与凭据/URL/长路径规则；白名单字段（method/status_code/trace 相关计数字段等）可直接落盘，其余文本需经摘要与截断。
- GUI 接口面：`internal/gui/service.go` 已有 OpenProject/CloseProject/ListFindings/ExportReport/ListSavedQueries 等；前端为单页 `App.vue` + tabs，绑定经 `analyzerApi.js`。
- 分析器是独立 Go module（`tools/log-analyzer/go.mod`），不依赖主应用包。

## 3. 数据合同

存储布局（每案例一个目录，便于原子替换与独立损坏隔离）：

```
<UserConfigDir>/Cursor Log Analyzer/
  cases/
    <case-id>/
      case.json        # 元数据 + 证据 + findings 快照 + 复验记录
      evidence.jsonl   # 证据条目，追加式；可选，>200 条时拆分
```

`case.json`（`schema_version=1`）：

| 字段 | 类型 | 说明 |
|---|---|---|
| id | string(uuid) | 案例身份键，创建后不变 |
| schema_version | int | 1 |
| title | string | 用户标题，≤200 rune |
| status | enum | 见状态机 |
| severity | enum(low/medium/high/critical) | 用户标注，不自动推断 |
| summary | string | 结论摘要，≤2000 rune，经脱敏 |
| created_at / updated_at / closed_at | RFC3339 | 时间戳 |
| client_version / analyzer_version | string | 采集时版本 |
| dataset | object | 数据集摘要：文件数、事件数、schema 集合、时间范围、输入目录 basename 哈希（不存完整路径） |
| evidence[] | object | 每条：id、kind(event/window/report)、trace_key、captured_at、fields(白名单,标量)、redacted_text(≤8KB)、content_hash |
| findings[] | object | code、severity、message(脱敏 ≤512 rune)、trace_key、count |
| verifications[] | object | 见 5 |
| notes | string | 调查记录，≤8000 rune |

身份键 vs 比较键：`id` 是身份键；复验比较使用 `(code, trace_key)` 与 `content_hash`，不使用临时 `DatasetID`——数据集关闭后该 ID 无意义。

容量：单案例 `case.json` ≤ 2 MB；证据单条文本 ≤ 8 KB；单案例证据 ≤ 500 条；案例目录总量 ≤ 50 MB 时拒绝新增并提示清理。所有写入先脱敏再落盘。

## 4. 状态机

```
draft ──open──▶ investigating ──conclude──▶ concluded
                    │                           │
                    │                           ├─fix──▶ fixed ──verify→ verified
                    │                           │            └─verify(fail)─▶ regressed
                    └─archive──▶ archived ◀──────┴──────────────┘
```

规则：
- 合法转换：`draft→investigating`、`investigating→concluded`、`investigating→archived`、`concluded→fixed`、`concluded→archived`、`fixed→verified`、`fixed→regressed`、`verified/regressed→investigating`（重新调查）。
- `fixed→verified` 必须存在一次 `outcome=passed` 的复验；`fixed→regressed` 必须存在 `outcome=regressed` 的复验。无复验不可直接标 verified。
- 每次转换写 `updated_at`，进入 `verified`/`regressed` 时保留 `closed_at`。
- `archived` 为终态，只读；恢复需显式 `archived→investigating` 并记 note。
- 非法转换返回明确错误，不写盘。

## 5. 复验

`verification` 记录：`ran_at`、`dataset`（同 3 的摘要）、`analyzer_version`、`outcome(passed/regressed/inconclusive)`、`findings_delta`（新增/消失/计数变化）。

判定（第一版，确定性规则，不引入 AI）：
- 案例关联的 `(code, trace_key)` 在新数据集中全部消失 → `passed`。
- 任一关联 finding 仍在且 count ≥ 原 count → `regressed`。
- 数据集不含相关 trace 或无法判定 → `inconclusive`（不计入修复）。

复验不自动改变状态，由用户确认后执行 `fixed→verified/regressed`；系统只提供建议结果与依据。

## 6. 失败、恢复与迁移

- 写入：单案例目录内临时文件 + `fsync` + `rename` 原子替换；失败保留旧文件并返回错误。
- 损坏隔离：读取失败或 `schema_version` 不支持时，该案例标记为 `corrupt` 并从列表排除错误详情，不影响其他案例；提供"导出原始文件"入口供人工排查。
- 并发：GUI 单实例，`Service` 内 mutex 串行化同一案例写入；跨进程不做文件锁。
- 迁移：`schema_version` 不匹配时拒绝加载并提示，不自动改写；后续版本迁移单独切片。
- 回滚：删除 `cases/` 目录不影响数据集查询、报告与已保存查询。

## 7. UI（分析器 GUI）

新增 `cases` tab：
- 列表：标题、状态、严重度、更新时间、关联版本；空态提示。
- 详情：摘要、证据列表（可跳转到当前数据集的对应事件）、findings、状态操作按钮（按状态机启用/禁用）、复验入口。
- 创建：从当前项目一键创建（快照当前 findings；可选中事件加入证据）。
- 复验：在本机打开新数据集后，从案例页选择"用当前数据集复验"。

不新增主应用 UI；不改 Cursor 客户端行为。

## 8. 安全与隐私

- 证据只保存白名单标量字段 + 脱敏文本摘要；不保存完整 payload、请求/响应正文、凭据、完整路径、用户名。
- 输入目录只保存 basename 的哈希；trace_key 保留（用于关联），但需确认其不含敏感信息（当前为事件内 trace id）。
- 案例文件权限 `0600`，目录 `0700`。
- 导出（后续证据包切片）必须再次过滤白名单，不能直接序列化 `case.json`。

## 9. 验证合同

- 单元：状态机全部合法/非法转换；schema 校验与损坏隔离；脱敏白名单（含凭据/URL/长路径用例）；容量上限；复验判定三种结果；原子写在写入失败时保留旧文件。
- 集成：打开项目 → 创建案例 → 关闭 → 重新初始化 → 读取案例 → 用第二份数据集复验 → 状态提升。
- 回归：`savedquery`、`report`、`workspace` 现有测试不受影响；分析器 `go test ./...`、`go vet`、`gofmt`、`git diff --check`。
- 不涉及真机与主应用；GUI 前端只做构建与手动本地核验（无真机验收要求）。

## 10. 追踪与后续

- 需求"持久调查案例、状态机、版本关联、修复后复验" → 本文 §3–§5 → 实施任务 `investigation-case-library`。
- `ai-evidence-bundle` 复用 §3 的 evidence/findings 结构与 §8 的过滤规则，不另建数据模型。
- `client-analyzer-launcher` 与 `distribution-verification` 不受本文影响。

## 11. 待确认决策（阻塞实施）

1. **状态集**：是否接受上述六态（draft/investigating/concluded/fixed/verified/regressed + archived）；是否需要 `wontfix`。
2. **证据粒度**：证据条目是否只允许"事件 + 白名单字段 + 脱敏摘要"，还是需要保存窗口级聚合摘要（多事件统计）。
3. **复验判定**：是否接受"关联 finding 全部消失 = passed"这一确定性规则作为第一版（不引入模型判断）。
4. **实施方式**：本切片是否包含 GUI（cases tab），还是先只做后端 API + 测试、UI 另开切片。

推荐：① 接受六态、不需要 wontfix；② 事件级 + 可选窗口摘要（均为白名单）；③ 接受确定性规则；④ 一次做完后端 + 最小 UI，避免 API 空转。
