# cursor-byok v1.92.0

本版本把用户附图纳入 provider 请求字节预算以缓解大请求上传超时,补齐上传失败诊断与请求体组成观测;同时包含自 v1.90.1 以来未随版本发布的协议提取器适配、Codex 缓存亲和观测、原生归档排除同回合固定输入、控制面未登录空响应与 README 文案更新。

## 用户附图请求字节预算

- 原图片预算仅裁剪 `role=tool` 的历史工具图,历史轮的用户附图虽计入请求估算但不会被裁剪,仍可能导致大请求上传超时。现按确认规则补齐:先占位旧工具图,再逐张占位历史轮用户图;当前轮及轮次未知的用户图不裁剪、不去重。
- 占位保留用户文本,其余图片不连带删除;用户图占位提示需要时请用户重新附图。预算为软限制,仍超出时继续发送并记录 `over_budget` / `over_budget_bytes` / `user_omitted`,不新增拒绝策略。
- 历史投影从 `HistoryEntry.TurnSeq` 注入仅内存 `Message.SourceTurnSeq`,`driveProvider` 传递当前 turnSeq;预算在请求副本上执行,不改 canonical history。

## provider 上传失败诊断

- `httptrace.WroteRequestInfo` 的 `Err` 此前被丢弃,`connect timeout` 无法区分「请求体上传失败」与「上传完成但等首包超时」。现 `RecordWroteRequest(at, err)` 仅在成功时记录完成时间,失败只保存稳定分类(`broken_pipe` / `reset` / `deadline` / `canceled` / `timeout` / `unknown`),原始错误文本不落盘。
- 分类经 stream diagnostics 输出到 provider terminal 事件的 `wrote_request_err` 字段,并加入观测白名单;修复 httptrace 与 attempt context 绑定顺序导致的追踪丢失。
- 新增 `request_composition` 请求体组成统计(总字节、消息/工具/指令字节、消息数、图片数与字节、最大消息字节),用于定位超大请求,不含正文内容。
- 终端错误摘要区分传输错误(`transport error (no HTTP response)`)与无状态错误,`LivenessTimeout` 按阶段标注 `(no response headers)` / `(no model output)`。

## 协议提取器适配新版描述符(自 v1.91.0 累积)

- Cursor 3.29.15 的扩展 bundle 改用紧凑描述符(`static $(){return["TypeName|字段..."]}`)而非旧版反射描述符,原提取器无法解析。现支持紧凑 message/enum 描述符、继承与常量包名、枚举工厂、依赖索引、嵌套类型、optional/repeated/map/oneof;严格模式下非法引用、未知标量与缺失包名会使同步失败而不是静默产出错误类型。
- 新增独立的 `proto/agent_v1_compat.proto` 与 `proto/aiserver_v1_compat.proto` 承载上游已删除、但本仓现有逻辑仍在使用的类型;`RequestedModel` 删除字段 8 后改从 unknown fields 读取,保持旧客户端语义。

## managed Codex 缓存亲和:契约与观测(自 v1.91.0 累积)

- 新增 HTTP 层端到端断言:401 后同账号刷新保持 `prompt_cache_key`、`session-id`、`thread-id`、`x-client-request-id` 稳定;quota 换号后亲和分区改变且自定义 header 不能覆盖保留 header。
- 新增 `codex_affinity_profile` 与 `codex_affinity_injected` 脱敏观测;OpenAI 两种 endpoint 与 Anthropic 流式摘要透传 `cache_read_present`。
- 真实上游 A/B:18 次请求中 `prompt_key`/`full` 两档共 12 次 `cache_read=0`,`control` 档仅一次命中且不可复现。**不宣称亲和字段带来缓存收益**,默认档位保持 `control`。

## forwarder 原生归档与控制面空响应(自 v1.91.0 累积)

- 原生归档移除同回合固定输入拒绝门禁:`summarized_messages` 仅排除 layout PinnedInputs,当前回合输入只由 ROOT 承载。
- 未登录独立控制面账号时 GetEffectiveUserPlugins/GetKnownServers 返回 200 空集合;已登录仍转发官方;Analytics Batch 本地接收后丢弃并返回空响应;GetScmConnectionStatus 任意身份返回 `connected=false`;NameTab 依 name 字段语义保持 404。
- README 改写开源初衷;AccessView 中 Anthropic 客户端提示文案调整。

## 明确限制

- 用户附图预算:裁剪策略基于占位而非降采样,当前大图仍超预算时可能超时;`providerRequestUploadBytesPerSecond` 为保守假设(256 KiB/s),尚无真实失败样本校准;未做真实 Cursor 大请求重放验收。
- 上传失败诊断:分类命中率未经运行验证;`wrote_request_err` 只出现在 provider terminal 事件。
- 协议生成的代码只通过本地同步、编译与相关测试验证;未安装并实机运行重新生成后的应用。
- 缓存 A/B 为单一账号、单一模型、单一时段的有限样本,无法排除上游缓存策略的时段性或按账号差异。
- updater 安装机制仍未设计。

## 验证边界

- `go test ./internal/backend/...` 全部通过;`go build ./...` 成功(链接器提示部分对象 macOS 14.0 高于目标 11.0);`go vet`、`gofmt -l`(除既有 `internal/backend/server/config/types.go` 偏差外)与 `git diff --check` 通过;详细命令与结论见 `docs/process.md`。
- 本机 macOS arm64 曾执行 `task build` 产出 DMG(基于 1.91.0 工作区);本版本 macOS、Windows、Linux 安装包由远端 CI 构建并随 Release 发布。