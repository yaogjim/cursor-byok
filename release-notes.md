# cursor-byok v1.91.0

本版本适配 Cursor 新版协议描述符格式，让协议提取器可以继续从本地 Cursor bundle 生成 Go/Connect 代码；同时补齐 managed Codex 缓存亲和的契约层与脱敏观测，并对真实上游做了一轮缓存 A/B 实测（结果为负）。

## 协议提取器适配新版描述符

- Cursor 3.29.15 的扩展 bundle 改用紧凑描述符（`static $(){return["TypeName|字段..."]}`）而非旧版反射描述符，原提取器无法解析，导致无法再生成协议代码。现支持紧凑 message/enum 描述符、继承与常量包名、枚举工厂、依赖索引、嵌套类型、optional/repeated/map/oneof；严格模式下非法引用、未知标量与缺失包名会使同步失败而不是静默产出错误类型。
- 同步仍保留既有 service 定义：按 package 从旧快照复制后，移除引用了已删除本地类型的 RPC 并输出诊断；bundle 中存在的同名 service 优先，bundle 缺失时保留旧快照。
- 新增独立的 `proto/agent_v1_compat.proto` 与 `proto/aiserver_v1_compat.proto` 承载上游已删除、但本仓现有会话/历史与控制面逻辑仍在使用的类型，不与自动提取产物混写。`build/Taskfile.yml` 的 protoc 调用已把两个 compat 文件纳入生成。
- `RequestedModel` 删除字段 8（变体字符串表示标记）后，改为从 protobuf unknown fields 读取，选模、推理强度、子模型与调试摘要行为保持与旧客户端一致。

## managed Codex 缓存亲和：契约与观测

- 新增 HTTP 层端到端断言：401 后同账号刷新时 `prompt_cache_key`、`session-id`、`thread-id`、`x-client-request-id` 保持稳定且 Authorization 换成新 token；quota 换号后亲和分区改变、`ChatGPT-Account-Id` 切换，且自定义 header 不能覆盖保留 header。
- 新增 `codex_affinity_profile` 与 `codex_affinity_injected` 两项脱敏观测，仅记录档位枚举与是否注入，不含派生值；同时向 OpenAI 两种 endpoint 与 Anthropic 的流式摘要透传 `cache_read_present`，用于区分「上游明确返回零」与「未提供缓存字段」。
- 真实上游 A/B：18 次请求（单一账号、单一模型、串行限速）中，`prompt_key`/`full` 两档共 12 次 `cache_read=0`；`control` 档仅一次出现自动前缀缓存命中，顺序反转后不可复现。**不宣称亲和字段带来缓存收益**，默认档位保持 `control`。

## 明确限制

- 生成的协议代码只通过本地同步、编译与相关测试验证；未安装并实机运行重新生成后的应用，其他新格式分片、旧版 Cursor 实机与跨平台均未验收。
- service 合并策略依赖「bundle 同名优先」的判断，旧快照中 bundle 未包含的方法被视为上游删除，但没有用真实服务端验证。
- compat 类型只用于维持现有历史会话与控制面行为，不证明上游仍支持对应接口。
- 缓存 A/B 为单一账号、单一模型、单一时段的有限样本；无法排除上游缓存策略的时段性或按账号差异。实验使该订阅账号 remaining 由 55% 降至 41%。
- 控制面未登录时插件/MCP 查询返回 404 的策略、同回合原生布局表示合同均未在本版本处理；updater 安装机制仍未设计。

## 验证边界

- 协议同步：6200 个 message、676 个 enum、21630/21630 字段，unresolved 与 placeholders 均为 0；`check:proto` 确认根协议与提取快照一致。
- `go build ./...`、`go test ./proto/ext_tool ./internal/backend/agent/protocol ./internal/backend/agent/prompt ./internal/backend/forwarder ./internal/backend/server/upstream`、`go test ./internal/backend/agent/model ./internal/subscriptionauth`、定向 `-race`、`go vet`、`gofmt -l` 与 `git diff --check` 通过；详细命令、退出码与复核结论见 `docs/process.md`。
- 本机执行 macOS 宿主平台 `task build` 生成安装包；Windows 与 Linux 安装包由远端 CI 构建。