# 活动任务

> 本文件是项目活动任务的唯一真值源。`.cursor/plans/*.plan.md` 的 frontmatter todo 仅作阶段索引，不代表任务已满足 Definition of Ready。

## 当前焦点

### 0.0.71.1 下载日志异常诊断与最小修复（2026-09-09）

- 范围：用户要求主控安排取证、根因分析、review、验证及必要修复；读取 `/Users/yaogj/Downloads/logs` 六份 app 日志和两个 trace 会话，对照 `5926cc4`。不改真实模型/账号/CA/代理配置，不重启或部署，不 commit/push；未启动产品级自动恢复能力。
- [completed] `log-triage`：旧版 0.0.71.0 当前样本 848 事件，新版 0.0.71.1 为 183889 事件；序号连续，JSON 解析无错误。新版 183 个唯一模型终态为 171 succeeded、8 failed、3 partial、1 canceled；`gpt-6-astra-211` 是 7 失败加 1 取消，不能表述成 8 次故障。
- [completed] `root-cause`：5 次 500 的上游摘要为 Codex TLS handshake EOF；1 次 503 为 auth_unavailable 并附 198.18.0.49 连接超时。六次均已有两次 HTTP attempt，候选耗尽；三条 Grok 流同时 unexpected_eof 支持共享链路中断，但具体网络节点未知。另有 1403 次客户端 CA 握手拒绝、默认模型 48 次本地 502、插件/MCP 本地 404、遥测 Batch 缺路由。
- [completed] `managed-skills-fix`：Host 实际入口先复现未登录 GetManagedSkills fallback 在 proto/JSON Content-Type 下都返回 502；只补 `newProtoMessage` 的现有 `GetManagedSkillsResponse` 类型注册，两例转为 200、protobuf 可解码且 skills 为空。生产改动 2 行、既有测试增加 40 行；JSON 入站使用合法 `{}` 正文，保留其他控制面与身份语义。
- [completed] `review-verify`：独立只读 review 未发现具体缺陷；主控复核 diff、原始日志统计、RED/GREEN，并运行相关两个包的定向契约测试与 vet，均通过。修正“gzip 已完全排除”“确定共用物理连接”“默认接口耗时证明上游成功”等过强诊断，详见 `docs/process.md` 最新节。
- [completed] `closeout`：分析与源码最小修复完成；交付状态 `verified-partial`，真实现场、上游服务和证书尚未验收。下一步为失败渠道出站/DNS/账号取证、Grok 同时断流关联及默认接口脱敏错误阶段取证；404 的回源/空响应策略属于待确认建议，不擅自实施。
- 恢复规则：本轮执行及 review 未发生服务中断，无需退避重试；若发生则在原 ID 按 20/40/80/160/320 秒最多五次恢复，持续失败暂停。测试环境的首次依赖缓存路径缺失已修正，不属于执行服务中断。

### 0.0.71.0 对话连接回归取证（2026-09-08；日志 UTC 日期 09-09）

- 范围：分析用户提供的 `/Users/yaogj/Downloads/logs`、本机应用和 Cursor 日志，对照 `4e4f2f1`（0.0.61.1）与 `818e293`（0.0.71.0）；保留用户已回退的实例，不改账号、证书、配置或服务。
- [completed] `connection-log-triage`：下载样本 841 条事件，56 次后端 `bidi_append` 完成全部为 502；7 次已结束 `run_sse` 均约 60 秒后 canceled。本机 0.0.71.0 会话有同型 8 次 502，未见模型调用事件；回退 0.0.61.1 的读取快照已有 226 次 BidiAppend 200，较早快照有 6 次 provider request/response 和 521 条回复 chunk。
- [completed] `connection-wire-probe`：继续分析时使用 `/tmp/gateway-wire-probe.d2P9ex` 的 Go overlay 与隔离 HOME，在原始 AgentRouteAction 上复现 gzip Protobuf / JSON 解码失败、502 与 RunSSE 等待；四种身份/选模×三种编码共 12 例，原源码 8 失败、4 通过。临时修正传入 Content-Encoding 并区分 JSON/Protobuf 后 12 例通过，转发 body/encoding 保持不变，相关分流与 Host 定向契约测试通过。不是实际官方/模型端到端验证。
- [completed] `connection-root-cause-and-fix`（源码与隔离验证）：按批准计划修复 agent_action.go/agent_route.go 的 Content-Encoding 接线和 protobuf/JSON 解码；原始请求与分流规则不变。既有测试固化 12 组合、Connect 真解码、损坏/不支持编码及 Host gzip→模拟 provider 回复。最终两个包测试、定向 race、vet 通过；真实 Cursor 实机未执行，整体仍为 verified-partial。
- 执行结果：`wire-regression` completed（分流 gzip/JSON RED；Host 普通请求成功、gzip 502）；`fix-wire-decoding` completed；`verify-and-record` completed；`runtime-acceptance` pending（需另行确认窗口）。独立 Spec 复审未发现生产缺陷；Standards 复审发现 Host 测试超时后接收协程收尾不可靠，主控移除不必要协程并检查 stream.Err，复审确认关闭，随后重跑全部计划内验证通过。
- 中断恢复：Host 测试任务两次 provider_terminal/status=not_recorded，均未返回可恢复 ID，但已写入测试；实际等待 20 秒、40 秒后基于同一工作区保留改动继续剩余任务，第 2 次重试成功，未用到 80/160/320 秒。有 ID 的实现与复审沿原 ID 继续；没有持续失败或暗中跳过恢复。未改真实运行实例、未打包/安装、未 commit/push。
- 最终收口证据：测试接收逻辑清理后，两个相关包完整测试、`AgentRoute|GatewayDuo` 定向 race、vet 再次退出 0；日志为 `/tmp/gateway-wire-closeout-{test,race,vet}.log`，命令及证据边界见 `docs/process.md` 最新修复收口节。
- 真实验收状态：双渠道对话仍属阻塞回归，继续保留用户已回退实例。上线前需确认官方、Auto、官方身份下 BYOK 以及后续工具/取消均正常，真实 502 消失且有首段回复；证书告警独立排查。详细实验命令、证据与建议见 `docs/process.md`。

### gateway-duo 合并（2026-09-08）

- 工作包：`gateway-duo-merge-20260908`；`DESIGN_READINESS=approved`，`DELIVERY_STATUS=verified-partial`。用户已批准实施并要求主控安排最多两个并发执行任务、独立 review、验证与修复；源码移植、接线和隔离验证已收口，真实 Cursor 验收待完成。
- 基线：目标 `4e4f2f176efee5b1f094b3b9ccd6f07182480375`，来源 `18ef0e219d7875fa2012d23b9774562ea8d7960c`，共同祖先 `334f538bedab3a27ce82e4c7ef772e2553c40be2`。仅功能移植，版本文件不变，不 commit/push，不替换真实代理实例。
- 需求：工作决策基线 §10.17；设计：系统架构 §18「gateway-duo 合并」D1–D4。完整链路：保留/注入身份 → 标注来源的目录 → 按模型与身份分流 → 本地现有 provider 管线或官方透传 → 同 request_id 流与后续消息返回。
- [completed] `record-dual-contract`：固化选模、身份、默认与 `[官方]`/`[BYOK]` 显示契约；不增加配置/UI/账号系统。
- [completed] `merge-dual-routing`：D1/D2/D4；协议选模/运行消息识别、请求级记忆、auth DB、MITM、官方转发、OAuth 分流及 Host 接线；身份×模型×消息、先流后上行与取消由隔离测试覆盖。复审修复路由存储跨配置重建分裂，Host 生命周期共用存储；OAuth local/upstream 两模式都接入同一身份分流处理器。
- [completed] `merge-labeled-catalog`：D3；目录合并与同 ID 本地优先、身份默认、失败不推荐本地默认、来源名称及 CLI 占位。纯本地目录同样标 `[BYOK]`，保留模型 ID 和 `cursor-byok-local`；目录及 Host 契约测试通过。
- [completed] `verify-close-merge`（代码与隔离验证）：完成 Spec/质量只读 review、缺陷修复与复审。OAuth 最终接线后 `go test -count=1 -timeout=5m ./internal/...`、backend/cursor/mitm/upstream/protocol 五包 race、`go vet ./internal/backend/... ./internal/cursor ./internal/mitm` 及 `git diff --check` 均退出 0。测试统一隔离 HOME，完整命令和日志见 `docs/process.md`。
- [pending] `real-cursor-acceptance`（test/env gap）：工作决策基线 §10.17 / 设计 D1–D4；在后续获准的实机窗口验证保留真实登录、桌面/CLI 来源显示、显式本地/官方及 Auto 对话、真实 OAuth 刷新、失败不跨渠道。当前无发布/替换实例授权，模拟官方服务不替代真实验收。
- 执行恢复：每个中断任务在原 ID 上按 20/40/80/160/320 秒最多恢复五次；持续失败停止等待用户调整。本轮目录任务中断后等待 20 秒，从已有上下文恢复成功，未发生持续无法恢复；收尾复审未中断。
- 测试隔离事件：早期后端测试构造 Host 触发真实用户目录过期 debug 清理，日志报告删除 8 个文件；已告知用户，没有原始内容不能恢复。测试 helper 已加临时 HOME，后续所有 Go 验证均隔离。复用教训记录于 `docs/process.md`。
- 回退：仅撤销本次 diff，不整树 reset，不覆盖用户后续修改或新登录身份；旧版本会重新注入本地身份的风险保留。阶段结果与命令证据收口到 `docs/process.md`。

### 重开确认、订阅状态与模型代理回归修复（2026-09-08）

- 授权：用户要求修复三项缺陷，后明确主控安排独立执行、review 和验证；服务中断按 20/40/80/160/320 秒在原上下文最多重试五次，持续失败暂停。两项复审启动 503、浏览器启动连接中断均无 ID；等待 20 秒后各首次重试成功，后续沿同一 ID 收集结果，未用到后四次重试。
- 已确认退出语义：退出暂停服务且保留 Cursor 接入配置；用户接受暂停期间代理不可用，显式停止清理仍保留 owner 边界。需求锚点为工作决策基线 §10.14，设计为系统架构 §6.0、§5.1、§14.19。
- [completed] `restart`（隔离验证）：退出不再清设置和 CA 环境；新实例构造真实隔离 backend host，断言免确认、backend/proxy 运行、新 owner 接管及接管后显式停止能清配置；首次/真变更确认和所有权保护仍通过。CA/账号注入/真实 Cursor 用替身，不当作桌面实机证据。
- [completed] `accounts`（组件及隔离浏览器验证）：active 与 ready/auth_required/quota 状态独立；刷新失败可再点击，错误保留后成功清除；激活后列表/页脚一致，旧 provider 响应不覆盖新列表。camelCase 为正式 DTO，PascalCase 仅兼容性覆盖，不当作已证实根因。
- [blocked] `proxy`（原场景未复现）：模型列表刷新凭据漏传代理已修复；真实 TestModelAdapter 入口通过假 CONNECT/TLS 完成刷新→推理→success，模型代理两个目标 host 命中且 env 代理零命中。用户真实 7890 测速失败仍待原始错误/受控实机诊断，不能用模型列表修复替代该验收。
- [completed] `verify`：独立 Spec/Standards review，无新增阻塞生产缺陷；主控补足新 owner/服务运行/不清 CA 的断言，并修复构建扫描将测试示例账号收进翻译资源的问题（RED/GREEN）。最终五个相关 Go 包完整测试及 vet、app Tray/AutoStart、十项前端测试、配置投影、54 调用点日志契约、production build 与发布 catalog 测试数据排除断言通过。命令/告警详见 `docs/process.md`。
- 状态：`verified-partial`。未打包安装、未验证真实 Wails/系统 CA/真实 7890，未停止或重启真实服务，未 commit/push。历史自动重调度仍缺 typed Cursor 证据，ACP 仍缺真实客户端，不擅自解锁。
- 测试隔离事件：早期复现曾调用真实 launchctl unsetenv NODE_EXTRA_CA_CERTS（执行方报告）；已补替身。主控只读确认当前 unset，清除前值无证据，不自动恢复。临时浏览器/Vite 已清理；复用教训与事件边界详见过程记录。

### 模型导入、全部测试与分层代理（2026-09-08）

- 授权：用户确认访谈结果及计划后选择 Build；实现与本轮定向验证已收口，交付 `verified-partial`（真实 Wails 文件对话框及外部代理实机验收未完成）。
- 需求锚点：工作决策基线 §10.14「模型导入导出与全部测试」「分层出站代理」；设计锚点：系统架构 §14.19 `DESIGN-MODEL-IMPORT-PROXY-001`。
- 范围：运行中可用的模型专用 YAML 导入→合并草稿→模型分区保存；全量物理模型测试；默认关闭的全局/模型自定义代理→真实出站请求。沿用现有保存、测试和网络层。
- [completed] `record-contracts`：同步用户已确认需求和设计；保留历史任务及工作区既有版本/发布/翻译变更。
- [completed] `fix-model-import`：只读 YAML 提取后复用 `NormalizeModelAdapterDrafts` 恢复 yaml 不序列化的派生 ID，跨模型引用校验延至合并后；同身份/唯一同名更新并映射导入 fallback 引用，保留当前未命中模型及引用。失败/取消不改草稿，导入对话框阶段即防重复，保存仍走模型分区。真实导出→读回 ID、部分 alias、前端合并/保存投影回归通过。
- [completed] `wire-test-all`：按钮接全部草稿快照，沿用并发 10、逐项结果与超时；停止后续调度、等待在途结束，逻辑 alias/空列表明确提示。浏览器仅显示 1 个搜索结果时实际批次仍处理 2 个模拟模型，两个本地校验错误分别展示，模型数保持 2。
- [completed] `wire-layered-proxy`：`outboundProxy` 配置、设置/编辑 UI、分区保存、请求级覆盖、全局热更新、推理/测试/发现/fallback 与共用网络层接线；修复候选请求在 liveness/retry 替换 context 时丢失模型代理。已保存全局使用响应式独立快照，保留设置草稿/仅模型重载时也更新真实已保存代理，旧测试结果正确标记需重测。
- [completed] `verify-and-document`：九个相关 Go 包完整测试通过；最终导入修正后 config/client/bridge 再验证通过。`node frontend/scripts/test-config-projection.mjs`、`node frontend/scripts/test-client-api-logging.mjs`（54 call sites）、`npm run build --prefix frontend`、`git diff --check` 通过。浏览器验证开关、草稿、非法代理提示、全量调度及关闭保留地址；使用合成内存数据，无桌面桥接或配置写入。详情与告警见 `docs/process.md`。
- 保留缺口：真实 Wails 文件选择器/保存桥接和外部 HTTP/HTTPS/SOCKS 实机未验证；本地 HTTP/SOCKS 代理与假上游已覆盖，不当作外部实机证据。未做完整仓库测试/race、打包部署或 macOS 低版本实测。
- 非目标：超时、SSL、Task 恢复、PAC 引擎、修改 OS/其他进程代理、打包部署、commit/push。既有服务/真实配置未改变；隔离浏览器预览已清理。
- 回退：未保存导入可重新加载；关闭模型/全局自定义代理恢复继承。代码仅回退本任务改动，保留既有工作及用户配置。

### 🎯 Active Work Package

**main-gateway-value-migration-20260907** (completed, verified-partial)
- 最新追加复审授权：用户要求“review 完成情况，如果发现问题请修复”。重新审查 `2fc04e7` 全部 diff 和未跟踪文件，确认并修复三类 Cursor 缺陷；前次最终全量/真实应用验证豁免保持，不部署、不重启、不提交。
- [completed] `review-followup-audit`：Spec `719d398e-ecab-441f-9479-1fbd52cdeee2` 与 Standards `b5a71c5c-b64b-45d2-ad49-6bd11a9a4b86` 只读复审；恢复/压缩、CLI、WebFetch、Shell/共享预算未报告新增具体缺陷。对两条建议核对基线后未采纳：不改变原有 Apply 的所有权转移语义，不在未知平台绕过退出保护直接写设置。
- [completed] `review-followup-fixes`（CURSOR-1）：隔离测试先复现跨实例/同原 owner 新值被旧快照回滚、过期计划覆盖、运行中 LastError 被事件清空、Inspect 排队。Restore 增加 owner 与旧值/目标值核对；ApplyPlanned 在同一锁内核对计划并写入，StartProxy 传递同一快照，CA 后不另建计划；读取 owner 错误和内层回滚错误对外可见。状态事件保留失败信息，Inspect 使用 TryLock。补 owner 写失败/二次回滚、外部设置与无关键保留、启动入口所有权冲突、部分成功事件与显式清错测试；修复复审未发现新具体缺陷。
- [completed] `review-followup-closeout`：修改后 `go test -count=1 -timeout=120s ./internal/cursor ./internal/client`、`go test -count=1 -timeout=90s ./internal/app -run 'Tray|AutoStart'`、`node frontend/scripts/test-config-projection.mjs`、`git diff --check` 均退出 0；macOS 版本链接 warning 保留。实际系统 CA/钥匙串、真实 Wails 事件投递、真实应用重启及最终整仓门禁未执行；隔离生命周期测试仍用设置/应用替身，不能据此称完整真实链验收。Windows/Linux 自动检测和设置切换仍未支持，不能将“手动退出后重试”提示当成该平台链路已实现。
- 前次实施授权与结果：用户在完成情况 review 后要求继续，允许省略最后验证。前次先修复 `review-r1`～`review-r4`，再推进原计划剩余功能；保留每切片必要定向回归，省略最后重复全量/race/build、打包及真实应用验收，未验证范围明确记录为 test-gap。当前源码不自动部署，既有进程不重启。
- Owner：orchestrator；Priority：P0/P1；Risk：high；Design Readiness：approved（行为合同与验收锚点：`docs/main_gateway_价值功能分析与迁移方案_20260907.md` §3～§4、§9～§13；执行编排：已批准 Cursor plan `价值功能迁移执行`）。
- 目标链路：provider overflow 有界恢复、CLI 本地模型目录与凭据隔离、Shell/Bash 全链兼容、WebFetch 直连 DNS 固定/代理兼容，以及 P1 共享规则和 Cursor 正常重启；交付状态从 `planned` 推进到证据支持的 `verified-partial` 或 `accepted`。
- 基线：`gateway@2fc04e7`；开工时仅迁移方案文档未跟踪，生产代码无既有修改。已安装 Gateway-byok 监听 `127.0.0.1:18080/18090` 且 `/healthz=ok`，Cursor 正在运行；`18091/9245` 未监听。测试使用合成凭据、临时目录和隔离端口，不替换现有进程。
- 非目标：Rust/Tauri 整树、P2 Antigravity/OAuth callback、全局 transport/安全框架、配置开关矩阵、强制终止 Cursor、提交或推送。既有 blocked 的 subagent runtime wiring 与 ACP 工作包不解阻。
- Subagent 中断规则：在原任务上按 20/40/80/160/320 秒最多恢复 5 次；仍失败则保存工作区和证据，停止后续修改等待人工调整。
- [completed] `stage-0-baseline`（S）：工作包和安全基线已登记。工具链为 Go 1.26.1、Node 25.6.1、npm 11.9.0、Task 3.53.1、Cursor Agent 2026.08.11；本机无 `wails3`。Gateway-byok PID 53552 继续监听 18080/18090 且 healthz=ok，18091/9245 未监听；未停止或替换既有进程。
- [completed] `stage-1a-overflow-classifier`（S）：可信 provider overflow 分类已接入 adapter；保持 HTTP/fallback 重试语义，排除普通错误/取消。
- [completed] `stage-1b-compaction-budget`（M）：比例 reserve、完整摘要请求预算、完整轮次裁剪及一次本地 fallback 已接线；本轮 Compaction 定向回归通过。
- [completed] `stage-1c-run-recovery`（M，verified-partial）：每 Run 一次恢复、当前调用输出门禁、旧事件隔离与 RunSSE 合成链已有实现；本轮修复普通 compiler/storage 错误被标成 overflow，使用 typed terminal code 并保留 usage_persistence_error；定向 overflow/compaction/handler 回归通过。真实 Cursor/BidiAppend 起始全链及最终 race 按最新授权省略。
- [completed] `stage-2-cli-routing`（M，verified-partial）：四路径共用 builder/policy；目录固定哨兵、optional baseUrl 未设置；新增 Codex/Grok 从目录 ID 到运行时凭据 resolver/合成 provider 的测试，保留官方端点与请求时解析。真实 CLI 最终验收省略，不再把合成链缺口与此前取消混为一谈。
- [completed] `stage-3-shell-alias`（M）：`CanonicalToolName` 在 known-tool/权限之前及 started/bridge/evidence/replay/truncation 边界统一四名称；工具目录仍仅 Shell，存量磁盘历史不重写；四名称与旧历史不重派发定向测试通过。
- [completed] `stage-4-webfetch-security`（M，verified-partial）：直连固定已验证 DNS 地址、redirect 逐跳代理选择；修复非公网 IPv6 漏检和 6to4/NAT64 错误改写拨号目标，补 HTTPS Host/SNI fixture。真实直连/SOCKS-only及最终重新代理探测省略。
- [completed] `p0-verification`（范围已调整）：主控完成 P0 diff 复核和逐切片定向回归，发现的三个反例均修复。最终全量/race/build 门禁按用户授权省略；不是完整 P0 发布验收。
- [completed] `stage-5-shared-rules`（M，verified-partial）：CLI 目录/运行已共享 manager/channel ID 事实源，没有为不存在的重复新增服务；新增静态/订阅/fallback/thinking/capability/旧 ID 一致性测试。工具预算提取到 `agent/toolresult`，迁移展示及历史回放薄调用层，保留 Shell 分字段、图片回放省略、资源结构与 edit 错误差异；冻结样本和 UTF-8/幂等定向测试通过。
- [completed] `stage-6-cursor-restart`（M，verified-partial）：macOS 已识别 bundle 的一次确认、正常退出总时限、被改键快照与失败回退接入真实 StartProxy/Wails/UI；只取消辅助命令，不强杀 Cursor；启动等待实际命令结果、只发一次；并发 start 返回 busy。托盘显式启动进入主窗口同一确认流程；自动启动需确认时显示 LastError、保持原状态且不自动弹窗。重启路径账号注入在正常退出之后；回滚/重启失败可见，已有共享 backend/MITM 不停止。Windows/Linux 不虚报未运行，需改设置时提示手动处理。真实窗口/退出/启动验收省略。
- [completed] `final-review-fixes`（代码层）：Standards `37ce3f91-fc43-41bb-b3be-4b50e1931b7c` 与 Spec `050ba47b-b48e-40a2-92ef-18b451385821` 只读双轴复核；修复退出 helper 未受 context 约束、启动只 Start 误报成功、未知平台、重复启动排队、回滚失败不可见，以及托盘确认未接线和注入顺序；两轴复审报告无剩余具体 P0/P1。未将静态复审升级为真实运行验收。
- [completed] `runtime-doc-closeout`（S）：最后 client/app 托盘与启动顺序定向回归、前端配置投影及 diff 检查均通过，证据已归档 `docs/process.md`；所有本轮受管测试已结束。没有停止既有 Gateway/Cursor，因此未执行恢复/重启；未部署/commit/push，历史外部条件 blocked 工作包保持原样。
- [skipped-by-user] `final-validation`：最后根全量 test/race/build、打包、lint 及真实 CLI/Cursor/代理验收省略；代码实施完成不等于上线验收，交付状态为 `verified-partial`。
- [completed] `review-completion-current`：完成情况 review 及修复已执行；三个临时反例转为永久回归，历史失败证据保留在 `docs/process.md`。
  - [completed] `review-r1-ipv6-public`（P1；FETCH-1）：`fec0::1`、`::2`、`4000::1` 现被拒绝，固定公网/特殊用途判断用于所有目标边界。
  - [completed] `review-r2-ip-identity`（P1；FETCH-1）：仅 IPv4-mapped 等价 unmap；内嵌 IPv4 用于附加风险检查，6to4/NAT64 允许目标保持原 IPv6；拨号/Host/SNI 回归通过。
  - [completed] `review-r3-error-category`（P1；REC-3）：compiler/storage/usage 失败保留类别，真正 overflow 维持唯一非 retryable terminal；失败注入回归通过。
  - [completed] `review-r4-cli-evidence`（CLI-1，合成部分）：补实际 Codex/Grok resolver 和 fake HTTP 请求，目录不刷新凭据；真实 CLI 部分转入授权省略的 test-gap。
- 验收与回退：逐切片 RED→GREEN→REFACTOR；命令与场景按方案 §11，回退按 §10.3；fixture 不替代真实 CLI/Cursor/代理证据，任何数据完整性、真实凭据或共享服务所有权反例立即暂停相应工作包。

**upstream-leookun-99d527d-review-20260906** (in_progress, verified-partial)
- 同步边界已冻结：本地 `main` 在隔离 worktree 中快进到 `leookun/main@99d527d`，比较范围为 `543f618..99d527d`；不合并或改写 `gateway`，不 push。
- 迁移纪律：P0 只移植行为规格、失败用例和状态不变量；只有新增 Go P0 测试失败后，才允许实施对应的最小局部 P1。禁止复制、重写或 cherry-pick Rust 业务实现。
- 32 个非 merge 提交已按完整 diff 初分：
  - P0 行为测试候选：`b6fc732`/`aa47152`（OpenAI terminal/tool-call 一致性与 content filter 安全拒绝）、`24c41d0`/`0f564c0`/`c3951a4`（Grep/MCP 截断预算、终止性、准确提示）、`ab4d3ad`（稀疏 usage 字段累加不擦除）、`a42f84c`（跨模型调用复用 tool-call ID 不挂起或串联）、`fdae9c4`（context overflow→压缩→重试、摘要输入边界及恢复不变量）。
  - Gateway 已有等价能力，仅做精确验证：`e87abac`（Rule 与 Blob/checkpoint 生命周期）、`924b5e5`（Cursor 配置/模型目录/本地路由）、`1734216`（请求覆盖、兼容路由、Commit Message）；静态存在不作为行为兼容证据。
  - Rust/Cursor/desktop/plugin/CI 专属，不移植：`86c3899`、`d7578bc`、`e4b5e13`、`e0f9bd6`、`49a38ca`、`8942287`、`3aae326`、`2428614`、`edcdd77`、`2f3bffd`、`0a8934b`、`4700d3a`、`8fbbcd5`、`2cb15b5`、`c379c31`、`20fdea8`、`7484bb9`、`03b25be`、`f1ab8c7`、`d8190fc`、`9637efd`；其中配置读取失败按架构差异记录，不误判为 Gateway 功能缺失。
- 与既有 `upstream-p0-p1-safe-port-20260830` 去重：已通过的 token anchor、终态竞态和 ReadImage 投影不重复实现；本轮优先补 OpenAI terminal 矩阵、跨模型调用 ID、稀疏 usage、截断结构与 over-limit 恢复链。
- 明确排除：Rust run engine、整套 Compaction、Task/subagent runtime、desktop、插件 UI 和 Cursor 专属 wire；Gateway 已有等价能力且 P0 回归通过时不改实现。
- [completed] `p0-openai-terminal`：新增 stop+tools、content_filter+tools、length+tools、tool_calls、空白/缺失 finish reason 的结束语义和事件序列；初始测试确认终态标签、过滤拒绝和 `[DONE]` fallback 缺口。
- [completed] `p0-tool-usage-gates`：新增跨模型调用复用 tool-call ID、稀疏 usage、空 tool arguments、UTF-8 截断、MCP 图片 MIME/base64/结构和 ListMcpResources 单位测试；ID 与 MCP 结构既有行为通过，稀疏 usage、空参数和资源提示初始失败。
- [pending] `p0-compaction-recovery`：既有 token anchor、append-only、fallback summary 与预算终止测试保持通过；provider context-overflow→压缩→重试的完整链仍与 `byok-recovery-hardening-20260903` 恢复合同交叉，本轮不以单个字符串解析测试冒充端到端兼容，也不先行改恢复主链。
- [completed] `p1-failed-only`：仅修复新 P0 证明的 OpenAI Chat terminal 收口、presence-aware usage、空参数 `{}` 规范化，以及 ListMcpResources 资源计数提示；未改 Rust/run engine/Compaction/Task/desktop。
- [completed] `verification-report`：`go test -count=1 ./internal/backend/agent/model ./internal/backend/agent/bridge/exec ./internal/backend/forwarder`、同范围 `go test -race -count=1`、`go vet` 与 `git diff --check` 均通过；中文报告明确区分 P0/P1 和 provider-overflow 全链缺口。

**byok-recovery-hardening-20260903** (in_progress)
- 冻结预算化恢复合同：Cursor 只见一个逻辑模型 / 一个 RunSSE；网关吸收上游 500/503；耗尽后至多一次 terminal，`IsRetryable=false`。
- 默认预算：全链 5 HTTP attempts、每渠道 2、累计退避 8s、建连 30s、首事件 600s、流空闲 240s、整呼 7200s；最多 1 primary + 4 candidates。`maxWaitSeconds` 只累计实际退避 sleep。
- HTTP 500/502/503/504/524、可恢复 transport、TLS handshake EOF、建连/首事件超时：同渠道最多 2 次，安全窗口内再切候选。429 遵守可容纳的 Retry-After，否则跳过等待并切换。401 只走凭据刷新/账号轮换。403/其他 4xx/529/父取消/证书校验/永久 DNS/请求构建/协议解析/provider terminal：快速失败。
- 安全门：任意 raw byte / 模型事件 / 副作用后禁止重试和切换。删除全局 `providerStreamIdleTimeout`，只保留模型级活性。单渠道计划仍注入 RecoverySettings + liveness。
- 排除：熔断、按供应商拆网络、partial-output continuation、OpenAI/Anthropic 跨协议混排。
- 保留未提交的原生图片生成改动，不 stash/reset。
- 2026-09-03/04 构建安装门禁已闭合：目标仓库 `workspace-gateway-native-fix` 的 `bin/macos-arm64.dmg` 校验有效；DMG 与 `/Applications/Gateway-byok.app` 均为 `0.0.56.0`、arm64，主二进制 SHA-256 同为 `001bc8db1b85fdeec7db82a7d5f92faf739f0690d093b4532f35eed94c87baf3`，应用签名校验通过。运行进程监听 `127.0.0.1:18080` / `127.0.0.1:18090`，`/healthz` 返回 `200 ok`；启动日志无配置归一化、panic 或 fatal 错误。
- 真实 Cursor 正常流量冒烟已闭合：同一 Cursor request 只观察到 1 次 RunSSE 入站/转发，内部多个 model call 均成功，未产生 terminal provider error。结构化事件已包含 failure/action、链级及渠道级 attempt/wait、安全门和最终动作字段；对最新 trace 的敏感 key 与常见 Bearer/API key 值模式扫描均为 0。
- 已安装应用的本机 Gateway 受控故障验收已闭合：HTTP 500 与 503 场景均在主渠道恰好失败 2 次后切备用并成功；TLS 握手阶段 accept-then-close 场景在客户端表现为 transport reset，主渠道 2 次后切备用并成功；partial-output 场景只命中主渠道 1 次，发布 `PARTIAL-CANARY` 后以 `output_observed` 关闭安全门，未请求备用；预算耗尽场景按主渠道 500×2、备用 503×2 消费全链 4 attempts，SSE 流体只有 1 个 `provider_error` terminal。五条 trace 的恢复字段完整，临时 key、Authorization/正文类字段和 canary/成功正文命中均为 0。
- 临时在线配置已完整回滚：`config.yaml` SHA-256 与原始备份同为 `a324d89d9b6eaf30b387e85a51924a03f6c13bcef1e2df1cef194d3a022e9d37`，模型数回到 32，临时模型为 0，`18091` 与 `18180–18183` 均已释放；正常应用继续监听 `18080/18090`，`/healthz` 返回 200。
- 当前交付仍为 `verified-partial`：上述受控请求直接经过本机 Gateway，不等同于 Cursor UI/RunSSE 故障注入。仍待用真实 Cursor 单 Run/Agent 验证内部恢复不创建新生命周期、任意输出后不重放，以及预算耗尽时 Cursor 最多收到一次 terminal provider error。

**gateway-provider-stream-truncation-20260830** (in_progress, verified-partial)
- 已完成共享 Provider HTTP 流观测第一阶段：`provider_stream_finished` 可投影最终 HTTP attempt 的协议、编码/自动解压、连接复用、原始字节数、最后 SSE 事件元数据、typed close cause 和累计零字节恢复次数；response ID 仅保存短哈希，不落完整正文或凭据。
- 已完成 OpenAI Responses 与 Anthropic 终态收口：支持 Responses CRLF、多行 `data:`、最后事件后立即 EOF；`[DONE]`、`response.completed`、`message_stop` 成功，failed/cancelled/incomplete/显式 error 为协议终态，无合法 marker 的 EOF 保持 `StreamTruncatedError`。
- 已修复实测事故中 `response.completed` 后继续等待连接关闭的问题：Responses 在完成最终内容和工具投影后立即进入统一收尾，不再被终态后的 `unexpected_eof` 覆盖为 `stream_decode`；回归覆盖连接不关闭、终态后异常 EOF、工具与 turn 完成事件仅发布一次。独立的连续 HTTP 502 保持既有三次 attempt 边界，本轮不扩大重试，也不执行 Transport A/B。
- 已完成零 provider 字节且零 `ModelEvent` 的同渠道单次恢复；typed EOF、unexpected EOF、TCP reset、HTTP/2 stream reset/GOAWAY 在预算内最多新增一次请求。任意原始字节、模型事件、4xx、认证/配额、JSON 语法错误、取消、deadline 或明确协议终态均不重放。
- 既有纯文本 checkpoint continuation 保持默认关闭、每 turn 一次及工具/checkpoint/pending interaction/subagent/Gateway 路径禁用；本轮只增加“非瞬时截断错误禁止续写”，未扩大部分输出恢复范围。
- 已增加默认关闭的 Provider-only Transport 单变量实验：环境变量 `CURSOR_BYOK_PROVIDER_TRANSPORT_PROFILE` 支持 `auto`（默认）、`http1`、`no_compression`、`fresh_connection`、`direct`；未知或组合值回退 `auto`。`direct` 只绕过显式 HTTP/SOCKS 代理，不能绕过操作系统 TUN。
- 验证：`go test -count=1 ./internal/netproxy ./internal/backend/...`、`go vet ./internal/...` 与 `git diff --check` 通过。额外的 `go test -count=1 ./internal/...` 中，受影响包均通过，但完整命令被两个外部 CLI smoke 阻断：Codex 临时插件目录清理竞态，以及本机 OpenCode 数据库缺少 `name` 列；未修改产品代码绕过环境问题。
- 待完成：按单变量实际运行并比较缺失终态率、零字节 reset 率与延迟；Cursor→Gateway 边界故障注入；确认无重复工具副作用后再评估是否扩大纯文本 continuation。HTTP/1.1 不作为默认修复，full/provider 日志采样后必须降级。

**subagent-readonly-reschedule-20260830** (blocked, foundation-partial)
- 真实 Cursor fixture 核查结论：当前生产链没有可稳定关联 parent/run/child/attempt/terminal 的 typed failure producer，也没有消费完整证据并驱动 relaunch 的 consumer；禁止按时间窗口或错误文本推断，runtime relaunch 必须保持 blocked。
- 已冻结未来兼容边界：顶层 `subagentReschedule.enabled` 默认关闭，旧配置缺失关闭；首版仅 readonly Task，总计最多 3 attempts。Settings 固定禁用，前端保存强制 `false`，当前配置不触发自动重调度。
- [completed] `config-contract`：正式配置类型、默认关闭和旧配置兼容；显式 true 仅保留未来合同，不代表当前运行时消费。
- [completed] `attempt-ledger-foundation`：`attempts.json` 版本化 ledger、原子写入/读取及 attempt 身份基础已落地；不等于存在可靠触发证据或在线 relaunch。
- [completed] `policy-foundation`：readonly-only、最多 3 attempts、typed evidence fail-closed 的策略基础与回归已落地；无稳定 typed 关联时结论只能是 suppressed/blocked。
- [completed] `settings-design`：Settings 禁用占位、默认 false 投影及四状态机、重启、回滚和费用边界文档。
- [blocked] `runtime-wiring`：生产 settings source 与在线 relaunch 不接线；等待稳定 typed producer/consumer 合同成立后另行实施。
- [blocked] `typed-fixture`：等待真实 Cursor 提供可稳定关联的 typed failure fixture，覆盖错误、取消、断连、重启与 `resume_agent_id`。
- [pending] `online-relaunch`：只有 typed fixture 解阻并证明 parent 单次结果、attempt 独立 usage/费用后才能进入；当前不得声称解决。

**upstream-p0-p1-safe-port-20260830** (completed, verified)
- 范围保持隔离：仅在现有 Go/Wails 架构内移植行为规格与回归测试，未引入 Rust/Tauri、SQLite、会话架构迁移、WebFetch 全文缓存或观测批处理。
- [completed] `token-anchor`：自动压缩锚点改为最近一次成功 Provider 调用的 `InputTokens + CacheReadTokens + CacheWriteTokens`，不含输出 token、不跨调用累计；覆盖大输出、缓存读写、后续调用覆盖和 partial usage 场景。
- [completed] `ttfr`：保留网络首字节、首模型事件和最后有效内容的既有语义，独立记录首个有效文本、思考或结构有效工具事件时间，并投影 `first_effective_content_at` / `ttfr_ms` 到脱敏观测链路。
- [completed] `tool-sse-regression`：未知/已移除工具回归覆盖 started、失败结果、completed 和 checkpoint 闭环；终态竞态测试发现并修复 `Cancel` / `Fail` 可覆盖既有终态及重复发布 End 的缺陷，late cancel 在写入 metadata/stats 前短路。未为未知工具引入空 `ToolCall` 持久化。
- [completed] `verification`：独立审查后修复上述终态缺陷；`go test -count=1 ./internal/backend/agent/model ./internal/backend/forwarder`、`go test -race -count=1 ./internal/backend/forwarder`、`go vet ./internal/backend/agent/model ./internal/backend/forwarder`、日志分析器 sanitize 测试和 `git diff --check` 通过；未 commit、未 push。

### ⏸️ 暂停工作包

**multi-client-acp-phase5-20260826** (blocked)
- 状态：等待真实 ACP Client/编辑器到位
- 风险：high
- 条件：提供能连接本地 stdio bridge 的真实 ACP Client/编辑器，或明确授权并提供其版本、启动方式和临时 HOME/workspace 验收边界

### ✅ 最近完成

**provider-history-sanitize-20260831** (completed, verified)
- 发送边界按目标来源身份判断 opaque 兼容性；跨身份剥离 opaque reasoning / Responses item 元数据，保留文本与工具链。
- HTTP 400 脱敏摘要进入诊断；最终编码请求体预检 32MiB，`request_build` 不重试、不 fallback。
- 复审修复 provider done origin 清零导致落盘丢失、tool flush 顺序和 safety suppression。
- 验证：`/opt/homebrew/bin/go test -count=1 ./internal/backend/agent/model ./internal/backend/agent/prompt ./internal/backend/forwarder`、同范围 `go vet`、`git diff --check` 通过；未 commit、未 push。

**upstream-v0.1.5-review-20260830** (completed, verified)
- 已核对正式版 `v0.1.5@b807608`、最新 `upstream/main@9120b90` 与此前基线 `76003a9`；正式版后的主线只删除 `server_backup/**`，没有新增核心运行时能力。
- 本地 `main@305b108` 与上游已分叉；隔离 worktree 的显式 merge 出现内容、修改/删除和目录迁移冲突，已按 Runbook `git merge --abort`。`main`、`gateway@534ffc0` 与工作树均未改变，无提交、无 push。
- 结论：P0 先加固现有 Rule 文件持久化；P1 独立补 managed Codex 缓存亲和；P1 在显式 opt-in 与隐私合同下设计 Rule 离线日志/镜像；P1/P2 仅增加官方/BYOK 展示分组。
- 已确认无需重复移植官方/BYOK 切换、双向子代理、普通 OpenAI/Anthropic cache、Provider 空闲超时、Codex/Grok 主链路、额度与账号轮换；拒绝整体移植 Deno 插件、Rust provider/router 和 conversation runtime。
- 上游性能、内存和缓存命中宣传缺少可复现 benchmark；完整审阅、最小移植单元和验证口径已写入 `docs/prd_cursor_byok_当前功能与上游差异.md` §16。

**macos-build-0.0.52.5-20260830** (completed, verified)
- 用户要求编译 Apple Silicon macOS 版本，版本号 `0.0.52.5`。
- 已将 `build/config.yml`、darwin Info.plist/Info.dev.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.5.md` 对齐到 `0.0.52.5`；保留构建前已有的工作树改动。
- 本版本包含共享 Provider 流截断第一阶段治理：OpenAI/Anthropic 终态收口、零输出安全恢复、最终 attempt 观测字段、`CURSOR_BYOK_PROVIDER_TRANSPORT_PROFILE` Transport 单变量实验。
- 首次尝试误用 Go 1.26.3 且缓存被清空，在 8GB 内存/swap 压力下大文件编译停滞；改用 Go 1.25.0 并加 `GOGC=20` 控制编译器内存后完成 `task build`，产物 `bin/release/0.0.52.5/cursor-byok-0.0.52.5-macos-arm64.dmg`（24,031,370 bytes）。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.5`；二进制注入版本 `0.0.52.5`；最低 macOS `10.15.0`；Mach-O arm64；codesign 有效；`hdiutil verify` VALID；SHA-256 `b131348e7afd154dd05eae6f00968d971401d92f10b26efeb109ba49b2ee3153`；`SHA256SUMS` 校验通过。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未创建提交。

**access-ui-screenshot-feedback-20260830** (completed, verified-partial)
- 按截图精简 Codex/Grok 接入页：删除标题下的冗余说明，把“订阅授权”、账号数和操作说明合并到同一标题行，移除账号卡片底部重复的导入与设备码授权按钮，保留顶部入口和“清除全部”。
- Gateway“启动 Gateway”按钮仅在可点击时使用与保存按钮一致的主色高亮；禁用时保持默认弱化样式。
- 修复独立 Vite 预览空白页：平台检测缺少 Wails 后端时回退为非 Windows，避免顶层导入失败阻断 Vue 挂载；桌面后端能力在独立预览中仍不可用。
- 验证：前端配置投影门禁和生产构建通过；Playwright 在 `http://127.0.0.1:4173/#/access?client=codex` 确认页面可挂载并显示调整后的授权布局。独立 Vite 预览仍会因没有 Wails 后端产生 `/wails/runtime` 404 和业务调用错误，未做真实 Wails 桌面窗口点击。

**macos-build-0.0.52.4-20260829** (completed, verified)
- 用户要求汇总更新情况作为 git 提交说明（版本号 `0.0.52.4`）、提交并 push，然后编译 Apple Silicon macOS 版本。
- 已将 `build/config.yml`、darwin Info.plist/Info.dev.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.4.md` 对齐到 `0.0.52.4`；保留构建前已有的工作树改动。
- 本版本新增 Gateway 可用性测试（启动后自动本机 HTTP 探测 `/v1/models`，卡片展示入口状态、模型数量与延迟）、token 复制/轮换改用 Wails 原生剪贴板、极简使用引导；managed Codex 请求体白名单 fail-closed 剥离 `previous_response_id`，static OpenAI 保留原请求体。
- 使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成 `task build`，产物归档为 `bin/release/0.0.52.4/cursor-byok-0.0.52.4-macos-arm64.dmg`（24,015,566 bytes）。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.4`；最低 macOS `10.15.0`；Mach-O arm64；codesign 有效（`codesign --verify --deep --strict` OK）；`hdiutil verify` VALID；SHA-256 `ba3321f551a74127dd760a9a40703e22492a5c6a93b5887eaf64ac41185c6a62`；`SHA256SUMS` 校验通过。
- 已提交并 push（提交 `9c4db4f`）；未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release。

**subscription-account-list-modal-fix-20260829** (completed, verified-partial)
- 修复 Codex/Grok 多账号列表在固定高度卡片中被裁切的问题：账号列表成为独立纵向滚动区，卡片标题和底部操作栏保持可见。
- 修复 sub2api 确认导入后选择弹窗未关闭的问题：成功路径不再调用受 `busy` 状态保护的手动关闭函数，而是直接重置并关闭弹窗状态。
- 验证：前端配置投影门禁覆盖滚动容器和成功关闭路径，生产构建与 `git diff --check` 通过；未做真实 Wails 窗口点击，未重新打包 DMG。

**macos-build-0.0.52.3-20260829** (completed, verified)
- 用户要求编译 Apple Silicon macOS 版本，版本号 `0.0.52.3`。
- 已将 `build/config.yml`、darwin Info.plist/Info.dev.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.3.md` 对齐到 `0.0.52.3`；保留构建前已有的工作树改动。
- 使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成 `task build`，产物归档为 `bin/release/0.0.52.3/cursor-byok-0.0.52.3-macos-arm64.dmg`（24,019,575 bytes）。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.3`；最低 macOS `10.15.0`；Mach-O arm64；codesign 有效；`hdiutil verify` VALID；SHA-256 `1b63be249e0bafb6955d2deedf1e29d81d17309123b4597d489ddbcd9c11f635`；`SHA256SUMS` 校验通过。
- 本版本包含 Codex 多账户池、sub2api JSON 多选导入和逐账户用量刷新桥接修复。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未创建提交。

**codex-previous-response-stateless-boundary-20260829** (completed, verified)
- Managed Codex ChatGPT Responses 在请求体白名单边界一律剥离 `previous_response_id`，无法验证引用所属账号时 fail-closed；static OpenAI Responses 保持原请求体不变。
- 当前正常 Codex 请求发送完整 `input`，生产路径目前不生成 `RequestBodyOverride` / `previous_response_id`；回归覆盖 managed Codex 删除与 static Responses 保留，未创建提交。

**subscription-sub2api-import-20260829** (completed, verified-partial)
- 修复 Codex 单账户“刷新用量”调用了未绑定 Wails 方法的问题：桥接层补齐 `RefreshSubscriptionAccountUsage` 转发。
- Codex 与 Grok 接入页增加 sub2api JSON 导入入口；后端先按当前页面类型过滤 OAuth 账号，前端弹窗展示候选账号并允许多选后导入。Codex 仅接受 `openai/codex + oauth`，Grok 仅接受 `grok/xai/x.ai + oauth`，其他平台、认证类型或缺少 access/refresh token 的项目跳过。
- 导入只读取源文件并写入本应用私有凭据池；重复账号更新原账号，Codex/Grok 都不因后续导入抢占当前激活账号。
- 验证：订阅认证 Go 测试（包含用户提供 sub2api 文件的只读解析、provider 过滤、选择导入和源文件不变）、Wails bindings 生成、前端配置投影和生产构建通过；桥接包完整测试因大型生成文件编译超过两分钟后终止，未使用真实外部订阅请求，未做 Wails 窗口点击。

**codex-multi-account-rotation-20260829** (completed, verified-partial)
- Codex 已升级为兼容旧单账户文件的多账户池；模型配置仍只保存 `credentialSource=codex`，第一个账户自动激活，后续账户作为备用，重复导入更新原账户。
- 已实现逐账户激活、删除、双窗口用量、401 定向刷新和零输出明确配额错误下的幂等轮换；并发重复失败不会连续跳过备用账户。
- 验证通过：`go test ./internal/subscriptionauth -count=1`、`go test ./internal/backend/agent/model -count=1`、`go test ./internal/backend/server/config -count=1`、前端配置投影测试和生产构建。
- 已重新生成并核验包含本功能的 Apple Silicon macOS `0.0.52.2` DMG：23,070,403 bytes，SHA-256 `467bb52b1a04ef421f7df6ae1cf09e2e15cf5cf8346e594a6f38336c956d7f7d`；未使用真实 Codex token，未做 Wails 窗口点击，未创建提交。

**macos-build-0.0.52.2-20260829** (completed, superseded)
- 用户要求编译 Apple Silicon macOS `0.0.52.2`。
- 已将 `build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.2.md` 对齐到 `0.0.52.2`，发布说明补充订阅模型测试端点修复；保留构建前已有的工作树改动。
- 已重新生成包含当前 Codex 多账户功能的安装包，旧产物校验已被新产物取代：`bin/release/0.0.52.2/cursor-byok-0.0.52.2-macos-arm64.dmg`（23,070,403 bytes），SHA-256 `467bb52b1a04ef421f7df6ae1cf09e2e15cf5cf8346e594a6f38336c956d7f7d`。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.2`；最低 macOS `10.15.0`；Mach-O arm64；codesign 校验有效；`hdiutil verify` VALID；`SHA256SUMS` 校验通过。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未创建提交。

**subscription-model-endpoint-normalization-20260829** (completed, verified-partial)
- 根因：模型测试只把订阅 token 注入请求，却继续使用模型配置中的接口地址；当 Codex 配置填写本应用 Gateway 入站地址 `127.0.0.1:18091/v1` 时，请求带 ChatGPT token 回到本地 Gateway，而不是发往 Codex Responses 上游。
- 修复：OpenAI 类型的 Codex 订阅固定归一化为 `https://chatgpt.com/backend-api/codex/responses` + `/v1/responses`；Grok 订阅固定归一化为 `https://api.x.ai/v1` + `/v1/chat/completions`。前后端保存、重载和测试使用同一规则，订阅模式下接口地址和端点不再允许编辑；静态 API key 配置保持自定义地址行为。
- 验证：后端 Codex/Grok 端点归一化测试、Codex Responses 请求头/请求体测试、客户端订阅模型测试链路、前端配置投影和生产构建通过；未使用用户真实 token 请求外部上游，未重新打包 macOS 安装包。

**macos-build-0.0.52.1-20260829** (completed, verified)
- 用户要求编译 Apple Silicon macOS `0.0.52.1`，并直接在 `bin/release` 下创建版本目录和带版本号的产物。
- 已将 `build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.1.md` 对齐到 `0.0.52.1`；保留构建前已有的工作树改动。
- 使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成构建，产物为 `bin/release/0.0.52.1/cursor-byok-0.0.52.1-macos-arm64.dmg`（24,001,624 bytes）。
- 校验：应用短版本与 bundle 版本均为 `0.0.52.1`；最低 macOS `10.15.0`；Mach-O arm64；adhoc codesign；`hdiutil verify` VALID；SHA-256 `6de50cd36b295e7a31d6c58af4d7bfd93dfd3d18e80981874d286aae7ccb8271`。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未创建提交。

**subscription-auth-access-layout-20260829** (completed, verified-partial)
- 按用户确认截图移除接入页底部混合的 Codex/Grok 全局授权区，把 Codex `auth.json` 导入、设备码授权、用量与清除操作全部放入 Codex 接入详情，把 Grok 设备码、账号切换、删除与用量全部放入 Grok 接入详情。
- 接入中心顺序调整为共享入口、Cursor、Codex、Grok、Anthropic；Anthropic 保留计划中占位，旧 `client=claude` 查询兼容映射到 Anthropic。
- 更新模型页提示、Grok/Anthropic 品牌样式、四套语言目录和前端结构门禁；未修改认证协议、凭据存储或请求链路。
- 验证：前端生产构建、配置投影测试、接入路由测试 5/5 和全局补丁格式检查通过；已重新打包并核验包含本次布局修复的 macOS DMG，未做真实 Wails 窗口点击。

**macos-build-0.0.52.0-20260829** (completed, verified)
- 用户要求编译 Apple Silicon macOS 版本，版本号 `0.0.52.0`。
- 已将 `build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.52.0.md` 对齐到 `0.0.52.0`，并保留构建前已有的跨平台元数据改动。
- 本机使用项目现有 Go 1.25.0 工具链和低并发参数完成 `task build`；布局修复后重新打包的 `bin/macos-arm64.dmg` 为 24,002,569 bytes。
- 校验：Info.plist 版本为 `0.0.52.0`；最低 macOS `12.0.0`；Mach-O arm64；adhoc codesign；`hdiutil verify` VALID；SHA-256 `8cfa4d7c6797d8bae83603e24012b24513288d23c536d016388e493b0981511a`。
- 未做 Developer ID 签名或 notarization；未构建 Intel 包，未发布 GitHub Release，未更新 README 当前稳定发布。

**gateway-subscription-auth-20260828** (completed, verified-partial)
- 完成 Codex 设备码与 `auth.json` 双入口、Grok 设备码与账号管理、私有凭据存储、managed credential 请求链路、usage/模型发现/轮换、Wails/Vue 独立订阅区和 token 脱敏。
- 修复 Codex usage 与会话窗口未持久化、Grok 耗尽账号仍可解析、设备授权终态清理、轮询失败等待态和 `pollToken` 脱敏缺口。
- Go 定向回归、除 OpenCode 外的完整 internal 回归、vet、前端构建/配置投影/路由测试及 `git diff --check` 通过；race 已通过 `subscriptionauth` 和 `model`，`forwarder` 因 Go 1.26.3 对大型生成文件持续编译超过 12 分钟而终止，未得到结果。
- 完整 internal 回归唯一失败是本机 OpenCode CLI 数据库缺少 `name` 列，属于外部安装环境问题；未修改产品代码绕过。
- 已确认 `/Applications/Gateway-byok.app` 正在运行并监听 `127.0.0.1:18080`、`127.0.0.1:18090`，因此未重复启动 `task dev` 或替换现有实例。

**macos-build-0.0.50.3-20260828** (completed, verified-partial)
- 用户要求编译 macOS 版本，版本号 `0.0.50.3`
- 已将 `build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/0.0.50.3.md` 对齐到 `0.0.50.3`
- 本机 Apple Silicon 执行 `PATH=/Users/yaogj/go/bin:$PATH task build`，产出 `bin/macos-arm64.dmg`（约 22 MiB）
- 校验：Info.plist `CFBundleShortVersionString`/`CFBundleVersion` = `0.0.50.3`；二进制注入版本 `0.0.50.3`；Mach-O arm64；adhoc codesign；`hdiutil verify` VALID；SHA-256 `ecf330bd61fbcac3133028dfb518df56722d57f6d075c573f22608889ba89ef9`
- 未做 Developer ID 签名或 notarization；未发布 GitHub Release；未更新 README 当前稳定发布（仍为已发布的 v0.0.50.1）

**ui-brand-icons-light-default-20260828** (completed, verified-partial)
- 用户确认三张附件区域的 UI 设计，并批准应用到真实系统；范围仅限 Codex 接入详情、Claude Code 接入详情和模型管理主体
- Codex 与 Claude Code 接入详情已应用授权管理布局；因真实授权 API 尚未接通，使用 0 账号空状态并禁用授权、同步、测试与保存操作，不写入演示账号
- 模型管理已应用提供商筛选、彩色提供商标识、清除全部确认和精简操作菜单；沿用真实模型数据与既有导入、导出、测试、编辑、保存能力
- 接入中心的 Codex 与 Claude Code 使用对应公司品牌图标；模型配置导入、导出按钮不显示图标
- 浅色保持为新配置默认主题，深色与跟随系统仍可选；未修改主导航、总览、Gateway、Cursor、设置及其他区域
- 2026-08-28 续验证：`npm run build --prefix frontend` 通过；`npm run test:config-projection --prefix frontend` 通过；`node --test frontend/src/router/access.test.js` 通过；相关文件 `git diff --check` 通过；`frontend/src` 无截图演示邮箱。配置投影测试已改为允许禁用的「添加授权」空态，并覆盖完整提供商 Tab。本机无 Wails 窗口视觉点击。

**model-save-identity-remap-20260828** (completed, verified-partial)
- 修复模型页保存身份变更与类型切换问题
- 渠道 ID 精确 remap、删除/新增不自动配对、类型切换保留 modelID
- 定向测试通过；未做 Wails 窗口点击

**interrupt-recovery-error-propagation-20260827** (completed, verified-partial)
- HTTP 524 零输出 allowlist、automatic-continuation、typed error propagation、stream diagnostics
- 实现与定向验证完成；真实 Cursor 多段 continuation/TCP-TLS 故障注入仍是证据缺口
- Design Gate: DESIGN-INTERRUPT-RECOVERY-001 approved

### 📋 Pending Work (当前执行队列)

- [completed] `rule-storage-hardening`：Rule 目录/文件私有权限、symlink 与路径逃逸拒绝、唯一临时文件原子替换、容量上限以及 Rule/docs index reconciliation 已实现并通过定向、race、vet 与 Windows 交叉构建验证；Windows 实机 ACL 行为仍归最终发布门禁。
- [completed] `managed-codex-cache-affinity`：在继续剥离 `previous_response_id` 的前提下，已实现稳定账号隔离的 HMAC 域分离、私密安装密钥、`prompt_cache_key` 与三个保留 header 注入，以及 control/prompt_key/full profile；fake upstream、脱敏缓存观测和收益 A/B 作为独立后续工作，不把字段接线宣称为性能收益。
- [pending] `managed-codex-cache-verification`：补 fake upstream、adapter HTTP retry、401 同账号刷新、quota 账号轮换、override/custom header 契约、脱敏缓存观测和 control/prompt/full 同负载 A/B 门禁。
- [pending] `rule-offline-journal-design`：P0 完成后设计显式 opt-in 的 Rule 离线 journal、本地镜像、回放幂等、冲突恢复和跨来源内容去重；默认不得上传 Rule 正文。
- [pending] `model-display-grouping`：仅增强官方模型、静态 BYOK、Codex 与 Grok 的模型列表展示分组，不改现有路由、凭据和子代理主链路。
- [pending] `investigation-case-library`：持久调查案例库、脱敏证据快照、状态机、版本关联和修复后复验
- [pending] `ai-evidence-bundle`：外部 AI 调查包导出、结构化分析结果导入和不可信日志数据边界
- [pending] `client-analyzer-launcher`：客户端日志采集区接入跨平台分析器检测、启动按钮和未安装引导
- [pending] `distribution-verification`：分析器独立发布、客户端归档隔离及跨版本/跨模块/跨平台验证证据

---

## Active Work Package

WORK_PACKAGE_ID: gateway-subscription-auth-20260828
STATUS: completed
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: approved（以 `.cursor/plans/gateway_subscription_auth_6eff22f5.plan.md` 为实施锚点；参考协议以 `/Users/yaogj/Downloads/works/gitprojects/cursor-byok` 的只读核对结果为准）
DELIVERY_STATUS: verified-partial

### CONTEXT

- 本地 `gateway` 已快进到 `89524e3`，包含 subscriptionauth 领域、managed credential 基础接线和订阅授权 UI 的 WIP。
- 已核实参考项目没有 refresh-token 刷新实现或独立 OAuth fixture；Codex refresh、401 单次安全重试必须先由 fake HTTP fixture 固定协议，再进入运行时。
- 当前工作树原有的跨平台构建元数据改动与本工作包无关，继续保留且不修改。
- 已运行的 `/Applications/Gateway-byok.app` 监听 `18080/18090`；实施和测试期间不替换、不重启，最终通过后再启动仓库 `task dev`。

### ACTIVE SLICES

- [completed] `auth-baseline`：冻结 Codex/Grok 协议 fixture、核对 WIP 行为和错误分类。
- [completed] `auth-domain`：补齐私有存储、刷新、账号状态和 resolver 合同。
- [completed] `credential-integration`：完成 ChatGPT headers/body、401 安全刷新、Grok quota 轮换和 fake upstream 测试。
- [completed] `subscription-auth-ui`：完成独立订阅区、多语言、Wails 脱敏和配置投影测试。
- [completed] `usage-routing-verification`：完成 usage、模型发现和同 provider 轮换验证。
- [completed] `auth-security-release`：完成 token 清洗、Go/前端/vet 回归、review 修复和运行状态确认；race 的 `forwarder` 包保留环境性验证缺口。

### DEFINITION OF DONE

- Codex 设备码与 `auth.json` 导入/恢复、Grok 设备码与账号管理闭环可用；源凭据文件不被修改。
- Cursor backend 与独立 Gateway 共用 resolver；static/OpenAI/Anthropic 现有路径无回归。
- 401 最多刷新重试一次；quota 仅在零输出窗口内同 provider 轮换；两者共享既有 attempt budget。
- token/JWT/auth.json 原文不进入配置、日志、观测、artifact 或 Wails/Vue 状态。
- 计划要求的 Go、race、vet、前端构建、配置投影和必要桌面验证均有本次运行证据。

## Paused Work Package

WORK_PACKAGE_ID: multi-client-acp-phase5-20260826
STATUS: blocked
RISK_LEVEL: high
OWNER: orchestrator
DESIGN_READINESS: blocked（本机仅发现 ACP Agent 服务端，缺少可端到端验收的 ACP Client/编辑器）
DELIVERY_STATUS: blocked

### CONTEXT

- 阶段 2 Chat tools 已通过 OpenCode 1.2.25 隔离真实工具循环；阶段 3 `/v1/responses` 已通过 Codex 0.144.4 隔离真实 `exec_command` 工具循环；阶段 4 独立生命周期与 metadata-only 入站观测已实现并自动验证。
- 本机可用的 `agent acp`/`opencode acp` 是 ACP Agent 服务端，不能冒充 ACP Client；未读取用户凭据或 prompt，也没有可验证 initialize/session/prompt/cancel/update 的真实编辑器。
- 在真实 ACP Client 到位前，禁止声称 ACP v1 完成，禁止把服务端自测当成端到端验收，且不提前抽取共享 Agent Core。

### ACTIVE SLICES

- [blocked] `gateway-acp-v1`：等待一个真实 ACP Client/编辑器及其允许的隔离验收环境。

### UNBLOCK CONDITION

- 提供能连接本地 stdio bridge 的真实 ACP Client/编辑器，或明确授权并提供其版本、启动方式和临时 HOME/workspace 验收边界。届时先作 metadata-only 探针，再冻结协议与权限合同。

## Completed Work Packages (Historical)
