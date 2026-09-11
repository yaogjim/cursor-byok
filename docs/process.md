# 项目实施进展

> 本文档记录项目当前状态、待完成事项、版本完成情况和时间线，是面向维护者的进展摘要。
> 详细实施阶段、逐步要求、路径范围和验收标准以 `[task/todo.md](../task/todo.md)` 为准。
> 每个阶段通过验收后，必须在同一次收尾中更新 `task/todo.md` 的执行结果和本文档的进展归档；没有验证证据的事项不得标为完成。
> 当前状态依据：仓库、Git 历史、项目计划及 2026-08-22 / 2026-08-24 / 2026-08-25 / 2026-08-26 会话运行记录。

## 一、待完成的内容

### 0.0.72.2 模型切换 imported replay Blob 兼容修复（2026-09-11；verified-partial）

**触发与日志证据**：用户提供 `/Users/yaogj/Downloads/logs 2`，现象为同一 Agent 会话先使用模型 A 完成讨论和任务，再切换高级模型分析时出现 `[internal] decode imported replay messages: invalid character '>'/'h' looking for beginning of value`。导出中找到请求 `d97857ea-9374-4ad2-8f81-74c2133ac34e`：`run_request` 的原始 Bidi 数据长 7,773,530 字节，解码成功后约 0.5ms 即进入 `dispatch_error(kind=run)` 并返回 500，随后 heartbeat 仍为 200；该请求没有进入 provider 调用。用户列出的另外三个请求 ID 不在本次导出中，payload 因配额降级未保留正文，所以无法从日志直接恢复报错字节。

**根因**：Cursor 当前本机 Agent runtime 的 `fromConversationStateStructure` 对 `rootPromptMessagesJson` 中每个元素调用 `getBlob`，再把 Blob 内容反序列化为 `coreMessage`，说明该字段在当前客户端中承载的是内容寻址 Blob ID。网关 `importedConversationStateModelMessagesWithBlobs` 原先无条件把这些元素交给 `DecodeReplayMessages` 做 JSON 解码，导致 32 字节 SHA-256 ID 的随机首字节被报告为 `>`、`h` 或其他非法 JSON 字符。模型切换/会话 fork 首次导入 `conversation_state` 时触发该路径。日志中的独立 `server_5xx status=502` 是 provider/上游错误，不是本地 replay 解码失败的原因。

**最小修复**：`internal/backend/forwarder/token_usage.go` 仅在 `root_prompt_messages_json` 的全部非空元素均为 32 字节 Blob 引用且 `turns` 非空时，跳过旧 JSON replay 分支并落入现有 Blob-aware turns 导入路径。普通非 JSON、JSON/Blob 混合内容，以及没有 turns 可回退的 Blob 引用仍保持错误，不静默丢失历史；未新增依赖、配置、协议抽象或迁移逻辑。

**回归与验证**：新增 `TestImportedConversationStateFallsBackFromBlobRootPromptsToTurns`，修复前稳定失败并复现同型错误：`decode imported replay messages: invalid character '\u008b' looking for beginning of value`；修复后恢复父会话 user/assistant 两条消息。另增加普通非法 replay 和无 turns Blob 引用继续拒绝的边界用例。最终 `go test ./internal/backend/forwarder -count=1` 通过（18.388s），`go vet ./internal/backend/forwarder` 与 `git diff --check` 退出 0。

**边界与交付状态**：交付状态 `verified-partial`。本轮未安装、部署或重启 Gateway/Cursor，未执行真实桌面“模型 A→高级模型”切换验收，未修改用户日志/配置；代码与文档已提交并推送（版本 0.0.72.2）。范围外的 provider 502 只记录，不处理。版本元数据（config.yml、darwin Info.plist、Windows/Linux 构建资产）对齐 `0.0.72.2`，发布说明见 `releaselog/0.0.72.2.md`。

### 0.0.72.1 OpenAI Responses 流式 [DONE] 兜底收口修复（2026-09-10；verified-partial）

**触发与结论**：修复 Responses 流仅收到 `[DONE]`（缺 `response.completed`）时的两条缺陷路径：普通成功收尾不补发 `TurnFinished` 导致客户端等待终态直到超时；`completeTool` 因参数非法 JSON 静默跳过后，`[DONE]` 兜底路径不再复查工具累加器，截断的 `function_call` 参数流被误判为普通成功收尾。

**修复内容**：`internal/backend/agent/model/openai.go`/`streamResponses`：`completeTool` 参数不完整时返回 `newStreamTruncatedError("openai", nil)` fail-closed，不再静默跳过；`[DONE]` 兜底路径先复查全部 `tools` 累加器，任一参数未收口即按流截断 fail；全部收口且尚未发 `TurnFinished` 时兜底补发（无工具普通成功默认 `stop`，已发工具完成保留空值交由 `effectiveFinishReason` 归一为 `tool_calls`）。正常 `response.completed` 终态路径与 Chat 路径行为不变。

**回归测试**：`TestOpenAIResponsesDoneWithoutCompletedEmitsTurnFinished`（仅 `output_text.delta` + `[DONE]` 仍发出 1 次 `TurnFinished` 且 finish reason 为 `stop`）、`TestOpenAIResponsesDoneWithIncompleteFunctionCallTruncates`（`function_call` 参数半截后 `[DONE]` 判定流截断 `missing completion marker`，不发 `ToolLikeCompleted`/`TurnFinished`）。

**验证证据**：`go test ./internal/backend/agent/model/ -run 'TestOpenAIResponses' -count=1` 通过。证据边界：Host 本地模拟上游证明两条兜底路径行为；真实上游截断/正常混合实机验收待后续窗口。

**边界与交付状态**：交付状态 `verified-partial`。未改变 Chat 路径、Anthropic 适配器、重试/fallback、证书与账号业务逻辑。版本元数据（config.yml、darwin Info.plist、Windows/Linux 构建资产）对齐 `0.0.72.1`，发布说明见 `releaselog/0.0.72.1.md`。

### 0.0.71.2 日志增强复核与范围内部署缺陷修复（2026-09-10；verified-partial）

**复核触发与结论**：用户要求复核完成情况并修复问题。本次对已实施的日志增强做独立只读复核，确认“已全部完成”的表述证据不足：先前收口只跑了定向测试与构建，未覆盖配额拒绝、持续轮转、异常队列预留和逐条追踪到真实入口的边界。复核确认并修复以下范围内缺陷。

- 普通写入准入与回收：manifest 原先忽略准入返回值，在普通分区被保护文件占满时仍会写盘；现改为真正拒绝超额写入，失败时保留上一份完好 manifest，临时文件在写入/权限/重命名任一失败时清理，任一写入失败都使缓存用量失效并在下次准入重新扫描；关闭时 manifest 落盘失败不再被静默吞掉，而由 `Status` 和关闭返回值上报。app 与事件写入保留普通分区内既有的 1 MiB 事件预留，仅供元数据与原子替换使用，总额度不变。
- 异常分区轮转：此前预留检查在追加之前执行，目录一旦接近 D 就再也不会触发轮转，形成永久冻结；现改为先回收最旧封存分片，必要时封存当前写入器自己的分片后重试准入，仍超出才丢弃并报告。超大单条记录不再先删除历史分片。其他写入器的活跃分片通过预算内的活跃路径登记得到保护，日志轮转不再删除同目录其他写入器正在使用的文件，且不再跟随或删除 symlink。
- 未知 manifest 保护：回收只接受受支持的 schema 版本（v1/v2）和已知 mode（full/basic）且身份与目录一致的已结束会话；未知 schema、未知 mode、身份不符或开始时间为零的会话一律保留。
- 队列异常预留：原先普通事件可以占满整个队列，导致 WARN/ERROR 在队列饱和时被丢弃。现保留单个 FIFO 与原有 `QueueSize`，普通入队不得占用 `max(1, QueueSize/8)` 的异常预留；队列丢弃总数维持原口径，异常队列丢弃另计入 `DiagnosticDropped` 并在排空或关闭后保持可见；`off` 模式不启用预留。
- 消费端完整性：跨全部输入路径统一排序，确保 trace 事件先于异常副本装载，full trace 的 `payload_ref` 不再因输入顺序丢失；冲突识别改用净化前事件的规范化摘要，只忽略 `payload_ref`，仍保留指向敏感差异的冲突报告但不保存原始数值；仅有异常分片（含缺身份记录）时通过既有 warning、报告与 GUI 总览提示材料不完整，不输出原始会话标识。
- checkpoint 拒绝 ACK：此前把 blob key 的十六进制和客户端任意错误正文写入 `error_summary`，会进入事件、异常副本和应用日志白名单；现只记录固定类别 `client rejected checkpoint blob`，待确认 checkpoint 丢弃与业务终态行为不变。

**验证证据**：隔离 HOME 下运行根模块定向测试、共享状态 race 与 vet，最终收口输出 `FINAL_ROOT_REVIEW_PASS`：`go test -count=1 -timeout=3m ./internal/observability ./internal/logsink ./internal/logger ./internal/backend/forwarder ./internal/client` 五包 `ok`；`go test -race -count=1 -timeout=3m ./internal/observability ./internal/logsink` 两包 `ok`；`go vet ./internal/observability ./internal/logsink ./internal/backend/forwarder` 退出 0。分析器侧运行 `go test -count=1 -timeout=3m ./internal/load ./internal/report ./internal/gui ./internal/analyze ./internal/project`、`go vet ./internal/load ./internal/report ./internal/gui ./internal/analyze` 与 GUI 前端 `npm run build`，均通过。新增回归包括：`TestReservedHeadroomKeepsTerminalManifestWritable`、`TestManifestRewriteRemovesTempAfterFailedRename`、`TestManifestRewriteCountsLeftoverTempAtNextAdmission`、`TestClosedWriterRewriteDoesNotReclaimOwnSession`、`TestReclaimableManifestAcceptsKnownSchemaVersions`、`TestNormalReclaimRemovesKnownV1Session`、`TestDiagnosticsReclaimsSealedShardsInsteadOfFreezing`、`TestDiagnosticsOverlappingWriterActiveShardSurvives`、`TestDiagnosticsSealOwnActiveShard`、`TestNormalReclaimProtectsUnknownManifests`、`TestRotatingFileCleanupKeepsProtectedShards`、`TestRotatingFileCleanupSkipsSymlinkedShards`、`TestRecorderQueueReservesDiagnosticSlots`、`TestRecorderQueueReserveClampsWithTinyQueue`、`TestRecorderQueueReserveDisabledInOffMode`，以及分析器的跨输入顺序、原始字段冲突和材料不完整用例。修复 ACK 泄露时先以 `TestCheckpointBlobEventsDistinguishRejectAndUnmatchedAck` 复现失败（断言拒绝 ACK 只能记录固定类别），再改为固定类别使该用例通过。

**边界与交付状态**：交付状态保持 `verified-partial`。本轮未重新打包，现有 `bin/release/0.0.71.2/cursor-byok-0.0.71.2-macos-arm64.dmg`（`79ce4e99…`）早于本次修复，不能作为本次修复的验收产物。未安装或替换运行实例、未重启、未读取或修改真实日志与配置、未 commit/push。真实 checkpoint ACK/超时、客户端 CA 信任来源、上游中断断点以及桌面/浏览器交互仍未验证。范围外问题仅在复核中记录，未做修改。

### 0.0.71.2 日志增强实施收口与本机构建（2026-09-10；verified-partial）

**实施结果**：已按工作决策基线 §5.5 / LOG-ENH-1–4 和系统架构 §14.4 / DESIGN-LOG-ENHANCEMENT-20260910 完成计划内日志增强。异常诊断副本纳入现有总额度 B 并预留 `floor(B/8)`；app、trace/payload、diagnostics 共用预算协调和降级状态链。WARN/ERROR 副本写入受管 diagnostics 分片；普通分区按过期受管日志、closed/full trace、closed/basic trace、旧 app 分片回收，保护活跃分片、未知或损坏 manifest、归属不明文件和 symlink。分析器按 `(app_session_id, sequence)` 稳定合并副本，优先保留 full trace 的 `payload_ref`，真实内容冲突仍报错；checkpoint 使用专用安全字段 `checkpoint_result`，生产端 human sink 与分析器端均执行白名单和脱敏。GLM、checkpoint 恢复业务逻辑、证书信任及上游请求、重试、fallback 行为未改变。

**验证结果**：隔离 HOME 下完成根模块相关定向测试、observability/logger 与 logsink race、受影响包 vet；`go test -count=1 -timeout=3m ./internal/observability ./internal/logger ./internal/logsink`、`go test -race -count=1 -timeout=5m ./internal/observability ./internal/logsink`、`go test -count=1 -timeout=3m ./internal/backend/forwarder ./internal/backend/server/upstream`、`go vet ./internal/observability ./internal/logger ./internal/logsink ./internal/backend/forwarder ./internal/backend/server/upstream ./internal/client`、`cd tools/log-analyzer && go test -count=1 -timeout=5m ./internal/...`、`cd tools/log-analyzer && go vet ./internal/...`、`node frontend/scripts/test-config-projection.mjs`、`gofmt -l` 和 `git diff --check` 均退出 0。forwarder 全包测试曾发现通用 `result` 字段过宽，改为 `checkpoint_result` 后重跑通过；未用不同请求后续成功冒充原请求恢复。

**只读复核与修复**：收口前对本轮 diff 做独立只读复核，发现三处范围内部署缺陷并已修复：普通分区回收原先按 closed/full、closed/basic、旧 app 分片三个独立层级删除，与批准的“closed/basic 与旧 app 分片合并为按时间排序的单一层级”不符，已改为 `fullCandidates` + `mergedCandidates` 两级并补双向时间顺序用例；诊断预留 D 原先只由各 `RotatingFile` 自身限额约束，配置重载期间新旧 sink 并存时 `logs/diagnostics` 可接近 2D，已改为在共享预算锁内按磁盘实际用量准入并在达到 D 时丢弃、以 `diagnostic_reserve_exceeded` 进入既有降级状态链，同时删除死代码 `diagnosticUsageBytes`；manifest 原先直接写盘但字节被计入普通分区用量，现已与事件、payload 同样经过 `admitNormalLocked` 准入与计量（保留临时文件加 rename 与 `0o600`）。复核同时确认锁序恒为 sink → budget → RotatingFile、无反转，活跃 app 分片与未结束 trace 受保护，配置重载失败恢复旧预算，`checkpoint_result` 在生产端、human sink、host 白名单和分析器白名单四处一致且通用 `result` 已不在任何白名单。修复后上述全部验证命令重跑通过。

**构建结果**：使用 Go `1.26.1`、Task `3.53.1`、Wails `v3.0.0-alpha.74`，并在隔离临时 `HOME`、既有 Go 缓存、`GOPROXY=off GOSUMDB=off GOGC=20` 环境中执行 `task build`，成功生成本机 Apple Silicon DMG，并归档为 `bin/release/0.0.71.2/cursor-byok-0.0.71.2-macos-arm64.dmg`。修复后重新构建，产物大小 `24292111` bytes，SHA-256 `79ce4e99204567ad63b969e428b8ff87a765ed36f330e603536bfdf1a4256367`；`hdiutil verify`、`codesign --verify --deep --strict` 通过，bundle short/build version 均为 `0.0.71.2`，Mach-O 为 arm64，签名为 adhoc。较早一次构建的产物 `988e92b9c51a0f06a767f666b829c8b34d261b138dde66c1b0246d8f41b375ee` 早于上述修复，已由本次重建取代，不再作为交付产物。构建没有改动 `go.mod`、`go.sum` 或 `build/` 受管文件；产物位于被忽略的 `bin/` 下。

**剩余边界**：交付状态保持 `verified-partial`。尚未安装或替换运行实例，未重启、未读取或修改真实日志/配置，未执行 Developer ID 签名、notarization、Intel/Windows/Linux 构建或 GitHub 发布，也未 commit/push。增强版本部署后的真实 checkpoint ACK/timeout、客户端 CA 信任来源和上游中断断点仍待后续运行证据。

### 0.0.71.2 日志增强授权同步与配额现实校准（2026-09-10；planned）

> **已被取代**：本文件上方的《0.0.71.2 日志增强实施收口与本机构建》取代本节：比例 `floor(B/8)`、淘汰顺序和消费端行为均已确认并实施。以下内容仅作历史记录。

**授权与范围**：用户已选异常独立保留本轮实现（implement），并明确纳入现有总额度（inside）；本次“继续”推进设计闭合，不重复询问这两个已确认决定。PRD §5.5 已同步为日志增强实施范围；GLM 及恢复点/证书/上游行为修复仍排除。开场“已确认优先保留分配方案”措辞过宽，随后已明确纠正为仅确认独立保留和总预算约束，比例/淘汰尚未批准。

**只读结果**：config 默认采集预算 1024 MiB/7 天，app 另设 100 MiB/14 天；storage 对根目录计量但不能协调 app 写入，额度回收只额外针对 closed/full。trace append 失败设置 fatal 并跳过 human sink，因此不能只在其后加异常文件。loader 只识别原事件/manifest/app 文件名；查询与报告分别消费安全字段投影，需要全链兼容。可核实符号和方案差异已写系统架构 §14.4 / DESIGN-LOG-ENHANCEMENT-20260910；默认值不代表读取了用户真实配置。

**设计结果与阻塞**：建议总额 B 内给异常预留 B/8（备选 B/4），继承现有 retentionDays，容量/期限先到先轮转；普通空间不挤占异常预留，淘汰对象/顺序、保护 open/未知文件、额度耗尽后停止超额写入并报告状态均列为待确认合同。此前额外 64 MiB/14 天不视作获批。后续还需闭合字段、状态、共享配额协调与消费者详情/案例/导出合同，`Design Readiness=not-ready`，不开始源码实施，也不声称增强日志已验证。

**验证与边界**：本次只读源码及现有文档，没有重新扫描运行日志或更改日志文件；只修改 PRD、系统架构、活动任务和本过程记录。文档范围/决策状态 Python 断言退出 0（`DOC_SCOPE_AND_DECISIONS_OK`），`git diff --check` 退出 0；无源码改动，不重复运行旧功能测试冒充新功能验收。下一步请用户确认预留与淘汰取舍，之后形成可执行计划。复用教训：inside 只确定预算来源，不自动批准比例和删除范围；保留能力须沿队列、写入顺序、配额协调和消费展示逐段核实。

### 0.0.71.2 本机日志复核与日志改造讨论（2026-09-10；分析完成，设计未批准）

> **已被取代**：本文件上方的《0.0.71.2 日志增强实施收口与本机构建》取代本节：比例 `floor(B/8)`、淘汰顺序和消费端行为均已确认并实施。以下内容仅作历史记录。

**已确认范围**：用户明确暂缓 GLM thinking 兼容性；恢复点写入超时、客户端证书信任保留为待排查方向；上游稳定性与“日志改造”不能缩减为“日志减量”，须同时评估诊断增强、异常分级与普通日志区分。范围已登记工作决策基线 §5.5，未批准运行行为修改。

**既有日志证据**：本机 `~/.cursor-local-assistant-v2/logs/traces/20260909T093558.146710000Z-f2d4c7ca7972/events.jsonl`，版本 0.0.71.2，固定窗口至 `2026-09-10T09:48:28.489566Z`。此前逐行复核为 1532 个唯一 model_call_id：1522 succeeded、3 failed、4 partial、3 canceled；164 次 checkpoint blob 写入超时涉及 20 个不同请求；10076 个不同连接 client_unknown_ca。不同层日志不重复累加，取消不计为已证实故障，后续其他请求成功不证明原请求已恢复。本次复读关键行 8293、111725、405296、1189968、1190028–1190030、1323849，未刷新或混入后续运行窗口。

**本轮核实的现状与缺口**：

- 已有 INFO/WARN/ERROR 与结构化 trace，不需要从零新建日志框架。`internal/backend/host.go/logObservabilityEvent` 的 app 投影仅保留少量公共字段及 `http_status`，未投影 trace 已有的错误摘要、重试决策、失败阶段、连接方向、checkpoint 跳过原因；普通官方上游的 `status_code` 也未在此分支投影。
- `tools/log-analyzer/internal/sanitize/sanitize.go/AllowlistedFields` 会进一步丢弃 `error_summary/provider_error_summary/skip_reason/missing_blob_key_count/attempt/max_attempts/retry_decision/retryable/failure_category/failure_cause/failure_phase/recovery_action` 等字段。增强产出必须同步读取、净化、查询与受限导出，不能未经脱敏直接扩大自由文本白名单。
- 真实第 405296 行同时为 `provider_response/status=retrying/semantic_outcome=degraded/error_category=server_5xx/severity=error`，`attempt=1/max_attempts=2/retry_decision=retry`。`observability/semantics.go/projectSeverity` 先检查错误类别，覆盖后续 retrying→warning 分支；因此不能声称所有重试已按 WARN 区分。现有测试明确要求 server_5xx 不被显式 info 覆盖。调整尝试级与调用终态级的分级是待确认语义，不是直接替换一个条件。
- 分析器 `analyze.go/addEvent` 为带真实错误类别的每条事件产生 request_error；事件条数/诊断条目数不是唯一失败请求数。`provider_response` 与 `retry_decision` 描述不同事实，不能为了降计数直接删除一类或按 model_call_id 粗暴去重。若增加独立异常投影，需要稳定事件身份与输入来源规则，保留因果事件、仅合并同一事件的副本，按 model_call_final 单独统计业务结果。
- `server/upstream/client.go/recordUpstreamFinished` 接受 err 但仅记录粗类别和状态码；`FetchUpstream` 未调用该观测路径。第 1190028 行官方上游状态码为 0，随后两层为本地 502，不能归因为官方返回 502。需按构造/连接/响应头/读取/解包/投影等可实际观测阶段增强，而不是从耗时猜原因。
- `forwarder/actor.go/applyProviderTerminalErrorStats` 目前从 HTTPStatusError 抽取 provider 摘要；协议终态错误缺少同等的受限摘要投影。第 111725 行只有 provider_terminal 和 HTTP 200，provider_error_summary_type 为 not_recorded，不能确定源端的具体失败原因。
- 上游已有同渠道有界重试、抖动、Retry-After、共享 fallback 预算和输出/工具进度门禁。第 1189968 行为两次 HTTP attempt 后失败，终态 `retryable=true` 表达错误类别可重试，不能代替“本次还会重试”。应同时呈现实际恢复动作、剩余预算/耗尽或抑制原因；不新增外层整轮重放，不默认关闭 HTTP/2、gzip 或证书校验。
- checkpoint 超时已有 error_summary、skip_reason 和 missing_blob_key_count；超时后丢弃待发布 checkpoint，保持原成功或失败终态，不是超时自动导致整轮失败。优先补投递→客户端确认→超时/取消/迟到确认的关联和阶段状态，核实恢复影响后再决定是否改行为。TLS 已有 direction/host/connection_id/tls_role/source，仍缺具体客户端信任来源证据；不得把大量拒绝都当无害遥测，也不因日志分级改变证书策略。

**推荐方向（proposal，非实施合同）**：保持现有 app 运行摘要和 trace 因果链；增加内容充分、受限净化的异常摘要。独立 WARN/ERROR 诊断投影比仅靠筛选多解决“普通日志轮转覆盖异常证据”的问题，建议纳入设计，同时保留从异常回到关联上下文与恢复成功的能力。WARN 与 ERROR 先同一诊断流，以级别筛选，不急于拆为两套文件；保留总配额、独立额度和兼容读取规则待确认。高频重复告警采用首条详情、累计计数、时间范围和恢复记录；关键终态不采样、不隐藏原始错误事实。业务成功、恢复点保存结果、事件级别是不同维度。日志自身的写盘失败、队列丢弃和配额降级也应可见，避免用同一失败 sink 递归报警。

**本轮验证记录**：使用临时 HOME、既有 Go 缓存及 `GOPROXY=off GOSUMDB=off` 运行 `go test -count=1 -timeout=60s ./internal/observability -run '^TestNormalizeEventSemanticsProjectsSeverity$'` 和在 `tools/log-analyzer` 运行 `go test -count=1 -timeout=60s ./internal/sanitize`，均通过。首次文档核验因 Git 对中文路径加引号而失败，改用 `git diff --name-only -z` 后通过；此为核验脚本问题，不是产品故障。文档/报告范围断言及 `git diff --check` 通过，变更仅为三份现有项目文档及仓库外辅助报告；测试没有验证新的日志功能，也没有执行实机恢复或证书修改。

**证据与边界**：本轮只读源码/原始 trace；更新 PRD 范围、活动任务、过程记录与既有辅助报告。未修改业务源码/配置/原始日志，未发真实上游请求、部署、重启或 commit/push。收尾核验限定为现有 severity 与分析器字段过滤测试、文档一致性及 `git diff --check`，不替代真实 CA、客户端恢复或上游稳定性验收。当前是分析结论，`Design Readiness=not-ready`：异常投影和保留合同未确认，运行故障原因仍有事实缺口。复用教训：日志改造必须同时考虑内容、分类、关联、保留与消费端；分级不能替代终态，增强字段不能在下游白名单中再次丢失。

### 0.0.71.1 下载日志异常诊断与 GetManagedSkills 修复（2026-09-09；verified-partial）

用户要求分析 `/Users/yaogj/Downloads/logs`、根因和最小方案，由主控安排执行、review、验证与必要修复。开工 `gateway@5926cc4` 工作区干净。三个有界工作流分别负责日志取证、模型故障链、控制接口/TLS；主控独立复核原始数据、源码 diff 和验证。以下 UTC 时间均指日志事件日期，不混用本机时区。

**覆盖及计数口径**：六份 app 日志；旧会话 `20260909T035825.861070000Z-4041ee8bee13` 为 0.0.71.0，当前 848 事件（03:58:26–06:33:37Z）；新会话 `20260909T064243.292774000Z-09262a3c9dde` 为 0.0.71.1，183889 事件（06:42:43–08:34:09Z）。两份 JSONL 均完整解析且序号连续；新 manifest 仍 open、basic，无失败正文重放材料，结论限定于这个导出快照。旧版 56 次 BidiAppend 502 是历史问题；新版为 9495 个 200 完成事件、1 个 502，唯一失败已走到官方上游且传输失败，不是已修的入站 gzip/JSON 分流故障。请求 ID 会被多次 BidiAppend/RunSSE 复用，不跨层加总为独立故障次数。

**已核实模型事实**：新版 183 个不同 `model_call_id` 的最终事件为 171 succeeded、8 failed、3 partial、1 canceled；`gpt-6-astra` 82/82 成功，`grok-4.6` 89 成功/3 partial，`gpt-6-astra-211` 7 failed/1 canceled，`gpt-6-astra-local` 1 transport EOF。取消不计为已证实服务故障。这是调用终态统计，不等于用户任务或子任务成功率；8 个 subagent_run_id 无 attempt ID，不能据此还原五次续跑或副作用完整性。

- `gpt-6-astra-211`：5 次最终 HTTP 500（新 events 行 4690、55622、107584、129656、178552），脱敏 `provider_error_summary` 均为请求 ChatGPT Codex 端点的 `utls: TLS handshake: EOF`；1 次 503（行 56396）为 `auth_unavailable: no auth available`，附 `198.18.0.49:443` 连接超时。六次均实际执行了两次 HTTP attempt，随后 exhausted / chain_exhausted；无证据表明漏重试或漏切可用候选。第 7 次失败是收到 HTTP 200 后的 provider_terminal（行 181191），有 82350 raw bytes，但没有具体终态错误摘要；取消在行 108820。
- 推断及方案：优先检查该上游转发服务的 Codex 出站 TLS、DNS/代理路由和账号可用性。198.18 地址支持排查代理 fake-IP 路径的假设，不能单凭它确认是哪台机器/哪层代理，更不能认定本机 7890 是根因；auth_unavailable 也不等于已证实账号过期。短期可由用户选择本样本表现正常且能力适用的渠道，或配置兼容备用渠道，不自动换模型/改账号。增加网关重试无法修复持续上游出站失败。
- Grok：三条 HTTP/2 + gzip 流在 `08:01:33.501544/501545/501587Z` 结束（最终事件行 107331/107334/107337），分别已收 87456/79053/85267 raw bytes，已解析 SSE delta，关闭原因为 unexpected_eof。支持共享服务/代理/连接中断假设；没有 connection ID，不能确认同一物理连接或断点。成功对照也使用 gzip，因此不是旧版入站解析同型问题，但仍不能排除响应尾部 gzip 截断。保持已收数据后不重放；先查同时间上游/代理日志，必要时使用已有 provider-only 单变量传输诊断，不默认禁用 HTTP/2 或 gzip，不新建续跑状态机。

**控制接口及证书**：

- GetDefaultModel 48 次 backend 502（代表行 506、180345），来自本地 `handleOfficialDefaultModel` 的统一失败响应。`FetchUpstream` 不走常规 upstream 事件记录，源码将网络错误、非 2xx、空 payload、解包/投影错误合并为同一异常（`catalog.go`）；因此不能认定“官方返回了 48 次 502”，也不能从约 250ms 耗时或 AvailableModels 200 推断上游成功（目录允许本地降级）。最小下一步为既有 fetch 路径补脱敏阶段、HTTP 状态、媒体类型/编码和长度，拿到证据再修；不将官方默认静默替换成本地默认。
- GetEffectiveUserPlugins 191 次 404、GetKnownServers 66 次 404，均无官方转发，控制面账号管理器未登录时当前 helper 明确返回本地 NotFound；插件路径还存在前面 EmptyMock 被后注册控制面路由覆盖。它使用的登录判断与入站官方身份判断不同。需按真实需求确认哪些只读接口无账号时应该合法空响应、哪些应保留官方数据后再修；不把所有 404 改成假 200，也不扩大真实身份透传。
- AnalyticsService/Batch 1139 次 MITM 转本地后 404，Host 没有该精确路由；属于遥测接口缺口，不是 1139 次模型失败。是否提供最小兼容响应需核实该 RPC 协议及既有遥测策略，本轮未新增接口。
- TLS `client_unknown_ca` 1403 次：api3 987、metrics 352、api2 64。方向为 Cursor→MITM，说明部分客户端拒绝代理证书；api3/metrics 本样本全部停在握手，而 api2 存在大量成功 HTTP。握手没有请求 path，不能说都只是遥测，也不能把这些错误与 provider TLS EOF 合并为一个根因。最小处理为确认具体进程的 CA 信任/加载和代理域名范围，必要时在用户允许的窗口重启相关客户端；保留证书校验，不自动重装 CA 或绕过验证。

**确定缺陷与修复闭环**：新会话 GetManagedSkills 有 7 次 502。`host.go` 已为未登录定义成功空 skills fallback，但 `client.go/newProtoMessage` 未注册已存在的 `aiserverv1.GetManagedSkillsResponse`，编码错误由中间件变为 `bad gateway\n`。在既有 `cursor_contract_test.go` 增加真实 Host mux 回归，隔离账号及 HOME；proto/JSON Content-Type 两例在原源码都 502，补 2 行类型分支后都 200、protobuf 可解码且 skills 为空。JSON 仅作为入站类型对照，响应仍为既有 protobuf 行为；没有改变协议协商、已登录转发、其他 404、CA、重试或默认模型语义。

独立 review 未发现具体 bug、回归或本次改动的阻塞测试缺口；主控修正了初步分析中的过强归因。最终 `retryable=true` 与 exhausted 的观测字段差异暂记为诊断语义待核实，不证明实际再发 HTTP/重启 Cursor 任务，也没有为改统计而改运行行为。GetManagedSkills 只是局部控制面修复，不能解释或解决模型渠道失败。

**运行验证证据**（全部使用临时 HOME、原缓存路径和 `GOPROXY=off GOSUMDB=off`）：

- 回归 RED→GREEN：`go test ./internal/backend/ -count=1 -timeout 60s -run TestHostGetManagedSkillsUnsignedFallbackEmptySkills -v`，前者退出 1（两例 502），后者退出 0；日志 `/tmp/gateway-control-review-20260909/test-logs/managed-skills-{RED,GREEN}.log`。
- 主控最终验证：`go test -count=1 -timeout=90s ./internal/backend ./internal/backend/server/upstream -run 'HostGetManagedSkills|CursorBackendContract|ControlPlane|MockProto|OfficialDefaultModel' -v`，两个包通过（0.448s/0.666s）；`go vet ./internal/backend ./internal/backend/server/upstream` 退出 0。日志 `/tmp/gateway-log-final-tests.log`、`/tmp/gateway-log-final-vet.log`；主控原始日志独立聚合 `/tmp/gateway-log-final-census.json` 与取证结果一致。
- 模型方向隔离表征：model/forwarder 定向覆盖 503 exhausted、零字节 EOF 恢复、已收字节不重试、Responses EOF/完成终态、gzip 元数据及脱敏错误摘要，两个包通过（0.741s/0.543s）。初次因隔离 HOME 后未完整保留模块缓存而 setup failed，修正环境后通过；不是产品失败或执行服务中断。控制面另跑默认模型成功/失败不降级、TLS 分类及 chi 重复路由隔离测试通过；测试不替代真实上游与 CA 现场证据。
- `git diff --check` 与当前编辑器诊断通过。未运行无关前端、独立 module、整仓/race/build 或真实网络模型故障注入。

**交付边界与下一步**：分析/复核/定向验证完成，最小修复在工作区；整体 `verified-partial`。上游根因精确断点、默认模型实际失败阶段和证书进程信任为 data/env gap，404 空响应/官方回源为待确认语义。没有部署/重启实例、修改用户日志和配置、调用真实模型、commit/push。执行与 review 无服务中断，未触发 20/40/80/160/320 秒恢复策略；该策略仅用于本轮协作任务，不引入产品自动重调度功能。教训：分开统计请求完成、模型终态、TLS 握手和任务结果；统一 502 不能代替上游错误证据；已有字节不代表完整压缩流成功，微秒级同时断流不代表已确认物理连接复用。

### 0.0.71.0 连接回归修复收口（源码与隔离验证完成；实机待验收）

用户批准计划并要求主控协调执行、独立 review、验证及修复。按系统架构 §18 D2.1 完成最小改动：`agent_action.go` 将 HTTP Content-Encoding 传入 BidiAppend/RunSSE 解析；`agent_route.go` 依次处理 HTTP 压缩、Connect 帧和 protobuf/JSON。只读取解码结果用于路由，原始 body、媒体类型、编码与身份仍用于后续转发；选模、会话归属、超时、账号和证书逻辑不变。下两节保留首次取证与临时实验的历史状态，以本节为最新执行结果。

- 回归先红后绿：既有 `agent_route_test.go` 固化四种身份/选模×三种编码共 12 组合，以及真实 Connect 解码、流先到、原始请求保留、损坏/不支持编码和帧压缩；原源码 gzip/JSON 失败，未压缩对照通过。`cursor_contract_test.go` 新增 `TestGatewayDuoHostGzipUnaryWire`，原源码普通 BYOK 可收到模拟模型回复、gzip 返回 502。RED 日志为 `/tmp/gateway-wire-route-red.log` 与 `/tmp/gateway-wire-host-red.log`，实现后转绿。
- 独立 review：行为复审 `82d08664-e6b5-45fe-869d-cb63d2fc74cc` 未报告生产缺陷；测试复审 `ae54043f-d363-4cc4-b9e6-9fa706c5f0c7` 指出接收协程超时收尾不可靠。主控移除多余协程/通道，改为同步 `stream.Receive`、延迟关闭流并检查 `stream.Err`；沿原复审任务确认问题关闭，无剩余已证实生产缺陷。
- 中断恢复：Host 测试任务两次 provider_terminal/status=not_recorded，未返回可恢复 ID；实际等待 20、40 秒后基于保留的工作区改动继续同一任务，第 2 次重试成功。有 ID 的任务沿原 ID 继续；未触及后续三次退避或持续失败停止条件。

文档收口核验：计划索引与 `task/todo.md` 均将 `verify-and-record` 标为 completed、`runtime-acceptance` 保持 pending；最终内容断言已通过。此检查仅证明记录一致，不替代实机验收。

最后测试清理修改后的验证均退出 0：

- `go test -count=1 -timeout=120s ./internal/backend/server/upstream ./internal/backend`：两个包通过（0.439s / 11.031s）。
- `go test -race -count=1 -timeout=120s ./internal/backend/server/upstream ./internal/backend -run 'AgentRoute|GatewayDuo'`：定向竞态检测通过（1.522s / 12.485s）。
- `go vet ./internal/backend/server/upstream ./internal/backend`、`git diff --check` 通过。
- 环境使用 `mktemp -d /tmp/gateway-wire-closeout.XXXXXX` 创建的隔离 HOME，复用 `GOCACHE=/Users/yaogj/Library/Caches/go-build`、`GOPATH=/Users/yaogj/go`、`GOMODCACHE=/Users/yaogj/go/pkg/mod`，设置 `GOPROXY=off GOSUMDB=off`。日志为 `/tmp/gateway-wire-closeout-test.log`、`/tmp/gateway-wire-closeout-race.log`、`/tmp/gateway-wire-closeout-vet.log`。

证据边界：Host 本地链可证明实际后端入口→模拟 provider→流中指定文本；官方/Auto 的 Host 替身只证明路由和原始请求保留，不是官方服务真实解码/回复。checkpoint blob 测试中仍有约 5 秒等待后跳过，不代表完整 checkpoint 验收。gzip 是现场首要触发判断，basic 日志缺失败首包，不能对全部 502 逐条归因。源码修复、review 与计划内隔离验证完成，整体保持 `verified-partial`；官方、Auto、官方身份下 BYOK 的实机首段回复、后续工具/取消仍待另行确认窗口。

本轮没有打包、安装、替换或重启当前 0.0.61.1，没有改真实配置/账号，也未 commit/push。未增加重试、等待状态机、安全门禁或无关前端/独立模块验证。复用教训：前置分流必须覆盖原 Connect 入口接受的真实编码，模型成功须断言实际回复，测试异步收尾能同步实现时优先保持简单。

### 0.0.71.0 对话连接回归：日志取证（2026-09-08；当时未修复）

用户报告官方账号模型、Auto、本地 BYOK 均持续连接，已自行回退 0.0.61.1。本轮仅检查既有日志与版本代码差异，并记录结论；未切换运行实例、修改真实配置/身份/证书或调用模型。

- 下载证据：`/Users/yaogj/Downloads/logs/traces/20260909T035825.861070000Z-4041ee8bee13/{manifest.json,events.jsonl}`，版本 0.0.71.0、basic、darwin-arm64，共 841 条事件。按 `layer=backend / event=request_finished / route=bidi_append` 聚合，56 次均为 502、handler_error；7 次已结束 RunSSE 均在 60000～60002 ms 后 canceled。没有 upstream 或 provider 调用事件。应用日志显示本地后端健康检查和代理启动成功，首次 BidiAppend 11:58:37 +08:00 失败，随后重复请求。
- 本机交叉证据：`~/.cursor-local-assistant-v2/logs/traces/20260909T035041.036672000Z-8ecec8326698`（0.0.71.0）8 次 BidiAppend 502、两次 RunSSE 60001 ms 后 canceled；`20260909T040419.301580000Z-2e2c6bad54f0`（0.0.61.1）读取快照已有 226 次 BidiAppend 200，前一次读取有 6 次 provider request/response、521 条 llm_response_chunk。旧版会话仍在增长，数量是读取时快照，不是固定最终总量。Cursor 3.15.19 同期日志另有 stream_stall、等待流活动超过 30 秒的记录。
- 定位边界：`git log` 确认两版相邻提交为 `4e4f2f1` 与 `818e293`。新增 `AgentRouteAction` 在 BidiAppend 分流前解析请求；解析或未知渠道错误直接返回，`writeServerError` 默认统一编码为 12 字节的 `bad gateway\n`；RunSSE 则等待同 request_id 的渠道决策。这与现场现象一致，但不是具体异常的证明。basic 记录只保留 handler_error，无原始异常/请求体，尚未建立可重放失败回归。
- 证书问题单独处理：下载样本有 24 次 client_unknown_ca，本机新版与已产生模型输出的旧版也均有该告警，不能把它直接认定为本轮共同主因。下载与本机日志来自不同用户路径，按 UTC 时间核对，不混用本地时区。
- 本轮命令证据：使用 Python 标准库逐行解析 JSONL，以 event/route/status_code 聚合并关联 RunSSE；读取应用与 Cursor 结构化日志；`git diff --stat 4e4f2f1 HEAD` 和分流/错误编码源码检查。未运行 Go 测试或真实请求重放，没有修复完成声明。
- 记录核验：诊断结论已写入本节与 `task/todo.md`，文件内容断言及 `git diff --check` 已通过；该结果仅证明文档记录，不证明连接故障已修复。

结论：0.0.71.0 实机对话已有阻塞回归证据，双渠道交付仍为 verified-partial，真实对话验收失败。下一步需要脱敏失败请求样本或受控诊断获取底层错误，先建立能复现 502 与等待现象的入口测试，再修复并覆盖官方、Auto、BYOK；继续保留用户当前回退状态。复用教训：模拟封装的路由测试通过不能替代真实 Cursor 请求编码与首包时序验证；通用错误类别不足以定位解析失败，不把猜测登记为现场根因。

### 0.0.71.0 编码回归：隔离复现与修复建议（续查；生产源码未修改）

以下证据更新前节首次取证时的“未复现”状态。用户要求继续分析并给出方案；实验只在 `/tmp/gateway-wire-probe.d2P9ex` 创建诊断测试、候选源码和 Go overlay，未替换仓库 Go 文件或真实代理实例。

- 已证实代码缺陷：`agent_route.go` 的 BidiAppend/RunSSE 解析调用 `extractCatalogProtoPayload(contentType, "", body)`，未传 HTTP Content-Encoding；并固定 `proto.Unmarshal`，不区分 application/json。旧入口所用 `connect.NewUnaryHandler` 能正确读取这些合法 unary 编码，而新增分流在它之前失败。路由尚未 Remember，先到的 RunSSE 因而继续 Wait。这是隔离测试已复现的 502 与等待因果链，不是根据错误名称推测。
- 本机运行关联：`Cursor/logs/20260908T205102/window1/exthost/anysphere.cursor-always-local/Cursor Structured Logs.log` 第 35、70、123 行均记录 HTTP/1.1 transport 的 `compression.sendCompression=gzip`。安装包传输构造另设置 useBinaryFormat=true 和 gzip；因此 gzip 兼容性是现场首要原因。没有失败首包，不能宣称下载日志每条请求都已证明用了 gzip；JSON 是同次发现的兼容性缺陷，不声称现场使用 JSON。
- 复现矩阵：纯本地身份选 BYOK、官方身份选 BYOK、官方身份选官方模型、官方身份选 Auto，分别使用未压缩 Protobuf、HTTP gzip Protobuf、JSON，共 12 例。相同 payload 先由 Connect unary 解码器验证合法，再经实际 AgentRouteAction + ErrorEncoder，RunSSE 先到。原源码未压缩四例成功，gzip/JSON 八例全部 502，错误为 `proto: cannot parse invalid wire-format data`，等待流未被唤醒；30ms 是隔离测试的观察窗口，不是产品超时。
- 单变量实验：先只传入 HTTP Content-Encoding，gzip 恢复、JSON 仍失败；再增加按 Content-Type 的 JSON 解码，全部恢复。最终候选断言 BidiAppend 200、等待流成功转发且状态 200、原始 body 和 Content-Encoding 不被修改。官方/provider 均为本机替身，不触发真实模型消费。
- 最新验证：原源码 `final-red.log`：12 例中 8 FAIL / 4 PASS，退出 1 是预期缺陷信号。临时候选 `final-green-contract.log`：12/12 PASS，既有 TestAgentRoute、TestParseBidiAppendRouting、TestParseRunSSE 和四项 TestGatewayDuo Host 测试通过，两个包退出 0。只证明隔离入口兼容性与所选契约，没有执行整仓、race、构建或真实 Cursor 对话。
- 可复跑命令：环境为 `HOME=/tmp/gateway-wire-probe.d2P9ex/home GOCACHE=/Users/yaogj/Library/Caches/go-build GOPATH=/Users/yaogj/go GOMODCACHE=/Users/yaogj/go/pkg/mod GOPROXY=off GOSUMDB=off`；原源码运行 `go test -overlay /tmp/gateway-wire-probe.d2P9ex/overlay.json -count=1 -timeout=45s ./internal/backend/server/upstream -run '^TestDiagnosticAgentWireCompatibility$' -v`；候选运行 `go test -overlay /tmp/gateway-wire-probe.d2P9ex/candidate-overlay.json -count=1 -timeout=90s ./internal/backend/server/upstream ./internal/backend -run '^(TestDiagnosticAgentWireCompatibility|TestAgentRoute|TestParseBidiAppendRouting|TestParseRunSSE|TestGatewayDuo)' -v`。临时文件保留便于转正式回归，不当作已交付源码。

建议正式修复范围（待实施，不将建议当作新批准 Design）：

1. 在 `agent_action.go` 向两个解析入口传递真实 Content-Encoding；`agent_route.go` 在只读副本上先处理 HTTP 压缩、再处理已有 Connect 帧、最后按实际媒体类型解码。复用既有 gzip 有界解压；JSON 用 protobuf JSON codec。保留既有 Connect 帧压缩与 HTTP 整体压缩的区分，unsupported/损坏数据明确报错，不猜渠道。
2. 保留原始请求体、Content-Type/Content-Encoding 和身份用于后续转发；不改本地 ID 优先、官方/Auto 选择、已决策会话归属或官方失败不跨渠道规则。无需禁用 Cursor 压缩、调整系统代理/CA，也不以增加重试/延长等待掩盖解析错误。
3. 将诊断测试固化为真实 Host 入口的回归，覆盖登录身份下 BYOK、gzip/JSON、stream-first、后续消息及取消。既有 Host 本地分支只检查未请求官方，不严格断言本地 200/模型输出，正式回归应补上这些成功条件，避免假上游接受任意请求而掩盖格式错误。
4. 正式落库并定向测试后，在获准实机窗口测试官方、Auto、BYOK 的首段回复和后续工具/取消；确认 502 消失后才将产品标为已修复。安全诊断可记录媒体类型、编码、长度、阶段和脱敏错误类别，不记录身份令牌/完整对话；当前不引入新的等待状态机、超时策略或日志框架。

当前结论：兼容性缺陷已定位、最小修正方向已获隔离验证；实际产品未修复、未打包部署。关联需求/设计仍为工作决策基线 §10.17 / 系统架构 §18 D1–D2；方案保持这些既有路由契约。复用教训：路由前置解析必须覆盖旧 Connect 入口真实接受的压缩与编码；构造 application/connect+proto 的合成首包不能替代 unary application/proto + Content-Encoding:gzip 验证。

### gateway-duo 双渠道移植（2026-09-08 发起，09-09 收口；verified-partial）

已在 `gateway@4e4f2f1` 工作区移植 `gateway-duo@18ef0e2` 相对 `334f538` 的双渠道功能，保留目标分支现有订阅、模型解析/变体/备用渠道、CLI 占位、API 与代理能力。没有整体覆盖旧文件，没有修改版本、前端管理页面或配置格式，没有提交、发布、替换正在运行的代理。契约真源为工作决策基线 §10.17、系统架构 §18「gateway-duo 合并」D1–D4；活动执行见 `task/todo.md`。

已接线与隔离验证的链路：身份 DB 保留已有非空官方身份 → 桌面/CLI 协议目录合并并显示 `[官方]`/`[BYOK]` → Host 按本地渠道 ID 优先、其余按官方身份分流 → 本地现有 provider 或模拟官方上游 → 同 request_id 的流与后续消息保持渠道。纯本地身份保留原有解析和默认行为；官方目录失败可展示本地可选项，但不推荐本地默认；官方默认或对话失败不自动切成本地。CLI 本地条目保留无秘密凭据 `cursor-byok-local`。

review 与修复：

1. Spec review 未报告已证实契约违规；质量 review 发现 P2：配置重建创建新 `AgentSessionStore`，重建前到达的 RunSSE 无法收到新 mux 上的 BidiAppend 路由。回归先失败，随后将存储提升到 Host 生命周期，在既有 `runMu` 保护下只初始化一次，两处理器共用；回归和复审通过。
2. 主控补 `TestGatewayDuoHostOAuthBothModes` 复现另一接线缺陷：`Routing.Mode=upstream` 绕过只挂在 Local 上的 OAuth 分流器，把本地占位 refresh 发给官方。为 `/oauth/token` 的 Upstream 分支挂相同 `MockOAuthAction`；local/upstream 两个子测试均通过，断言本地零官方命中、真实刷新保留请求体/身份且返回官方响应。最新独立只读复审 `2d96e06f-54ab-4bc8-9ca4-4c2d7f9fce5e` 确认关闭，未报告其他已证实缺陷。
3. 目录构造编译问题、旧测试无来源后缀的断言在集成阶段修正；纯本地目录也统一 `[BYOK]`，没有为迎合断言改变模型 ID 或去掉 CLI 占位。

最终源码验证（OAuth 修复之后，命令均退出 0）：

- `go test -count=1 -timeout=90s ./internal/backend -run TestGatewayDuoHostOAuthBothModes -v`：两个模式通过。
- `go test -count=1 -timeout=5m ./internal/...`：internal 全量包通过；不是根模块 `./...` 或独立 module 全量验收。
- `go test -race -count=1 -timeout=5m ./internal/backend ./internal/cursor ./internal/mitm ./internal/backend/server/upstream ./internal/backend/agent/protocol`：五包通过。
- `go vet ./internal/backend/... ./internal/cursor ./internal/mitm` 与 `git diff --check` 通过。
- 最终完整日志：`/tmp/gateway-duo-close-test.log`、`/tmp/gateway-duo-close-race.log`、`/tmp/gateway-duo-close-vet.log`。先取得 `GOCACHE/GOPATH/GOMODCACHE`，再使用临时 `HOME=/tmp/gateway-duo-close.yebSJ5` 执行；不通过真实身份或真实官方服务验证。

协作恢复：目录执行任务曾遇到 `stream_idle_timeout`，等待 20 秒并定位已有上下文后恢复成功；没有重新创建替代任务掩盖中断，没有触及最多五次恢复的停止条件。并发执行不超过两个；最终复审正常完成。

测试隔离事件：早期直接运行后端契约测试时，既有 Host 初始化清理触碰真实用户目录，日志报告删除 8 个过期 debug 文件。已向用户说明；没有原始内容不能凭空恢复。已为 `newHostConfigTestManager` 等辅助入口补临时 HOME，之后 Go 测试/race/vet 均在临时 HOME 下执行。复用教训：任何可能构造 Host 的测试，第一次运行前就隔离应用数据目录；会话相关路由存储不能随配置重建丢失；模式分流必须从真实 Host mux 入口分别验证，孤立处理器测试不等于接线完成。本项目没有既有 lessons.md，教训记录在本节，不另建文件。

当前交付为 `verified-partial`：代码移植、接线、隔离契约验证、review 和文档收口完成；test/env gap 是真实 Cursor 登录保留、桌面与 CLI 来源字段消费、官方/本地/Auto 对话及真实官方 OAuth 刷新。后续在获准实机窗口按 D1–D4 逐项验收，不能将模拟官方 HTTP 响应升级为真实端到端证据。本次没有执行打包安装、前端构建或独立模块测试；无相关代码变更。未恢复以前被覆盖的真实令牌，未解锁其他历史 blocked 工作包。

### 重开确认、订阅状态及模型代理回归（2026-09-08 发起，09-09 收口；verified-partial）

用户要求修复三项缺陷并由主控安排独立执行、review、验证。代码仍处于工作区，未打包/安装/提交。用户经选项确认：退出 BYOK 保留 Cursor 接入配置，接受暂停期间本地代理不可用；需要断开须在本实例成功接管设置后显式停止。该决定已同步工作决策基线 §10.14 和系统架构 §6.0。

根因与修复边界：

1. 重开重复确认：`doShutdownForQuit` 原先清除 settings.json，下一次 `Plan.NeedsChange` 因键缺失而要求 Cursor 重启。移除退出清理，保留显式 `StopProxy` 所有权清理、真正变更的确认及失败补偿。隔离生命周期先复现、修复；主控强化新实例 backend host、backend/proxy 运行、新 owner 接管、接管后停止清理与 CA 清理替身不得调用的断言。强化阶段发生测试字段名拼错及配置写在 host 快照创建后造成的失败，已修正测试并重新通过，未为迎合测试改变生产配置逻辑。
2. 订阅状态：原 UI 仅在 active 且 ready 时显示当前使用，并用禁用动作承载状态；auth_required 时刷新被禁用，刷新失败后列表也可能滞后。面板独立显示当前激活及账号健康状态/名称，失败后加载最新列表且保留错误、解除 busy 允许重试，成功激活后同步徽标和页脚。`subscriptionAccounts` helper 接入真实 Vue 模板；补 provider 切换响应代数保护。PascalCase 仅兼容测试，正式 AccountStatus JSON 使用 camelCase，不能将大小写猜测记作现场根因。
3. 代理：`FetchModelAdapterModels` 在 Resolve 前未挂 `OutboundProxy`，导致订阅 token 刷新使用默认代理；补 `WithRequestConfig`。这只证明模型发现链路缺陷。`TestModelAdapter` 原有刷新与推理已挂代理；新增成功链路使用真实入口、过期合成 JWT、本地 HTTP CONNECT 代理和 TLS 假上游，断言刷新后的 token 被推理使用、`success`、两个目标 host 与 env 零命中。**用户真实 7890 测速失败仍未复现，根因未知**，等待不含凭据的原始错误和受控实机取证，不能宣称第三项完整修复。
4. 构建 review 附带修复：静态翻译扫描将新增测试中的 `one@example.test` 文案打入 catalog。主控用临时目录通过 `syncCatalogFiles` 建立失败回归，再在扫描/transform 共用排除条件中排除 `.test`/`.spec` 文件。重新生成 catalog/locales 并构建，断言无测试 ref/该示例账号。没有把 fixture 注入产品运行时。

独立复审：Spec 与 Standards 两路均未报告新增阻塞生产缺陷；Spec 指出的生命周期/网络证据边界保留，并补新 owner/运行断言。两路初次启动均 503、浏览器初次启动 stream_decode 失败，均未返回 ID；执行 `sleep 20` 后各第 1 次重试成功，后续按已返回 ID 恢复结果收集，未使用其余四次重试。浏览器在独立临时 Vite、合成账号及 stub API 中验证三账号当前异常标记、refresh 两次（先失败后成功）、busy 禁用/恢复、切换激活及页脚；consoleErrors/forbiddenCalls 均为空。三次截图工具成功但无可用磁盘路径，不虚构截图链接；没有独立 Network 抓包或真实 API 证据。临时 tab/Vite 与 fixture 已清理（执行方验证端口 43187 无监听）。

主控最终验证（最后源码/测试修正后）：

收口核验范围：文档更新后另行执行差异格式、任务阻塞状态和已生成发布资源的只读检查；这些检查不替代真实 7890 或桌面实机验收。

- `go test -count=1 -timeout=120s ./internal/client ./internal/cursor ./internal/subscriptionauth ./internal/backend/agent/model ./internal/netproxy`：五包均 `ok`，分别 12.936s/1.908s/0.671s/6.558s/1.797s。
- 同五包 `go vet` 通过；`go test -count=1 -timeout=60s ./internal/app -run 'Tray|AutoStart'` 通过（0.449s）。
- 在 frontend：`node --test plugins/static-i18n-plugin.test.js src/components/SubscriptionAuthPanel.test.js src/state/subscriptionAccounts.test.js`：10/10；`node scripts/test-config-projection.mjs` 与 `node scripts/test-client-api-logging.mjs`（54 call sites）通过。
- `npm run build`：136 modules，3.00s；构建后的 catalog 无 test/spec ref 及测试账号文本断言通过。保留 >500kB chunk、Node localstorage-file 与测试 renderer Vue feature flags 告警；Go 保留 macOS 14.0 object/11.0 link target 告警，不作为兼容性实测。
- 原先的代理回 502、仅验证命中测试只证明路由，不作模型成功证据；完整成功证据限定于本地 CONNECT/TLS fixture（注入测试 RootCAs），不能外推真实服务。

测试隔离事件与教训：执行方报告首次 RED 曾调用真实 `launchctl unsetenv NODE_EXTRA_CA_CERTS` 成功；主控随后只读检查当前确为 unset，但没有调用前的值证据，不自动猜测恢复。已为 fixture 加系统 CA 环境清理替身，退出保留测试断言不调用该替身。今后 lifecycle 回归必须在第一次运行前隔离设置、账号、钥匙串和 launchctl；测试命中网络不等于业务成功；提取 helper 必须实际接入组件并有真实渲染交互验证；生产资源扫描不能收录测试文案。本项目无既有 lessons.md，本次记录复用教训于此，不新建文档。

剩余缺口：env/test gap 为真实 Wails、Cursor 正常重启/钥匙串授权与用户 7890 模型失败；无整仓/race/桌面打包验收。没有停止生产 Gateway/Cursor，因此无需恢复服务；已恢复本轮因连接失败中断的 review/浏览器工作。历史只读重调度缺可稳定关联的 Cursor typed failure 证据，ACP 缺真实客户端，继续 blocked，不把本轮授权当作前置条件已满足。

### 模型导入、全部测试与分层代理（2026-09-08，verified-partial）

用户确认访谈及计划后授权 Build。需求和设计已同步至工作决策基线 §10.14、系统架构 §14.19；本次只实现模型专用导入、全部测试和分层出站代理，没有启用全局超时、跳过 SSL、Task 恢复或 PAC 引擎。

完成的代码链路：

- 模型导入不再因服务运行被静默禁用，新增 `ReadModelAdaptersForImport`，只读取现有 YAML 的 `modelAdapters`，合并到模型草稿，显式保存后才写模型分区。先按渠道身份、再按唯一同名更新，保留未命中项和位置，新增项追加；取消/失败不污染草稿，保留完整配置导入后端和原导出语义。
- “清除全部”替换为“全部测试”，忽略搜索/提供商筛选，沿用并发 10、现有单模型接口、结果与超时；单项失败继续，停止只停止后续派发并等待在途结束。空列表/全逻辑 alias 有明确提示。
- 根配置及模型 `outboundProxy` 默认关闭，关闭保留地址。优先级为模型自定义 > BYOK 全局自定义 > 环境/OS > 直连；自定义代理失败不降级默认代理，回环/内部通信保持直连。接通配置规范化/分区保存、UI、热更新、普通推理、候选渠道切换、测试、模型发现及共用 netproxy 出站客户端；不修改系统代理或其他进程。

整合验证中修复的具体缺陷：

1. fallback 候选共用 liveness context，HTTP retry 再替换 context，会丢掉请求级代理。定向假上游测试先返回错误路线的 502；在两个 context 边界保留代理后，候选各用自己的代理且测试通过。
2. 模型 `ID` 标为 `yaml:"-"`，真实导出不写 ID。原模型专用读取返回空 ID，前端不能映射候选引用；新增导出→导入回归先报 `ID = ""`，再复用身份规范化恢复 ID。`NormalizeModelAdapterDrafts` 只恢复单项规范化和排序，跨集合 fallback/capacity 检查保留在合并/保存之后，允许导入引用当前模型的部分 alias。前端对导入 ID→当前草稿 ID 重写引用，覆盖同名但端点/Key 改变、新增候选和未命中旧引用。
3. 已保存全局代理快照原先只在 settings capture 更新，保留未保存设置草稿或 models-only 重载时可能陈旧。改为应用持久配置时先更新响应式快照；前端哈希与继承说明使用已保存值而非未保存草稿。
4. 导入 busy 从原先文件选择后提前到入口，重复点击不再开启多个导入；取消恢复可点。本次模型草稿合并不额外增加确认门禁。

运行证据：

- 主控 `go test -count=1 -timeout=180s ./internal/netproxy ./internal/backend/server/config ./internal/runtime ./internal/backend/agent/model ./internal/client ./internal/backend/agent/bridge/interaction ./internal/backend ./internal/app ./internal/bridge` 退出 0，九包均通过。覆盖本地 HTTP/SOCKS5 代理、优先级、失败不降级、并发隔离、热更新/在途请求、推理/候选/测试/发现和网页抓取原有 DNS/私网规则。
- 最后导入 ID 修正后 config/client/bridge 再次完整测试，三包均 `ok`；补充的实际 YAML 导出读回及部分 alias 用例通过。定向 `go vet ./internal/backend/server/config ./internal/client` 通过。
- 最后前端修改后，`node frontend/scripts/test-config-projection.mjs` 输出 `config projection tests passed`；`node frontend/scripts/test-client-api-logging.mjs` 输出 `clientApi logging contract tests passed (54 call sites)`。
- `npm run build --prefix frontend` 退出 0，134 modules、`built in 3.24s`。构建按既有流程刷新翻译目录及 catalog，未回退原有 catalog 改动。保留 chunk >500 kB 与 Node localstorage-file warning，Go 保留 macOS 14.0 对象/11.0 链接目标 warning；不以构建通过证明低版本实机兼容。
- `git diff --check` 通过。既有版本号、发布资产、release-notes/releaselog 修改保留，没有 commit/push。

浏览器证据（隔离 Vite `127.0.0.1:5179`，合成内存状态，无桌面后端）：网络页代理默认关闭，切换后出现待保存状态；输入 `ftp://proxy.example:8080` 保存显示“全局自定义代理 URL 仅支持 http、https 或 socks5”，没有未捕获 JS 异常。模型页模拟 service/backend/proxyRunning 均为 true 时，导入按钮 `disabled=false`；两条无 Key 假模型仅搜索显示一条，点击全部测试后 `filtered=1,total=2,completed=2`，两条均有可见校验错误且模型数仍为 2。模型编辑代理默认继承，勾选后显示模型自定义，输入 SOCKS5 地址再关闭仍保留地址并恢复继承文案。浏览器测试状态已清除，测试预览停止，不触碰真实模型凭据或已运行服务。

证据边界：真实 Wails 原生文件选择/保存 RPC、外部代理实机及真实模型请求未验证；本地假代理/假上游与浏览器内存 fixture 不能替代这些证据。本次不打包/安装/重启应用，不执行无关完整仓库测试/race。交付为 `verified-partial`：代码及相关验证完成，桌面实机验收待后续受控执行。

### main → gateway 价值迁移追加完成情况复审（completed，verified-partial）

用户要求再次 review 完成情况并修复问题；本轮审查 `git diff 2fc04e7` 和未跟踪源码/测试，保留前次最终验证豁免。Spec 复审 `719d398e-ecab-441f-9479-1fbd52cdeee2` 未报告 REC/CMP、CLI/MODEL、WebFetch 的新增具体缺陷；Standards 复审 `b5a71c5c-b64b-45d2-ad49-6bd11a9a4b86` 未报告 TOOL/RESULT 缺陷，提出的 Cursor 候选问题由主控核对、复现并修复。

本轮确认并修复三类问题：

1. **设置并发写入/回滚（P1）**：`Restore` 原先不核对当前 owner，另一个实例写入后会被旧快照覆盖；原 owner 未变化但值已更新也会被覆盖。新增失败测试证实问题。现在回滚核对 owner 及待恢复键的旧值/本次目标值，冲突时保留现状并返回错误；`ApplyPlanned` 在同一所有权锁内验证计划中的 owner 与全部注入键旧值再写入，防止 Plan→Apply 间隙覆盖新状态。StartProxy 传同一快照至实际应用路径，CA 操作后不重新 Plan；owner 读取错误和内层回滚失败不再吞掉。新测试覆盖外部 owner、原 owner 更新值、写 owner 失败后的补偿、无关键保留和重复补偿，以及 StartProxy 失败时不得回滚另一实例的设置。
2. **部分成功错误事件丢失（P2）**：Cursor 启动失败时 RPC 已报 `cursor_launch_partial`，但 `emitState` 因代理仍运行清空 LastError，前端收到事件后可能隐藏错误。强化实际 StartProxy fixture 的事件接收端断言先失败；移除清空后通过，并断言显式 ClearLastError 仍能清错。未启动真实 Wails 应用。
3. **预检查排队（P2）**：`InspectCursorProxyStart` 仍使用阻塞 Lock，可在另一次 30 秒退出等待后才返回。新增隔离并发测试先失败，再改为 TryLock 返回统一 busy 错误，与 StartProxy 防重复规则一致。

主控未采纳两条建议：基线 `2fc04e7` 已明确 `Apply` 转移设置清理所有权，本轮不将原行为改成“同 URL 永不接管”；Windows/Linux 未实现可靠检测/正常退出是已有平台限制，不能通过未知状态直接写设置掩盖。该平台设置不匹配时仅退出 Cursor 再重试仍不能完成自动切换，明确保留 feature-gap，不声称全平台实现。复审原任务复查上述三个修复，未发现新的具体缺陷。

修改后主控验证（均退出 0）：

- `go test -count=1 -timeout=60s ./internal/cursor -run 'TestRestore|TestPlan'` 修复后通过。上述新缺陷的 RED 复现测试曾按预期失败，不能计为通过；对应 GREEN 结果及受影响包回归才是修复证据。
- 文档收口证据：更新 `task/todo.md` 与本节后已执行 `git diff --check`，退出 0；该检查仅证明差异格式正常，不替代源码测试或真实应用验收。
- `go test -count=1 -timeout=90s ./internal/cursor ./internal/client -run 'TestRestore|TestPlan|TestApplyPlanned|StartProxy|InspectCursor'`。
- 最后产品/测试修改后：`go test -count=1 -timeout=120s ./internal/cursor ./internal/client`（两个受影响包完整测试）、`go test -count=1 -timeout=90s ./internal/app -run 'Tray|AutoStart'`、`node frontend/scripts/test-config-projection.mjs`、`git diff --check`。
- macOS 链接器仍报告部分 object 的 14.0 与目标 11.0 版本 warning；未因此修改产品逻辑，也不把测试通过当作低版本实机兼容证据。

证据边界：本轮不执行最终全仓 test/race/build、打包/lint、真实 CLI/网络/Cursor 验收；生命周期 fixture 仍替换 CA/钥匙串、账号注入、应用控制和状态事件接收端，store 的 ApplyPlanned 有独立语义测试，但不等于真实系统设置链已验收。没有修改真实凭据/设置、停止当前 Gateway/Cursor 或部署/提交/推送。方案文档和原有改动保留，历史 blocked 工作不启动。可复用教训：设置快照不是跨实例事务，补偿必须核对所有权与当前值；RPC 错误可见不代表异步状态事件保留错误；启动锁防重需要覆盖预检查入口。交付继续为 `verified-partial`。

### 前次：main → gateway 价值迁移实施收口（completed，verified-partial）

用户在 review 后授权继续，明确“最后的验证可以不做”。本轮完成剩余实施与代码层修复，保留必要定向回归；最终全量、race、根构建/打包和真实 CLI/Cursor 验收按授权省略，不宣称 accepted 或已部署。行为合同仍为 `main_gateway_价值功能分析与迁移方案_20260907.md` §3～§4、§9～§13，任务真值为 `task/todo.md`。

- REC/CMP：修复恢复入口对 compiler/storage/usage 错误的错误归类，只有明确溢出保留非 retryable overflow terminal；此前一次恢复、摘要完整预算/完整轮次、fallback/usage/历史保留继续通过定向回归。
- FETCH：修复非公网 IPv6 放行和 6to4/NAT64 改写实际拨号目标的问题，附加嵌入 IPv4 风险判断与真实目标规范化分离；新增 TLS SNI、Host、解析结果/拨号一致性 fixture。
- CLI/MODEL：四路径与凭据隔离实现保留；补 Codex/Grok 目录 ID→凭据 resolver→fake provider 的实际 HTTP 合成链、thinking/capability/fallback/旧 ID 契约测试；现有 manager/modelchannel 已是事实源，没有为无重复规则新增生产服务或凭据缓存。
- TOOL/RESULT：贯通 Shell/shell/Bash/bash 的权限前规范化、bridge、占位、证据、历史与预算；目录只发布 Shell、磁盘旧历史不改。共享预算包 `agent/toolresult` 收敛 UTF-8 截断、bytes/items 提示和额度，保留客户端展示与模型回放差异；exec/interaction/projector 的真正重复计算移除，冻结样本和重复应用测试通过。
- CURSOR：`StartProxy`/Wails/UI 实际接入一次确认、正常退出等待、被改键快照、旧设置恢复、已有共享服务保护及部分成功展示；确认后的顺序为 Quit→Inject→Settings→Launch。退出/轮询辅助命令受 context 总时限约束，仅取消 helper；启动等待 `open` 退出结果且只尝试一次。Windows/Linux 未实现检测/正常退出时提示手动处理，不虚报未运行。自动启动遇到需确认时保留原状态、写可见 LastError，不普遍弹窗；托盘显式启动唤起主窗口同一确认流程。
- 代码层终审：Standards `37ce3f91-fc43-41bb-b3be-4b50e1931b7c`、Spec `050ba47b-b48e-40a2-92ef-18b451385821` 两轴只读评审；主控及评审发现的 helper 超时、启动误报、未知平台、重复 start 排队、回滚失败不可见、托盘确认接线和账号注入时序问题均修复，两轴复审报告无剩余具体 P0/P1。未采纳“未确认就静默启动服务”的建议，以保持已批准的拒绝/未确认状态合同。

本轮主控在对应修改后运行的关键定向命令均退出 0：

- `go test -count=1 -timeout=90s ./internal/backend/forwarder -run 'Overflow|Compaction|HandleProviderDone'`；`go test -count=1 -timeout=60s ./internal/backend/agent/bridge/interaction -run 'WebFetch'`。
- `go test -count=1 -timeout=120s ./internal/backend/agent/core ./internal/backend/agent/bridge/exec ./internal/backend/forwarder -run 'Canonical|Alias|Shell|Overflow|Compaction'`。
- `go test -count=1 -timeout=120s ./internal/backend/agent/toolresult ./internal/backend/agent/bridge/exec ./internal/backend/agent/bridge/interaction`；`go test -count=1 -timeout=90s ./internal/backend/forwarder -run 'FreezeReplay|ShellAlias|LegacyShellAlias|TrimReplay|Projected.*Replay'`。
- `go test -count=1 -timeout=120s ./internal/backend ./internal/backend/agent/model ./internal/backend/server/upstream ./internal/backend/server/config -run 'CLICatalog|CatalogCLI|SelectChannelForModelResolvesCatalog|NormalizeModelAdapterConfigsPinsManaged'`。此前一次宽名称过滤在 config 显示 `[no tests to run]`，不计该包场景通过；此命令已匹配实际 config 契约测试并通过。
- `go test -count=1 -timeout=90s ./internal/cursor ./internal/client -run 'StartProxy|InspectCursor|QuitGracefully|Launch|IdentifyDarwin|UnsupportedPlatform|Helper|Plan|Restore'`。
- 最后托盘/注入修复之后：`go test -count=1 -timeout=90s ./internal/client -run 'StartProxy|InspectCursor'`；`go test -count=1 -timeout=90s ./internal/app -run 'Tray|AutoStart'`；`node frontend/scripts/test-config-projection.mjs`；`git diff --check`，均通过。链接器有 macOS 14.0 对象与 11.0 目标的版本 warning，不作为低版本实机兼容证据。

剩余证据边界：真实 CLI 默认/指定模型请求、真实 Cursor UI/退出/重启、SOCKS-only/真实直连、最终全量/race/build/打包/lint 均未最终验收；开发中曾有局部包构建、vet 和前端生产 build，不等于最后整仓验证。未读取或改变真实凭据，未退出/重启当前 Gateway/Cursor，未部署/commit/push，也未解阻历史缺少外部条件的工作包；本轮受管测试均结束，无需恢复原应用。代码实施与文档工作完成，交付保持 `verified-partial`。

### 历史：main → gateway 价值迁移续跑（取消后暂停时记录）

行为合同为 `main_gateway_价值功能分析与迁移方案_20260907.md` §3～§4、§9～§13；活动状态以 `task/todo.md` 的 `main-gateway-value-migration-20260907` 为准。保留 `2fc04e7` 上全部未提交代码及方案文档，不 reset/stash/commit/push。

- 恢复接管初次和等待 20 秒后的重试均 503，等待 40 秒后的第 2 次重试成功。续接 ID `e12d6146-991b-40e9-892e-6174d9570cc3`。已修复前序工具标记误抑制恢复、当前空白文本/thinking、synthetic thinking 和已发布工具参数输出门禁；补合成 provider 经实际 `Service.RunSSE` 订阅入口的恢复、再次溢出唯一非 retryable terminal、延迟 Blob ACK、取消/旧事件、独立 usage 和历史保留测试。这不是真实 Cursor 或 BidiAppend 起始全链证据。
- WebFetch 复核续接 ID `d93d407e-7fdf-47e0-a57c-5463bbdb2fe4`。修复尾随点主机、IPv6 zone、6to4/NAT64 内嵌私网 IPv4 及 `3fff::/20` 检查。子任务报告当前显式 HTTP 代理抓取 `https://example.com/` 成功（207 bytes，0.29s）；不宣称真实直连、SOCKS-only 或代理最终解析地址受控。
- 主控在代码修改后独立执行 `go test -count=1 -timeout=300s ./internal/backend/agent/model ./internal/backend/forwarder ./internal/backend/agent/bridge/interaction ./internal/netproxy`、同四包 `go vet` 及 `git diff --check`，串联命令退出 0。恢复子任务另报告 model/forwarder `go test -race -count=1 -timeout=600s` 通过，主控本轮未另跑 race；根全量 test/build、打包和最终双轴 review 尚未运行。
- 后续 CLI 复核启动返回 `transport status=not_recorded`，没有 ID；等待 20 秒后的第 1 次重试返回 `Subagent was aborted by the user`。尊重用户取消而暂停后续调度，不能记录成五次 503 耗尽，也不自动绕过取消重建任务。
- 剩余 feature/test gap：Stage 1 的 `actor.go` 对 `tryAcceptOverflowRecovery` 所有错误统一使用 overflow terminal，与 REC-3 其他故障保留语义存在静态风险，需先补规划/存储失败反例再修复；真实 CLI 隔离请求链、WebFetch 最终安全/SNI 复核、Shell 别名、共享预算/模型规则、Cursor 生命周期、全量门禁和最终 review 均未收口。
- 暂停前运行检查：Gateway PID 53552 仍监听 18080/18090 且 `/healthz=ok`；Cursor 主进程 PID 32928 存在，18091/9245 未监听。本轮主控等待/测试进程均已结束，没有停止或重启既有 Gateway/Cursor，也没有部署工作区源码或启动历史 blocked 工作包。用户恢复后先续接恢复任务处理错误语义，再完成 CLI 复核和后续依赖阶段。

### 价值迁移完成情况 review（未实施修复）

本次按用户要求由主控审查当前 `2fc04e7` 上的全部改动范围并聚焦行为与验收缺口，不替代最终独立双轴评审。没有修改产品或永久测试文件，没有启动应用/实施任务；仅更新任务与过程记录。

- 现有测试：`go test -count=1 -timeout=300s ./internal/backend/agent/model ./internal/backend/forwarder ./internal/backend/agent/bridge/interaction ./internal/netproxy ./internal/backend/server/upstream ./internal/backend/server/config ./internal/backend`，随后同七包 `go vet`，串联退出 0；`git diff --check` 通过。本次未重跑全量/race/build 或真实 CLI/Cursor。
- 三个临时 overlay 合同反例均复现失败（`go test -overlay=<临时映射> -count=1 -timeout=60s -run '^TestReviewProbe' -v ./internal/backend/agent/bridge/interaction ./internal/backend/forwarder`，退出 1）：非公网 IPv6 `fec0::1`、`::2`、`4000::1` 被放行；6to4 `2002:0808:0808::1234` 的拨号候选被改为 `8.8.8.8`；注入 compiler failure 后终态错误标为 `context_overflow_after_compaction`。临时文件随命令结束清理，没有实际连接内网。
- 修复建议已登记 `task/todo.md` 的 `review-r1`～`review-r4`：补完整特殊用途 IP 判定；拆开地址等价规范化与内嵌地址安全检查，保持原拨号目标；按 typed error 保留终态类别；补目录→managed resolver→合成 provider 及目标真实 CLI 的隔离链证据。均是待实施建议，非已修复结论。
- 完成结论：Stage 0 完成；Stage 1/2/4 已有实现及部分通过证据，但上述缺陷/CLI 运行链阻止验收；Stage 3/5/6 尚未实施，最终全量验证、构建和发布仍待完成。当前 `blocked, verified-partial` 合理，不可标 accepted。

### 0.0a BYOK 预算化恢复（本轮实施）

2026-09-03 冻结并实施预算化恢复合同，覆盖旧的“HTTP 500 禁止 fallback 切换”。Cursor 只见一个逻辑模型 / 一个 RunSSE；网关内部吸收 500/503，耗尽后至多一次 terminal 且 `IsRetryable=false`。默认全链 5 attempts、每渠道 2、累计退避 8s、建连 30s、首事件 600s、流空闲 240s、整呼 7200s。`maxWaitSeconds` 只累计实际退避 sleep，退避期间暂停整呼时钟。500/502/503/504/524、可恢复 transport、TLS handshake EOF、建连/首事件超时：同渠道 2 次后可安全切换。429 遵守可容纳的 Retry-After，否则跳过等待并切换。403/其他 4xx/529/父取消/证书校验/永久 DNS/请求构建/协议解析/provider terminal 快速失败。任意 raw byte / 模型事件 / 副作用关闭窗口。删除全局 `providerStreamIdleTimeout`。单渠道计划仍注入 RecoverySettings。不实施熔断、供应商网络分流、partial-output continuation 或 OpenAI/Anthropic 混排。回滚关闭该模型 `providerFallback.enabled` 或恢复安装前 `.app`。

2026-09-03/04 已完成目标仓库产物安装与在线正常流量冒烟。`workspace-gateway-native-fix/bin/macos-arm64.dmg` 通过 `hdiutil verify`；DMG 内应用与 `/Applications/Gateway-byok.app` 均为 `0.0.56.0`、Mach-O arm64，主二进制 SHA-256 同为 `001bc8db1b85fdeec7db82a7d5f92faf739f0690d093b4532f35eed94c87baf3`，应用签名校验通过。安装实例监听 `127.0.0.1:18080` 与 `127.0.0.1:18090`，`GET /healthz` 返回 `200 ok`；最新启动日志显示 backend、MITM 和 Cursor 代理设置均成功，无配置归一化、panic 或 fatal 错误。真实 Cursor 正常请求只观察到一次 RunSSE 入站/转发，内部 model call 均成功且无 terminal provider error。

2026-09-04 已完成直接经过本机 Gateway 的五类受控故障验收。HTTP 500 与 503 均在主渠道恰好失败 2 次后切备用成功；TLS 握手阶段 accept-then-close 在客户端表现为 transport reset，主渠道 2 次后切备用成功；partial-output 只请求主渠道 1 次，发布 `PARTIAL-CANARY` 后以 `output_observed` 关闭安全门，未重放到备用；预算耗尽按主渠道 500×2、备用 503×2 消费 4 个全链 attempts，已建立 HTTP 200 SSE 后仅在流体内输出 1 个 `provider_error` terminal。关联 trace 分别为 `65dee066553eddeb1b85bfe5470aadcf`、`4a4cd69e021a2eeef8ea9ae9b2a45ed6`、`ae2bdb7a6ff108b59d54935240e213e5`、`a5da9e5289d962eebbc41e1459d67f95`、`db10b4e26be085ed9a34d10130fda209`；failure/action、逻辑模型、渠道/provider、链级与渠道级 attempt、累计等待/退避、fallback 原因、安全门和最终动作字段均存在。临时 key、Authorization/请求响应正文类字段及 canary/成功正文扫描命中均为 0。

临时在线配置已完整恢复：`config.yaml` 与原始备份 SHA-256 均为 `a324d89d9b6eaf30b387e85a51924a03f6c13bcef1e2df1cef194d3a022e9d37`，模型数回到 32，临时模型为 0，`18091` 与 `18180–18183` 均关闭；正常应用继续监听 `18080/18090`，`GET /healthz` 返回 200。当前仍为 `verified-partial`：这些是本机 Gateway 端到端请求，不是 Cursor UI/RunSSE 受控故障注入；真实 Cursor 单 Run/Agent 生命周期、输出后不重放和耗尽后至多一次 terminal 仍需产品级最终验收。

### 0.0 Cursor Subagent 只读重调度基础设施

2026-08-30 已完成默认关闭的基础切片，当前状态为 `blocked, foundation-partial`。仓库新增未来兼容的 `subagentReschedule.enabled` 配置（旧配置缺失或 UI 保存均为 false）、禁用的 Settings 占位、`attempts.json` 版本化 ledger/CAS API、仅接受 typed `stream_decode` / `stream_idle_timeout` 的 readonly/3-attempt fail-closed policy，以及 attempt 级 observability 和日志分析器投影。上述 attempt ledger、policy 和 fencing API尚未接入生产 Task 生命周期，不改变现有 subagent 派发和 durable handoff。

真实 Cursor 证据核查未找到能把 child provider typed terminal 稳定关联到 parent `subagent_run_id + attempt_id + exec_id` 的 producer/consumer 合同；现有错误结果仍主要压平为自由文本。根据已批准安全门，本轮没有按错误文本或时间窗口猜测，没有启用在线 relaunch，也没有在 backend 重启后自动重派。解阻仍需 Cursor fixture/API 在同一权威链路回显 physical attempt correlation 与 typed failure；在此之前不得宣称自动恢复已解决。定向 Go、race、20 次并发重复、vet、前端配置投影/build、日志分析器 test/race/vet 和补丁格式检查已通过；完整 backend 回归仅一次命中既有 observability 时序测试抖动，目标测试单独重复 3 次通过。

### 0.1 198 远程 CLI 使用本机 18090（方案 A）

已完成（2026-08-24）。用户确认远端 `cursor-cli` 容器用 Docker `--network host` 消费本机 `127.0.0.1:18090`。本机用公钥登录 `jandar@172.16.23.198`，LaunchAgent `com.yaogj.cursor198-18090-tunnel` 维持 `ssh -N -R 127.0.0.1:18090:127.0.0.1:18090`；未开本机远程登录，未改绑 18080/18090。验证：远端与容器内 `/healthz` 均为 `ok`；`agent models` 与本机 `CURSOR_API_ENDPOINT=http://127.0.0.1:18090` 的 21 个模型 ID 完全一致；本机 `127.0.0.1:18080`/`18090` 仍由原 Cursor 进程监听。未转发 18080。Mac 休眠后隧道会断，唤醒后 LaunchAgent KeepAlive 会重连。逐步操作、非官方 `auth.json` 会话复用、安装与代理细节见 [`docs/ops_198_cursor_cli_session_reuse.md`](ops_198_cursor_cli_session_reuse.md)。

### 0.2 198 浏览器终端（WeTTY + 宿主机 nginx HTTPS/Basic）

已完成（2026-08-24）。ttyd 交互差，已换成 [WeTTY](https://github.com/butlerx/wetty)（xterm.js）。构建上下文在仓库 `cursor-cli-docker/`，运行镜像 `cursor-cli-runtime:wetty`。容器 `--network host`，WeTTY 以 root 听 `127.0.0.1:7681`，PTY 经 `cursor-cli-shell` 降权为 `bun` 的 `/bin/sh`。宿主机 nginx 443：自签 TLS + HTTP Basic，反代 loopback 7681；隐藏 WeTTY 误发的 `ws://` CSP 并允许 `wss:`（否则 WebSocket 被拦，终端约 10 秒断开）；`X-Frame-Options` 为 `SAMEORIGIN` 且 CSP 带 `frame-ancestors 'self'`（`DENY` 会拦 WeTTY 同源 iframe `/assets/xterm_config/index.html`，刷新后抛 `SecurityError`）。验证：无凭据/错凭据 401，正确凭据 200；配置 iframe 200 且头为 `SAMEORIGIN`；经 nginx 的 socket.io 可维持 ≥20s 且出现 `$ ` 提示；`:80` 仍 200；本机 18080/18090 未改。口令与 `auth.json` 正文不入库。操作细节见同一 ops 手册 §13。

### 0.3 三层模型路由与 CLI 模型池（实现完成，证据部分闭合）

2026-08-24 已完成三层实现、自动化门禁和两路独立终审，当前 `delivery_status=verified-partial`，未提交、未推送、未发布。第一层真实 IDE metadata-only probe 证明新建 Explore/generalPurpose child 可使用不同于父会话的模型，并且 child `requested_model.model_id` 与 runtime ModelID 一致；这只证明新 spawn 的模型传播，不证明运行中 child 热切换。

第二层 Provider fallback 已支持全链 `maxHttpAttempts` 默认 5/可配 2–9、`maxWaitSeconds` 默认 8/可配 1–30，单渠道固定最多 3。共享预算覆盖 `3+2`、`1+3+1`、wait=0、超预算 `Retry-After`、HTTP 500 只同渠道重试、取消、raw bytes/model event 后零切换和兼容性跳过。配置保存改为用后端派生 ID 的完整 adapter 集合先校验、再剥 ID 序列化；逻辑 adapter 不得嵌套成为另一条 fallback 链的物理成员。UI 增加预算、逻辑路由、费用/隐私/模型语义/工具兼容提示，逻辑 alias 不再直接发送 endpoint 测试请求。

第三层新增独立 `tools/cursor-cli-model-pool` Go module，按有序物理模型各启动一次。它用 `agent models` 与 BYOK 配置精确交叉核验并拒绝 fallback-enabled alias；prompt 只走 stdin；未知错误 fail closed；任意 thinking/assistant/tool/未知事件或 Cursor worktree mutation 后禁止跨模型重放。write 模式不执行 `git worktree add`，只监视 Cursor 的真实 `~/.cursor/worktrees/<repo>/<name>` 路径；fsnotify 事件与前后 snapshot 共同形成 sticky mutation 门禁。metadata-only journal 权限为 `0600`，不保存 prompt、NDJSON 正文、工具参数或凭据。使用说明见 `tools/cursor-cli-model-pool/README.md`，198 可选部署边界见运维手册 §14。

验证已通过：根 module `go test ./...`、全量 race、vet；Provider 定向 test/race/vet；前端配置投影与生产构建；CLI module test/race/vet/build；日志分析器 test/race/vet；临时 HOME 的 CLI `validate`/`dry-run`；发布隔离、禁止路径与 `git diff --check`。独立终审首轮发现并修复三类承重问题：前端重复实现渠道 SHA-256；CLI 监视错误 worktree 路径；终审阶段逻辑 alias 可嵌套入 Provider 链、短暂 worktree mutation 可漏检以及成功 HTTP 200 被错误分类。返工后两路复审均无剩余 P0/P1。

2026-08-25 用户本机真实观测（未提交、未改 18080/18090 PID）：逻辑路由 `grok-HA`/`543fe17c50d81660` 启用 fallback，primary 为故障物理渠道 `Grokeeror`/`d5ab6805830e5baa`（`127.0.0.1`，`modelID=dd`），candidates 为 `grok-hongai`/`3d2b0ff4a6be3e42` 与 `grok`/`378723ba5535e672`，全链 5 attempts / 8s。`provider_fallback_attempt` 共 54 条、28 个 `model_call_id`：13 次 Grokeeror `transport` 后 hongai 成功（used 4/5）；13 次 hongai 随后 `server_5xx` 且 `attempt_budget_exhausted`（used 5 remain 0），第三候选未启动，符合 primary 吃满 3 后只剩 3+2。预算字段 used+remaining 恒等于 5，wait 未耗尽。CLI 池在临时 HOME（复制 `config.yaml`，Library 软链以通过 `agent login`）对用户指定的 `3d2b0ff4a6be3e42`/`45719971585b2646`/`506d30d8e14b7b5e` `validate`/`dry-run` 通过；引用 `grok-HA` 返回「禁止引用 providerFallback.enabled=true 的逻辑适配器」。仓库 cwd 的 Ask `run` 首模型成功（`system/init → user → thinking* → assistant → result/success`），因此未切到第二/第三物理模型；从 `/tmp` 启动时 Cursor Workspace Trust 产生非结构化错误，控制器记 `unknown` 并 fail-closed、不换模型。journal `0600`、无 prompt/凭据。副作用：运行中 18090 把 `lastAgentModelHash` 从 `grok-HA` 写成 `grok-hongai`；23 个 adapter 与 fallback 链字段未变，未创建真实 `cli-model-pool.yaml`。

证据缺口：本机没有 `wails3`，独立 Vite 页面因 `/wails/runtime` 404 无法挂载，因此浏览器视觉验收未通过；真实 CLI 两模型 pre-output 故障切换仍未发生（首物理模型已成功）。故本工作包标 `verified-partial`，不得宣传为 CLI 故障切换和 Wails 浏览器验收均完成。

### 0.4 配置写竞态与物理上游容量（能力验收完成，在线未启用）

已完成（2026-08-25；随本次汇总提交交付，未推送、未发布）。普通 UI 保存现在会在 Store 锁内读取最新 YAML，以 UI payload 更新用户字段并保留最新 `lastAgentModelHash`；运行时 hash 更新只 patch 该字段，相同值不写盘、不通知 listener、不触发 Host rebuild；完整导入继续通过显式 replace 全量替换并允许替换 hash。Manager 写事务锁串行提交磁盘、current 和 snapshot，写盘失败不推进内存。确定性交错、并发 race、失败回滚、hash no-op 和 import replace 均有自动化覆盖。

物理 adapter 新增可选 `maxConcurrentRequests`：缺失/0 保持无限并发，非零允许 1–16；逻辑 fallback alias 必须为 0，同 provider、规范化 Base URL 和 API Key 的物理渠道必须配置一致。resolver 将限制和只在内存存在的 SHA-256 上游组身份投影到真实请求；进程级 limiter 固定最多等待 2 秒，槽覆盖完整 Stream 及同渠道 retry。容量超时为 typed `capacity_unavailable`，不消耗 HTTP attempt 或 fallback retry/backoff wait，只在零 HTTP、零原始字节、零 model event、零副作用窗口切到不同上游组；同组候选跳过，父 context 取消不 fallback，release 不泄漏。API Key、Base URL 和组 hash 不进入日志、事件或错误。

主控集成复查发现并修复了一个跨切片 P1：resolver 虽已产生容量值，`applyChannelToRequest` 原先没有用 `ResolvedChannel` 覆盖 `StreamRequest`，导致真实配置无法生效而仅测试手工请求字段有效。修复后补了请求投影、真实 HTTP 峰值、Manager legacy snapshot 和上游组规范化回归；独立复审确认无剩余 P0/P1。

验证通过：容量与配置定向 test；根 module `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`；前端投影测试和生产 build；CLI 独立 module test/race/vet/build；日志分析器 test/race/vet；`git diff --check` 与 proto/MITM/certs 保护路径检查。Apple Silicon / macOS 14.6.1 使用隔离临时工具链运行 `task build`，生成版本 `0.0.49.5`、约 23 MiB 的 `bin/macos-arm64.dmg`（SHA-256 `8ad056b49386841ba2cd1a8a3cff7ae3489948f38e9be47373da693090d93841`）；`hdiutil verify`、只读挂载、Info.plist/buildinfo 版本、Mach-O arm64 与 adhoc codesign 校验通过。DMG 受 `bin/` ignore 规则保护，不纳入 Git，且未做 Developer ID 签名或 notarization。详细合同见工作决策基线 §10.9、系统 Design §14.10，详细任务证据见 `task/todo.md` 的 `config-race-upstream-capacity-20260825`。

用户明确选择不修改当前真实 `grok-HA` 配置，本次也未读取后改写该文件、未占用或重启 18080/18090。因此可声明“容量能力已实现并经非零 fixture 验证”，当前在线 `grok-HA` 仍为无限并发，不能声明在线容量风险已启用关闭。

### 0.5 `v0.0.49.6` 发布

本次发布版本使用 `0.0.49.6`。版本事实源 `build/config.yml`、Wails macOS/Windows/Linux 构建元数据、`release-notes.md` 与归档 `releaselog/0.0.49.6.md` 已按该版本对齐；发布说明包含 Provider fallback 覆盖优先预算分配、五物理渠道配置与测试交互修正，以及此前已验证的多层模型路由、配置写事务和可选物理上游容量能力。2026-08-25 用户最新要求已取代此前“仅生成本地资产”的安排：`cli` 变更合入 `noad` 后，README 的“当前稳定发布”更新到 `v0.0.49.6`，发布资产上传到 `yaogjim/cursor-byok` GitHub Release，标签必须指向 `noad` 的发布提交。

### 0.6 Fallback 配置交互修正（自动化验收完成，视觉待补）

2026-08-25 已完成本轮配置交互调整，并纳入本次提交及 `0.0.49.6` 本地发布候选。“测试全部”继续只调度物理 adapter，但现在静默跳过逻辑 alias，不再弹出“已保存/需实际运行验证”的误导提示；单独点击逻辑 alias 测试仍保留运行验证提示。通用 Select 的视口可用高度现在施加到真实 `ul` 滚动容器，并保留键盘聚焦时的就近滚动，修复长列表只能用方向键看到后续 adapter 的问题。

Fallback 链上限从 1 primary + 2 candidates 扩展为 1 primary + 4 candidates，总共最多 5 个物理渠道。前端候选槽改为数据驱动的连续 4 槽，后一槽只在前一槽已选时显示，清空中间槽会截断后续槽；每个下拉继续排除 primary、逻辑 alias 和其他槽已选渠道。前后端共享上限校验，第 5 candidate 被拒绝；resolver 保持原循环并经测试证明五通道顺序完整投影。全链预算合同未改变：默认 5 attempts / 8 seconds，范围 2–9 / 1–30，单渠道最多 3，渠道增多不保证链尾一定获得 HTTP attempt。

验证通过：`node frontend/scripts/test-config-projection.mjs`；后端 config/client/model 定向测试；`go test ./internal/... -count=1`；`npm run build --prefix frontend`；受改文件 lint、gofmt 与 `git diff --check`。测试/build 只有既有 macOS deployment target、`--localstorage-file` 和 chunk-size warning。构建生成的 i18n 文件已恢复，不纳入变更。本机仍缺少 `wails3` 和 `task`，没有可用开发 UI 端口；为避免扰动运行中 Cursor 的 18080/18090，未做真实窗口视觉点击，因此当前状态为 `verified-partial`，缺口仅为 UI 视觉交互证据。

### 0.7 Multi-Client Chat Gateway 阶段 0/1（实现完成，证据部分闭合）

2026-08-25/26 已按已批准计划交付最小纵向切片，`delivery_status=verified-partial`，未提交、未推送。合同见工作决策基线 §10.10、系统 Design §14.11 和 `task/todo.md` 的 `multi-client-chat-gateway-phase0-1-20260825`。独立 `internal/gateway` 监听默认 `127.0.0.1:18091`，默认关闭、loopback + Bearer；复用现有 Provider Gateway/Router；token 不进普通投影/导出/localStorage/日志；默认导出剥离 token 后再导入会 overlay 现有 token。自动门禁（定向五包、`internal/...`、相关 race、`go vet ./internal/...`、前端投影/build、`git diff --check`）通过；`proto/`、MITM、certs 无 diff。真实 Cherry/OpenAI SDK、Wails 视觉验收和 Cursor 全量回归仍是证据缺口，不得标 accepted。本包不扰动运行中 18080/18090。

- **阶段 1 补充验证（2026-08-26）**：修复 OpenAI Chat 纯文本 `content` 数组兼容性，补充流式首块 `assistant` 角色，并严格拒绝非 `null` 的空 `tools` 数组。新增真实 TCP listener smoke 在隔离 `127.0.0.1:18091` 上依次验证 `/v1/models`、非流式 Chat 和流式 Chat；Provider 使用内存 fixture，未连接真实上游。`18080`、`18090` 未被启动、停止或替换。

### 0.8 双集成导航与数据概览（实现已落地，Wails 视觉未做）

2026-08-26 已按已批准计划 `.cursor/plans/双集成导航与数据概览改造计划_2622a26e.plan.md` 把主窗口改成五页同层导航，`delivery_status=verified-partial`。合同见工作决策基线 §7.4/§10.13、系统 Design §4.1/§10.1/§14.15，任务证据见 `task/todo.md` 的 `dual-nav-overview-20260826`。

- 页面：数据概览 `/`、Cursor 集成 `/cursor`、网关集成 `/gateway`、上游模型 `/models`、系统设置 `/settings`。侧栏短名为「概览 / Cursor / 网关 / 模型 / 设置」。Cursor 与 Gateway 同层，不是父子模块。旧 `/config`、`/model-config` 分别重定向到设置和模型页。主窗口默认 `1100×720`，最小 `980×640`。
- 保存：`SaveGatewayConfig` 只合并启用状态、监听地址和公开模型映射，磁盘 token 保留；Cursor / 模型 / 系统设置各有独立 section 入口。启停只使用已保存配置；Gateway dirty 时提示先保存本页。
- 运行隔离：Gateway 仍只操作 `127.0.0.1:18091`，不启动 MITM，不改变 18080/18090。复制 token 区分尚未生成、WebView 拒绝和成功。
- 数据概览：`GetHomeMetricsReport` 从 `usage.json` 的 `daily[]` 按 `7d` / `30d` / `all` 过滤并聚合 KPI；时区固定 UTC。首期没有近 1 小时、今日、自定义范围，也没有小时热力图；不用 `recent_events` 冒充完整日历。
- 本轮文档收口只修改决策基线、系统 Design、`task/todo.md` 和本文，未重跑 Go/前端命令，未做 Wails 窗口视觉点击。不得声称视觉验收已完成，不得标 accepted。

### 0.9 v5 四页控制面（自动化验证通过，视觉与真实授权部分待补）

2026-08-26 已按已批准 v5 计划完成四页控制面实现，当前 `delivery_status=verified-partial`。合同见工作决策基线 §7.5/§10.14、系统 Design §4.2/§10.2/§14.16，详细任务与命令证据见 `task/todo.md` 的 `ui-v5-shell-20260826`。

- IA：总览 `/`、接入 `/access?client=gateway|cursor|codex|claude`、模型 `/models`、设置 `/settings`。旧 `/cursor`、`/gateway` 重定向到对应 client；接入页按 master-detail 展示真实 Cursor/Gateway，Codex/Claude 为生产 unsupported 空态。
- 保存：继续 per-scope；接入标签脏点是 Cursor/Gateway 脏状态并集；路由切换、顶层离开和兼容路径均按 scope 检查未保存修改。模型页和设置页保留独立保存、导入导出与错误状态。
- 主题：枚举为 `light` / `dark` / `system`，默认缺失或未知值回退 `light`；`system` 持久化并在运行时跟随操作系统主题变化。
- 统计：`24h` 消费 `usage.json` 持久小时桶，`30d/all` 消费日桶；报告返回真实空态，不从 `recent_events` 或 fixture 补造活动。小时 schema、迁移、保留、并发写入和报告测试已覆盖。
- 运行控制：Cursor 启动调用真实 `LaunchCursor`，应用未发现或启动失败会返回明确错误；未批准有界 `RestartProxy`，页面不使用 stop/start 模拟重启。
- 模型与设置：模型页已完成真实计数、筛选、搜索、列表/栅格、排序、编辑/测试/复制/删除、批量取消和完整配置导入导出；设置页已完成基本设置、会话与日志、网络与请求、数据与恢复四段面板，计划中控件保持禁用且无副作用。

自动化验证已通过：`node frontend/scripts/test-config-projection.mjs`、前端 Node 路由/指标测试、`npm run build --prefix frontend`（121 modules）、`go test ./... -count=1`、`go vet ./...`、`ReadLints` 和 `git diff --check`。构建仍只有既有 Node `--localstorage-file` 与 chunk-size warning，Go 测试仍只有既有 macOS deployment target linker warning。

证据缺口：本机没有可用 Wails runtime/`wails3`，因此四页真实窗口点击、980×640 窄窗口、系统主题 OS 变化和拖拽区视觉验收未执行；Codex/Claude 真实授权、续期、配置同步和 ACP 端到端验收分别等待独立后端/真实客户端 Design Gate。故当前状态保持 `verified-partial`，不得标为 accepted。

### 0.11 中断恢复与错误传播治理（实现完成，真实故障注入部分待补）

2026-08-28 已完成中断恢复工作包实现，`delivery_status=verified-partial`。HTTP 524 仅在零输出、零工具进度窗口进入精确 retry/fallback；RunSSE terminal 保留脱敏 HTTP 状态、错误分类、Request ID 和真实 retry decision。安全 automatic-continuation 已接入 RunSSE/Bidi actor，默认关闭、每 turn 最多一次，只允许已有文本/思考且无工具、副作用、checkpoint 或取消的场景，并创建新的 `model_call_id`、独立 usage/trace、重叠剥离和无进展熔断。

流诊断补齐 header/首末字节/body 结束、close cause、partial boundary 和 transport outcome；observability rotation 只由存储设置触发，退出先 drain、超时再取消并记录原因和结果；SCM/FSSync/checkpoint/client TLS 预期噪声降级，真实 provider/5xx/timeout/upstream TLS 失败保持 ERROR；fallback `_fbN` 不再污染业务 `model_call_id`，成功解码 Bidi 不再误标 `decode_error`。相关 package 定向 test/vet、日志分析器测试、`git diff --check` 与根模块构建通过。真实 Cursor 多段 continuation 和真实 TCP RST/TLS 故障注入未执行，故不得标 accepted。

### 0.12 Provider 流截断根治（第一阶段完成，Transport A/B 与真实注入待补）

2026-08-30 已完成共享 Provider 流读取路径的第一阶段治理，`delivery_status=verified-partial`，未提交、未推送、未发布。故障证据表明 OpenAI/Anthropic 都可能在 HTTP 200 和正常输出后以 `unexpected_eof` 结束，优先共同故障面是 Gateway→Provider 的共享 HTTP Transport 与公共流读取路径，而不是 Cursor 客户端解码器或上下文硬上限。

- 观测：最终 HTTP attempt 记录协议版本、Content-Encoding/Go 自动解压、Content-Length、连接复用/idle、原始字节数、首末字节、底层 typed error、最后 SSE 事件类型、response ID 短哈希、sequence/status 和累计零字节恢复次数；这些字段投影到 provider/turn terminal，不记录正文、工具参数或 Authorization。
- 协议：OpenAI Responses 支持 CRLF、空行事件边界、多行 `data:` 与最后事件后直接 EOF；`[DONE]`/`response.completed`、Anthropic `message_stop` 为成功终态，failed/cancelled/incomplete/显式 error 为独立 Provider 协议终态；未见合法终态的 EOF 保持 truncated，未知类终态事件不会被猜成成功。
- 恢复：只有零 provider 字节、零 `ModelEvent` 且属于 typed EOF/unexpected EOF/TCP reset/HTTP/2 reset/GOAWAY 白名单时，才在同渠道最多新增一次请求并受现有 attempt/wait 预算约束。读到任意字节或事件后禁止原请求重放；4xx、认证/配额、语法错误、取消、deadline 和明确协议终态不重试。
- continuation：既有 checkpoint continuation 的开关、单次预算、工具/副作用/checkpoint/pending interaction/subagent/Gateway 路径门禁不变；新增非瞬时截断错误抑制，避免显式 Provider 终态进入纯文本续写。本阶段没有扩大部分输出自动恢复。
- Transport A/B：OpenAI/Anthropic Provider 客户端新增环境变量 `CURSOR_BYOK_PROVIDER_TRANSPORT_PROFILE`，允许互斥的 `auto`（默认）、`http1`、`no_compression`、`fresh_connection`、`direct`。未知值或组合值 fail-closed 回到 `auto`；`direct` 只绕过显式 HTTP/SOCKS proxy，不能绕过操作系统 TUN。HTTP/1.1 的真实 TLS 协商已有自动测试，但没有真实截断率数据。
- 验证：无缓存 netproxy/model/forwarder 定向回归、`go test -count=1 ./internal/netproxy ./internal/backend/...`、`go vet ./internal/...` 与 `git diff --check` 通过。确定性 fixture 覆盖 completed-at-EOF、failed/cancelled/incomplete、CRLF/多行 data、缺失终态、plain/unexpected EOF、TCP reset、HTTP/2 reset/GOAWAY、一次上限、raw bytes/model event/取消/4xx 抑制、最终 attempt diagnostics 重置/投影及 HTTP/1 实际协商。额外完整 `go test -count=1 ./internal/...` 的受影响包均通过，但命令最终被两个外部 CLI smoke 阻断：Codex 临时插件 clone 目录清理竞态，以及本机 OpenCode 数据库缺少 `name` 列；没有为通过测试而修改产品逻辑。

仍待完成：按 `auto`、`no_compression`、`fresh_connection`、`http1`、`direct` 顺序做真实单变量网络采样；Cursor→Gateway 断连与 Provider 截断边界注入；full/provider 日志采样后降回 basic。当前不得宣称真实网络截断率已下降或默认 Transport 策略已经确定。

### 0.10 模型页保存身份变更（自动化复现与修复通过，视觉未点）

2026-08-28 修复模型管理页编辑后保存失败。用户截图两类症状：`模型适配器 providerFallback.primaryChannelID 引用了不存在的渠道 "dce4005402c60e65"`，以及切换 OpenAI/Anthropic 后 `模型 1 的模型标识不能为空`。任务证据见 `task/todo.md` 的 `model-save-identity-remap-20260828`。

- 根因 1：渠道 ID 由身份字段派生；前端原先剥 ID 后再保存，后端重算 ID，fallback 仍引用旧 ID。`SaveModelAdapters` / `SaveUserConfig` 现在使用请求携带的旧 ID 与磁盘 adapter 精确 remap；新 adapter 的 ID 为空，等数量删除加新增不会被误配为身份变更。
- 根因 2：HEAD 类型切换把 `draft.modelID` 置空。现在保留当前标识，只补缺省 endpoint / thinking effort。
- 真悬空渠道引用、空模型标识仍拒绝。未改 proto、MITM、证书、18080/18090。
- 本轮未做 Wails 窗口点击，不得标 accepted。

### 0.12 Codex/Claude 接入详情与模型管理 UI（自动化通过，Wails 视觉未点）

2026-08-28 已按用户确认的三张截图区域应用到真实前端，`delivery_status=verified-partial`。范围仅限 Codex 接入详情、Claude Code 接入详情和模型管理主体；未改主导航、总览、Gateway、Cursor、设置。Codex 使用 OpenAI 品牌图标，Claude Code 使用 Anthropic 品牌图标与品牌橙色。因无真实订阅授权 API，接入详情展示 0 账号空态「暂无授权账号」，授权/同步/测试/保存按钮全部 disabled 且无 click handler，不写入截图演示账号。模型页保留真实模型数据，并应用提供商筛选、彩色 chip、无图标的导入/导出、清除全部确认和精简操作菜单。浅色仍为新配置默认主题。

验证：`npm run build --prefix frontend`；`npm run test:config-projection --prefix frontend`；`node --test frontend/src/router/access.test.js`；相关文件 `git diff --check`。配置投影测试已对齐禁用「添加授权」空态和完整提供商 Tab。本机没有可用 Wails runtime，未做真实窗口视觉点击，故不得标 accepted。

### 0.13 本地编译 macOS v0.0.50.3

2026-08-28 按用户要求将版本号升到 `0.0.50.3` 并编译本机 macOS 包。`build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.50.3.md` 已对齐。未发布 GitHub Release，未改 README 当前稳定发布（仍为已发布的 v0.0.50.1）。

验证：`PATH=/Users/yaogj/go/bin:$PATH task build` 生成 `bin/macos-arm64.dmg`（约 22 MiB）。挂载后 Info.plist 版本为 `0.0.50.3`，可执行文件为 Mach-O arm64 且包含注入版本 `0.0.50.3`，adhoc codesign，`hdiutil verify` VALID，SHA-256 `ecf330bd61fbcac3133028dfb518df56722d57f6d075c573f22608889ba89ef9`。未做 Developer ID 签名或 notarization，未构建 Intel 包。

### 0.14 Cursor 设置清理所有权隔离

2026-08-28 已修复 `internal/client` 生命周期测试误删真实 Cursor 代理设置并诱发 `ERROR_NOT_LOGGED_IN` 的问题。`ProxyService` 现在只有在成功执行 `ApplyCursorSettings` 后才获得清理资格；设置写入会用实例 owner 标记和跨进程文件锁转移所有权，旧实例或未 Apply 的测试实例不能删除新实例设置，macOS `NODE_EXTRA_CA_CERTS` 清理也在同一所有权校验内执行。生命周期 fixture 已改用临时 Cursor 配置路径，并覆盖未 Apply 退出保留设置、旧 owner 不清理新 owner 和所有权存储行为。

验证通过：`go test ./internal/cursor -count=1`；`GOMAXPROCS=2 go test ./internal/client -count=1 -timeout 180s`。完整 client 测试前后真实 `~/Library/Application Support/Cursor/User/settings.json` 的 SHA-256 一致；未启动、停止或替换运行中的 18080/18090。

### 0.15 本地编译 macOS v0.0.52.0

2026-08-29 按用户要求将版本号升到 `0.0.52.0` 并编译 Apple Silicon macOS 包。`build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.0.md` 已对齐；构建资产生成后恢复并保留工作树原有的 macOS 最低版本、Linux GTK/WebKit 和 Windows 安装范围改动。未发布 GitHub Release，未更新 README 当前稳定发布。

首次使用 Go 1.26.3 构建时，大型生成文件 `aiserver_v1.connect.go` 的编译进程被系统终止；切换项目现有 Go 1.25.0 后定位到剩余磁盘空间不足。清理失败构建临时目录和 Go build cache 后，以 `GOMAXPROCS=2 GOFLAGS=-p=1 task build` 成功生成 `bin/macos-arm64.dmg`（24,000,981 bytes）。挂载验证 Info.plist 与二进制注入版本均为 `0.0.52.0`，可执行文件为 Mach-O arm64，adhoc codesign 有效；`hdiutil verify` VALID；SHA-256 `0adcb0b3a32c5d581169642c1b530b819380f200f027d15017db4d8f8bfcf365`。未做 Developer ID 签名或 notarization，未构建 Intel 包。

### 0.16 Codex/Grok 授权接入页拆分

2026-08-29 根据用户启动 `0.0.52.0` 后的截图反馈，移除接入中心底部混合展示的全局“上游订阅认证”区域。Codex 的 `auth.json` 导入、设备码授权、状态、用量和清除副本全部进入 Codex 接入详情；Grok 的设备码授权、账号列表、激活、删除和用量进入独立 Grok 接入详情。左侧顺序固定为共享入口、Cursor、Codex、Grok、Anthropic；Anthropic 保留计划中占位，旧 `client=claude` 查询兼容归一化到 `anthropic`。

本轮只调整前端信息架构、提示、品牌样式、语言目录和路由门禁，不修改认证协议、凭据存储或模型请求链路。验证通过：`npm run build --prefix frontend`、`node frontend/scripts/test-config-projection.mjs`、`node --test frontend/src/router/access.test.js`（5/5）。构建只有既有 chunk-size warning；未重新打包 macOS DMG，未做真实 Wails 窗口点击，因此状态为 `verified-partial`。

### 0.17 Codex 多账户管理与安全轮换

2026-08-29 按用户确认策略实现 Codex 多账户池，同时保持模型配置只选择 `credentialSource=codex`、不绑定具体账户。`codex-auth.json` 从旧单账户结构兼容迁移为版本化 `accounts[]`；第一个账户自动激活，后续新增账户作为备用，重复导入同一账户更新凭据但保留激活、套餐和用量状态。Codex 接入页改为真实账户列表，支持激活、逐账户刷新双窗口用量、删除和清除全部副本。

运行时 401 定向刷新原请求账户；刷新确认失效后只将该账户标记为需要重新授权，再选择备用账户。明确 quota 错误只在零模型输出、非模型测试且共享 retry budget 可用时幂等标记失败账户、切换下一可用账户并重试一次；同一旧账户的并发重复信号不会把 active 从 B 继续推进到 C。已知额度重置时间到达后账户恢复候选资格。固定 Codex 上游、`ChatGPT-Account-Id`、请求体白名单和 token 不写配置的边界保持不变。

验证通过：`go test ./internal/subscriptionauth -count=1`、`go test ./internal/backend/agent/model -count=1`、`go test ./internal/backend/server/config -count=1`、`node frontend/scripts/test-config-projection.mjs` 和 `npm run build --prefix frontend`。已重新生成并核验包含本功能的 Apple Silicon macOS `0.0.52.2` DMG：应用版本 `0.0.52.2`、Mach-O arm64、最低 macOS `10.15.0`、codesign 有效、`hdiutil verify` VALID；产物 23,070,403 bytes，SHA-256 `467bb52b1a04ef421f7df6ae1cf09e2e15cf5cf8346e594a6f38336c956d7f7d`。未使用真实订阅 token 请求外部 Codex，未做 Wails 窗口视觉点击，因此交付保持 `verified-partial`。
### 0.18 sub2api 账号选择导入与用量刷新桥接

2026-08-29 根据 Codex 接入页运行截图修复逐账户用量刷新绑定缺失，并为 Codex/Grok 接入页增加 sub2api JSON 导入。导入流程先在后端按当前页面 provider 过滤：Codex 接受 `openai/codex + oauth`，Grok 接受 `grok/xai/x.ai + oauth`，同时要求 access token 与 refresh token；随后前端弹出候选账号列表供多选，只导入用户选择且属于当前页面类型的账号。源文件保持只读，凭据只进入应用私有账号池，重复账号更新原记录且后续导入不抢占当前激活账号。

验证通过：订阅认证 Go 测试（包含用户提供 sub2api 文件的只读解析、provider 过滤、选择导入和源文件不变）、Wails bindings 生成、前端配置投影和生产构建。Wails 生成结果确认公开了 `RefreshSubscriptionAccountUsage`、`PreviewSub2APIImport` 与 `ImportSub2APIAccounts`；桥接包完整测试因大型生成文件编译超过两分钟后终止。未使用真实外部订阅请求，未做 Wails 窗口点击，因此状态为 `verified-partial`。

### 0.19 本地编译 macOS v0.0.52.3

2026-08-29 按用户要求将版本号升到 `0.0.52.3` 并编译本机 macOS 包。`build/config.yml`、darwin Info.plist/Info.dev.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.3.md` 已对齐。未发布 GitHub Release，未改 README 当前稳定发布。

使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成 `task build`，产物为 `bin/release/0.0.52.3/cursor-byok-0.0.52.3-macos-arm64.dmg`（24,019,575 bytes）。挂载后 Info.plist 版本为 `0.0.52.3`，可执行文件为 Mach-O arm64 且最低 macOS `10.15.0`，codesign 校验有效，`hdiutil verify` VALID，SHA-256 `1b63be249e0bafb6955d2deedf1e29d81d17309123b4597d489ddbcd9c11f635`。本版本包含 Codex 多账户池、sub2api JSON 多选导入和逐账户用量刷新桥接修复。未做 Developer ID 签名或 notarization，未构建 Intel 包。
### 0.20 订阅账号列表滚动与 sub2api 弹窗关闭修复

2026-08-29 根据 `0.0.52.3` 运行截图修复两个前端问题：Codex/Grok 账号列表在固定高度授权卡片中改为独立纵向滚动区，标题和底部操作栏保持可见；sub2api 确认导入成功后直接重置并关闭选择弹窗，不再调用会被 `busy=true` 拦截的手动关闭函数。验证结果以 `task/todo.md` 的 `subscription-account-list-modal-fix-20260829` 为准；未重新打包 macOS DMG。

### 0.21 Gateway 可用性测试与 managed Codex 请求体边界

2026-08-29 本轮在上一轮 0.0.52.3 功能基础上补齐两处改动，随 0.0.52.4 提交。

Gateway 接入卡片新增「测试可用性」：启动 Gateway 后自动对本机入口执行一次真实 HTTP 探测（`GET /v1/models`，带 Bearer token），返回监听地址、公开模型数量与毫秒延迟；卡片状态区实时显示入口可用性与极简使用引导，token 复制/轮换改走 Wails 原生剪贴板 `Clipboard.SetText` 并等待写入结果，失败给出可读错误。桥接层新增 `TestGateway`、`RefreshSubscriptionAccountUsage`、`PreviewSub2APIImport` 与 `ImportSub2APIAccounts` 绑定。

managed Codex ChatGPT Responses 请求体白名单不再透传 `previous_response_id`：该 ID 与签发它的 ChatGPT 账号绑定，托管场景无法验证归属，按 fail-closed 剥离；static OpenAI Responses 保持原请求体不变。回归覆盖 managed 剥离与 static 保留两条路径。订阅模型测试改为可注入的 stream 函数，验证 Codex/Grok 固定上游归一化后凭据元数据正确注入且不写回原 adapter。

验证结果以 `task/todo.md` 的 `macos-build-0.0.52.4-20260829` 为准。使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成 `task build`，产物归档为 `bin/release/0.0.52.4/cursor-byok-0.0.52.4-macos-arm64.dmg`（24,015,566 bytes）；应用短版本与 bundle 版本均为 `0.0.52.4`，最低 macOS `10.15.0`，Mach-O arm64，codesign 校验有效，`hdiutil verify` VALID，SHA-256 `ba3321f551a74127dd760a9a40703e22492a5c6a93b5887eaf64ac41185c6a62`，`SHA256SUMS` 校验通过。已提交（`9c4db4f`）并 push 到 `gateway` 分支。未做 Developer ID 签名或 notarization，未构建 Intel 包，未发布 GitHub Release。

### 0.22 上游 `v0.1.5` 深度审阅与同步安全中止

2026-08-30 已完成上游正式版 `v0.1.5@b807608`、最新主线 `upstream/main@9120b90` 与当前 `gateway@534ffc0` 的三方能力核对。正式版后的主线只删除废弃 `server_backup/**`。本地 `main@305b108` 与上游已分叉，隔离 worktree 的显式 merge 出现跨架构冲突，已按 Runbook 中止；本地 `main`、`gateway` 和工作树保持不变，无提交、无 push。

移植顺序已经收口：P0 先修复 Rule 文件权限、symlink/路径、原子写和容量上限；P1 独立实现账号隔离的 managed Codex 缓存亲和，同时继续 fail-closed 删除 `previous_response_id`；P1 再设计显式 opt-in 的 Rule 离线 journal/镜像；P1/P2 只增加官方、静态 BYOK、Codex 与 Grok 的展示分组。官方/BYOK 切换、双向子代理、普通 OpenAI/Anthropic cache、Provider 空闲超时、Codex/Grok 主链路、额度与轮换无需重写；Deno 插件框架、Rust provider/router 和完整 conversation runtime 不整体移植。上游性能、内存和缓存命中幅度缺少可复现 benchmark，继续标为未验证。详细证据和最小移植单元见 `docs/prd_cursor_byok_当前功能与上游差异.md` §16。

### 0.23 Provider 历史净化（已完成并验证）

2026-08-31 已完成目标感知来源身份的 Provider 历史净化，`delivery_status=verified`，未提交、未推送。发送前按目标来源身份判断 opaque 兼容性；跨身份剥离 opaque reasoning 与 Responses item 元数据，保留文本和工具链。HTTP 400 脱敏摘要进入诊断；最终 JSON 编码请求体预检 32MiB 上限，`request_build` 不重试、不 fallback。复审发现并修复 provider done origin 清零导致落盘丢失、tool flush 顺序和 safety suppression。验证：`/opt/homebrew/bin/go test -count=1 ./internal/backend/agent/model ./internal/backend/agent/prompt ./internal/backend/forwarder`、同范围 `go vet`、`git diff --check` 通过。任务证据见 `task/todo.md` 的 `provider-history-sanitize-20260831`。


已完成（2026-08-23，未提交、未发布）。治理实现位于隔离分支/worktree `agent-governance-0.0.49.2` / `cursor-byok-governance-0.0.49.2`，基线仍为 `v0.0.49.2` 发布提交 `487856170b29380671477e843d7fec15250323ae`；当前主工作树的无关 WIP 与两个 recorder/exporter 专用 stash 均保持隔离。

本治理包已接通五项能力：按 `model_call_id` 聚合内部 reasoning replay；metadata-only `execution_evidence` 账本；mutation 后 verification stale 判定；证据不足时最多一次提醒续跑的完成门禁；agent/subagent prompt、`turn_completed` 和 debug recorder 的白名单诊断。账本只接受结构化 ToolCall 与终态 result，成功 mutation/verification 使用最终持久 sequence，tool result 与 evidence 同一追加批次写入；重复 result、跨 turn、restart、subagent recovery、unknown MCP、pending/failed/canceled 均按保守规则处理。完成门禁从最新持久 canonical history 重建，不依赖可能滞后的 live 索引；纯问答与 Ask/Plan 不受编辑门禁影响。

三路独立终审均已闭合。replay 方向修复了两个 P1：orphan reasoning 不再跨 `model_call_id` rehome；provider signature 与 exact reasoning content 绑定，不再形成“新正文 + 旧签名”。此前阶段已关闭弱 tuple 丢失、AwaitShell 无 exit、脚本前缀假阳性、编辑+解释绕过、中文建议问句误触发、公共 transcript 合法 tool path 误报和诊断投影 fail-open。最终复核未发现新 P0/P1；账本/门禁和 prompt/诊断/隐私方向无可复现 P0/P1/P2。

最终新增集成回归证明：completion gate 的结构化 prompt reminder、`execution_evidence` 与 `completion_gate` metadata 会保留在 canonical history，但不会进入公共 transcript；用户可见 assistant 文本和合法结构化 `tool_use.path` 仍被保留，reasoning 与内部诊断继续零投影。最终实跑通过公共 transcript 专项、forwarder 全包/race/vet、根模块 `go test -p 1 ./... -count=1 -timeout 900s`、`go vet -p 1 ./...`、gofmt 和 `git diff --check`。Stage 6 已通过的 `internal/...`、根客户端构建、prompt、前端配置投影/生产构建及两个独立 Go module 的 test/race/vet 证据继续有效。

环境记录：初次根全量并发链接曾因磁盘 `ENOSPC` 失败，串行 `-p 1` 后通过；本轮根测试首次因隔离 worktree 缺少真实 `frontend/dist` 在 `go:embed` setup 阶段停止，随后只读复用主工作树 `node_modules`/bindings 生成隔离临时 dist，根测试通过。临时 dist、依赖 symlink、bindings symlink、`gen` symlink 和验证进程均已清理。公开 proto、MITM/证书、发布资产、标签和两个专用 stash 无漂移。

保守兼容边界：裸 `modeladapter.Message` 二次 normalize 缺少 model-call 身份时不做 orphan rehome；旧 history 连续工具批次双方身份都为空时继续保留旧合并行为。这两项只用于避免旧数据破坏，不宣传为新 history 的身份隔离能力。真实 Cursor success/error/background/resume 六场景 fixture 仍待独立补证；本治理包不证明 child 从中断执行点自动续跑。

指定 transcript 最终复盘结论：原会话中因服务中断停止的 Stage 4 已在用户切换 LLM 服务后恢复并闭合；Stage 6 四条 `Superseded by newer request` 只是同一根测试命令被后续请求替代，后续串行全量测试已闭合，并无可直接 resume 的独立遗留任务。原始建议中唯一未逐字覆盖且可在当前合同内补齐的是显式 15K reasoning canary；现已把三工具共享 reasoning 回归升级为 15 KiB start/end canary，并通过专项、forwarder 全包/race/vet 和 diff 检查。更强的“assistant 文件声明逐项对照 Git diff”和“reasoning 超阈值时中途打断”不属于已批准 metadata-only 完成门禁语义，若要实施需另行设计；recorder/exporter stash 与真实 Cursor 六场景仍是独立工作包，未混入本治理包。

阶段状态、命令和缺口以 [`task/todo.md`](../task/todo.md) 的 `agent-governance-completion-20260822` 为准；治理包当前 `DELIVERY_STATUS=accepted`，但尚未 commit、push、tag 或发布，后续发布必须等待单独批准。

### 0.24 本地编译 macOS v0.0.60.0

2026-09-07 完成 `v0.0.60.0` Apple Silicon macOS 本地构建。版本号统一为 `0.0.60.0`（纠正提交 `f6ce0b0` 中 `0.060.0` 的写法，与历史 `0.0.x.y` 四段格式一致），`releaselog/0.060.0.md` 重命名为 `0.0.60.0.md`。使用 Go 1.26.1、Task 3.53.1、`PATH=~/go/bin:$PATH task build` 生成 `bin/macos-arm64.dmg`（24,225,414 bytes），并归档为 `bin/release/0.0.60.0/cursor-byok-0.0.60.0-macos-arm64.dmg`。挂载核验：Info.plist 短版本与 bundle 版本均为 `0.0.60.0`，二进制注入版本 `0.0.60.0`，Mach-O 64-bit arm64，adhoc codesign 有效；`hdiutil verify` VALID；SHA-256 `7e0d86254c84e9f8211337e5feb96866e0343a083b751c3e6fa083e8325d641f`，`SHA256SUMS` 复核通过。构建同时再生成 i18n catalog 行号引用（97 处 line 值刷新，无文案变化）。未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布；版本号修正与构建证据未提交，待用户确认。

### 1. 版本发布与交付

#### `v0.0.49.2` transcript 热修复

已完成（2026-08-22）。修复公开 Agent transcript 泄漏并重复输出 reasoning/thinking 的问题；内部 provider replay/signature 数据保持不变。发布提交为 `487856170b29380671477e843d7fec15250323ae`，三平台 GitHub Release：<https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.49.2>。

`v0.0.49.1` 标签仍固定指向 `716b436ca0d79e34e52ea02a8ecc07f6579b5cfe`，旧标签和归档未覆盖。未完成 exporter 与独立 subagent recorder 已分别隔离在专用 stash，未进入本补丁版。

#### `v0.0.49.1` 发布收尾

已完成（2026-08-22）。最终交付范围为 macOS arm64 与 macOS amd64，不含 Windows/Linux。

### 2. Provider、子代理与 MITM 证据



#### 真实 Cursor 子代理协议补证

本地合同与 synthetic 回归已完成（2026-08-22；**不归档为完成**，synthetic 不等于真实）：

- [x] 系统 Design §14.7.10 已冻结六场景。
- [x] 落地 `source=synthetic`、`closes_stage_2=false` 的 `internal/backend/forwarder/testdata/subagent_contract_scenarios.json` 与 `subagent_fixture_test.go`；独立审查无 P0/P1。
- [x] 主控验证已实际运行并通过（2026-08-22）：`go test ./internal/backend/forwarder ./internal/observability -count=1 -timeout 90s`（forwarder 与 observability ok）；`go test -race ./internal/backend/forwarder ./internal/observability -count=1 -timeout 120s`（两包 ok，无 race）；`go vet ./internal/backend/forwarder ./internal/observability` 通过；`gofmt -d internal/backend/forwarder/subagent_fixture_test.go internal/backend/forwarder/subagent_real_observation_test.go` 无输出；fixture JSON 值级隐私扫描无用户绝对路径、credential-like token 或 raw UUID；`git diff --check` 通过；`internal/mitm`、`internal/certs`、`proto` 保护路径 diff 为空。本轮验证覆盖 synthetic 回归与 partial real observation，**不是** `source=real` 六场景通过。

partial real observation 已完成（2026-08-22；`source=real`、`closes_stage_2=false`，**不能**关闭 Stage 2）：

- [x] `internal/backend/forwarder/testdata/subagent_real_success_observation.json` + `internal/backend/forwarder/subagent_real_observation_test.go`。
- 14 个 run 均 `acknowledged` / handoff acknowledged / terminal succeeded / result file present；identity presence root/parent/tool/parent_request/parent_model_call/agent 为 14，`child_conversation` 为 0；`protocol_envelope_presence_available=false`。
- 采集只读取 `run.json`；`result.json` 只检查存在，不打开/复制正文；不保存 raw IDs。排除 1 个 dispatched run，但该排除数不是 JSON 可复算字段。
- 只能证明匿名成功交接聚合，不能证明 typed message / 五字段 presence、error/cancel/restart/resume/missing-parent。

系统 Design §14.7.11/12 已完成并经独立终审（实现尚未开始）：

- [x] §14.7.11 metadata-only `subagent_protocol_observed` 与 §14.7.12 离线 exporter 已写入系统 Design；终审 P0/P1 无，P2 已闭合。
- 明确只是 Design approved：未添加生产事件，未实现 `cmd/subagent-fixture-exporter`，未启动/停止第二 Backend，未修改 Cursor/MITM/CA/系统代理/proto。

真实证据仍阻塞（Stage 2 保持 `in_progress`；`source=real` 六场景未完成，绝不能标 completed）：

- [ ] 后续须先实现已批准的 metadata 事件与 exporter（必须再次获得明确实施授权），再在隔离环境采集六场景。真实运行只能来自另一 OS 用户、VM、独立机器或未来另批批准 harness；本批 exporter 只读用户提供副本，不承诺本机第二实例/独立 Backend。
- [ ] 采集新的真实 Cursor 子代理协议 fixture，覆盖父子会话、工具调用、RunSSE、取消、重连和终态交接。
- [ ] 用真实 fixture 验证 `terminal_prepared → parent_committed → acknowledged` 的持久交接和关联字段。
- [ ] 补真实 Success/Error envelope、取消消息类型、`parent_committed` 未 ack 后重连、未终态 resume/`run_id`、父会话缺失后 resume。
- [ ] 明确验证边界：当前实现只保证已生成终态的持久、幂等交接；未完成 child 进入 `awaiting_client_resume`，不承诺从中断执行点自动续跑。



#### MITM/TLS 决策证据

已完成（2026-08-22）。基线报告见 [`docs/mitm-tls-baseline-report.md`](mitm-tls-baseline-report.md)。输入为 9 个 closed `v0.0.49.1` session，结论保持当前路由/CA/白名单；证据闭合前仍不修改 MITM 白名单、CONNECT/直通策略、CA/证书、系统代理、透明代理或协议行为。未闭合项摘要见下文已完成归档。

### 3. 独立日志分析器产品能力



#### 调查案例库

- [ ] 实现持久调查案例、状态机、版本关联和修复后复验。
- [ ] 案例默认只保存脱敏证据快照，不复制 full payload、凭据或项目路径。



#### 外部 AI 调查包

- [ ] 实现脱敏证据包导出和结构化分析结果导入。
- [ ] 保持日志内容为不可信输入；分析器第一版不直接调用 AI、不修改仓库、不执行外部命令。



#### 客户端启动器

- [ ] 在客户端日志采集区增加独立分析器检测、启动按钮和未安装引导。
- [ ] 保持客户端只负责采集与受限启动，不在客户端进程内读取历史日志、运行分析或生成报告。



### 4. 分析器发布隔离与性能验收

- [ ] 建立分析器独立发布、客户端归档隔离、跨版本、跨模块和跨平台验证证据。
- [ ] 补充真实大日志目录性能门禁：当前约 `1.7 GB` 数据集在 14 分钟内未完成；已验证可取消，但尚未通过全量性能验收。
- [ ] 明确性能验收指标、测试机环境、完成耗时、峰值内存和取消清理结果，避免只记录“能运行”。



### 5. 后续路线图（尚未进入当前实施包）

- [ ] 建立统一 Capability Registry，用于路由能力分类、工具名单和前端执行目标说明。
- [ ] 补齐 Bidi、RunSSE、工具循环、取消与重连的协议基线测试。
- [ ] 继续治理 actor 状态机、持久化格式版本与迁移、Keychain、日志安全和架构拆分。
- [ ] 在独立设计批准后推进本地检索和高价值 Cursor 能力补齐；不得与已完成的 `v0.0.49.1` 发布收尾混做。



## 二、已经完成的内容



### 1. 按版本号归档



#### `v0.0.49.2` — 2026-08-22（已发布）

- [x] 修复 `assistant_text` 把 `ReasoningContent` 拼入公开文本的问题。
- [x] 修复多个 `tool_call` 各自重复附带同一 reasoning 的问题；工具调用仍以结构化 `tool_use` 输出。
- [x] 导入 `model_message` 时不再向公开 JSONL 投影 reasoning；内部 history/context 与 provider replay 数据未删除。
- [x] reasoning canary、多个工具调用、导入消息、ConversationFileStore 与 agent-transcripts 回归测试通过。
- [x] 根模块、forwarder、race、vet、客户端构建、前端构建及两个独立 Go module 的完整发布前门禁通过。
- [x] macOS arm64、macOS amd64、Windows amd64 归档和三平台 `update.json` 已构建；架构、版本、签名结构、分析器隔离、URL、size 与 SHA-256 已复算验证。
- [x] 发布提交 `487856170b29380671477e843d7fec15250323ae`、标签 `v0.0.49.2` 与 GitHub Release 已完成；远端重新下载资产逐字节等于本地产物，`latest/download/update.json` 指向 `0.0.49.2`。

Release：<https://github.com/yaogjim/cursor-byok/releases/tag/v0.0.49.2>

#### `v0.0.49.1` — 2026-08-19 至 2026-08-22（发布已收尾）

版本代码已经提交并推送；2026-08-22 发布收尾完成。最终交付范围为 macOS arm64 与 macOS amd64（`bin/release/0.0.49.1/` 下对应 tar.gz 与 `update.json`），不含 Windows/Linux。

- [x] Provider 错误与流终态：补齐 typed HTTP error、脱敏错误摘要、attempt/response 审计、截断 EOF 识别和唯一业务终态。
- [x] Provider 安全重试：只在首个 model event、原始流字节和副作用均未发生时有限重试；覆盖 transport、429、500、502、503、504、`Retry-After`、取消和等待预算。
- [x] Provider 与 MITM trace：修复 attempt 事件只进入默认关闭审计链的问题，补齐 request/response/retry/final、CONNECT 决策和 TLS 失败的正式 trace 落盘及关联字段。
- [x] 日志隐私：不记录请求/响应正文、header、token、cookie、API key、child prompt/result 或 Provider 原始敏感响应。
- [x] 子代理关联与终态：贯通 root、parent、tool、subagent、child、agent、model-call 和 attempt；实现 typed terminal、首个持久终态胜出和冲突保护。
- [x] Durable handoff：实现版本化 `SubagentRunStore`、原子 run/result、checksum、损坏隔离、启动恢复扫描和 parent tool-result 幂等提交。
- [x] 缺失父会话保护：父会话不存在或已删除时保留 durable result 并进入 `awaiting_parent_resume`，不创建孤立 conversation。
- [x] Provider fallback：实现默认关闭的显式有序 allowlist；retry/fallback 共享 attempt 与等待预算；任意输出、model event 或副作用后禁止切换。
- [x] Fallback 兼容门禁与前端：覆盖 Provider family、工具、图片、上下文和原始请求体兼容性；完成配置投影、导入导出、候选编辑和多语言入口。
- [x] 集成验证：相关及全仓 Go test、race、vet、前端配置投影与生产构建、故障注入、敏感信息审查和禁止路径反向审查均已通过。
- [x] 发布元数据：将版本更新为 `0.0.49.1`，补齐四段版本比较测试，并把内外发布说明收敛为简版。
- [x] 关键提交：`9e6936f`、`eed4ca6`、`0850958`、`c6a462d`、`716b436`；当前 `noad` 已与 `origin/noad` 对齐。
- [x] 发布收尾：2026-08-22 完成 macOS arm64 与 macOS amd64 资产交付；本地标签 `v0.0.49.1` 已存在。Windows/Linux 不在本轮范围。
- [x] MITM/TLS 基线：2026-08-22 基于 9 个 closed `v0.0.49.1` session 生成 [`docs/mitm-tls-baseline-report.md`](mitm-tls-baseline-report.md)；分析器合计 `events=128456 traces=14338 findings=11468`；`cd tools/log-analyzer && go test ./...` 通过。TLS 失败 906 次全部为 `cursor_to_proxy` + `client_unknown_ca`（api3 592、metrics 282、api2 32）；无 upstream TLS 失败。结论保持当前路由/CA/白名单；未改 `internal/mitm`、证书或 `proto`。open session 未计入。

已知限制：Stage 2 保持 `in_progress`。本地合同与 synthetic 回归已完成；partial real observation 已落盘（`source=real`、`closes_stage_2=false`），只能证明匿名成功交接聚合；系统 Design §14.7.11/12 已终审但实现尚未开始。真实 Cursor 子代理协议六场景 fixture 仍待补证，synthetic 与 partial observation 都不能关闭 Stage 2；不承诺 child 从中断执行点自动续跑。MITM 未闭合证据：api3/metrics 100% `client_unknown_ca` 的机制未知；Dashboard/MCP `client_error` 与 TLS 无 ID 级因果；provider attempt 层 `trace_id`/`http_request_id` 完整率 49.2%；`bidi.raw decode_error=true` 在 basic 模式下的语义未闭合。

#### `v0.0.49` — 2026-08-19 至 2026-08-20

- [x] 将 `upstream/main@564f2bd` 经 `main@305b108` 合入 `noad`，形成 `f969ca0`。
- [x] 保留并整合 `noad` 上已有的 Provider 首包前安全重试与 MITM 只读诊断开发线。
- [x] 清理对外发布说明中的非产品联系信息。
- [x] 上游 `v0.0.49` 标签已存在；本地后续 P0/P1 改进归入 `v0.0.49.1`，不回写为 `v0.0.49` 已发布能力。



#### `v0.0.48` — 2026-08-12 至 2026-08-17

- [x] 完成上游同步、合并、版本记录与 `v0.0.48` 标签。
- [x] 支持会话统计重置和已关闭日志的安全清理，并补齐相关前端、后端、i18n 和测试。
- [x] 对齐上游同步发版流程，并把 `v0.0.47` 编排结果写回任务记录。



#### `v0.0.47` — 2026-07-27 至 2026-08-10

- [x] 完成 `upstream/main → main → noad` 同步、冲突处理、窄范围验收、推送和 `v0.0.47` 标签。
- [x] 发布 macOS arm64、macOS amd64、Windows amd64 三个平台资产，不包含 Linux；GitHub Release 已完成。
- [x] 清理辅助 worktree，同时保留已验证分支、标签和发布资产。
- [x] 接入全局 Agent 规范并确立 `task/todo.md` 为任务唯一真值源。
- [x] 建立分析器临时 SQLite workspace、批量流式导入、有界 reducer 和流式 JSON/HTML/ZIP 报告。
- [x] 完成 SQLite 兼容性、大规模内存、故障注入、race/vet、离线跨平台构建和客户端发布隔离验证。
- [x] 完成日志语义 v2、项目生命周期、组合检索 DSL、保存查询、增强诊断、独立 Wails/Vue GUI 和默认日志目录异步自动加载。
- [x] 独立分析器 GUI 通过 macOS arm64 原生启动/退出 smoke、ad-hoc 签名、DMG/tar.gz 结构与 SHA-256 校验。



### 2. 按时间索引

- **2026-08-31**：完成 Provider 历史净化。目标感知来源身份，跨身份剥离 opaque reasoning/Responses item 元数据但保留文本和工具链；400 脱敏摘要进入诊断；32MiB 最终编码请求体预检且 `request_build` 不重试/fallback。复审修复 provider done origin 清零落盘丢失、tool flush 顺序和 safety suppression。定向 `go test`/`go vet` 与 `git diff --check` 通过。详见 `task/todo.md` 的 `provider-history-sanitize-20260831`。

- **2026-08-30**：完成 Rule P0 与 managed Codex affinity 基础检查点。Rule 私有权限、symlink/非普通文件拒绝、原子有界持久化、docs index 来源隔离与 reconciliation 已通过定向/race/vet 和 Windows 交叉构建；Windows 实机 ACL 仍待发布门禁。Codex 已接入稳定账号隔离、独立私密密钥、四字段 HMAC 域分离、精确 ChatGPT Responses 注入及 `control | prompt_key | full` profile，继续删除 `previous_response_id`。fake upstream、缓存观测和 A/B 尚未完成，不宣称性能收益；Rule journal 与手动同步尚未开始。

- **2026-08-30**：完成上游 `v0.1.5@b807608` 与最新 `upstream/main@9120b90` 深度审阅。隔离 worktree 合并本地 `main@305b108` 发生真实跨架构冲突后已安全中止；`main`、`gateway` 未变，无提交、无 push。确定优先加固 Rule 持久化，再独立补 managed Codex 缓存亲和；Rule 离线同步必须显式 opt-in；不整体移植 Deno/Rust runtime。详细结论见差异 PRD §16。

- **2026-08-30**：完成 `v0.0.52.5` Apple Silicon macOS 本地构建。版本元数据与发布说明已对齐，产物归档为 `bin/release/0.0.52.5/cursor-byok-0.0.52.5-macos-arm64.dmg`（24,031,370 bytes）。构建首次因误用 Go 1.26.3 且清空编译缓存，在 8GB 内存/swap 压力下大型生成文件 `aiserver_v1.connect.go` 编译停滞；改用项目 Go 1.25.0 并加 `GOGC=20` 控制编译器内存后成功。已核验 `0.0.52.5` 版本（Info.plist 与二进制注入）、arm64 架构、adhoc 签名、`hdiutil verify` 与 SHA-256 `b131348e7afd154dd05eae6f00968d971401d92f10b26efeb109ba49b2ee3153`。未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布。

- **2026-08-29**：实现 Codex 多账户管理与安全轮换。模型配置仍只选择 Codex 凭据来源；旧单账户私有文件兼容迁移为账户池，接入页支持激活、逐账户用量和删除。401 定向刷新失败后切换备用账户，明确 quota 仅在零输出安全窗口内幂等轮换并单次重试。自动化验证范围见 `task/todo.md`；未使用真实 token、未做 Wails 视觉点击或发布包重构建。

- **2026-08-29**：完成 `v0.0.52.2` Apple Silicon macOS 本地构建，产物为 `bin/release/0.0.52.2/cursor-byok-0.0.52.2-macos-arm64.dmg`；版本元数据与发布说明已对齐，DMG 校验、版本、架构、签名和 SHA-256 已核验。未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布。

- **2026-08-29**：修复订阅模型测试沿用用户填写接口地址的问题。Codex 订阅统一使用 ChatGPT Codex Responses 官方上游，Grok 订阅统一使用 xAI 官方上游；前后端保存、重载和测试采用同一归一化规则，订阅模式下接口地址与协议端点只读。后端端点、Codex 请求协议、客户端测试链路、前端投影和生产构建通过；未使用用户真实 token 请求外部上游，未重新打包 macOS 安装包。

- **2026-08-29**：完成 `v0.0.52.1` Apple Silicon macOS 本地构建。版本元数据与发布说明已对齐，带版本号的产物位于 `bin/release/0.0.52.1/cursor-byok-0.0.52.1-macos-arm64.dmg`；已核验 `0.0.52.1` 版本、arm64 架构、adhoc 签名、DMG 完整性和 SHA-256。未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布。

- **2026-08-29**：完成 `v0.0.52.0` Apple Silicon macOS 本地构建。版本元数据与发布说明已对齐，`bin/macos-arm64.dmg` 的版本、arm64 架构、adhoc 签名、DMG 完整性及 SHA-256 已验证；未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布。

- **2026-08-28**：修复模型页保存时身份字段变化导致 fallback 渠道 ID 悬空，以及切换 OpenAI/Anthropic 清空模型标识。`SaveModelAdapters`/`SaveUserConfig` 使用请求旧 ID 与磁盘 adapter 精确 remap；新 adapter 不携带旧 ID，删除加新增不会误配。类型切换保留当前 `modelID`。定向 Go/前端测试与生产构建通过；未做 Wails 视觉点击。详见 `task/todo.md` 的 `model-save-identity-remap-20260828`。
- **2026-08-26**：冻结 v5 四页控制面阶段 0 合同（`/access?client=...`、per-scope dirty、真实计数、完整配置导入导出文案、`system` 主题、持久小时桶、Cursor 启动/重启安全语义、Codex/Claude 非生产 fixture）。决策基线 §7.5/§10.14、系统 Design §4.2/§10.2/§14.16 与 `task/todo.md` 的 `ui-v5-shell-20260826` 已同步。本轮只改文档，实现未开始，不得标完成。ACP 工作包保持 blocked。
- **2026-08-26**：按已批准双集成导航计划落地五页同层控制面、Gateway section 保存/运行隔离和数据概览 `GetHomeMetricsReport`（`7d`/`30d`/`all`，UTC daily）。决策基线 §7.4/§10.13、系统 Design §4.1/§10.1/§14.15 与 `task/todo.md` 的 `dual-nav-overview-20260826` 已同步。本轮只改文档；Wails 视觉点击未做，交付保持 `verified-partial`。
- **2026-08-24**：198 远程 CLI 方案 A 隧道落地；浏览器终端由 ttyd 换成 WeTTY。随后修复 HTTPS 反代下 WeTTY helmet 只放行 `ws://`、浏览器 `wss://` 被 CSP 拦导致终端约 10 秒断开的问题；再将 `X-Frame-Options` 从 `DENY` 改为 `SAMEORIGIN`（并加 `frame-ancestors 'self'`），避免 WeTTY 同源 xterm 配置 iframe 被拦。`https://172.16.23.198/` 仍为 HTTPS + HTTP Basic，7681 仅 loopback。
- **2026-08-22（`v0.0.49.2`）**：完成 Agent transcript reasoning/thinking 公开投影修复、全套验证、三平台资产构建与 GitHub Release。发布提交和标签均指向 `487856170b29380671477e843d7fec15250323ae`；远端资产与本地 SHA-256 一致；`v0.0.49.1` 保持不可变；未完成 exporter 与 subagent recorder 未纳入补丁版。

- **2026-08-22**：`v0.0.49.1` 发布收尾完成；最终交付范围为 macOS arm64 与 macOS amd64，不含 Windows/Linux。Stage 3 MITM/TLS 基线完成：报告 `docs/mitm-tls-baseline-report.md`，9 个 closed `v0.0.49.1` session，分析器合计 `events=128456 traces=14338 findings=11468`，结论保持当前路由/CA/白名单。Stage 2 仍 `in_progress`：本地合同与 synthetic 回归已完成（`closes_stage_2=false`）；partial real observation 已落盘（14 个 acknowledged/succeeded run，`protocol_envelope_presence_available=false`，`closes_stage_2=false`）；系统 Design §14.7.11/12 已终审但实现尚未开始。本轮主控已实际运行 test/race/vet/gofmt/隐私扫描/`git diff --check`/保护路径检查并通过，**不是** `source=real` 六场景通过。`source=real` 六场景仍阻塞；当前阶段仍为 Stage 2 真实 Cursor 子代理协议补证。
- **2026-08-21**：完成 `task/todo.md` 实施计划与 `docs/process.md` 进展归档的职责拆分和同步合同；完成 `v0.0.49.1` 子代理 durable handoff、Provider fallback、前端配置、集成验证、版本配置和简版发布说明；源码提交并推送。发布构建第一次因磁盘不足失败，清理缓存后开始第二次构建并生成 macOS arm64 资产。
- **2026-08-20**：完成 Provider/MITM 正式 trace 落盘、日志级别与关联字段修复；完成 `v0.0.49` 上游合并和发布说明清理。
- **2026-08-19**：完成 Provider 首包前安全重试、流截断正确性和 MITM metadata-only 诊断；提交 `9e6936f`。
- **2026-08-17**：完成 `v0.0.48` 同步记录、会话统计重置和已关闭日志安全清理。
- **2026-08-10**：完成 `v0.0.47` 三平台发布和 GitHub Release。
- **2026-08-02**：完成日志观测语义、分析项目、查询、诊断和独立 GUI 的主要能力；提交 `17659ec`。
- **2026-07-27**：完成分析器临时 SQLite 工作区与流式管线基础；提交 `a1b1cf2`。

历史完成项只在本文档归档，不再与详细执行步骤混排。实施步骤以 `task/todo.md` 为准，产品决策与设计细节以相应 PRD、系统 Design、`.cursor/plans/` 和 Git 提交为准。