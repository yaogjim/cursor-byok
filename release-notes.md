# Gateway-byok v0.0.76.0

本版本交付「官方/BYOK 混用兼容」C2B 保存阶段的连续切片：已预取原生正文/摘要保全（§19.3h）、回合引用图与官方摘要/归档来源保全（§19.3i/j）、本地压缩原生归档与同源前缀冻结（§19.3g/k）及 actor 命令等待竞争修复。整体混用功能保持 planned，已实施切片为 `verified-partial`（隔离协议链验证完成；默认原生 writer 未接线，缺失内容异步读取、增量同步与真实 Cursor/官方端到端验收待后续）。设计锚点为系统 Design §19.3g/h/i/j/k。

## 功能内容

### 原生正文与摘要保全（§19.3h）

- 历史载荷新增可选 `native_message`/`native_message_parts`：整段配对校验成功后按原文绑定连续消息组，保留重复/空实例及多结果分组；旧载荷缺省仍走旧路径。
- 修复三类落盘缺陷：外层 JSON 缩进/HTML 转义导致参数与对象结果失配（现按保存规则规范化 JSON）；字符串工具结果被误套本地重编码规则（原生组直接校验原文安全数值域）；负数 parts 被当默认 1 及孤立标记未按分组停止（现仅 0 使用默认 1，非法值明确失败）。
- 摘要原文不包装、模型视图只包装一次。

### 回合引用图与官方摘要/归档来源保全（§19.3i/j）

- 复用 ContentBlobStore 保全回合及具名子节点原字节；官方 summary/summary_archive/summary_archives 来源绑定 runtime_state 可选 `native_summary*` raw32 字段，按最后一条有来源的 entry 整组覆盖。
- 独立原生投影按原引用重新校验并输出完整图；顺序与重复实例保留；旧非 32 字节父节点按正确类型校验再内容寻址。
- 缺节点、哈希失配、未知字段、域外引用或内容保存失败均停止并保全原会话。

### 本地压缩原生归档与连续尾段（§19.3g/k）

- 活动历史改为固定连续尾段：摘要前缀冻结保留边界，摘要后新增工具不再使旧工具退出；同一 ModelCallID 的工具批次跨非 prompt 元数据完整保留。
- `PendingCompaction` 新增同源前缀冻结字段（深拷贝身份/内容/半开区间）与 `NativeArchiveSource`（五字段写入不带 omitempty，读取逐项检查存在性，0 与缺字段不是同一状态）。
- 修复归档来源整数必填字段接受 `null` 的缺陷：`parseNativeArchiveSource` 对每个 raw token 检查严格整数，未知字段/类型越界明确失败；永久回归覆盖逐字段 null/非整数/缺失及完整 0 值。
- 修复活动尾段复用模型投影而将工具结果裁到 16KiB 的缺陷：`compactedNativeCheckpointEntries` 复用同一连续尾算法但只取原文，native 域校验与 root 编码均改接后者；默认模型裁剪语义不变。

### actor 命令等待竞争修复

- `waitPostedStreamCommand` 在 actor 退出后优先取已返回的命令结果，无结果仍明确中断；测试清理跳过已终态流，取消中断后仍等待 actor 结束。修复摘要归档 race 中 `provider loop interrupted` 误报丢结果。

### 文档与仓库整理

- 移除仓库内 AGENTS.md/CLAUDE.md/docs/agents/project.md 母版入口（规范改由全局配置加载）；.gitignore 同步。
- PRD §17、docs/process.md、task/todo.md 同步上述切片的验收事实与状态。

## 实现要点

- `forwarder/types.go`：`PendingCompaction` 前缀冻结与归档来源字段；`modelMessageEntryPayload.NativeMessage*`；`runtimeStateEntryPayload.NativeSummary*`；`compactionSummaryEntryPayload.NativeArchiveSource`。
- `forwarder/compaction.go`：同源前缀冻结、`parseNativeArchiveSource` 严格整数校验、`compactedNativeCheckpointEntries` 未裁剪原生投影。
- `forwarder/projector.go`：原生检查点投影、连续尾段算法、分组工具身份恢复。
- `forwarder/imported_blobs.go`：原生正文/摘要/归档来源读取校验。
- `forwarder/token_usage.go`：原生消息组配对绑定。
- `forwarder/actor.go`：`waitPostedStreamCommand` 双通道等待。
- 不新增依赖；默认 `service.go` 入口与 provider replay 未改。

## 验证（verified-partial）

- 上述切片均为 RED→GREEN 永久回归；真实 Connect HTTP BidiAppend→导入/保存→投影→确认链、保存/压缩/导入定向 race、两包 `go test`、`go vet`、gofmt 及 `git diff --check` 在各切片收尾时全批通过（`NATIVE_ROOT_PRESERVATION_PRIMARY_VALIDATION_PASS`、`MIXED_SAVE_19K_PRIMARY_VALIDATION_PASS` 等，证据见 docs/process.md）。
- 本版编译 macOS arm64 宿主平台分发包（`task build`），Windows/Linux 未编译。
- 默认 writer 未切换：推理/签名/provider 元数据、图片/文件、未完成工具、数值/结果类型及外部 State 内容仍有独立 native 拒绝门禁，C2B 保存阶段仍 in_progress。

## 版本

- 版本号 `0.0.76.0`（0.0.75.0 之后的功能版本）；构建元数据（config.yml、darwin Info.plist、Windows/Linux 构建资产）同步对齐。