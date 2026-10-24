# 项目实施进展

> 本文档记录项目当前状态、待完成事项、版本完成情况和时间线，是面向维护者的进展摘要。
> 详细实施阶段、逐步要求、路径范围和验收标准以 `[task/todo.md](../task/todo.md)` 为准。
> 每个阶段通过验收后，必须在同一次收尾中更新 `task/todo.md` 的执行结果和本文档的进展归档；没有验证证据的事项不得标为完成。
> 当前状态依据：仓库、Git 历史、项目计划及 2026-08-22 / 2026-08-24 / 2026-08-25 / 2026-08-26 会话运行记录。

## 一、待完成的内容

### 日志配额恢复与上下文压缩续接复核（2026-10-23；源码与本地链路已验证）

整合负责人：主会话。前轮起始工作树干净，本轮二次评审沿用前轮未提交改动；修改尚未提交或发布。用户导出只读，未操作真实服务和会话。最终桌面/实机验收仍待授权候选安装。以下前轮记录保留，当前补充修复与证据见本节末尾“二次评审”。

**现场证据与结论**

- `/Users/yaogj/Downloads/logs 3` 约 229 MiB，其中 diagnostics 162 MiB、traces 42 MiB、app 25 MiB。导出不是截图中完整的 2 GiB 现场，不能把导出大小当现场配额用量。
- app 中 63,096 条普通「链路事件」合计 25,542,991 字节，表明结构化事件又被投影成可读日志，重复占用同一预算。diagnostics 中 `tls_handshake_failed/client_unknown_ca` 104,102 条，为明确的重复网络噪声。基础 traces 还包含大量 `llm_response_chunk`、`send_message` 和双向帧。
- 原清理只处理 closed traces；当前 open 会话及应用/诊断历史占额时，删除 0 session / 0KB 可以反复出现。新实现保留当前写入路径、清理可回收历史，且在同一预算锁下与轮转同步；未知 schema、坏 manifest、链接目录/文件保守跳过或报错。
- 主 trace 同一会话在 UTC 03:06:00、03:14:08、03:14:34 三次模型调用出现 HTTP 400 并结束。本轮没有重新访问真实上游，导出仍缺具体400错误体；本文件历史调查已记录 sol 拒绝 `reasoning.effort=none` 的最小真实对照。当前代码已有摘要最低合法等级 `low` 修复，本轮通过真实适配器+模拟HTTP上游重新验证，不将历史实验冒充本轮真实回放。

**实施与安全边界**

- 基础模式省略逐块/逐帧/心跳记录；普通链路 info 不再写两份。核心请求结束、模型摘要/终态和告警错误仍保留。仅无会话/模型/工具关联的同类证书未知CA噪声按目标聚合；每30秒输出重复计数，正常关闭补写尾批，最多128个聚合键；完整模式不聚合。突发退出前尚未刷出的计数仍可能缺失，不能视为持久审计计数。
- events 超10 MiB轮转；预算内优先回收可清理历史，核心错误在普通分区不足时可封存当前分片后继续写，诊断分区仍独立。app只有单分片时也可在持锁关闭后回收自己的封存文件，避免「保留最新」使它永久停写。未知数据或物理磁盘耗尽仍可能阻止写入，状态不得伪报正常。
- 手动清理覆盖 closed traces、历史 app/diagnostics、已知 open traces 的封存片段；当前 events/manifest及会话业务数据保留。旧版本遗留 open 会话的未轮转 events 不自动强删；需要明确安全生命周期证据才可扩展。清理即使没有 traces 目录也能处理 app 历史。分析器补齐轮转分片读取，按既有时间/序号排序，拒绝 `events-index.jsonl` 等辅助名。
- 压缩新增独立 `compaction_summary_started/completed/failed/canceled` 记录，保留阶段、关联、耗时和HTTP状态，不记录输入、摘要或上游错误正文。新增同会话失败后继续的永久回归，验证新回合不继承失败/恢复一次性状态、摘要一次发布、工具不重复、摘要low后主回合恢复high；未通过吞掉400或无限重试规避问题。
- 清理提示与结果文本同步四语种，小于1KiB按字节显示，避免实际释放字节被四舍五入成0KB。

**本轮验证与适用条件**

- 已实际观察红→绿：分析器轮转历史缺失（只读到1/3条）、普通链路info重复写app、摘要生命周期记录缺失、无traces清理失效、链接目录误删、TLS噪声100条未聚合、目录和分片双重扣减配额、仅一个app分片永久配额阻塞。上述均为临时目录/模拟接口，未改原始材料。
- `go test ./... -count=1 -timeout=300s` 最终退出0，输出 `/tmp/cursor-byok-log-recovery-final-tests.log`。首次全量曾失败：关联测试在basic中要求持久化双向原始帧，与新的采集合同冲突；将该原始帧关联测试改用full，既有basic上下文关联测试保留，随后全量通过。旧失败记录为 `/tmp/cursor-byok-log-recovery-tests.log`，不抹去失败证据。
- 最后app/配额修改后 `go test -race ./internal/observability ./internal/logsink ./internal/logger -count=1 -timeout=120s` 通过；压缩/host未变实现的 `go test -race ./internal/backend/forwarder ./internal/backend -run 'TestCompaction|TestLogObservabilityEvent' -count=1 -timeout=180s` 通过。
- `go vet ./internal/observability ./internal/logsink ./internal/backend/forwarder ./internal/backend` 退出0；独立 `tools/log-analyzer` 模块 `go test ./... -count=1 -timeout=120s` 全部通过；`npm run build` 和 `npm run test:config-projection` 通过；`git diff --check` 通过。macOS链接目标版本、前端包尺寸和Node localstorage警告保留，不等于相应旧系统已实测。
- 初轮独立评审指出分片读取、未知格式保护、活跃文件保护与状态恢复缺口，已修正并补测；续接评审工具返回503，没有成功返回最终复审结论，不把其状态记为通过。主会话补查并修复配额双计数和单app分片阻塞，承担整合责任。

**二次评审：压缩中、压缩后及下一回合的恢复（2026-10-23）**

- 已确认前轮“400后继续”测试用 `Prewarm` 后手工设置运行状态，不能证明正常新回合入口。已删除该简化分支，保留 sol 参数兼容专项；用 `TestCompactionRecoveryThroughRunIntentHTTP` 经正式 `handleRunIntent`、默认编译器、真实 OpenAI HTTP 适配器和模拟上游完成六种双回合验证：摘要专用参数400、摘要流截断、摘要401、摘要未知400、摘要成功后主请求400、溢出恢复后再次溢出。每种都验证下一回合恢复、当前“继续”输入进入主请求、摘要只提交一次及已完成工具不丢失/不重复。客户端 hook/存储确认仍为测试替身，不能称真实 Cursor 验收。
- 修复冻结摘要输入超预算就直接终止的问题：改用已有本地摘要生成器，冻结前缀和归档边界不变，提交前仍重编译检查预算。不能通过裁掉未完成工具、当前输入或伪造摘要成功绕过。补 `TestCompactionLocalBudgetFallbackResumes`，明确断言不发超预算摘要请求、原回合续接成功。
- 摘要专用参数400（错误码及 reasoning/输出额度参数均命中白名单）、5xx及可恢复流截断允许使用本地历史生成替代摘要，不发布截断内容；增加 `compaction_summary_fallback` 降级记录，仅有原因枚举及关联元数据。真实适配器测试还发现错误体会先被整理成 message，原 `param` 丢失，已在 `HTTPStatusError` 保留白名单参数名而不保留任意原文。401/403、429、安全拒绝、未知400、本地错误及取消继续失败收口；普通主请求的副作用/重试保护不变。
- 修复所有语言都按四字符一个 token 的严重低估：ASCII不变，非ASCII按UTF-8字节作更保守估算。该值仍非模型精确分词，会使部分中文大输入提前压缩或拒绝；无法缩减的当前输入超窗口仍保留原历史并明确失败，不宣称任意长度都能恢复。压缩前用量不能直接当压缩后真实用量，一次溢出恢复上限不解除。
- 摘要开始前和结束后检查取消、终态及当前调用身份；新增 `TestCompactionInFlightCancelRejectsLateFallback`，在摘要执行中取消后返回503，验证迟到错误不能提交摘要、复活会话或发起主请求。降级诊断、错误分类边界及预算都有永久回归。
- 独立复核已成功返回，未发现候选新增的确定性阻断缺陷；指出的旧测试覆盖误导已替换，并补取消/重复溢出链路。关于无code/无param及422兼容错误，继续保守失败，未把缺少可信分类的错误扩大成允许回退；真实提供商差异仍待现场验收。关于basic过滤，已核查 `TestRecorderBasicSkipsHighFrequencyInfoKeepsCore` 保留生命周期，`TestAppLogQuotaReclaimsItsOnlySealedShard` 覆盖单分片恢复；这两项并非未覆盖的缺陷。独立复核未证明所有导入/客户端requestID行为，相关推测不作为已确认根因。

本轮执行证据（仅隔离本地/模拟HTTP）：

- `/tmp/cursor-byok-compaction-review-red.log`：新增参数400、503、流截断回退、非ASCII估算、冻结前缀预算回归先失败；修复后定向通过。`/tmp/cursor-byok-compaction-entry.log` 还捕获真实适配器丢失param导致回退未触发，修复元数据后通过。
- `go test ./... -count=1 -timeout=300s` 退出0：`/tmp/cursor-byok-review-final-all.log`。随后只缩小一个超预算测试夹具并新增“继续”输入断言，未再修改产品源码；受影响用例按下述重复race重新验证。
- `go test -race ./internal/backend/forwarder ./internal/backend/agent/model -run 'TestCompaction|TestPrepareCompaction|TestApplyCompaction|TestOverflow|TestBuildHTTPStatusError' -count=1 -timeout=180s` 退出0：`/tmp/cursor-byok-review-race-final.log`。首次race在超大hook夹具上超过测试助手5秒等待，未报告数据竞争；把夹具从50,000组缩至仍超预算的4,000组后通过，失败记录保留为 `/tmp/cursor-byok-review-race.log`。
- 最终入口、取消、本地预算回退和降级诊断四项再以 `go test -race ./internal/backend/forwarder -run 'TestCompactionRecoveryThroughRunIntentHTTP|TestCompactionLocalBudgetFallbackResumes|TestCompactionInFlightCancelRejectsLateFallback|TestCompactionSummaryFallbackRecordsSafeWarning' -count=2 -timeout=120s` 退出0：`/tmp/cursor-byok-review-final-entry-race.log`。
- 日志三包 `go test -race ./internal/observability ./internal/logsink ./internal/logger -count=1 -timeout=120s` 退出0：`/tmp/cursor-byok-review-log-race.log`；独立 `tools/log-analyzer` 目录 `go test ./... -count=1 -timeout=120s` 退出0：`/tmp/cursor-byok-review-analyzer.log`。
- `go vet ./internal/backend/forwarder ./internal/backend/agent/model ./internal/observability ./internal/logsink ./internal/backend`、`gofmt`与差异检查通过。前端本轮未变更，沿用前轮证据，不声称重跑构建。

下一步：获准后构建/安装本次候选，在用户真实 Cursor 长会话验证摘要后继续和满额清理；本轮没有部署、重启或发布，不能称现场已彻底验收。

### 解除上游关联、分支重构与版本重编号（2026-10-20；local-only，未推送）

**已完成本地分支重构、全仓版本重编号与上游署名清理；远程尚未同步。** 负责人：主会话。用户确认的边界：先在本地完成并验证，推送前再确认；删除原上游与中转镜像两个远程；版本按 `0.0.50` 之后统一改为 `1.x`（旧 tag 保留，另建 `v1.77.0`）；`cli` 保持 `0.0.49.6` 不参与新序列；删除作者入口与外部帮助链接；`LICENSE` 署名改为 `yaogjim`。

- **分支**：删除原上游镜像 `main`（原 `31e9f5c`），将 `noad`（原 `954b7ac`）改名为 `main`，再合并 `gateway@96a6d6a`。合并仅 `README.md` 一处内容冲突（下载入口文案），取 `gateway` 版本；`internal/backend/forwarder/subagent_store.go` 自动合并，已核对 `b61a4de` 的 Windows 目录 fsync 修复仍在。合并提交 `6f8e9c2`。
- **版本重编号**：`0.0.N.M`（N≥50）→ `1.N.M`，如 `0.0.51.0`→`1.51.0`、`0.0.77.0`→`1.77.0`；`0.0.49.x` 及更早保持不变。覆盖 `build/config.yml`、`build/darwin/Info.plist`、`build/darwin/Info.dev.plist`、`build/linux/nfpm/nfpm.yaml`、`build/windows/info.json`、`build/windows/nsis/wails_tools.nsh`、`build/windows/wails.exe.manifest`、`README.md`、`release-notes.md`、`releaselog/`（26 个文件改名）、`docs/**`、`task/todo.md`、`internal/updater/semver_test.go` 中的版本 fixture。`go run ./scripts/release version -config ./build/config.yml` 输出 `1.77.0`。
- **署名清理**：删除 `internal/bridge/window_author.go`（`GetFooterAuthorInfo`/`OpenFooterAuthorHome`），移除 `MainLayout.vue` 的作者入口、“使用教程”按钮及对应 handler/常量，清理 `clientApi.js` 的绑定导出，`LICENSE` 版权行改为 `Copyright (c) 2026 yaogjim`，updater 负向 fixture 改用中性外部仓库地址。`tab.leokun.cn`、`ads.leokun.cn` 属运行时中继/广告端点配置，不是仓库署名，未改动。
- **远程**：已删除原上游与中转镜像两个 remote，仅保留 `origin`（`https://github.com/yaogjim/cursor-byok`）。`main` 解除了对 `origin/noad` 的旧上游跟踪；因尚未推送，`origin/main`、`origin/noad`、`origin/HEAD` 仍是旧状态。
- **文档**：`README.md` 分支说明改为 `main`（主线）/`gateway`（开发）/`cli`，并注明不再跟踪上游；`docs/cursor_byok_upstream_merge_requirements.md` 标记为已停用，原上游地址替换为“已解除关联”；`prd_cursor_byok_工作决策基线.md`、`prd_cursor_byok_当前功能与上游差异.md`、`main_gateway_价值功能分析与迁移方案_20260907.md`、`task/todo.md` 中的上游仓库链接与作者标识一并移除。
- **历史记录口径**：本次按用户要求对文档中的 `0.0.50+` 版本号做了全局重编号，因此下方与历史发布记录中出现的 `v1.x` tag、`1.x` 版本号在重编号之前实际为 `v0.0.x`。涉及“已推送/已发布”的旧记录描述的是重编号前的真实操作，当前仓库尚未以新版本号推送或发布。
- **验证**：`go build ./...` 退出0；`go test ./... -count=1 -timeout=300s` 全部通过（36 个包 ok，0 FAIL，保留 macOS 目标版本链接 warning）；`go test ./internal/updater ./internal/bridge ./internal/backend` 通过；`gofmt` 无输出。前端未执行 `yarn build`（本轮改动仅删除 UI 入口，且未安装依赖）。**平台构建、GitHub 发布与远程同步均未执行，不得记为已发布。**
- **待办**：按用户确认后在推送窗口执行：删除 `origin/main`、推送新 `main`、删除 `origin/noad`、把 `origin/HEAD` 指向 `main`、同步 `gateway`/`cli`、推送 `v1.77.0` tag；并重新构建 `1.77.0` 安装包与 release 资产。

### 应用改名 cursor-byok 与 GitHub Actions 三平台构建（2026-10-22；local-only，未推送）

**应用名、构建产物名与文档已统一改为 `cursor-byok`，安装包改由 GitHub Actions 三平台自动构建；工作流尚未在 GitHub 上真实运行，远程未推送。** 负责人：主会话。用户要求：把 `Gateway-byok` 改为 `cursor-byok`（含构建应用名与各文档），push 后由 GitHub 自动构建 macOS/Windows/Linux 安装包，并一并修复在 GitHub 上构建可能失败的问题。

- **改名范围**：37 个文件中的 `Gateway-byok` 替换为 `cursor-byok`，包括 `build/config.yml`（companyName/productName/description/copyright/comments）、`build/darwin/Info.plist`、`build/darwin/Info.dev.plist`、`build/windows/info.json`、`build/windows/nsis/wails_tools.nsh`、`build/linux/nfpm/nfpm.yaml`、`Taskfile.yml` 的 `APP_NAME`、`internal/app/runner.go` 的 `appName`、`README.md`、`release-notes.md`、`releaselog/*.md`；`build/linux/desktop` 随 `git mv` 改名为 `build/linux/nfpm/cursor-byok.desktop`。改完执行 `wails3 task common:update:build-assets`，`git diff` 未产生额外差异，说明手工改名与 wails 生成结果一致。
- **历史记录口径**：`docs/process.md`、`task/todo.md`、`releaselog/1.57.0.md` 中记录旧版本真实文件系统路径（`/Applications/Gateway-byok.app`）与旧运行实例名的语句保持原样，不改写当时的验证证据；正文其余品牌表述按用户要求改名。
- **CI 工作流**：新增 `.github/workflows/build.yml`。push `main` / `gateway` / `v*` tag、PR 到 `main`、手工触发都会跑三平台：macOS（`macos-14`，arm64 与 amd64 的 DMG 与 updater tar.gz）、Windows（`windows-latest`，ZIP 与 NSIS 安装包）、Linux（`ubuntu-24.04`，updater tar.gz 与 deb/rpm）。tag 触发的第四个 job 汇总三平台产物、校验 tag 与 `build/config.yml` 版本一致、生成 `update.json` 后发布 Release；分支 push 只上传 Actions Artifacts。
- **CI 失败点修复**：`gen/` 与 `frontend/bindings` 未入库，干净检出必须先执行 `wails3 task common:generate:proto`，工作流已显式加入该步骤；`generate:bindings` 与三平台 `build:native` 不再依赖 `go:mod:tidy`（原先与 proto 生成并行执行，干净检出时 `gen/` 缺失会直接失败）；`generate:proto` 的 `-I /opt/homebrew/include` 改为按操作系统选择（Linux 用 `/usr/include`，Windows 不追加）；Linux job 安装 `libgtk-3-dev`、`libwebkit2gtk-4.1-dev`、`protobuf-compiler`、`nfpm`，并在构建前用 `common:update:build-assets` + `git diff --exit-code` 校验生成资产与 `build/config.yml` 一致；Windows job 安装 `nsis`、`zip`、`protoc`，`create:nsis:installer` 的 webview2 bootstrapper 改为文件已存在时跳过下载。
- **打包缺陷修复**：`build/linux/nfpm/nfpm.yaml` 原先引用不存在的 `build/linux/Gateway-byok.desktop` 与 `scripts/postinstall.sh`，版本与维护者硬编码（CI 无 `GIT_COMMITTER_NAME`）；现改为仓库根目录相对路径、`VERSION`/`ARCH` 由调用方注入、维护者固定、512×512 应用图标，并新增 `postinstall.sh` / `postremove.sh`。新增 `linux:package:installers`（一次编译产出 deb 与 rpm）、根任务 `release:build:linux:installers`、`release:build:macos:installers`、`release:build:windows:installer`、`release:verify:installers`，并把安装包纳入 `release:prepare:macos` / `:darwin` / `:linux`。
- **本地验证**：`go build ./...` 退出 0；`go test ./internal/updater ./internal/bridge ./internal/app` 通过；`wails3 task release:prepare:macos` 成功，产出 `cursor-byok-1.77.0-macos-arm64.dmg`（24,343,468 字节）、`cursor-byok-1.77.0-macos-amd64.dmg`（25,665,638 字节）与两个同名 tar.gz（21,309,616 / 22,363,677 字节），两个 DMG 均通过 `hdiutil verify`；arm64 包内二进制为 `Mach-O 64-bit executable arm64`，`Info.plist` 的 `CFBundleName`、`CFBundleExecutable` 与 `CFBundleShortVersionString` 分别为 `cursor-byok`、`cursor-byok`、`1.77.0`；`release:build:windows:amd64` 在 macOS 上交叉编译成功并产出 `cursor-byok-1.77.0-windows-amd64.zip`；`nfpm` 用桩二进制产出 deb/rpm，解包核对 deb 的 `Package`、`Version`、`Depends`、维护者与安装路径均正确。
- **未验证/受阻**：GitHub Actions 尚未真实运行（需要推送后观察）；Windows NSIS 安装包、Linux 真实二进制与 deb/rpm 的安装运行行为未在对应实机验证；本机无 Docker daemon，未执行 `release:build:linux:amd64`。
- **提交与分支状态**：`main` 的改名为 `6a0e370`，CI 触发条件收敛为 `739dc6d`；`gateway` 以合并提交 `398d50e` 同步同一批改动（并补齐其余上游署名清理），两条分支当前内容完全一致。`v1.77.0` 指向 `main@739dc6d`，尚未推送。
- **待办**：推送后在 Actions 页面确认首次三平台运行结果；按用户确认的顺序同步远程分支与 `v1.77.0` tag。远程同步已执行完毕，真实运行结果与后续修复见下一节。

### CI 三平台构建 Windows/Linux 失败分析与修复（2026-10-21 至 2026-10-22；已全绿并发布 v1.77.0）

**`main`、`gateway` 与 `v1.77.0` 的 GitHub Actions 运行已全部成功，`v1.77.0` Release 已发布 9 个安装包资产与 `update.json`。** 负责人：主会话。

- **第一次运行（run 39557893089，`main@5ec8065` 与 `v1.77.0` 同一 commit；`gateway@79dc686` 为 run 39557889110）**：macOS job（119082219572）成功；Linux job（119082219212）`nfpm package -p deb` 报 `matching "./build/linux/cursor-byok.desktop": file does not exist`；Windows job（119082219652）`windows:create:zip` 的 `zip -qry` 报 `zip error: Invalid command arguments (short option 'y' not supported)`，`release:build:windows:amd64` exit 16。
- **第二次运行（run 39560016345，`main@6e852ff`）**：macOS job（119088737003）成功；Linux job（119088737026）在“校验构建资产与 build/config.yml 一致”步骤失败；Windows job（119088737044）`release:build:windows:installer` 报 `"makensis": executable file not found in $PATH`，exit 127。
- **关键事实（wails v3.0.0-alpha.74 行为）**：`build/linux/nfpm/nfpm.yaml` 属于 `wails3 update build-assets` 的可更新资产，每次执行都会被模板重写（`arch: ${GOARCH}`、`version` 取自 `build/config.yml`、`maintainer` 用 `${GIT_COMMITTER_*}`、contents 引用 `./build/linux/<binary>.desktop`），而同一命令实际生成的桌面文件名是 `build/linux/desktop`，模板与实际文件名不匹配。直接修改 `nfpm.yaml` 会被 CI 的一致性校验判为不一致，第二次运行的 Linux 失败即此原因。
- **修复 1（Linux 打包）**：`build/linux/nfpm/nfpm.yaml` 恢复为生成态；新增自维护配置 `build/linux/nfpm/cursor-byok.nfpm.yaml`（`version`/`arch` 由任务注入的 `${VERSION}`/`${ARCH}` 提供，维护者与 homepage 固定，图标 1024×1024 并加 pixmaps，启用 `postinstall`/`postremove`），`linux:package:installers` 改为显式引用该文件，不受 build-assets 重写影响。`postinstall.sh`、`postremove.sh` 因 `.gitignore` 的 `build/` 规则从未入库，已 `git add -f` 纳入版本控制。
- **修复 2（Windows 打包）**：`scripts/release` 新增 `zip` 子命令（`archive/zip` 实现），`windows:create:zip` 改为 `go run ./scripts/release zip -src <暂存目录> -out <zip>`，不再依赖平台 `zip` 工具，`choco install` 去掉 `zip`。NSIS 方面，choco 安装的 NSIS 不在默认 PATH：工作流在安装步骤内搜索 `C:\Program Files (x86)\NSIS`（兼容 `C:\Program Files\NSIS`），写入 `GITHUB_PATH` 并用 `makensis -VERSION` 自检，找不到直接报错退出。
- **修复 3（发布资产）**：发布 job 的 `files` 增加 `bin/release/*/*installer.exe`，此前 Windows NSIS 安装包不会进入 Release。
- **修复 4（发布 job 依赖）**：发布 job 需要在 `ubuntu-24.04` 上编译 `wails3` CLI，而该 CLI 通过 cgo 链接 GTK/WebKit；补充 `pkg-config`、`gcc`、`libgtk-3-dev`、`libwebkit2gtk-4.1-dev`、`libglib2.0-dev` 后 `go install` 通过。
- **本地验证**：`go build ./...` 退出 0；`go test ./scripts/release ./internal/updater ./internal/bridge ./internal/app` 全部 ok，新增 `TestWriteZipDirArchivesDirectoryContents`；`wails3 task release:build:windows:amd64` 在 macOS 上完整跑通（含 `go run ... zip`），`unzip -l` 归档根为 `cursor-byok-windows-amd64.exe`，解包后 `file` 判定 `PE32+ executable (GUI) x86-64`；`nfpm` 用桩二进制按新配置重打 deb 与 rpm，deb `control` 的 `Version=1.77.0-1`、`Architecture=amd64`、维护者与 homepage 正确，`control.tar.gz` 含 `postinst`/`postrm`，`data.tar.gz` 含 desktop、`hicolor/1024x1024` 与 `pixmaps` 图标；执行 `wails3 task common:update:build-assets` 后 `git diff` 不再包含 `build/` 变更；工作流与各 Taskfile/nfpm 配置均通过 YAML 解析。
- **第三次运行（run 39561163065，`main@94215cc`；`gateway` 为 run 39561166881）**：三平台 job 全部 `success`，`发布 GitHub Release` 因非 tag 触发而 `skipped`。macOS 上传 `macos-assets`（90,618,209 字节）、Windows 上传 `windows-assets`（46,189,797 字节）、Linux 上传 `linux-assets`（66,161,119 字节）。日志佐证：Windows 的 `go run ./scripts/release zip` 生成 zip，`makensis` 输出 `bin/cursor-byok-amd64-installer.exe` 并重命名为 `cursor-byok-1.77.0-windows-amd64-installer.exe`；Linux 的 `nfpm` 依次生成 `cursor-byok-1.77.0-linux-amd64.deb` 与 `.rpm`。
- **第四次运行（run 39562864852，`v1.77.0@99b5269`）**：三平台 job 成功，`发布 GitHub Release` 失败。原因：发布 job 在 `ubuntu-24.04` 上执行 `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha.74`，缺少 `libgtk-3-dev` / `libwebkit2gtk-4.1-dev`，cgo 链接报 `cannot find -lwebkit2gtk-4.1` 等错误。修复：发布 job 增加与 Linux 构建 job 一致的系统依赖安装步骤（`pkg-config`、`gcc`、`libgtk-3-dev`、`libwebkit2gtk-4.1-dev`、`libglib2.0-dev`）。
- **第五次运行（run 39563567703，`v1.77.0@7716a33`）**：四个 job 全部 `success`；GitHub Release `v1.77.0` 已发布，资产为 macOS arm64/amd64 的 DMG 与 tar.gz、Windows amd64 的 zip 与 `-installer.exe`、Linux amd64 的 tar.gz/deb/rpm，共 9 个安装包加 `update.json`（22–26 MB 量级）。`v1.77.0` tag 已重指至 `7716a33`。
- **未验证/受阻**：Windows NSIS 安装包已由 CI 真实编译成功，但未在 Windows 实机安装运行；Linux deb/rpm 未在 Linux 实机安装运行（本机无 `makensis`，brew 安装失败）。

### 1.77 主对话摘要失败与同聊天切换官网模型（2026-10-21）

**主对话摘要参数缺陷已修复并通过源码回归；两条缺失控制接口已补齐；同聊天BYOK→官网模型要求新建聊天的现场症状仍未关闭。** 主会话持续负责整合；基线 `ca4d6ed`，修改未提交。当前保留工作区原有协作规范修改；未替换运行实例、构建发布、部署/重启、修改Cursor安装/真实会话/配置或commit/push。

#### 摘要失败：最小真实请求对照与永久续接回归

- 用户材料为 `/Users/yaogj/Downloads/logs 3`；主trace为 `20261021T030127.693286000Z-38902ba83896`。摘要请求连续400后结束，导出缺少具体错误体。使用已配置渠道、合成短文本及4096输出预算作最小请求：`gpt-6.1-sol` / `reasoning.effort=none` 返回400，明确 `unsupported_value`；仅改为 `low` 后200、收到 `response.completed`。没有发送真实会话正文；没有输出凭据。参数缺陷已直接复现，错误体大小与现场记录一致仅为辅助证据，不当作原请求完整重放。
- 根因为摘要流程关闭思考，适配器按gpt-6前缀误推支持none。`openai.go` 只对精确 `gpt-6.1-sol` 和 `gpt-6.1-sol-*` 将disabled映射low，同时覆盖Responses与Chat形状；普通gpt-6、其它模型及显式high保持既有语义。没有扩大重试、吞错、修改摘要预算或持久化格式。
- 新增 `TestCompactionSolSummaryResumesMainTurn`：真实请求构建→模拟上游拒绝none→摘要应用→actor续接主回合。修复前永久回归退出1（状态failed，等待恢复超时）；修复后退出0（1.212s）。断言主回合完成、工具调用/结果各保留一条、摘要只发布一次、摘要low后续接恢复high、摘要额度仍4096且不带工具。模拟上游不是实机Cursor验收；适配器单测另覆盖两个端点及显式high不变。

#### 模型切换：独立404修复与客户端限制取证

- nudge接口在主trace第124–125行、`03:01:50Z`即404，早于相关对话失败；元数据更新也多次404。只给 `GetNewChatNudgeParameterizedModelPicker`、`UpdateConversationMetadata` 增加显式Host路由及官方身份转发；沿用现有官方透传，本地relay仍走本地404，不放宽未知AgentService方法。Host测试覆盖原凭据/请求体/响应保留、内部路由头不外泄、local-relay不送官网、目录不被吞、未知接口仍404；既有模型身份分流/配置重建回归保留。
- 继续读取本机Cursor 3.15.19工作台产物，SHA-256=`18f9a52779fa03f58989bf45f4abbcf18f3efec8dbf205e6aa71429297fd2ef9`。动态提取实际 `shouldDisableModelSwitchingForConversation` 及其首消息判断、首次提交方法，使用合成服务替身执行9项断言通过（`CLIENT_SWITCH_PROBE_PASS`）。实际选模受限条件为已有历史且 `applyAgentBackendTypeRestrictions===true`，编辑唯一首条消息时例外；受限点击对应“Switching models can degrade quality…Start New Chat?”。构建环境首次提交方法直接返回cursor-agent且不设限制，不能凭发现该分支就认定现场会话被锁。
- 实际nudge调用失败分支只上报 `model_nudge.rpc_failed`，没有设置上述限制字段；因此不能用修补404解释或承诺消除新建聊天提示。客户端还存在缺失Blob时的“This conversation's data is missing and can't be restored. Start a new chat to continue.”独立错误。当前用户导出未记录这两类提示正文/会话限制状态；目标会话已有确定的本地sol流失败，但未建立一次真实官网切换成功或失败的完整证据链。源码函数级断言不替代现场复现，也不把历史混用专项的缺口当成该次现场的已证实根因。

#### 验证、证据适用性与下一步

证据统一归档 `/tmp/cursor-byok-077-closeout.o0DDAt`：`provider-none.log` / `provider-low.log` 为合成真实上游对照；`summary-red.log` / `summary-green.log` 为永久回归先失败后通过；`regression.log` / `race.log` 为最终Go代码后的回归；`client-switch-probe.log` 为本次客户端函数断言；`vet.log`为空且命令实际退出0。

- `go test -count=1 -timeout=300s ./...`：根Go模块全部通过；model 8.275s、forwarder 23.736s、upstream 2.238s、backend 12.516s。有macOS对象14.0/链接目标11.0的warning，不等于低版本实测；独立子模块/前端构建未执行。
- `go test -race -count=1 -timeout=180s ./internal/backend/forwarder ./internal/backend/agent/model ./internal/backend/server/upstream ./internal/backend -run 'TestCompactionSol|TestCompactionSummaryOutputLimit|TestCompactionSummaryRealAdapter|TestOpenAIThinkingDisableKind|TestApplyOpenAIThinkingDisable|TestHostAgentModelSwitch|TestAgentRouteModelSwitch|TestGatewayDuoHostAgentRouteIdentity|TestGatewayDuoRunSSESurvivesConfigRebuild'`：四包通过（5.381s/2.845s/1.953s/3.347s）。全量和race结果本次已复核日志及成功退出状态，无需重复未变化源码的全量测试。
- 本次续办另以隔离HOME、固定宿主Go缓存和 `GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` 执行 `go vet ./internal/backend/agent/model ./internal/backend/forwarder ./internal/backend/server/upstream ./internal/backend`，退出0。源码修改范围为openai适配器及其测试、主回合压缩测试、Host和Agent路由及两个契约测试，共7个Go文件；没有启用原生writer或扩展历史同步。

- 文档首次落盘后的定向复测：沿用上述隔离环境及race相同包/筛选式（去掉 `-race`，`-timeout=90s`），四包再次通过，forwarder 2.492s、model 0.906s、upstream 0.578s、backend 1.728s；实际输出为同目录 `closeout-tests.log`。该记录只补充源码与隔离接口证据，桌面验收状态不变。

源码/模拟接口交付为 `verified-partial`。下一步须在获准候选窗口验收主对话摘要后续接，并取得同聊天切换的准确提示截图或同次客户端日志/请求ID，先区分客户端限制与历史Blob缺失，再针对已证实分支修复。缺少现场证据时保留会话和内容，不改安装JS、真实数据库或会话限制字段绕过门禁，不把两个症状合并称为已解决；原官方/BYOK混用专项仍维持自身状态。

#### 原 0.0.77.0 同版本发布（2026-10-21；重编号后记为 1.77.0）

用户授权按当时的原版本号 `0.0.77.0`（重编号后写作 1.77.0）发布本轮修复。**提交、推送、macOS 构建与 GitHub release 资产更新均已完成并经线上校验；真实桌面验收仍待执行。** 本节记录的是重编号之前、以 `v0.0.77.0` 为 tag 的真实操作；新的 `v1.77.0` tag 与改名后的 `main` 尚未推送。

- **提交与推送**：完成情况汇总为提交说明，`96a6d6a`（11 文件 +450/-8）含修复源码/测试、release-notes、task/process 与协作规范修改；`release-notes.md` 追加“主对话摘要思考等级修复”“AgentService 控制接口补齐”两章节，验证小节补充四包定向与竞态回归，版本小节注明同版本更新。`gateway` 分支推送 `ca4d6ed..96a6d6a`；当时的 `v0.0.77.0` tag 强制移动至 `96a6d6a` 并推送（旧指向 `5240bed`，非上版源码提交 `f9503e9`）。
- **macOS 构建**：`wails3` 位于 `~/go/bin`（alpha.74）。`task release:prepare:macos` 完整执行：清理发布目录、更新构建资产、生成绑定与发布说明、arm64/amd64 归档打包（21310618/22364484 字节，ad-hoc 签名），分析器/CLI池隔离校验“2 archive(s)”通过。
- **清单与资产**：`SHA256SUMS` 与 `update.json` 按 `release:github` 同款字段结构本地生成（仅 macOS 两平台，与上版线上 update.json 一致；Windows/Linux 本次不编译故不进入清单）。manifest 脚本会因缺失全平台资产失败，且 `release:github` 资产校验要求四平台，因此发布采用与上版相同的手动 PATCH+资产替换流程。
- **GitHub release 更新**：PATCH release 419038484 更新说明正文（线上确认含 4 处“本次更新”标记）与 target_commitish。首次资产上传误用 `api.github.com` 主机 404（arm64 旧资产已删、未上传成功）；改用 release `upload_url` 后 4 资产（arm64/amd64 tar.gz、SHA256SUMS、update.json）全部完成旧删新传。线上校验：各资产 digest 与本地构建摘要一致（arm64 `sha256:840b4d25…`、amd64 `sha256:d8058a83…`），tag 远端指向 `96a6d6a`。README 同步任务为 internal 且直接调用时无变更输出，未产生额外提交。
- **副产物**：发布后 `git status` 干净；本段归档为未提交的工作区修改，随下次提交入库。

### 官方/BYOK 混用兼容：分段设计与实施（整体 delivery_status=planned）

#### 执行 `da959273`：本地压缩归档 §19.3k 已通过隔离验收（2026-10-13）

本轮按用户指定的实施计划先推进 C2B 保存；系统 Design §19.3k 已批准，不再把本地压缩归档记为“只读取证、未授权编码”。原生归档实现已有生产plan的同源前缀冻结、深拷贝、显式计数、预算停止、正确Archive图及本地/导入来源组合，本轮收口最新失败并独立复核。**§19.3k 独立保存切片通过源码和隔离协议验收；默认 writer 仍未接线，C2B 未通过，整个专项仍 planned。**

- **整数/null修复**：[本地归档实现](77f825d6-8d69-4938-8788-241e23e458d9)先复跑 `TestParseNativeArchiveSourceRejectsNullAndNonIntegerFields`，确认 `prefix_start:null` 被接受；原因是字段仅检查存在性，Go `encoding/json` 将null解入整数不报错、保留0。补强五字段×null/字符串/小数/布尔/数组/对象及缺字段、完整0值的永久断言，再在 `parseNativeArchiveSource` 对每个raw token检查严格整数后解码，未知字段/类型越界仍明确失败。顶层缺省或null对象保留旧本地兼容，不凭此推算原生归档来源。原始与补强RED均退出1，GREEN及复验退出0；证据 `/tmp/cbyok-19.3k-p1-3-20261013194937/{p1-3-red.log,p1-3-red-strengthened.log,p1-3-green.log,p1-3-green-reverify.log}`。
- **独立复核发现并修复正文裁剪**：[归档合同独立复核](aa2d988d-648f-4a40-8b3f-e9be4e0070e6)发现native域校验/root编码复用 `compactedPromptProjectionEntries`，该函数按原模型政策将保留工具结果裁到16KiB，违背§19.3k未裁剪活动尾。主控亲读确认后退回实现，新增 `TestNativeCheckpointLocalCompactionRootKeepsUnclippedPreservedToolResult` 经生产build plan/apply与真实root Blob读取，RED为 `len=16384 want 18432`，不是夹具/编码错误。`projectCompactedPromptEntries` 复用同一连续尾及批次算法，模型入口仍改写保留结果，`compactedNativeCheckpointEntries` 只取原文；native域校验及root编码均改接后者。测试同时断言canonical和投影输入不变、默认模型视图仍裁剪、磁盘重读后正文完整。RED→GREEN及复验日志在 `/tmp/cbyok-19.3k-p1-4-20261013202106`；原复核上下文再次只读复审确认阻塞关闭、未见新具体缺陷。复审不运行测试，测试验收由主控另行完成。
- **主控最后修改后的验收**：新隔离 HOME=`/tmp/mixed-save-19k-primary.lqX3M1/home`，`GOPATH=/Users/yaogj/go GOMODCACHE=/Users/yaogj/go/pkg/mod GOCACHE=/Users/yaogj/Library/Caches/go-build GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOGC=20 GOMAXPROCS=2`。五个定向永久测试通过（0.526s），含五字段/零值、未裁剪活动尾、正确本地归档以及真实 Connect HTTP 最后archive子节点ACK门禁；`go test -p=1 ./internal/backend/forwarder ./internal/backend/agent/prompt -count=1 -timeout=180s` 两包通过（18.127s/0.265s）；`go test -race -p=1 ./internal/backend/forwarder -run 'TestWaitPosted|TestParseNativeArchiveSource|TestNativeArchiveSource|TestCompaction|TestApply.*Compaction|TestReasoningPrefix|TestApplyRejectsForked|TestNativeCheckpoint|TestNativeRawGroups|TestNativeToolGroups|TestImported|TestCheckpoint|TestCancellationDiscardsUnpublishedCheckpoint' -count=1 -timeout=180s` 通过（9.000s）。两包 `go vet -p=1`、四个本轮Go文件gofmt及 `git diff --check` 通过；`exit-codes.txt` 六项均0，`result.txt` 为 `MIXED_SAVE_19K_PRIMARY_VALIDATION_PASS`。完整命令与日志在该目录 `*.command`/`*.log`，早期批次仅保留为历史证据。
- **范围及下一检查点**：本轮新增业务/永久回归仅 `compaction.go`、`compaction_append_only_test.go`、`projector.go`、`projector_fork_test.go`；主控同步已有task/process及本次指定计划，保留其它用户原有改动与AGENTS/CLAUDE/project删除。default service仍调用 `ProjectCheckpointProjection`；剩余推理/签名/provider元数据、图片/文件、未完成工具、数值/结果类型及外部State内容仍有独立native拒绝门禁/未闭合合同。下一步必须闭合对应无损映射、同对象回读、兼容/失败保全及默认启用合同，再接入口级回归；直接换默认调用会让已有域外会话投影失败，本轮不作这一未批准变更。缺失读取、增量/旧会话恢复、父子链及整体验收依赖/设计门槛保持，不把本地归档GREEN提升为整链交付。

- **文档收口验证**：任务、过程和指定计划已核对相同验收目录、通过终态及阶段状态；`document-closeout.log` 记录 `MIXED_SAVE_19K_DOCUMENT_STATUS_PASS`。文档写入后再次执行 `git diff --check`，并以 `git diff --exit-code HEAD -- internal/backend/forwarder/service.go go.mod go.sum` 确认默认入口与依赖未修改；这一收口检查只验证记录与范围，不替代前述源码测试。

未运行无关全仓/前端测试，未新增仓库文件/依赖/数据库/配置，不读取或修改真实会话/凭据/安装，不调用真实模型/官方服务，不构建发布、部署、重启、提交或推送。发布后回滚仍须保留可选正文/来源及完整引用图读能力，全部历史和不可变内容保留。

#### 连续实施：回合引用图与官方摘要归档来源保全已验收（2026-10-13）

完成系统 Design §19.3i/j 的已预取保存切片：复用 ContentBlobStore 保全回合及具名子节点原字节，官方 summary/summary_archive/summary_archives 来源绑定现有 runtime_state 可选字段；独立原生投影按原引用重新校验并输出完整图。回合/归档顺序和重复实例保留；来源按最后一条有来源的 entry 整组覆盖，后续无来源状态更新不擦除。旧非32字节父节点按正确类型校验再内容寻址；不把普通32字节业务正文猜成引用。缺节点、哈希失配、未知字段、域外引用或内容保存失败均停止，保全原会话；已写但未被引用的不可变内容允许留存。永久回归覆盖磁盘重读、服务重建、旧内联、重复/空槽、来源更新、失败保全和真实 Connect HTTP 确认：保留归档/回合一个子节点至最后 ACK，此前不得发布，最后确认后只发布一次，迟到重复确认不重复终态。

主控确认前次回合图两包测试/race/vet通过（`/tmp/mixed-turn-main.N4T0vj`）。摘要归档初次主控race在清理失败导入请求时暴露 `provider loop interrupted`（`/tmp/mixed-save-graphs-final.TCUYkd/race.log`，该批次未通过）。实际根因为 actor 已发送命令结果后退出，wait 的 select 可能选择 done 而丢掉已返回结果。`actor.go::waitPostedStreamCommand` 现优先取已返回结果，无结果仍明确中断；测试清理跳过已终态流，取消中断后仍等待 actor 结束，不放宽模型未启动/原会话未改写断言。稳定双通道就绪回归先RED再GREEN；独立修复与只读复核无剩余阻塞，目标重复20次通过，证据 `/tmp/imported-turn-graph-cleanup.rvIuIi`。

最后源码修改后主控在 `/tmp/mixed-save-graphs-reviewed.RZ7pVI/home` 隔离HOME，固定Go缓存、禁依赖下载、`GOMAXPROCS=2 GOGC=20`：`go test -p=1 ./internal/backend/forwarder ./internal/backend/agent/prompt -count=1 -timeout=180s`通过（18.659s/0.346s）；`go test -race -p=1 ./internal/backend/forwarder -run 'TestWaitPosted|TestImportedTurnGraph|TestNativeCheckpoint|TestNativeToolGroups|TestNativeRawGroups|TestImportedNative|TestImportedConversationState|TestImportedStructured|TestCompaction' -count=1 -timeout=180s`通过（8.332s）；两包vet、修改Go文件gofmt及`git diff --check`通过，`exit-code=0`。读侧/隔离协议为verified-partial，写侧仍implemented-not-wired，专项planned。默认入口未切换；保存还缺本地压缩原生归档、其它内容域及默认接线，后四阶段未开始。本地压缩实际输入与连续归档前缀不等价，现有载荷不能可靠重建window_tail，后续仅按§19.3k关闭该局部取舍，不把尾段长度或默认0冒充实际计数。无真实会话、配置、凭据、迁移、部署、重启、提交或推送。

#### 连续实施：原生正文与摘要保全已验收（2026-10-13）

承接用户允许在现有历史记录增加可选原生正文/归档来源信息的确认，完成系统 Design §19.3h 的已预取 root 正文及摘要切片。`types.go` 增加可选 `native_message`/`native_message_parts`，`token_usage.go` 在整段配对校验成功后按原文绑定连续消息组；`imported_blobs.go` 严格接受原生摘要标记并只包装模型视图；独立 `projectNativeCheckpointProjection` 复核全部原文、视图和工具配对，一组只写一次原文。重复/空实例、多工具结果分组保留；旧载荷缺省仍走旧路径。读侧已接真实 HTTP BidiAppend→持久化→编译→模拟模型，摘要不重复包装、原生保全字段不进入模型请求；写侧仍为独立路径，默认入口未切换。

主控复核发现并修复三类缺陷，永久测试均先在业务断言处 RED：① 外层 JSON 落盘会改变原文缩进/HTML转义，而参数和非字符串结果在 Message 中保存成字符串，重解码后失配；单条解析先按保存规则规范化 JSON，不经浮点数，不改字符串内空白。② 原文已明确是字符串的工具结果被按本地重编码猜成 JSON 值，误触数值/结果类型门禁；原生组现直接校验原文的安全数值域，工具配对单独校验，本地生成编码规则保持不变。③ 负数parts被当默认1，以及有parts无原文的标记未按原生分组合同停止；现只对0使用默认1，非法值及孤立标记明确失败。失败输入不被改写。第一次新落盘测试误向保存接口传nil entries，得到空root；已纠正测试准备，真实RED为 `native message view mismatch`，不把空root当生产缺陷。

最后行为修改后在 `/tmp/mixed-native-save.gZeS67/home` 隔离HOME，固定宿主Go缓存、`GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`：`go test -p=1 ./internal/backend/forwarder ./internal/backend/agent/prompt -count=1` 通过（20.538s/0.351s）；`GOMAXPROCS=2 go test -race -p=1 ./internal/backend/forwarder -run 'TestNativeCheckpoint|TestNativeToolGroups|TestNativeRawGroups|TestImportedNative|TestImportedConversationStateNative|TestCompaction' -count=1` 通过（7.527s）；两包vet、gofmt及diff检查通过，整批退出0，`NATIVE_ROOT_PRESERVATION_PRIMARY_VALIDATION_PASS`。证据同目录 `tool-roundtrip-red.log`、`raw-result-type-red.log`、`native-parts-red.log`、`packages.log`、`race.log`、`vet.log`。本轮续办由主控亲自复核/修复，不将自审称为独立评审。

**状态与下一步**：正文/摘要读侧及隔离协议为verified-partial，独立写侧仍implemented-not-wired，整个专项planned。保存阶段仍缺原生归档来源/完整引用图、本地压缩归档及其它消息域和默认接线；`ConversationSummaryArchive.window_tail` 的真实语义是待摘要与实际摘要消息条数之差，不能用连续活动尾段长度替代，更不能把投影归档集合当模型实际摘要输入。下一步先闭合对应保存合同再编码，不重复询问已批准的格式方向；后续读取、增量、子任务及整链仍pending。无迁移、新数据库或依赖，不读写真实会话/凭据/配置，不部署、重启、提交或推送。复用教训：嵌入RawMessage的语义保全不等于字节哈希保全；原文有明确类型时不再从规范化字符串猜类型；简单文本落盘回归不能证明嵌套工具数据落盘正确。

#### 连续实施：活动历史改为固定连续尾段（2026-10-12）

用户授权按保存→缺失读取→新增历史同步→子任务→整体验收连续推进并减少非必要文档。本轮先落实系统 Design §19.3g 的活动尾段切片，未切默认原生 writer。业务仅修改既有 `projector.go` 和 `compaction_append_only_test.go`：摘要前缀冻结保留边界，摘要后新增工具不再使旧工具退出；从最早保留项取完整连续尾段，同一真实 ModelCallID 的工具批次跨非 prompt 元数据仍完整；旧摘要排除、重复实例保留、原裁剪仅作用原保留集合。既有取消/fork以及 `applyCompactionPlan` 编译后、持久化前预算检查保持原样，无新格式/阈值/依赖。独立复核指出压缩候选耦合、兄弟工具起点与裁剪作用域，主控闭合合同；实现的同批元数据缺陷由主控复核指出并补永久RED修复。另一“不同ModelCallID”对照最初制造悬空结果，主控拒收并改成前批完整结束后的合法样本，不将测试准备错误当业务支持。

永久稀疏/冻结/连续两次/兄弟工具/裁剪5项旧代码RED及工具跨元数据RED在 `/tmp/cbyok-19.3g/{red-targeted.log,red-metadata-batch.log}`，GREEN在 `green-metadata-batch-fix.log` 等。最后源码修改后主控隔离 HOME、固定宿主 Go缓存并设 `GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOGC=20 GOMAXPROCS=2`，运行 `go test -p=1 ./internal/backend/forwarder ./internal/backend/agent/prompt -count=1 -timeout=3m`（20.294s/0.500s）、`go test -p=1 -race ./internal/backend/forwarder -run 'Compacted|Compaction|^TestNativeCheckpoint|^TestCheckpoint|^TestCancellationDiscardsUnpublishedCheckpoint|^TestImportedConversationState|^TestImportedStructured' -count=1 -timeout=120s`（9.189s）、两包 `go vet -p=1`、修改Go文件gofmt及 `git diff --check`，整批退出0，`SAVE_CONTINUOUS_TAIL_PRIMARY_VALIDATION_PASS`。证据 `/tmp/mixed-save-progress.FCSGXt/{packages.log,race.log,vet.log}`。该切片源码/隔离路径为 verified-partial，不证明原生压缩输出或现场恢复已通过。

继续检查原生保存时，两个仅在/tmp的Go overlay用原生摘要标记、工具调用后文本块重现导入表示缺口，分别失败于 `invalid or unsupported native root message` 和 `unsupported native text after tool call`（`native-preservation-gap.log`，Go预期退出1）。当前 `modelMessageEntryPayload` 仅含转换后 Message，压缩记录没有原生归档来源；要保全这些复杂原文并再次输出，提出在既有历史载荷增加可选原生正文/归档来源信息，属于计划要求单独确认的新增持久化合同，不擅自实现。推荐保持新旧读取、无批量迁移/覆盖旧记录；发布后只能回滚到具备对应读取能力的版本，真实发布仍独立授权。待用户聚焦确认后闭合精确字段/生命周期合同并继续保存，随后顺序推进其余功能。默认入口与原生archive尚未完成，整体保持 planned；未操作真实配置/会话、模型、安装、部署、重启、提交或推送。本轮记录仅增加必要设计合同和本段/task续办摘要，未修改批准计划或新增仓库文件。

#### 完成情况复审：已修两项语义缺陷并补齐结构化 HTTP 失败证据

本轮按用户“review 完成情况，如果有问题，请修复”的授权，主会话复审已批准的 C0-W1 原生文本/完整工具写出和 C0-W2 计划/待办引用双读。**这两个独立切片的边界修复已通过源码与隔离协议验收；默认原生写出仍未接线，整体混用和用户现场错误仍未验收关闭。** 未把尚未批准的压缩、异步读取或默认切换作为本轮修复实施。

- **待办语义空值缺陷**：新引用的 protobuf 正文即使非空，也可通过显式默认字段解码为全零 TodoItem。原判据仅看正文长度，导致接受空对象。永久测试 `TestImportedConversationStateRejectsDefaultEncodedTodoReferences` 三项语义 RED 在 `empty-wire-red.log`，旧非32字节内联对照通过；`imported_blobs.go` 在未知字段校验后以 `proto.Size(item)==0` 拒绝引用式全零对象，沿用 `decode imported todo blob: empty todo`。新引用停止并保全，旧内联兼容不变；定向 GREEN 在 `empty-wire-green.log`。
- **分组工具调用缺陷**：pending 按 ID 指向整条 assistant 消息，而结果 override 原先作用于整条消息，可能把某工具名称/参数覆盖同批次其他调用，也会用失配结果重命名调用。`TestNativeCheckpointMessagesPreservesGroupedToolIdentities` 固化两项 RED（`grouped-tools-red.log`）；`projector.go` 现逐项匹配调用 ID，只对匹配项恢复参数，并在改写前拒绝非空名称失配。调用顺序、身份、其他项参数和原会话保持不变；定向 GREEN 包含完整工具结果和原身份回归（`grouped-tools-green.log`）。
- **完成声明的证据缺口**：旧 `TestImportedConversationStateStructuredFailuresPreserveConversation` 主要在导入函数层验证，不能证明真实 HTTP 失败入口不启动模型/不写会话。已更正上述 W2 协议归档，并新增 `TestImportedStructuredFailuresStopRequestWithoutWrites`，通过真实 Connect HTTP BidiAppend 覆盖缺失 plan/todo、歧义旧32字节 plan、显式默认字段全零 todo。四分支均断言具体错误类别、无模型请求、已存会话完整相等；`structured-http-green.log` 全部通过（含既有工具失败对照，0.669s）。这是隔离 HTTP 协议入口和模拟 provider，不是 Cursor/官方实机验证。
- **最后源码修改后的完整相关验收**：隔离 HOME=`/tmp/mixed-w1-main.yakIpl/home`，固定 `GOCACHE=/Users/yaogj/Library/Caches/go-build`、`GOPATH=/Users/yaogj/go`，`GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOGC=20 GOMAXPROCS=2`。执行 `go test -p=1 ./internal/backend/forwarder ./internal/backend/agent/prompt -count=1 -timeout=3m` 两包通过（18.377s/0.369s）；`go test -p=1 -race ./internal/backend/forwarder -run '^TestNativeCheckpoint|^TestProjectCheckpointProjection|^TestCheckpoint|^TestCancellationDiscardsUnpublishedCheckpoint|^TestImportedConversationState|^TestImportedStructured' -count=1 -timeout=120s` 通过（6.209s）；`go vet -p=1 ./internal/backend/forwarder ./internal/backend/agent/prompt`、改动源码/测试 gofmt 检查及 `git diff --check` 通过。整批成功终态 `COMPLETED_SLICES_REVIEW_PRIMARY_VALIDATION_PASS`，证据目录 `/tmp/mixed-completion-review.VIrJFy`，完整日志 `packages.log`、`race.log`、`vet.log`。未运行无关前端/独立 module/整仓构建，不用局部验收代替全仓通过。
- **范围、回滚与后续**：本轮新增修复仅在既有 projector/imported_blobs 和两个永久测试文件，状态同步至已有 task/process/Design，保留执行前全部改动。默认 service、compaction/state_tools、持久化 schema、依赖、配置和两份批准计划保持原样；无需迁移或删除记录。W2 已发布新引用时必须保留同对象双读，裸 `07aa83f` 不能兜底。feature gap=默认原生 writer 适用域/接线、缺失 Blob 异步恢复、历史增量及完整父子参数/结果链；design gap=“摘要＋连续尾段”的预算/固定边界/失败恢复合同；test/env gap=真实客户端/官方往返与实际产物。下一步仍先闭合压缩合同及适用域，不从本轮通过推导默认切换批准。整体 planned，生产预取读取及隔离协议为 verified-partial，独立写侧为 implemented-not-wired。

**复用教训**：protobuf 编码非空不代表对象语义非空；调用 ID 指向分组消息时，修改范围必须进一步限定到该调用；函数级失败保全不能直接升级为 HTTP 入口证据。本轮无新增委派，不把主控自审表述为独立复审。未读取/改写真实会话、凭据或配置，未打包、部署、重启、提交或推送。文档落盘后四项定向收口测试通过（0.640s，`closeout.log`），覆盖两项新边界、结构化 HTTP 失败及结构化全 ACK 发布；默认入口/压缩/schema/依赖/计划无差异。首次文档断言脚本因要求与实际同义文本不一致而退出1，未视为验收通过，也不属于业务测试失败；修正该临时断言后复核文档、日志与差异，不重复已通过且源码未变化的包测试。

#### W2 收尾：计划/待办引用写出与完整预取双读（默认未切换）

C0-W2 已按系统 Design §19.3e 完成代码和隔离协议验收，本次续办补齐过程归档并重新运行结构化入口回归。**原生写侧仅接独立测试路径，生产读侧已接完整预取双读；整体混用仍未交付，用户现场缺失内容与子任务错误尚未关闭。**

- **实际改动**：`projector.go` 的独立 `projectNativeCheckpointProjection` 将计划/待办的 protobuf 正文纳入完整 Blob 图，State.Plan/Todos 使用 raw32 SHA-256 引用，Plans map 仍内联；`token_usage.go` 与 `imported_blobs.go` 的真实导入读侧复用已校验预取仓库。长度恰32字节且正文缺失时停止，不按 protobuf 解析成功回退；其他长度保留旧内联兼容。新引用正文的未知字段/损坏/空待办明确拒绝，旧内联不新增未知字段限制。Todo 时间戳保留完整 int64，顺序和 A/B/A 重复实例保持不变。
- **纠偏与永久失败证据**：读/写 RED 在 `/tmp/mixed-structured-probe.2dpCD5/{read-red.log,write-red.log}`；独立复审发现全零 TodoItem 的空正文无法进入 Blob 图，会丢实例，主控拒收并固化两项 RED（`empty-todo-red.log`），亲修为 writer 返回 `native checkpoint: empty todo`、引用读侧拒绝空正文。另修测试自身非法 JSON 快照，使断言在业务行为处失败，不将测试准备错误冒充生产 RED。`structured-green.log` 的早期批次退出1，不能凭文件名当作通过；后续新鲜完整验收取代该失败批次。
- **协议证据**：`TestImportedStructuredStateReachesProviderThroughBidiAppend` 走真实 HTTP/Connect 导入、保存、提示编译及 provider gateway，模拟模型实际收到计划/待办提示。缺失/歧义此前只有导入函数级失败保全证据，原“入口回归验证不启动模型、不改已存会话”的表述过宽；本次完成情况复审新增 `TestImportedStructuredFailuresStopRequestWithoutWrites`，补足对应 HTTP 失败证据，详见本节开头的复审归档。`TestNativeCheckpointStructuredAcknowledgementsThroughBidiAppend` 将独立投影送入既有 SetBlob 队列，计划和 A/B/A 待办原生回读正确，保留一项待办正文到最后 HTTP ACK，确认前不发布、确认后唯一终态。证据层级是隔离协议链，不是默认模型入口写出或真实 Cursor/官方恢复。
- **主控完整验收归档**：隔离 HOME 使用 W1 证据目录的 `home`，`GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`，固定 `GOCACHE=/Users/yaogj/Library/Caches/go-build`、`GOPATH=/Users/yaogj/go`；资源受限使用 `GOGC=20 GOMAXPROCS=2` 与 `-p=1`。forwarder 全包测试（`w2-package.log`，18.203s）、导入/原生/检查点定向 race（`w2-race.log`，6.458s）、forwarder vet（`w2-vet.log`）及结构化 ACK 单独测试（`structured-protocol.log`，0.476s）已通过；日志均在上述结构化目录。本次续办再次执行 `go test -p=1 ./internal/backend/forwarder -run '^(TestImportedStructuredStateReachesProviderThroughBidiAppend|TestImportedConversationStateReadsPrefetchedStructuredReferences|TestImportedConversationStateStructuredFailuresPreserveConversation|TestNativeCheckpointProjectionStructuredReferences|TestNativeCheckpointStructuredAcknowledgementsThroughBidiAppend)$' -count=1 -v -timeout=90s` 和 `go vet -p=1 ./internal/backend/forwarder`，整批退出0，测试0.637s，输出 `W2_CLOSEOUT_TEST_VET_PASS`，日志 `w2-closeout-test.log`、`w2-closeout-vet.log`。本次没有修改 Go 源码，不重复无关全仓/前端验证。
- **兼容与回滚**：用户已批准没有正文的32字节旧内联停止并保全。当前未发布新格式；未来一旦发布，最低回滚版本必须包含 W2 双读并核验实际产物，裸 `07aa83f` 不满足这一对象的读取合同。全部 Blob、已发布引用与切换后新增历史必须保留，不恢复旧快照或删除会话。
- **范围与下一步**：本次收尾只修改已有 task/process/Design；保留此前全部代码与用户文档修改。默认 `service.go`、`state_tools.go`、proto/schema、provider replay、依赖与配置保持原样。W2 独立切片完成，读侧/模拟协议为 `verified-partial`，写侧为 `implemented-not-wired`；C2B 仍 pending。下一步只读核验压缩的活动摘要标记、连续归档与尾部顺序/排他映射，合同闭合前不实施压缩 writer，也不切换默认入口。

未操作真实会话、配置或凭据，未打包、部署、重启、提交或推送。

#### 压缩续查：集合可重建仍不足以保证原生历史顺序

W2 收尾后推进 `mixed-checkpoint-compaction-contract` 的有限只读取证，未实施压缩代码。主控委派[压缩映射核对](e70b8b9a-08a6-42fe-bc8c-ba61d17c9df5)仅核对现有 Go 保留算法；首次报告把取消过滤后的全量 checkpoint 条目称作“归档集”，主控退回纠正，不将它当原生 archive writer，也不接受“移除集合可重建”直接推出有序归档。复核确认完整 Entries 的 helper 会受摘要后同轮工具影响；客户端消费与合成运行由主控亲验。

- **主控探针**：在既有 `/tmp/mixed-structured-probe.2dpCD5` 创建临时 Go overlay，追加合成测试到现有 `compaction_append_only_test.go` 的只读副本，直接调用 `compactedPromptProjectionEntries`；相同隔离环境执行 `go test -p=1 -overlay="$P/compaction-overlay.json" ./internal/backend/forwarder -run '^TestCompactionSparseTailEvidence$' -count=1 -v -timeout=90s`（`P` 为上述证据目录，另设 `PROBE_DIR="$P"`）。第一次仅因临时测试缺少 `encoding/json` import 编译失败，不作为业务 RED；补 import 后退出0，`compaction-mapping.log` 显示 `SPARSE_TAIL_FIRST=[8 3 6 7] SECOND=[12 9 10 11]`，并确认同轮摘要后新增工具会使保留集合从 `[8,3,6,7]` 变为 `[8,3,9,10]`（`POST_SUMMARY_TOOL_CHANGES_PRIOR_KEEP_SET`）。最终 Go 探针0.575s。没有修改仓库测试、压缩代码或真实历史。
- **实际消费者反例**：核验与 Design 基线相同的 exec/workbench 完整 SHA-256，动态提取实际 `DIu/IIu`，仅以合成替身替代 Blob 存储、protobuf serde、水合及观测。`PROBE_DIR="$P" node "$P/compaction-order-probe.mjs"` 退出0；单次候选归档+活动消息恢复为 `[1,2,4,5,3,6,7]`，连续两次为 `[1,2,4,5,3,6,7,9,10,11]`，均与原序不同，数量、成员和唯一实例却完全相同。日志 `compaction-native-order.log` 输出两条 `NATIVE_ORDER_COUNTEREXAMPLE_CONFIRMED` 与 `COMPACTION_NATIVE_ORDER_PROBE_PASS`。退出0证明反例断言成立，不证明压缩功能已修；存储/serde被替代，不宣称完整 Cursor 恢复。函数与摘要标记片段在 `compaction-native-fragments.log`，样本在 `compaction-mapping.json`。
- **已核实及候选边界**：现有稀疏活动序列无法直接按“新增移除集归档”满足原生 archive→root 的顺序合同。连续前缀归档/连续后缀活动的合同样本经同一 DIu 保序，但尚未实现，也未证明预算/取消/fork/回读闭合。实际摘要标记是 user 消息的 `providerOptions.cursor.isSummary=true`；当前原生读侧未接受此元数据。将 helper 截在摘要前缀可定义稳定压缩时点，但会改变现有活动政策，仍为候选。
- **决策与状态**：事实归档至系统 Design §19.3f，Design Readiness=not-ready。用户随后明确选择 `continuous_tail_design`，允许按“摘要+连续尾段”完善压缩设计、继续核验预算与失败保全；方向决策关闭，压缩合同设计继续。该方向可能增加当前轮模型输入；主控读取 `validateCompactionCandidateBudget`/`compactionBudgetTokens`，确认已有上下文窗口减预留量与超限错误可作现实基线，但不能证明连续尾段总能容纳。具体边界、取消/fork与读取/回滚合同未闭合前不编码，未授权新阈值/持久字段、默认启用或真实会话操作。整体 planned、默认入口不变；完整原始 Entries 始终保留，不因此增加迁移。
- **范围验收**：真实源码、临时 Go overlay 与 Node 探针均由主控核对；`git diff --exit-code HEAD --` 确认 service/compaction/state_tools/types、provider replay、proto、依赖和批准计划未改；`git diff --check` 通过，输出 `COMPACTION_PRIMARY_COUNTEREXAMPLE_AND_SCOPE_PASS`。用户方向确认落盘后，三份文档中的 `continuous_tail_design`、只读设计状态和证据日志再次核对通过，输出 `CONTINUOUS_TAIL_USER_DECISION_RECORDED_PASS`、`FINAL_USER_DECISION_DOC_AND_SCOPE_PASS`；该验证只证明决策记录和改动范围，不提升压缩功能状态。此次业务代码零新增修改，仓库仅收尾已有 task/process/Design，未执行真实会话、模型、部署、重启、提交或推送。

#### 续办：结构化判别与压缩来源取证（功能未启用；历史过程）

本轮继续第一步 C2B 的其余数据域，不重复已验收 W1，不越过历史读取预算、结构化兼容或压缩映射的未决门槛。主控同步 `task/todo.md` 中 W1 独立切片的完成边界；本轮新增工作仅为两项只读委派、主控合成探针和既有文档收口，没有新增业务实现。

- **委派与验收纠偏**：[结构化引用核对](a8d94b19-99bd-44c7-be03-2f9dffd99767)仅核查 plan/todos schema、生产/读取路径和最小双读草案；[压缩归档来源核对](b8fb1d79-4bbb-44e0-a6f4-c53d5026f005)仅核查 append-only 历史、活动投影与原生归档。串行委派，禁止改仓库/真实数据/配置，主控独占文档写入。首项提出“Blob 未命中后 protobuf 解析成功即旧内联”，主控拒收并运行反例；另指出 JSON 判据不可套作 protobuf 判据、W1 的回滚能力不可覆盖 plan/todos。第二项以缺少显式 Seq 区间断言必须新增持久字段，主控亲读 `compactedPromptProjectionEntries`/`autoCompactionPreservedEntryIndexes` 后退回纠正；实际投影移除集合可重建，不据弱结论扩展 schema。
- **结构化主控探针**：在 `/tmp/mixed-structured-probe.2dpCD5/main.go` 用当前生成类型及真实 protobuf/SHA-256，隔离 HOME 运行 `go run -p=1`；退出0，输出 `LEGACY_PROTO_32_CONFIRMED plan=32 todo=32`、`REAL_SHA256_ACCEPTED_AS_PLAN attempt=167 plan_empty=true unknown_bytes=32`、同样的 TODO 结果与 `STRUCTURED_PROTO_AMBIGUITY_PROBE_PASS`。完整日志 `result.log`。证明合法旧内联和内容引用长度可能重叠，而且真实引用能被当成空 protobuf；不能靠解析成功隐藏缺内容。该探针没有读真实会话或调用模型，不是结构化双读已修复的证据。
- **压缩主控复核**：读取当前 `types.go` 摘要 payload、`compaction.go::applyCompactionToConversation/newCompactionSummaryEntry`、`projector.go::compactedPromptProjectionEntries`。公开客户端 bundle 的实际 SHA-256 与 Design 基线匹配；按真实生产代码核实 windowTail 为待摘要消息数减实际摘要消息数的非负条数，`DIu` 顺序拼接 archive/root，不去重。exec 实际生产位置约2848394/2848559，DIu为19538609，纠正报告近似偏移及省略参数后的伪字面匹配。首次按报告简写精确搜索失败退出1，未作为通过；随后实际源码上下文检查退出0，`COMPACTION_PUBLIC_SOURCE_RECHECK_PASS`，完整 `compaction-source.log`。未执行整个 Cursor/官方恢复，未证明全部路径都不消费 windowTail。
- **已消除与仍未消除的缺口**：原始正文仍在 Entries，实际投影移除集合可由既有算法重建；因此不自动增加压缩持久字段。模型实际摘要输入（含候选与预算裁剪）不等于投影移除集合，原生 archive 与活动 root 的排他映射、摘要标记及连续压缩/取消/fork/回读仍待合同和合成样本闭合。结构化域需要用户确认32字节歧义时的停止边界：候选为已校验 Blob 优先、非32字节保留旧内联、未命中且恰32字节明确失败并保全；此候选会阻止该类旧内联状态继续，尚未获准。新结构化引用若发布，最低回滚目标也须含对应双读，不能借 `07aa83f` 的 W1 root 能力兜底。
- **任务与文档范围**：事实/候选写入系统 Design §19.3d，状态 `not-ready`；`mixed-checkpoint-domain-probe` 只读取证完成，`mixed-checkpoint-structured` 为兼容决策阻塞。C2B 仍待完成，默认 `service.go` 未接 native writer；C2A/C3/C4/真实旧会话恢复未实施。本轮保持原7文件工作树改动，仅进一步编辑已有 task/process/Design，未改 Go 源码/测试、计划、依赖、配置或持久格式；不重跑此前已通过且代码未变的全包/race/vet，只做落盘后的范围、证据和差异核对。

**续办结果**：两项有限取证已由主控纠偏验收，不视为功能交付；结构化兼容取舍需单独确认，压缩继续保持 fact/design gap。既有 W1 为独立源码 `implemented-not-wired`、模拟路径 `verified-partial`，整体仍 planned，用户现场错误未修复验收。未部署、重启、提交、推送或读取/写回真实会话。

#### 本轮实施与主控验收：W1 独立原生编码及确认协议

本轮按用户授权的 `.cursor/plans/官方_byok_混用改进_7be7660b.plan.md` 主控推进，保留开工时四份文档的既有修改。先完成已闭合 C0-W1 的永久失败回归、独立原生编码与定向确认，不自动把有限数据域替换成全部请求的默认 writer。**独立编码和模拟确认已验证，默认入口仍未接线，整体混用仍未交付；用户真实会话的 missing blobs 和三个子任务错误未验收关闭。**

- **分工与范围控制**：[根引用与语义测试](d8776f9a-329f-465b-8818-5f1014687e0d)只写 `projector_fork_test.go`；[有界原生编码](386b77cc-be09-4407-98ce-07be52af116a)只追加 `projector.go` 编码 helper；[确认协议测试](6722365e-870b-4fa4-aa86-bd4a7dd32045)只写 `checkpoint_blobs_test.go`；[独立复审](5465eb26-46a4-45a7-8659-b6e36e985083)只读审查启用边界与实现。生产代码按文件串行，主控是唯一文档写入者，并亲自实现原始条目投影、补强测试、验证原始输出和最终验收。各任务交代输入域、准确函数、固定失败判据、隔离环境、不可改文件和停止条件，未委派整个专项。
- **首次交付纠偏**：根引用任务漏了两条样本，主控退回补齐，不把 19 条与重复实例替代两条验收。完整工具结果复审发现主控早期接缝取自旧回放视图，编辑结果经面向模型的裁剪丢失 `path`；该交付拒收，补永久 RED 后由主控接管改为 `nativeCheckpointMessages` 从有效原始条目取保存正文。块级 `isError` 的测试原先误查结果内部键，已改为块级字段与未知字段检查。主控另复现并修复工具 ID 空白归一化误配和 user 的额外工具身份被静默丢弃。复审确认两个阻塞项关闭；不会照单接受 PASS 自报。
- **永久 RED 证据**：旧默认方法生成的两条、19 条及重复实例在仓库查找处失败，root 为60/45字节内联 JSON，前缀 `7b22636f6e74`，Go退出1，原始 `/tmp/native-root-red-8QP8Tl/red.log`。完整 Write 历史沿独立方法成功投影后，断言失败 `tool result lost path: {"success":{"after_full_file_content":"完整正文"}}`，退出1，证据 `/tmp/native-write-SAWRYh/test.log`。身份/额外字段两项 RED 同样退出1，主控核验具体原因后外层输出 `EXPECTED_IDENTITY_RED_CONFIRMED`，证据 `/tmp/mixed-w1-main.yakIpl/identity-red.log`。根回归随后转向独立原生方法验证；这是旧方法 RED→独立新方法 GREEN，不是默认入口已经转绿。
- **实现内容**：`projector.go` 的独立 `projectNativeCheckpointProjection`/`nativeCheckpointMessages`/`encodeNativeCheckpointRoot` 输出原生单条 `{role,content}` 正文与 raw32 SHA-256 引用；复用回合/user/step 内容图，重复消息实例保留而 Blob 去重，原始参数/完整结果不经 float64 重编码或模型结果裁剪。身份精确配对、结果倒序和重复完整批次保留；reasoning/图片/未知字段/不完整工具/压缩/plan/todos/未解析导入引用等域外输入固定错误，投影及输入完整保全。JavaScript 安全整数、JSON 字符串结果类型与5,000,000/5,000,001字节边界有回归。未改 provider 回放、读入口、持久化 schema、依赖、配置或默认发布入口。
- **读取与回滚基线**：`TestNativeCheckpointProjectionReadCompatibility` 用完整预取的新格式和旧内联格式读取相同文本/工具语义，重复实例不丢；相关旧内联、fork、resume、取消、导入完整性测试继续通过。HEAD=`07aa83f`，主控差异断言确认 `imported_blobs.go`、`token_usage.go`、`prompt/replay.go` 和既有规范化逻辑未改变，匹配最低回滚读取基线的源码能力；没有重建/核验旧安装包，不承诺无预取或其它数据域回滚。
- **实际协议参与与边界**：`TestNativeCheckpointProjectionAcknowledgementsThroughBidiAppend` 将独立投影进入既有队列，捕获真实 SetBlob 消息，在模拟客户端仓库校验哈希；ACK 经真实 Connect HTTP BidiAppend→解码→串行 actor→写确认。最后 ACK 前无 checkpoint/终态，之后严格 checkpoint→TurnEnded→End且仅一次；原生 root 与 turn/user/step 子引用均可解析，迟到重复 ACK 不重复发布，actor 收尾。该 HTTP 仅验证确认上行；未从默认模型运行入口产生原生投影，未调用真实模型、Cursor 或官方服务，不能据此标完整 C2B 通过。
- **主控最终验证**：隔离 HOME=`/tmp/mixed-w1-main.yakIpl/home`，`GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`，固定 `GOCACHE=/Users/yaogj/Library/Caches/go-build`、`GOPATH=/Users/yaogj/go`。最后代码/测试修改后执行 `go test ./internal/backend/forwarder ./internal/backend/agent/prompt -count=1 -timeout=3m`，两包退出0（17.755s/0.939s）；`GOGC=20 GOMAXPROCS=2 go test -p=1 -race ./internal/backend/forwarder -run '^TestNativeCheckpoint|^TestProjectCheckpointProjection|^TestCheckpoint|^TestCancellationDiscardsUnpublishedCheckpoint|^TestImportedConversationState' -count=1 -timeout=120s`退出0（测试6.493s）；同环境 `go vet -p=1` 两包退出0。原始证据 `packages-test.log`、`race-bounded.log`、`vet.log` 均在该临时目录。初次 race 在大型生成代码编译阶段缓慢，主控仅终止本轮所属编译/测试进程，未记为通过；降低编译资源重试一次后通过，未继续重复验证或跑无关前端/全仓套件。
- **范围与状态核对**：验证批次 `git diff --exit-code HEAD -- ...` 确認默认 `service.go`、actor、`checkpoint_blobs.go`、读取器与 `go.mod/go.sum` 无修改；输出 `W1_BASELINE_READ_AND_DEFAULT_ENTRY_UNCHANGED_PASS`、`W1_PRIMARY_VALIDATION_PASS`，`git diff --check`通过。业务代码仅修改已有 `projector.go`，测试仅修改已有两文件。文档收口后另做一致性/范围/gofmt/差异检查，不重复源码测试。
- **后续有限取证**：[历史读取限制核对](d3503d78-6565-4a3d-b3ff-f1300e9912d2)只读报告现有预算落点。主控亲自核对 `ContentBlobStore.Get/Put` 与预取仓库，确认没有可直接继承的读取数量/总字节/等待期限；报告引用的 provider/gateway 限制不套用到 Bidi GetBlob，读写共用编号/分开等待已是计划规则，不接受报告把它重新列为用户决策。该取证不等于 C2A 设计获批，未实现 GetBlob。

**验收与暂停边界**：`mixed-red-regressions` 的原生根回归与 `mixed-checkpoint-w1-codec` 独立切片已验收归档；W1 源码为 `implemented-not-wired`（默认未接），模拟投影/读取/确认路径为 `verified-partial`。完整 C2B 仍 pending：默认入口同时覆盖推理/压缩/计划待办/进行中工具等域，直接替换成有限 W1 会把这些已有会话变为投影失败，不能从局部 PASS 自动批准这一用户可见变化。下一步是闭合这些数据域及默认切换合同，而不是宣称只剩实机验证；C2A 的预算/取消、C3 的稳定对齐/部分提交、旧会话受控恢复和 C4 的完整父子参数仍分别 pending。没有新增文件/依赖/配置/迁移，没有读取或改写真实会话、打包、安装、重启、commit/push。

#### 独立检查点写出与回滚合同（2026-10-11；mixed-design-contract）

本次只完成用户指定的 `mixed-design-contract` 设计任务，不实施后续 writer，也不修改 `.cursor/plans/官方_byok_混用改进_0ab94bd0.plan.md`。开工已有任务与过程记录修改（真实日志/截图续证），本轮保留这些内容，仅增补现有 Design/PRD/任务/过程记录，未新建仓库文件。

- **交付对象**：系统 Design §19.3c C0-W1 v1 闭合文本/可无损完整工具批次的原生消息编码、raw32 内容引用、完整可达 Blob 图、确认发布顺序、5秒写降级、错误/取消/重复/并发与回滚合同。W1 的 Design Readiness=approved、delivery_status=contract-ready；整体仍 planned，完整 C0/C2B 未通过。作者完成正向/失败/回滚模拟，未做独立评审，不将自审冒充外部复核。
- **新核实的兼容事实**：实际 Cursor 3.15.19 exec serde/构造器把 plan/todos/summary/archive 作为引用读取；当前后端 plan/todo 读写仍是内联 protobuf，archive 投影还误用 summary schema。原生 JavaScript 会舍入超安全整数，工具结果字符串正文是合法 JSON 时 `pi/hi` 会改变其类型；工作台单条 root 超过5,000,000字节会被省略。这些限制记为具体数据域门禁，不凭 Go 内部回放成功承诺完整客户端无损。
- **局部阻塞**：W-STRUCTURED、W-COMPACTION、W-CONTENT、W-NUMERIC、W-RESULT-TYPE、W-IMPORTED 与 W-ID-AMBIGUITY 只约束各自载荷；异步 GetBlob/历史增量/子模型承载未知不成为 W1 的整体前置。没有缩减完整产品目标，没有授权静默删块、数值转字符串、全局 writer 切换或新配置门禁。后续 `mixed-checkpoint-codec` 可先按此锚点拆永久回归及有界实现，完整链路仍按原依赖推进。
- **回滚闭合**：回滚目标按读能力限定为经相同样本验证的 `07aa83f`（1.75.0）源码基线或等价版本；W1 恢复仍要求必需 Blob 完整预取，不能保证无预取/复杂状态/更早版本。SetBlob 已写而状态未发布只会留下不可变孤立内容；状态发布后客户端引用不可因代码回退撤回。保留 context/state、全部 Blob 与切换后新增历史，不恢复旧磁盘快照、不删除会话；内容或读能力不足时保留停止。真实产物/部署窗口需另行授权，本轮无此操作。
- **本轮合成客户端证据**：运行 `node --input-type=module`，按源码摘要和函数界限动态提取实际 exec `pi/hi` 及工作台 `YMs`，隐私值容器、内容仓库和 JSON serde 的外部依赖用合成替身。5条消息（含重复用户消息、完整工具调用/结果）保存为4个不同 Blob，引用加载恢复5条、旧内联加载0条；实际转换断言角色/文本/ID/名称/参数/结果相等，并断言数字/JSON字符串结果限制。退出0，输出 `NATIVE_CHECKPOINT_CONTRACT_PROBE_PASS messages=5 distinct_blobs=4 inline_restored=0`、`NATIVE_JSON_COMPAT_LIMIT_CONFIRMED unsafe_integer_and_json_string_result`、`STRUCTURED_REFERENCES_CONFIRMED plan_todos_summary_archives limit_bytes=5000000`。此为函数级合成合同取证，不是完整 Cursor/官方运行验收。
- **文档修改后的源码基线验证**：隔离临时 HOME、固定已安装 GOPATH/GOMODCACHE/GOCACHE、`GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` 下执行 `go test ./internal/backend/forwarder -run '^(TestCheckpoint|TestCancellationDiscardsUnpublishedCheckpoint|TestProjectCheckpointProjection|TestContentBlobStore|TestImportedConversationState)' -count=1 -timeout=90s`，退出0，包耗时1.033s。只验证现有确认/取消/旧timer、投影、内容存储及已预取读取基线，不证明新 writer 已实现；没有改业务代码，未重跑无关全仓/前端/race/vet。
- **设计缺陷仍为预期 RED**：复用现有临时 Go overlay，执行 `go test -overlay=/tmp/byok-screenshot-probe.gckLUP/overlay.json ./internal/backend/forwarder -run '^(TestScreenshotNativeRootReferencesResolve|TestProjectCheckpointProjectionBuildsResolvableForkState)$' -count=1 -v -timeout=90s`。Go测试预期退出1、0.467s：既有本地回放对照通过，原生断言仍为 `native root content lookup: missing blobs=2, want 0`（root 95/49字节内联 JSON）。外层明确核验指定失败原因，未把 RED 写成修复通过。两份本轮输出在 `/tmp/mixed-checkpoint-contract.nGTHG5/{baseline.log,native-red.log}`。
- **记录一致性验证**：首次范围断言因 Git 默认转义中文路径使检查脚本退出1，测试结果未受影响；改用 `git diff --name-only -z` 后复验退出0。检查 §19.3c 唯一、局部阻塞/不可撤回/回滚基线均已写入，业务/协议/计划无差异；计划 SHA-256 始终为 `bf9c84f678a291eebf20a4981966ff1b612e889b3aa8a41ac4dbb7f813484519`，`git diff --check` 通过，输出 `DESIGN_SCOPE_AND_PLAN_UNCHANGED_PASS`、`CHECKPOINT_CONTRACT_BASELINE_PASS; NATIVE_WRITER_EXPECTED_RED_CONFIRMED`。记录落盘后已另行完成同范围与状态一致性核验，输出 `MIXED_DESIGN_CONTRACT_FINAL_VERIFY_PASS`；计划、业务代码及既有诊断保持不变，改动仅限四份既有文档。

**收口**：本设计任务完成并从活动认领项移入完成索引；后续功能仍有 feature gap（原生 writer/复杂状态/缺失读取/增量/完整子参数）及 test/env gap（真实官方兼容），不能宣称用户现有 `missing blobs` 已被修复。未部署、重启、改真实凭据/配置/业务数据、commit 或 push。

#### 1.75.0 真实日志与设计差距核对（日志窗口：2026-10-12 UTC）

用户提供 `/Users/yaogj/Downloads/logs`，反馈最新版本的实际效果与专项计划相差甚远。本轮仅分析日志和当前源码，记录已核实事实与缺口，不实现未闭合设计，不修改运行配置、证书、业务历史或原日志，不部署/重启/commit/push。

- **材料与去重**：manifest 为 `app_version=1.75.0`、`mode=basic`，会话 `20261012T003841.157486000Z-a69604a34ce5`。events 共2324条，sequence 1–2324连续；diagnostics 171条逐条等于 events 对应 sequence 的副本，不能累加成独立故障。当前仓库 HEAD 为 `07aa83f`，构建版本同为1.75.0；manifest 无构建 commit，不能仅凭版本号证明运行二进制与 HEAD 完全相同。
- **现场主链事实**：本地主请求 `aa9dc247…` 自00:38:56运行，5次模型调用均收到 provider HTTP 200；前4次 `model_call_final=succeeded`，结束原因为工具调用，第5次被取消。00:39:38.389–38.599派发3个不同 subagent_run_id；随后3个 RunSSE 请求及两次后续 RunSSE 请求存在 `execution_target=official_upstream` 的HTTP完成事件，状态200。外层后端 RunSSE的 `local_runtime` 标签不能否定内层官方透传；HTTP成功不能证明官方业务成功，也不能仅按时间推断全部请求与子任务的一一关系。
- **结果与取消边界**：00:39:46.580、47.773主请求收到两条 `exec_client_message`，47.969之后再次调用模型；basic模式不包含工具结果正文，不能认定两个结果均成功或第三个结果丢失。00:39:50.690收到 `conversation_action`，50.730派发 `kind=cancel`，随后流取消、terminal_error及退订；不能反推取消由用户主动点击，或用最后的取消解释此前异常。
- **检查点与独立噪声**：39次checkpoint Blob派发对应39次ACK，未出现timeout/reject/unmatched；这证明内容确认链有进展，不证明官方能解码完整检查点。TLS握手拒绝87次（api3 55、metrics 27、api2 5）；默认模型接口2次本地502，另有插件/MCP等404。尚无证据把这些独立事件认定为混用异常的唯一根因。
- **实现差距已核实**：`runtime_summary.go:51–57`仍仅在本地Entries为空时导入客户端状态；`projector.go:616–622`仍把本地replay编码直接放入root字段，未切原生内容引用写出；`exec/bridge.go:803–833`选取并发送ModelId，未承载完整参数/独立Max Mode；缺失Blob异步读取尚未接线。任务真值与`releaselog/1.75.0.md`同样明确整链未交付。差距首先属于feature gap，不能以“缺实机验收”概括，更不能把参数快照/预取文本工具读取的隔离PASS当作混用完成。
- **验证与停止条件**：本轮已运行Python日志解析、事件关联、sequence/副本一致性核对及源码/版本只读定位；未重放官方响应或运行修复测试。basic日志缺失入出站正文、子模型参数及业务终态，尚不能建立捕获现场具体业务失败的可重放RED→GREEN回路，因此不猜测具体子任务失败原因并修改代码。下一步先用合成协议链补“父Task→客户端子请求→模拟官方终态→父结果唯一追加”的可观察断言；需要现场对应时仅补最小脱敏错误/终态证据，不要求整份真实prompt或凭据。既定实现顺序仍优先缺失内容异步恢复，再原生写出与增量同步，各切片遵守对应设计门禁。

- **记录落盘验证**：本轮已在文档修改后运行日志事实断言与 `git diff --check`，均通过；只修改 `docs/process.md` 与 `task/todo.md`。本轮未运行 Go 测试，不将日志诊断记为代码修复验收。

**复用教训**：版本号最新不等于专项目标已交付；feature gap与env/test gap分开；官方透传的HTTP200、Blob ACK、内部参数保留分别只能证明各自层级，不能证明跨渠道整链正确。本轮仅完成差距诊断，整体仍为planned，已交付切片保持verified-partial。

#### 界面截图续证：根消息内联 JSON 被当成 Blob 引用

- **界面已确认的失败**：用户补充截图中，三个 Explorer 子任务均为 `Stopped with error`；当前选模显示官方渠道，继续操作时界面提示 `Conversation data missing`、`19 missing blobs`。截图没有时间戳与完整切换过程，不能由当前选模反推最初父/子任务模型。顶部Git命令另报 `not a git repository ... /Volumes`，证明该次命令没有识别到Git仓库；具体工作目录及是否为子任务失败原因未明，不能擅自改工作目录或认为所有错误同源。
- **标识特征**：界面可见的缺失标识前缀反复为 `7b22636f6e74`，十六进制解码是 `{"cont`，与本地消息JSON的content键开头一致。它不是“真实内容已经删除”的证据，而是根消息正文被当成内容引用的强证据。`projector.go:616–622`调用`EncodeReplayMessages`直接赋root；`prompt/replay.go:228–232,265`构造role/content映射后JSON编码。原生消费者按系统Design §19.2取每项作为32字节内容引用，不能直接消费该内联格式。
- **最小失败复现**：仅在`/tmp/byok-screenshot-probe.gckLUP`创建Go overlay和合成两条消息，不改仓库测试或业务代码。真实`NewHistoryProjector().ProjectCheckpointProjection`输出root为95/49字节，两条前缀均为`7b22636f6e74`、`inline_json=true`；按原生内容引用契约从输出Blob集合读取，均`found_in_projection_blobs=false`。测试失败`native root content lookup: missing blobs=2, want 0`；这是同机制的合成复现，不是恢复现场19条真实正文，也不是实际运行整个Cursor客户端。
- **测试证据与盲点**：隔离HOME、固定Go缓存、`GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`执行`go test -overlay=/tmp/byok-screenshot-probe.gckLUP/overlay.json ./internal/backend/forwarder -run '^(TestScreenshotNativeRootReferencesResolve|TestProjectCheckpointProjectionBuildsResolvableForkState)$' -count=1 -v -timeout=90s`，Go测试预期退出1（包耗时1.312s），外层核验脚本确认失败原因及对照后退出0。既有`TestProjectCheckpointProjectionBuildsResolvableForkState`通过，新原生引用测试失败：本地编解码自洽不等于原生恢复兼容。输出在临时目录`result.log`，未将预期RED表述为修复通过。
- **日志关联边界**：截图请求ID `06e58ca8-249f-471b-8ef9-cf93500f2a34`及界面错误正文不在现有events中。截图补上业务失败事实，但不能逐帧对应已导出官方HTTP200或确认三个子任务均同一根因；此前“结果状态未知”现在对截图中的三个子任务可更正为界面明确错误停止，具体错误原因仍待详情。现有两条exec_result可能承载错误，不能因父模型继续而视为成功。
- **结论与修复验收落点**：已定位并合成复现与现场标识一致的检查点写出格式缺陷（阶段2B）；它不是仅仅缺实机验收，更不是靠重试、改证书或新建聊天完成修复。后续须输出原生CoreMessage内容及其真实引用，保持写确认与旧历史兼容，并把上述原生读取断言固化为永久回归；19条现场缺失与三个子任务的完整因果仍需最小额外证据。当前仅诊断，不启用尚未闭合的写出设计，不改存量会话、不部署/重启/commit/push。

- **截图记录落盘核验**：文档修改后已核对隔离测试原始输出、缺失引用数量与十六进制前缀、截图请求覆盖范围，并通过 `git diff --check`；仓库改动仅为任务与过程记录，业务代码及永久测试均未修改。

**续证教训**：截图业务错误必须与HTTP/ACK分层核对；反复出现JSON十六进制前缀应首先核对引用/正文边界，不能直接归因内容删除；恢复验收必须检查客户端消费合同，不能只用生产者自己的旧解码器验证输出。

#### 完成情况复核（2026-09-30）

- 主会话复核已交付的参数快照、原生文本/工具读取及工具批次规范化，未确认新的业务代码缺陷。本轮改动限于既有 `imported_history_test.go` 和必要任务记录：补强失败原因/阶段断言，将失败保全验证接到真实 Connect HTTP BidiAppend；未新增业务行为、依赖或文件。
- 永久回归覆盖未知消息/内容字段、错误角色/载荷、工具缺失/失配/重复、根引用部分缺失/混合、SHA-256 校验及后段 plan/todo 解码错误；断言具体错误且完整原会话不变。HTTP 失败路径断言返回内部错误、未调用模拟模型、持久会话不变，防止任意提前错误掩盖待测行为。
- 测试修改后使用隔离 HOME、固定 Go 缓存、`GOPROXY=off GOSUMDB=off`：`go test ./internal/backend/agent/core ./internal/backend/agent/bridge/exec ./internal/backend/agent/prompt ./internal/backend/forwarder -count=1 -timeout=3m`、`go test -race ./internal/backend/forwarder -run '^TestImportedConversationState|^TestImportedTurnIDs|^TestRewindImportedTurnPrefix|^TestNormalizeReplayToolBatches|^TestTrimReplayDangling|^TestSubagentModelParam' -count=1 -timeout=90s`、上述四包 `go vet`、改动 Go 文件 gofmt 检查及 `git diff --check` 全批通过，输出 `MIXED_COMPLETION_FINAL_REVIEW_PASS`；编辑器诊断未报错。文档同步后再运行导入/参数/工具批次定向收口及差异检查，不重复无关全仓或前端套件。
- 完成边界保持：参数保留仅到服务端快照/执行桥；历史输入仅支持内容已预取齐全的文本和完整工具批次。缺失 Blob 异步读取、原生检查点写出、同会话增量同步、客户端完整参数/独立 Max Mode 与四种父子组合仍未完成；无法表达的内容仍明确失败。整体保持 planned，已实施路径为 verified-partial。纠正任务摘要中“原生数组转换仍未实现”的旧表述，明确文本/工具数组已交付，其余类型待办。未做真实 Cursor/官方验收，未部署、重启、提交或推送；下一项仍是缺失内容恢复，不把设计缺口当作整个已交付切片失败。

#### 最新交付：已预取文本块与完整工具历史读取

- 实现 `imported_blobs.go` 的 text/tool-call/tool-result 转换与工具批次校验；参数/结构结果保留原始 JSON 和大整数，字符串结果保留原文本。未知/不可表达内容明确失败，不裁块继续。永久失败回归先复现工具失配和额外内容被静默接受，再修复转绿。
- HTTP BidiAppend→导入/保存→真实 PromptCompiler/provider gateway→模拟模型请求验证完整工具历史、结果倒序和参数精度；另验证失败请求不调用模型且原持久会话逐字段不变。测试范围是隔离协议链，不是实际 Cursor/官方或真实 provider。
- [原生历史独立复审](8da7b35d-42b5-479e-9eeb-06c73942d78c)发现分组 ID 触发结果重排；主会话补永久入口回归 RED，并进一步复现重复工具批次持久化3条而非4条。`projector.go` 改为完整单批次不重排结果、已有结果后仅凭实际非空模型调用身份合并交错片段；两项转绿，复审确认关闭。新增同/不同/缺失调用身份对照，保留同一调用的既有合并行为。
- 最终隔离 HOME、固定 Go 缓存并禁下载：`go test ./internal/backend/forwarder ./internal/backend/agent/prompt -count=1 -timeout=3m`、`go test -race ./internal/backend/forwarder -run '^TestImportedConversationState|^TestImportedTurnIDs|^TestRewindImportedTurnPrefix|^TestNormalizeReplayToolBatches' -count=1 -timeout=90s`、两包 `go vet`、`git diff --check` 全批退出0，输出 `NATIVE_TOOLS_REVIEWED_VALIDATION_PASS`；独立复审另跑上述新增顺序/重复/调用身份及既有穿插文本回归，含 race，通过。
- 该输入切片为 `verified-partial`；仍未支持 reasoning/image/file、错误标记、多模态工具结果和调用后的文本。缺失 Blob 异步读取、原生检查点、同会话增量和完整子模型参数链仍待实施；不新增依赖/持久字段，不部署、重启或提交。下一项优先补无预取内容的异步恢复，验收目标为模型输入完整且取消/超时/错误保持旧记录。

#### 前轮交付：原生文本历史读取已完成隔离验收

按系统 Design §19.3a C0-H1 推进阶段2A的输入恢复前置，保留此前参数修复。当前确认并修复：root 内容已经预取齐全时仍被跳过或误当内联 JSON；导入后段失败前就提前改动 token/回合引用/下一回合编号。本轮业务变更为既有 `forwarder/imported_blobs.go`、`token_usage.go`，永久测试在 `imported_history_test.go`；没有新文件、依赖、协议字段或持久化格式。

**修复范围**：解析已校验 SHA-256 的 root 引用，接通原生 user/assistant 字符串消息，保持顺序和重复实例；完整 root 优先于 turns，不再次拼接。旧系统提示不进入本轮模型输入。保留旧内联读取和全部 root 缺失时的既有 turns 回退；部分缺失、混合格式、损坏或尚不支持的数组内容明确失败。全部解码和 entry 编码成功后才更新会话元数据，失败返回 nil entries 并保全调用者会话。本轮不是完整原生 codec，也没有启用 GetBlob。

**RED→GREEN**：`TestImportedConversationStateReadsPrefetchedNativeRootWithoutTurns` 在旧实现退出1（0.662s），失败为已齐全 root 仍被直接 JSON 解码，`invalid character '÷' looking for beginning of value`；修复后同一测试退出0（0.567s）。补齐 root 优先、完整会话失败保全六分支、未知/损坏/数组内容拒绝七分支及恰32字节内联 JSON 的永久测试。调整旧 turns 回退夹具：原夹具错误地把可用 protobuf turn Blob 当作 CoreMessage root Blob，现在改为真正缺失的原生 root 引用；保留原来的回退断言。

**真实入口接线**：`TestImportedConversationStateNativeRootReachesProviderThroughBidiAppend` 通过本机随机端口 Connect HTTP、protobuf/hex 上行、真实 `BidiAppend`/串行处理/导入与文件保存/默认 prompt compiler/provider gateway，到模拟模型适配器捕获 `StreamRequest`。断言三条历史按 user→assistant→user 到达（相同问题出现两次仍保留），当前 user_query 一次、旧系统提示未传入、三条 model_message 已保存；取消并等待模拟 provider 及消息处理器结束。没有访问真实模型、官方服务或用户配置。测试早期曾因33字节夹具、错误动作类型、将 current_user_request 提醒中的相同文字计成重复用户历史而失败，已核实真实 schema/既有提醒规则后只修正测试，不改范围外业务语义。

**验收证据**：临时隔离 HOME，固定 `GOPATH=/Users/yaogj/go`、`GOMODCACHE=/Users/yaogj/go/pkg/mod`、`GOCACHE=/Users/yaogj/Library/Caches/go-build`，`GOPROXY=off GOSUMDB=off`。最终批次执行 `go test ./internal/backend/forwarder ./internal/backend/agent/prompt -count=1 -timeout=3m`、`go test -race ./internal/backend/forwarder -run '^TestImportedConversationState' -count=1 -timeout=90s`、`go vet ./internal/backend/forwarder ./internal/backend/agent/prompt`、`git diff --check`，批次退出0，输出 `NATIVE_ROOT_READ_VALIDATION_PASS`。主会话复审业务 diff 和测试清理逻辑；编辑器诊断无报错。未新增独立评审，不将自审记成外部验收；未跑无关前端或整仓套件。

**状态与下一步**：C0-H1 的源码/模拟入口链为 `verified-partial`；整体混用功能仍为 planned，完整 C2A/C2B/C3/C4 未完成。下一步补齐原生内容数组转换并接缺失 Blob 异步读取，目标为没有 preFetchedBlobs 时也能完整恢复模型输入，且取消/超时/错误不启动模型；随后推进原生检查点写出与增量同步。此步骤只证明已预取文本历史可读，不能宣称真实官方切换或工具/推理/图片/文件已恢复。所有修改保留在工作区，未部署、重启、提交或推送。最后文档落盘后另做定向回归及状态/差异复验。

#### 前轮参数保留：已从设计进入永久回归与修复

用户要求基于当前代码和文档跨过设计门禁、向前推进。本轮落实计划“未闭合项只阻塞相关工作包”：闭合系统 Design §19.4b C0-P1，仅实施已经明确的“参数键值保留及深拷贝”，不擅自通过整个 C0，不启用新的父子关联或官方正文改写。开工工作区已有四份 docs/task 文档修改；本轮业务变更限于 `internal/backend/agent/core/types.go`、`internal/backend/forwarder/service.go`，测试落在既有 `lifecycle_test.go`，没有新文件/依赖，也没有修改真实数据、配置、客户端或运行实例。

**RED→GREEN 证据**：先新增 `TestSubagentModelParametersRemainDistinctAfterInboundDecode`，沿真实 `decodeInboundIntent(run_request)` 比较同模型 low/high 选择；旧源码退出1，失败为“子模型 low/high 参数经真实入口解析后成为相同状态，参数键值已丢失”（0.597s）。保留完整参数序列并深拷贝后，同一命令退出0（0.683s）。补齐5个测试，覆盖参数顺序/重复/空字符串/nil/未知 protobuf 字段，客户端源对象、克隆、lookup 返回值的独立所有权，inherit/disabled/无效覆盖的原有选择规则及日志摘要不泄露参数值。

**接线证据**：`TestSubagentModelParametersReachTaskBridgeAsIsolatedSnapshot` 沿请求解析→`handleRunIntent` 初始化→stream 参数保存→真实 `handleToolInvocation(Task)`→真实执行桥（外包捕获器）核对 high/Max Mode 与子模型 ID，并修改桥接副本证明父 stream 不受污染。启动使用 prewarm 开关抑制 provider；这不是完整 HTTP、真实 Cursor 子请求或官方运行证据。解析、保存、派发和查找都复用同一 `Clone`，派发读取在 stream 锁内完成；`SubagentArgs` 仍只有模型 ID，不能声称客户端最终参数已补齐。

**本轮验收**：隔离临时 HOME、保留本机 Go 缓存、设置 `GOPROXY=off GOSUMDB=off`。`go test ./internal/backend/agent/core ./internal/backend/agent/bridge/exec ./internal/backend/forwarder -count=1 -timeout=3m`、`go test -race ./internal/backend/forwarder -run '^TestSubagentModelParameter' -count=1 -timeout=90s`、`go vet ./internal/backend/agent/core ./internal/backend/agent/bridge/exec ./internal/backend/forwarder` 和差异格式检查的整批命令完成、退出0。源码/测试修改后运行过验证。最终收口核验使用 `go test ./internal/backend/forwarder -run '^TestSubagentModelParameter' -count=1 -v -timeout=90s`、`git diff --check` 和 `git status --short`，必须在最后一次文档写入后执行；核验范围仅为参数保留切片及工作区差异，不能证明整个混用功能完成。没有运行无关前端、独立 module 或整仓构建。

**状态与下一步**：参数保留切片的内部合同已验收；阶段1开始，四项回归只完成参数丢失这一项。混用完整功能仍为 planned，父子参数链至多 verified-partial；历史读取、检查点格式、增量对齐和子请求参数承载仍未修复。下一步直接闭合原生根消息编解码的输入/输出、旧格式读取和错误合同，然后落对应永久回归，继而接 GetBlob 异步读取；不继续把它们阻塞于四组合参数调查。新历史写出、对齐/崩溃恢复及官方正文改写仍按对应 C0 审核。未部署、重启、提交或推送。本轮未新增委派评审，复杂历史和承载设计仍保留独立审查要求。

#### 前轮取证记录（历史过程，不是本轮修复验收）

用户授权主会话主持分阶段执行并验收委派结果，保持最小改动与范围约束。开工 `git status --short` 无输出；本次仅更新既有 PRD §17、系统 Design §19、`task/todo.md`、本文及计划，不改功能、schema、依赖、真实配置或 Cursor 安装，不部署/重启/commit/push。`docs/lessons.md` 不存在，未为此新增文档；本专项更正直接记入对应设计和执行记录。

**委派及验收**：并行安排 [原生消息核查](d5dbe311-191c-4905-900b-cd24e23ad9a5) 和 [子模型参数核查](4057c515-d08a-4b9b-a023-5f02eeb16f78)，均限定只读客户端发布代码/指定后端文件、合成临时探针、禁止真实数据和功能修改。主会话逐段核对真实代码，未直接接受其结论：要求参数任务将手工等价实现/打印输出改为动态提取原方法及断言，纠正非 fork Max Mode 的全局配置来源和空参数的偏好优先；另亲自发现 reasoning 经 `unknown` 包装保留，纠正仅因 `hi` 缺少同名 case 就认定其不支持的推断。

**本次实际验证**：主会话运行 `node /tmp/mixed_model_probe/probe_assert.mjs`，输出 `SUMMARY pass=11 fail=0`。探针动态提取当前客户端的模型配置及变体选择方法，合成目录断言 low/high 变体串不还原、普通模型显式参数可区分、空参数先取全局偏好、无偏好才取目录默认；仅替换反应式环境，不运行完整 UI。早期手工探针不作为最终消费链证据。

主会话另执行 `node --input-type=module` 合成探针，按源码锚点提取 exec 包 `class oi` 至 `function fi(e,t)` 之间的内容类型和 `pi/hi`、`LH/OH`，以及工作台 `YMs`：输出 `PRIMARY_NATIVE_PARTS_PASS`、`PRIMARY_NATIVE_REFERENCES_PASS`，整条命令退出 0。断言 reasoning/redacted-reasoning 经内部 unknown 往返、toolCallId/toolName/args/result 保留、image/file 的 Uint8Array 往返；以同两条 CoreMessage 构造 SHA-256 内容仓库，raw32 引用恢复2条，旧内联 JSON 恢复0条。隐私值容器、内容仓库及外围依赖为合成替身，验证消息结构和引用合同，不证明隐私策略、完整客户端或真实官方服务。脚本是临时取证，C1 仍需建立不依赖本机 Cursor 安装的永久回归。

**用户决策与续查**：用户选择保留完整参数目标，继续只读设计和验证替代方案，不以拒绝子任务替代修复；已同步 PRD、Design、任务和计划。续查发现工作台 `ODg` 可输出父请求/工具调用 header，但 `_deriveLineageFromParent` 取 generationUUID，不能直接当作网关传输 request_id。主会话拒收“现有内存已完整保存参数”（解析已丢键值）与“官方不改正文也能补参数”（现有路径只是透明转发）的过宽结论，接管后续关联核查。候选与剩余证据缺口见 Design §19.4a，不授权新增关联或正文改写。

主会话进一步沿实际 `_runSubagent` 读到提交前重建逻辑，纠正“创建阶段等于最终提交”的遗漏：最终 Max Mode 来自已加载父会话，resume 可按条件保留旧子模型或使用新模型。动态提取该原始代码片段及 `_deriveLineageFromParent`，配合前述真实配置方法和合成父/子对象，执行 `node --input-type=module`，输出 `PRIMARY_SUBAGENT_SUBMIT_PASS`、`PRIMARY_LINEAGE_SOURCE_PASS`，退出0。此项补核保持“变体串不会还原参数”的结论，但不将创建阶段局部证据冒充完整提交路径。

**结果与边界**：原生 root 引用和内容块合同已核实；原计划“目录变体字符串即可承载本地子模型参数”的前提被否证，已在计划与 Design 更正。存储复核发现现有锁内保存无读取基线复查，context/state 为顺序单文件替换，不可宣称已有同步原子事务。C0 仍未通过：历史预算/对齐/压缩/回退/恢复和回滚合同待闭合；子模型完整参数目标已确认，具体替代承载仍待设计与证据；真实官方端到端仍属 test/env gap。没有运行 Go/前端/全仓套件，因为没有功能变更，不将历史临时 PASS 当作修复验收。文档检查曾发现两处新增末尾空行，修正后 `git diff --check` 和五份文档状态/范围检查通过。下一步按 Design §19.3/§19.4a 补齐历史合同及可靠父子关联证据，相关设计批准后才进入永久失败回归与功能编码。

**续查结论（仍是阶段 0，未进入编码）**：两项限定只读调查分别回报历史同步与父子参数链，主会话复核 `proto/agent_v1.proto`、`imported_blobs.go`、`token_usage.go`、`projector.go`、`file_store.go`、`rewind.go`、`service.go`、`agent_action.go`、`agent_route.go`、`client.go` 和客户端工作台的原始片段。核实本地导入仍只在 Entries 为空时发生；内联 turn 没有可持久的引用身份；summary 有消息时不单独建 summary entry；回退按 turn 数截断导入 ID 并覆盖 entries；context/state 顺序写入并非跨文件事务。否决调查建议的“用 SHA-256 引用集合直接当跨渠道消息身份”：相同内容消息可重复、本地编码可改变摘要。参数链核实父 generation 与 attempt 的 ID 不必相同，网关只按每个子请求的自身模型和身份选出口，纯官方父无本地 Task run 记录；否决“BYOK 父必使官方子被本地路由”的无条件推断。仍须调查父子头在实际传输中的连通、纯官方父参数快照、官方正文局部重写、跨文件崩溃恢复及稳定对齐。以上仅源码取证和更正文档，没有永久回归或功能修复；C0 的 `Design Readiness=not-ready`，用户目前不能依赖混用修复。下一步先收敛历史合同和子模型承载的最小方案并申请批准，再按 C1–C5 顺序实施。未碰真实数据/配置，不部署、重启、提交或推送。

**用户补充选择**：允许把严格校验父子关联/参数来源后，仅对官方子请求 `RequestedModel` 的参数与 Max Mode 做局部改写纳入 C0 设计讨论，不授权立即编码；其余请求及字段维持原样透传。还需证明纯官方父参数来源、重试/并发/恢复关联、帧压缩与未知字段保留和出错后的安全状态，之后提交完整设计确认。该选择不改变保留完整参数目标。

**收口范围说明**：本轮交付对象仅为阶段 0 的部分取证与文档记录；C0 和后续功能阶段均未完成。文档写入后须另行执行差异格式、修改范围和状态一致性复验，不能以写入前的检查替代。

### 模型启停与共享入口默认公开（2026-09-24；verified-partial）

用户确认停用影响 Cursor 等全部入口，默认公开原模型名，重名仅冲突项加稳定后缀，已写入工作决策基线 §10.10 和系统 Design §14.11.3a。实现旧模型缺省启用、模型列表/编辑启停、Cursor/CLI 目录与旧 ID 拒用、fallback 跳过停用渠道，以及 Gateway 动态默认公开、保留旧自定义公开名和显式取消公开。仍复用现有配置分区保存与 Gateway token，不新增依赖、迁移脚本，不触碰真实用户配置。

主会话核查并修复两个反例：前端冲突后缀可能占用另一个模型本应保留的默认名；Go 将超过 128 字节的自定义公开名静默截断。两者均先新增失败回归再修复。隔离 HOME、复用本机 Go 缓存且禁止依赖下载时，修正回归后六个相关包最终测试及六包 vet 均通过。前端配置投影测试、production build、`git diff --check` 通过（构建仅有 localstorage 与 bundle 体积告警）。浏览器以合成内存数据检查 3 模型仅列 2 启用项、同名区分后缀、取消公开偏好与模型列表开关草稿；没有调用 Wails 保存桥接或真实上游；预览进程已停止。原定委派接口曾返回 503，未将其不确定状态作为验收证据。

状态为 `verified-partial`：代码/隔离模拟路径已验证，真实 Wails 保存/重载、Cursor 和外部 Gateway 客户端的最终调用仍属 test/env gap；未部署、重启或 commit/push。后续需另行安排真实配置副本与客户端验收，不以本轮测试代替实机结果；活动记录见 `task/todo.md` 同名节。

### 本机日志异常分诊与检查点过早超时修复（2026-09-21；verified-partial）

**授权与执行边界**：用户要求主控安排独立调查及当前明确风险的最小处理。三个调查分别覆盖检查点、模型/控制面/TLS、日志配额；主控复核原始日志并统一决策，只有已得到失败回归的本地 timer 标识重用缺陷进入修复。没有新增依赖、抽象、兼容层或迁移脚本；没有修改真实配置、证书、账号或日志，没有部署/重启、版本升级或 commit/push。

**取证口径及更正**：真实目录 `~/.cursor-local-assistant-v2/logs` 有 12 个 diagnostics 分片、24 个 trace 会话，先前“11/26”不准确。重点固定在主会话 `20260911T101629.967688000Z-94cff1bba964` 最新分片 `diagnostics-20260921T174246.704727000Z-000010.jsonl`，UTC `2026-09-21T17:42:46.704664Z` 至 `2026-09-22T03:58:54.477331Z`，sequence ≤ 9452300：9470 条、294 error/9176 warning。按 session+sequence 核对无重复，按 model_call/http_request 区分同链多层记录；异常分片不含普通成功事件，不能计算完整失败率。排查期间新增日志不并入统计。

- **P1 检查点**：230 次 `checkpoint_blob_timeout`、230 次 `checkpoint_blob_sync_skipped`、602 次 `checkpoint_blob_result=unmatched`。这是等待客户端 `SetBlobResult` 确认，不是已证实本地磁盘写入缓慢。诊断分片过滤掉 info 级 dispatch/成功 ACK，所以不能断言全部写入失败。旧完整 trace 第 41937–42028 行，同请求新 blob 发出后约 270ms，下一次甚至不足 1ms 就 timeout，而正常等待值是 5 秒；迟到 ACK 被记 unmatched。此异常足以建立针对过早超时的最小隔离复现，不证明全部 230 次同源或真实数据已丢失。
- **P1 模型最终失败**：8 条 summary、8 条 terminal、8 条 fallback 是 8 个 model_call 的多层记录，不能算 24 次故障。8 个 fallback 事件均 `fail_fast`（4 context_done、3 output_observed、1 chain_exhausted），无实际切渠道成功证据。明确 `model_call_final` 为 2 failed/1 partial：`c2e51372…` 两次 HTTP 502 耗尽，摘要 `Upstream service temporarily unavailable`；`88ac46a5…` HTTP 200 后 provider_terminal/failed，摘要缺失；`9a938108…` 为 partial，不能当完整成功。另有 4 个 context_canceled，不等同已证实用户主动取消；1 个 max_output_tokens 缺最终记录，另两个只有 retrying 的调用终态未知。真实渠道排查与修改重试/fallback 不在本轮修复范围。
- **P1 观测不足**：主 events 停在 `2026-09-11T12:34:54.877038Z`、sequence 1539957，app 同时停写；diagnostics 持续增长。只读日志额度为 2048MiB/30 天，异常预留 256MiB，普通额度 1792MiB；实测普通用量约 1791MiB 加 1MiB 元数据预留，符合既有额度降级合同。进程仍持有主日志句柄，长期 open 不是进程停止或内存泄漏证据；其余 23 个历史 open 也受现有保护规则限制。`dropped_events=276372` 是队列入队失败的累计快照，不是本窗口增量，更不是 quota 拒写总数，不对逐行值求和。恢复普通日志取证需要另行确认受控操作，不删除日志、不擅自扩大额度或改变回收策略。
- **P2 Dashboard**：9 个 GetManagedSkills、1 个 GetEffectiveUserPlugins 独立请求是 upstream status_code=0→backend/mitm 本地 502。样本行 1155–1157、3461–3463；没有收到上游 HTTP 状态，不可认定官方返回 502，也不可归因未登录的旧编码问题。已登录 `ForwardToUpstream` 事件缺明确失败阶段/摘要，需进一步受控取证，不擅自失败回空数据。
- **P2 TLS**：7134 次入站 `client_unknown_ca`，有目标域名而无对端进程身份，不能认定是非 Cursor 进程或不影响业务。先明确客户端来源，再决定信任配置；本轮未改 CA。遥测/埋点 404 仅报告，不扩大为兼容接口开发。

**失败复现与最小修复**：新增 `TestCheckpointBlobStaleTimerAfterPublishDoesNotSkipNextPending`，通过真实 queue A→ACK/publishReadyCheckpoint→queue B→handleTimerEvent(A 旧事件)，旧源码稳定失败 `stale timer skipped B pending=false writes=0`（退出 1）。根因为 clear/delete 后，下次按 key 递增重新从 1 开始，尚未消失的旧 timer 可匹配新 pending。修复仅在 `ActiveStream` 增加 `NextTimerToken`，`scheduleStreamTimer` 在既有锁内递增后赋值到 `TimerTokens[key]`；clear 仍删除键，不积累已清理的键，也不因 map 重建重用计数。生产差异仅 `actor.go` +3/−2、`types.go` +1；既有测试文件 +149。回归同时断言 B ACK 正常发布、C 当前有效 timer 仍使未确认 checkpoint 超时而不发布。保留 5 秒期限、ACK 齐全才发布与真正超时的降级语义。

**实际验证**：隔离临时 HOME，保留本机 GOPATH/GOMODCACHE/GOCACHE，设置 `GOPROXY=off GOSUMDB=off`，不访问真实业务目录。目标新回归先 RED（0.608s），修复后 GREEN（0.720s）；主控运行 `go test ./internal/backend/forwarder -count=1 -timeout=3m` 通过（18.032s），以及 `go test -race ./internal/backend/forwarder -count=1 -timeout=3m -run 'TestCheckpointBlobStaleTimerAfterPublishDoesNotSkipNextPending|TestCheckpointBlobTimeout|TestCheckpointBlobSync|TestCancellationDiscardsUnpublishedCheckpoint'` 通过（1.740s）。完整验证进程退出 0、输出 `CHECKPOINT_VALIDATION_PASS`；`git diff --check` 通过。没有跑无关模块、前端构建或全仓测试。早期调查一次缓存未导出导致离线依赖查找失败，改为显式保留缓存后相关测试正常；该次失败不作为通过证据。

**检查点专项续办（本轮，最多两个并行任务）**：用户将范围收敛为“检查点过早超时”一项。主控核实既有修复仍在工作区，安排一项独立源码复审和一项隔离验证，均实际返回成功结果。独立复审未发现阻塞问题，覆盖同 stream 的标识生命周期、map 重建、锁、共享 provider/shell/orphan timer 兼容和回归测试判别力；上一轮三次 503 导致的独立复审缺口已解除。没有继续修改运行逻辑，仅给 `NextTimerToken` 增加“同一流内递增；清理或重建 TimerTokens 时不得重置”的行内说明。

**本轮验证证据**：隔离 HOME、复用现有 Go 缓存并禁用依赖下载，以 `/tmp/ckpt-overlay.tw2xLv/overlay.json` 临时映射旧 `actor.go` 逻辑，不回退或覆盖工作区。`go test -overlay=/tmp/ckpt-overlay.tw2xLv/overlay.json ./internal/backend/forwarder -run '^TestCheckpointBlobStaleTimerAfterPublishDoesNotSkipNextPending$' -count=1 -v -timeout=2m` 预期退出 1（0.991s），失败为 `stale timer skipped B pending=false writes=0`；去掉 overlay 的同一命令退出 0（0.447s）。`go test -race ./internal/backend/forwarder -count=1 -timeout=3m -v -run 'TestCheckpointBlobStaleTimerAfterPublishDoesNotSkipNextPending|TestCheckpointBlobTimeout|TestCheckpointBlobSync|TestCancellationDiscardsUnpublishedCheckpoint'` 的 9 个用例通过（1.715s）。主控读取并核实三份实际输出 `/tmp/ckpt-overlay.tw2xLv/{red,green,race}.stdout.txt`。本轮没有重复全仓或无关模块验证。

**最终落盘验收命令**：文档及注释落盘后，在隔离 HOME、复用现有 Go 缓存且禁止依赖下载的环境中执行 `go test ./internal/backend/forwarder -run '^TestCheckpointBlobStaleTimerAfterPublishDoesNotSkipNextPending$' -count=1 -timeout=2m`，并执行 `git diff --check`；这些检查不构成真实运行实例验收。

**未完成缺口与状态**：检查点专项的源码修复、独立复审及隔离验证已完成；未打包/替换/重启运行实例，真实 Cursor checkpoint 链路仍属于 test/env gap，整体保持 `verified-partial`，不能表述为现场所有过早超时均已消失。下一步仅需另行确认部署验收窗口与必要的日志取证方式。模型失败具体远端原因、Dashboard 失败阶段、TLS 进程身份及日志配额处理均不属于本轮，不追加调查或修改。

**复用教训**：日志级别不是业务结果；字段值出现次数不是独立故障数；fallback 事件不证明切换成功；502 必须区分上游响应与本地映射；WARN/ERROR 分片缺少成功事件不能据此推断零成功；`open` 与累计 dropped 字段必须结合运行进程及生产端口径解释。

### 1.72.2 模型切换 imported replay Blob 兼容修复（2026-09-11；verified-partial）

**触发与日志证据**：用户提供 `/Users/yaogj/Downloads/logs 2`，现象为同一 Agent 会话先使用模型 A 完成讨论和任务，再切换高级模型分析时出现 `[internal] decode imported replay messages: invalid character '>'/'h' looking for beginning of value`。导出中找到请求 `d97857ea-9374-4ad2-8f81-74c2133ac34e`：`run_request` 的原始 Bidi 数据长 7,773,530 字节，解码成功后约 0.5ms 即进入 `dispatch_error(kind=run)` 并返回 500，随后 heartbeat 仍为 200；该请求没有进入 provider 调用。用户列出的另外三个请求 ID 不在本次导出中，payload 因配额降级未保留正文，所以无法从日志直接恢复报错字节。

**根因**：Cursor 当前本机 Agent runtime 的 `fromConversationStateStructure` 对 `rootPromptMessagesJson` 中每个元素调用 `getBlob`，再把 Blob 内容反序列化为 `coreMessage`，说明该字段在当前客户端中承载的是内容寻址 Blob ID。网关 `importedConversationStateModelMessagesWithBlobs` 原先无条件把这些元素交给 `DecodeReplayMessages` 做 JSON 解码，导致 32 字节 SHA-256 ID 的随机首字节被报告为 `>`、`h` 或其他非法 JSON 字符。模型切换/会话 fork 首次导入 `conversation_state` 时触发该路径。日志中的独立 `server_5xx status=502` 是 provider/上游错误，不是本地 replay 解码失败的原因。

**最小修复**：`internal/backend/forwarder/token_usage.go` 仅在 `root_prompt_messages_json` 的全部非空元素均为 32 字节 Blob 引用且 `turns` 非空时，跳过旧 JSON replay 分支并落入现有 Blob-aware turns 导入路径。普通非 JSON、JSON/Blob 混合内容，以及没有 turns 可回退的 Blob 引用仍保持错误，不静默丢失历史；未新增依赖、配置、协议抽象或迁移逻辑。

**回归与验证**：新增 `TestImportedConversationStateFallsBackFromBlobRootPromptsToTurns`，修复前稳定失败并复现同型错误：`decode imported replay messages: invalid character '\u008b' looking for beginning of value`；修复后恢复父会话 user/assistant 两条消息。另增加普通非法 replay 和无 turns Blob 引用继续拒绝的边界用例。最终 `go test ./internal/backend/forwarder -count=1` 通过（18.388s），`go vet ./internal/backend/forwarder` 与 `git diff --check` 退出 0。

**边界与交付状态**：交付状态 `verified-partial`。本轮未安装、部署或重启 Gateway/Cursor，未执行真实桌面“模型 A→高级模型”切换验收，未修改用户日志/配置；代码与文档已提交并推送（版本 1.72.2）。范围外的 provider 502 只记录，不处理。版本元数据（config.yml、darwin Info.plist、Windows/Linux 构建资产）对齐 `1.72.2`，发布说明见 `releaselog/1.72.2.md`。

### 1.72.1 OpenAI Responses 流式 [DONE] 兜底收口修复（2026-09-10；verified-partial）

**触发与结论**：修复 Responses 流仅收到 `[DONE]`（缺 `response.completed`）时的两条缺陷路径：普通成功收尾不补发 `TurnFinished` 导致客户端等待终态直到超时；`completeTool` 因参数非法 JSON 静默跳过后，`[DONE]` 兜底路径不再复查工具累加器，截断的 `function_call` 参数流被误判为普通成功收尾。

**修复内容**：`internal/backend/agent/model/openai.go`/`streamResponses`：`completeTool` 参数不完整时返回 `newStreamTruncatedError("openai", nil)` fail-closed，不再静默跳过；`[DONE]` 兜底路径先复查全部 `tools` 累加器，任一参数未收口即按流截断 fail；全部收口且尚未发 `TurnFinished` 时兜底补发（无工具普通成功默认 `stop`，已发工具完成保留空值交由 `effectiveFinishReason` 归一为 `tool_calls`）。正常 `response.completed` 终态路径与 Chat 路径行为不变。

**回归测试**：`TestOpenAIResponsesDoneWithoutCompletedEmitsTurnFinished`（仅 `output_text.delta` + `[DONE]` 仍发出 1 次 `TurnFinished` 且 finish reason 为 `stop`）、`TestOpenAIResponsesDoneWithIncompleteFunctionCallTruncates`（`function_call` 参数半截后 `[DONE]` 判定流截断 `missing completion marker`，不发 `ToolLikeCompleted`/`TurnFinished`）。

**验证证据**：`go test ./internal/backend/agent/model/ -run 'TestOpenAIResponses' -count=1` 通过。证据边界：Host 本地模拟上游证明两条兜底路径行为；真实上游截断/正常混合实机验收待后续窗口。

**边界与交付状态**：交付状态 `verified-partial`。未改变 Chat 路径、Anthropic 适配器、重试/fallback、证书与账号业务逻辑。版本元数据（config.yml、darwin Info.plist、Windows/Linux 构建资产）对齐 `1.72.1`，发布说明见 `releaselog/1.72.1.md`。

### 1.71.2 日志增强复核与范围内部署缺陷修复（2026-09-10；verified-partial）

**复核触发与结论**：用户要求复核完成情况并修复问题。本次对已实施的日志增强做独立只读复核，确认“已全部完成”的表述证据不足：先前收口只跑了定向测试与构建，未覆盖配额拒绝、持续轮转、异常队列预留和逐条追踪到真实入口的边界。复核确认并修复以下范围内缺陷。

- 普通写入准入与回收：manifest 原先忽略准入返回值，在普通分区被保护文件占满时仍会写盘；现改为真正拒绝超额写入，失败时保留上一份完好 manifest，临时文件在写入/权限/重命名任一失败时清理，任一写入失败都使缓存用量失效并在下次准入重新扫描；关闭时 manifest 落盘失败不再被静默吞掉，而由 `Status` 和关闭返回值上报。app 与事件写入保留普通分区内既有的 1 MiB 事件预留，仅供元数据与原子替换使用，总额度不变。
- 异常分区轮转：此前预留检查在追加之前执行，目录一旦接近 D 就再也不会触发轮转，形成永久冻结；现改为先回收最旧封存分片，必要时封存当前写入器自己的分片后重试准入，仍超出才丢弃并报告。超大单条记录不再先删除历史分片。其他写入器的活跃分片通过预算内的活跃路径登记得到保护，日志轮转不再删除同目录其他写入器正在使用的文件，且不再跟随或删除 symlink。
- 未知 manifest 保护：回收只接受受支持的 schema 版本（v1/v2）和已知 mode（full/basic）且身份与目录一致的已结束会话；未知 schema、未知 mode、身份不符或开始时间为零的会话一律保留。
- 队列异常预留：原先普通事件可以占满整个队列，导致 WARN/ERROR 在队列饱和时被丢弃。现保留单个 FIFO 与原有 `QueueSize`，普通入队不得占用 `max(1, QueueSize/8)` 的异常预留；队列丢弃总数维持原口径，异常队列丢弃另计入 `DiagnosticDropped` 并在排空或关闭后保持可见；`off` 模式不启用预留。
- 消费端完整性：跨全部输入路径统一排序，确保 trace 事件先于异常副本装载，full trace 的 `payload_ref` 不再因输入顺序丢失；冲突识别改用净化前事件的规范化摘要，只忽略 `payload_ref`，仍保留指向敏感差异的冲突报告但不保存原始数值；仅有异常分片（含缺身份记录）时通过既有 warning、报告与 GUI 总览提示材料不完整，不输出原始会话标识。
- checkpoint 拒绝 ACK：此前把 blob key 的十六进制和客户端任意错误正文写入 `error_summary`，会进入事件、异常副本和应用日志白名单；现只记录固定类别 `client rejected checkpoint blob`，待确认 checkpoint 丢弃与业务终态行为不变。

**验证证据**：隔离 HOME 下运行根模块定向测试、共享状态 race 与 vet，最终收口输出 `FINAL_ROOT_REVIEW_PASS`：`go test -count=1 -timeout=3m ./internal/observability ./internal/logsink ./internal/logger ./internal/backend/forwarder ./internal/client` 五包 `ok`；`go test -race -count=1 -timeout=3m ./internal/observability ./internal/logsink` 两包 `ok`；`go vet ./internal/observability ./internal/logsink ./internal/backend/forwarder` 退出 0。分析器侧运行 `go test -count=1 -timeout=3m ./internal/load ./internal/report ./internal/gui ./internal/analyze ./internal/project`、`go vet ./internal/load ./internal/report ./internal/gui ./internal/analyze` 与 GUI 前端 `npm run build`，均通过。新增回归包括：`TestReservedHeadroomKeepsTerminalManifestWritable`、`TestManifestRewriteRemovesTempAfterFailedRename`、`TestManifestRewriteCountsLeftoverTempAtNextAdmission`、`TestClosedWriterRewriteDoesNotReclaimOwnSession`、`TestReclaimableManifestAcceptsKnownSchemaVersions`、`TestNormalReclaimRemovesKnownV1Session`、`TestDiagnosticsReclaimsSealedShardsInsteadOfFreezing`、`TestDiagnosticsOverlappingWriterActiveShardSurvives`、`TestDiagnosticsSealOwnActiveShard`、`TestNormalReclaimProtectsUnknownManifests`、`TestRotatingFileCleanupKeepsProtectedShards`、`TestRotatingFileCleanupSkipsSymlinkedShards`、`TestRecorderQueueReservesDiagnosticSlots`、`TestRecorderQueueReserveClampsWithTinyQueue`、`TestRecorderQueueReserveDisabledInOffMode`，以及分析器的跨输入顺序、原始字段冲突和材料不完整用例。修复 ACK 泄露时先以 `TestCheckpointBlobEventsDistinguishRejectAndUnmatchedAck` 复现失败（断言拒绝 ACK 只能记录固定类别），再改为固定类别使该用例通过。

**边界与交付状态**：交付状态保持 `verified-partial`。本轮未重新打包，现有 `bin/release/1.71.2/cursor-byok-1.71.2-macos-arm64.dmg`（`79ce4e99…`）早于本次修复，不能作为本次修复的验收产物。未安装或替换运行实例、未重启、未读取或修改真实日志与配置、未 commit/push。真实 checkpoint ACK/超时、客户端 CA 信任来源、上游中断断点以及桌面/浏览器交互仍未验证。范围外问题仅在复核中记录，未做修改。

### 1.71.2 日志增强实施收口与本机构建（2026-09-10；verified-partial）

**实施结果**：已按工作决策基线 §5.5 / LOG-ENH-1–4 和系统架构 §14.4 / DESIGN-LOG-ENHANCEMENT-20260910 完成计划内日志增强。异常诊断副本纳入现有总额度 B 并预留 `floor(B/8)`；app、trace/payload、diagnostics 共用预算协调和降级状态链。WARN/ERROR 副本写入受管 diagnostics 分片；普通分区按过期受管日志、closed/full trace、closed/basic trace、旧 app 分片回收，保护活跃分片、未知或损坏 manifest、归属不明文件和 symlink。分析器按 `(app_session_id, sequence)` 稳定合并副本，优先保留 full trace 的 `payload_ref`，真实内容冲突仍报错；checkpoint 使用专用安全字段 `checkpoint_result`，生产端 human sink 与分析器端均执行白名单和脱敏。GLM、checkpoint 恢复业务逻辑、证书信任及上游请求、重试、fallback 行为未改变。

**验证结果**：隔离 HOME 下完成根模块相关定向测试、observability/logger 与 logsink race、受影响包 vet；`go test -count=1 -timeout=3m ./internal/observability ./internal/logger ./internal/logsink`、`go test -race -count=1 -timeout=5m ./internal/observability ./internal/logsink`、`go test -count=1 -timeout=3m ./internal/backend/forwarder ./internal/backend/server/upstream`、`go vet ./internal/observability ./internal/logger ./internal/logsink ./internal/backend/forwarder ./internal/backend/server/upstream ./internal/client`、`cd tools/log-analyzer && go test -count=1 -timeout=5m ./internal/...`、`cd tools/log-analyzer && go vet ./internal/...`、`node frontend/scripts/test-config-projection.mjs`、`gofmt -l` 和 `git diff --check` 均退出 0。forwarder 全包测试曾发现通用 `result` 字段过宽，改为 `checkpoint_result` 后重跑通过；未用不同请求后续成功冒充原请求恢复。

**只读复核与修复**：收口前对本轮 diff 做独立只读复核，发现三处范围内部署缺陷并已修复：普通分区回收原先按 closed/full、closed/basic、旧 app 分片三个独立层级删除，与批准的“closed/basic 与旧 app 分片合并为按时间排序的单一层级”不符，已改为 `fullCandidates` + `mergedCandidates` 两级并补双向时间顺序用例；诊断预留 D 原先只由各 `RotatingFile` 自身限额约束，配置重载期间新旧 sink 并存时 `logs/diagnostics` 可接近 2D，已改为在共享预算锁内按磁盘实际用量准入并在达到 D 时丢弃、以 `diagnostic_reserve_exceeded` 进入既有降级状态链，同时删除死代码 `diagnosticUsageBytes`；manifest 原先直接写盘但字节被计入普通分区用量，现已与事件、payload 同样经过 `admitNormalLocked` 准入与计量（保留临时文件加 rename 与 `0o600`）。复核同时确认锁序恒为 sink → budget → RotatingFile、无反转，活跃 app 分片与未结束 trace 受保护，配置重载失败恢复旧预算，`checkpoint_result` 在生产端、human sink、host 白名单和分析器白名单四处一致且通用 `result` 已不在任何白名单。修复后上述全部验证命令重跑通过。

**构建结果**：使用 Go `1.26.1`、Task `3.53.1`、Wails `v3.0.0-alpha.74`，并在隔离临时 `HOME`、既有 Go 缓存、`GOPROXY=off GOSUMDB=off GOGC=20` 环境中执行 `task build`，成功生成本机 Apple Silicon DMG，并归档为 `bin/release/1.71.2/cursor-byok-1.71.2-macos-arm64.dmg`。修复后重新构建，产物大小 `24292111` bytes，SHA-256 `79ce4e99204567ad63b969e428b8ff87a765ed36f330e603536bfdf1a4256367`；`hdiutil verify`、`codesign --verify --deep --strict` 通过，bundle short/build version 均为 `1.71.2`，Mach-O 为 arm64，签名为 adhoc。较早一次构建的产物 `988e92b9c51a0f06a767f666b829c8b34d261b138dde66c1b0246d8f41b375ee` 早于上述修复，已由本次重建取代，不再作为交付产物。构建没有改动 `go.mod`、`go.sum` 或 `build/` 受管文件；产物位于被忽略的 `bin/` 下。

**剩余边界**：交付状态保持 `verified-partial`。尚未安装或替换运行实例，未重启、未读取或修改真实日志/配置，未执行 Developer ID 签名、notarization、Intel/Windows/Linux 构建或 GitHub 发布，也未 commit/push。增强版本部署后的真实 checkpoint ACK/timeout、客户端 CA 信任来源和上游中断断点仍待后续运行证据。

### 1.71.2 日志增强授权同步与配额现实校准（2026-09-10；planned）

> **已被取代**：本文件上方的《1.71.2 日志增强实施收口与本机构建》取代本节：比例 `floor(B/8)`、淘汰顺序和消费端行为均已确认并实施。以下内容仅作历史记录。

**授权与范围**：用户已选异常独立保留本轮实现（implement），并明确纳入现有总额度（inside）；本次“继续”推进设计闭合，不重复询问这两个已确认决定。PRD §5.5 已同步为日志增强实施范围；GLM 及恢复点/证书/上游行为修复仍排除。开场“已确认优先保留分配方案”措辞过宽，随后已明确纠正为仅确认独立保留和总预算约束，比例/淘汰尚未批准。

**只读结果**：config 默认采集预算 1024 MiB/7 天，app 另设 100 MiB/14 天；storage 对根目录计量但不能协调 app 写入，额度回收只额外针对 closed/full。trace append 失败设置 fatal 并跳过 human sink，因此不能只在其后加异常文件。loader 只识别原事件/manifest/app 文件名；查询与报告分别消费安全字段投影，需要全链兼容。可核实符号和方案差异已写系统架构 §14.4 / DESIGN-LOG-ENHANCEMENT-20260910；默认值不代表读取了用户真实配置。

**设计结果与阻塞**：建议总额 B 内给异常预留 B/8（备选 B/4），继承现有 retentionDays，容量/期限先到先轮转；普通空间不挤占异常预留，淘汰对象/顺序、保护 open/未知文件、额度耗尽后停止超额写入并报告状态均列为待确认合同。此前额外 64 MiB/14 天不视作获批。后续还需闭合字段、状态、共享配额协调与消费者详情/案例/导出合同，`Design Readiness=not-ready`，不开始源码实施，也不声称增强日志已验证。

**验证与边界**：本次只读源码及现有文档，没有重新扫描运行日志或更改日志文件；只修改 PRD、系统架构、活动任务和本过程记录。文档范围/决策状态 Python 断言退出 0（`DOC_SCOPE_AND_DECISIONS_OK`），`git diff --check` 退出 0；无源码改动，不重复运行旧功能测试冒充新功能验收。下一步请用户确认预留与淘汰取舍，之后形成可执行计划。复用教训：inside 只确定预算来源，不自动批准比例和删除范围；保留能力须沿队列、写入顺序、配额协调和消费展示逐段核实。

### 1.71.2 本机日志复核与日志改造讨论（2026-09-10；分析完成，设计未批准）

> **已被取代**：本文件上方的《1.71.2 日志增强实施收口与本机构建》取代本节：比例 `floor(B/8)`、淘汰顺序和消费端行为均已确认并实施。以下内容仅作历史记录。

**已确认范围**：用户明确暂缓 GLM thinking 兼容性；恢复点写入超时、客户端证书信任保留为待排查方向；上游稳定性与“日志改造”不能缩减为“日志减量”，须同时评估诊断增强、异常分级与普通日志区分。范围已登记工作决策基线 §5.5，未批准运行行为修改。

**既有日志证据**：本机 `~/.cursor-local-assistant-v2/logs/traces/20260909T093558.146710000Z-f2d4c7ca7972/events.jsonl`，版本 1.71.2，固定窗口至 `2026-09-10T09:48:28.489566Z`。此前逐行复核为 1532 个唯一 model_call_id：1522 succeeded、3 failed、4 partial、3 canceled；164 次 checkpoint blob 写入超时涉及 20 个不同请求；10076 个不同连接 client_unknown_ca。不同层日志不重复累加，取消不计为已证实故障，后续其他请求成功不证明原请求已恢复。本次复读关键行 8293、111725、405296、1189968、1190028–1190030、1323849，未刷新或混入后续运行窗口。

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

### 1.71.1 下载日志异常诊断与 GetManagedSkills 修复（2026-09-09；verified-partial）

用户要求分析 `/Users/yaogj/Downloads/logs`、根因和最小方案，由主控安排执行、review、验证与必要修复。开工 `gateway@5926cc4` 工作区干净。三个有界工作流分别负责日志取证、模型故障链、控制接口/TLS；主控独立复核原始数据、源码 diff 和验证。以下 UTC 时间均指日志事件日期，不混用本机时区。

**覆盖及计数口径**：六份 app 日志；旧会话 `20260909T035825.861070000Z-4041ee8bee13` 为 1.71.0，当前 848 事件（03:58:26–06:33:37Z）；新会话 `20260909T064243.292774000Z-09262a3c9dde` 为 1.71.1，183889 事件（06:42:43–08:34:09Z）。两份 JSONL 均完整解析且序号连续；新 manifest 仍 open、basic，无失败正文重放材料，结论限定于这个导出快照。旧版 56 次 BidiAppend 502 是历史问题；新版为 9495 个 200 完成事件、1 个 502，唯一失败已走到官方上游且传输失败，不是已修的入站 gzip/JSON 分流故障。请求 ID 会被多次 BidiAppend/RunSSE 复用，不跨层加总为独立故障次数。

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

### 1.71.0 连接回归修复收口（源码与隔离验证完成；实机待验收）

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

本轮没有打包、安装、替换或重启当前 1.61.1，没有改真实配置/账号，也未 commit/push。未增加重试、等待状态机、安全门禁或无关前端/独立模块验证。复用教训：前置分流必须覆盖原 Connect 入口接受的真实编码，模型成功须断言实际回复，测试异步收尾能同步实现时优先保持简单。

### 1.71.0 对话连接回归：日志取证（2026-09-08；当时未修复）

用户报告官方账号模型、Auto、本地 BYOK 均持续连接，已自行回退 1.61.1。本轮仅检查既有日志与版本代码差异，并记录结论；未切换运行实例、修改真实配置/身份/证书或调用模型。

- 下载证据：`/Users/yaogj/Downloads/logs/traces/20260909T035825.861070000Z-4041ee8bee13/{manifest.json,events.jsonl}`，版本 1.71.0、basic、darwin-arm64，共 841 条事件。按 `layer=backend / event=request_finished / route=bidi_append` 聚合，56 次均为 502、handler_error；7 次已结束 RunSSE 均在 60000～60002 ms 后 canceled。没有 upstream 或 provider 调用事件。应用日志显示本地后端健康检查和代理启动成功，首次 BidiAppend 11:58:37 +08:00 失败，随后重复请求。
- 本机交叉证据：`~/.cursor-local-assistant-v2/logs/traces/20260909T035041.036672000Z-8ecec8326698`（1.71.0）8 次 BidiAppend 502、两次 RunSSE 60001 ms 后 canceled；`20260909T040419.301580000Z-2e2c6bad54f0`（1.61.1）读取快照已有 226 次 BidiAppend 200，前一次读取有 6 次 provider request/response、521 条 llm_response_chunk。旧版会话仍在增长，数量是读取时快照，不是固定最终总量。Cursor 3.15.19 同期日志另有 stream_stall、等待流活动超过 30 秒的记录。
- 定位边界：`git log` 确认两版相邻提交为 `4e4f2f1` 与 `818e293`。新增 `AgentRouteAction` 在 BidiAppend 分流前解析请求；解析或未知渠道错误直接返回，`writeServerError` 默认统一编码为 12 字节的 `bad gateway\n`；RunSSE 则等待同 request_id 的渠道决策。这与现场现象一致，但不是具体异常的证明。basic 记录只保留 handler_error，无原始异常/请求体，尚未建立可重放失败回归。
- 证书问题单独处理：下载样本有 24 次 client_unknown_ca，本机新版与已产生模型输出的旧版也均有该告警，不能把它直接认定为本轮共同主因。下载与本机日志来自不同用户路径，按 UTC 时间核对，不混用本地时区。
- 本轮命令证据：使用 Python 标准库逐行解析 JSONL，以 event/route/status_code 聚合并关联 RunSSE；读取应用与 Cursor 结构化日志；`git diff --stat 4e4f2f1 HEAD` 和分流/错误编码源码检查。未运行 Go 测试或真实请求重放，没有修复完成声明。
- 记录核验：诊断结论已写入本节与 `task/todo.md`，文件内容断言及 `git diff --check` 已通过；该结果仅证明文档记录，不证明连接故障已修复。

结论：1.71.0 实机对话已有阻塞回归证据，双渠道交付仍为 verified-partial，真实对话验收失败。下一步需要脱敏失败请求样本或受控诊断获取底层错误，先建立能复现 502 与等待现象的入口测试，再修复并覆盖官方、Auto、BYOK；继续保留用户当前回退状态。复用教训：模拟封装的路由测试通过不能替代真实 Cursor 请求编码与首包时序验证；通用错误类别不足以定位解析失败，不把猜测登记为现场根因。

### 1.71.0 编码回归：隔离复现与修复建议（续查；生产源码未修改）

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

2026-09-03/04 已完成目标仓库产物安装与在线正常流量冒烟。`workspace-gateway-native-fix/bin/macos-arm64.dmg` 通过 `hdiutil verify`；DMG 内应用与 `/Applications/Gateway-byok.app` 均为 `1.56.0`、Mach-O arm64，主二进制 SHA-256 同为 `001bc8db1b85fdeec7db82a7d5f92faf739f0690d093b4532f35eed94c87baf3`，应用签名校验通过。安装实例监听 `127.0.0.1:18080` 与 `127.0.0.1:18090`，`GET /healthz` 返回 `200 ok`；最新启动日志显示 backend、MITM 和 Cursor 代理设置均成功，无配置归一化、panic 或 fatal 错误。真实 Cursor 正常请求只观察到一次 RunSSE 入站/转发，内部 model call 均成功且无 terminal provider error。

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

### 0.13 本地编译 macOS v1.50.3

2026-08-28 按用户要求将版本号升到 `1.50.3` 并编译本机 macOS 包。`build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/1.50.3.md` 已对齐。未发布 GitHub Release，未改 README 当前稳定发布（仍为已发布的 v1.50.1）。

验证：`PATH=/Users/yaogj/go/bin:$PATH task build` 生成 `bin/macos-arm64.dmg`（约 22 MiB）。挂载后 Info.plist 版本为 `1.50.3`，可执行文件为 Mach-O arm64 且包含注入版本 `1.50.3`，adhoc codesign，`hdiutil verify` VALID，SHA-256 `ecf330bd61fbcac3133028dfb518df56722d57f6d075c573f22608889ba89ef9`。未做 Developer ID 签名或 notarization，未构建 Intel 包。

### 0.14 Cursor 设置清理所有权隔离

2026-08-28 已修复 `internal/client` 生命周期测试误删真实 Cursor 代理设置并诱发 `ERROR_NOT_LOGGED_IN` 的问题。`ProxyService` 现在只有在成功执行 `ApplyCursorSettings` 后才获得清理资格；设置写入会用实例 owner 标记和跨进程文件锁转移所有权，旧实例或未 Apply 的测试实例不能删除新实例设置，macOS `NODE_EXTRA_CA_CERTS` 清理也在同一所有权校验内执行。生命周期 fixture 已改用临时 Cursor 配置路径，并覆盖未 Apply 退出保留设置、旧 owner 不清理新 owner 和所有权存储行为。

验证通过：`go test ./internal/cursor -count=1`；`GOMAXPROCS=2 go test ./internal/client -count=1 -timeout 180s`。完整 client 测试前后真实 `~/Library/Application Support/Cursor/User/settings.json` 的 SHA-256 一致；未启动、停止或替换运行中的 18080/18090。

### 0.15 本地编译 macOS v1.52.0

2026-08-29 按用户要求将版本号升到 `1.52.0` 并编译 Apple Silicon macOS 包。`build/config.yml`、darwin Info.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/1.52.0.md` 已对齐；构建资产生成后恢复并保留工作树原有的 macOS 最低版本、Linux GTK/WebKit 和 Windows 安装范围改动。未发布 GitHub Release，未更新 README 当前稳定发布。

首次使用 Go 1.26.3 构建时，大型生成文件 `aiserver_v1.connect.go` 的编译进程被系统终止；切换项目现有 Go 1.25.0 后定位到剩余磁盘空间不足。清理失败构建临时目录和 Go build cache 后，以 `GOMAXPROCS=2 GOFLAGS=-p=1 task build` 成功生成 `bin/macos-arm64.dmg`（24,000,981 bytes）。挂载验证 Info.plist 与二进制注入版本均为 `1.52.0`，可执行文件为 Mach-O arm64，adhoc codesign 有效；`hdiutil verify` VALID；SHA-256 `0adcb0b3a32c5d581169642c1b530b819380f200f027d15017db4d8f8bfcf365`。未做 Developer ID 签名或 notarization，未构建 Intel 包。

### 0.16 Codex/Grok 授权接入页拆分

2026-08-29 根据用户启动 `1.52.0` 后的截图反馈，移除接入中心底部混合展示的全局“上游订阅认证”区域。Codex 的 `auth.json` 导入、设备码授权、状态、用量和清除副本全部进入 Codex 接入详情；Grok 的设备码授权、账号列表、激活、删除和用量进入独立 Grok 接入详情。左侧顺序固定为共享入口、Cursor、Codex、Grok、Anthropic；Anthropic 保留计划中占位，旧 `client=claude` 查询兼容归一化到 `anthropic`。

本轮只调整前端信息架构、提示、品牌样式、语言目录和路由门禁，不修改认证协议、凭据存储或模型请求链路。验证通过：`npm run build --prefix frontend`、`node frontend/scripts/test-config-projection.mjs`、`node --test frontend/src/router/access.test.js`（5/5）。构建只有既有 chunk-size warning；未重新打包 macOS DMG，未做真实 Wails 窗口点击，因此状态为 `verified-partial`。

### 0.17 Codex 多账户管理与安全轮换

2026-08-29 按用户确认策略实现 Codex 多账户池，同时保持模型配置只选择 `credentialSource=codex`、不绑定具体账户。`codex-auth.json` 从旧单账户结构兼容迁移为版本化 `accounts[]`；第一个账户自动激活，后续新增账户作为备用，重复导入同一账户更新凭据但保留激活、套餐和用量状态。Codex 接入页改为真实账户列表，支持激活、逐账户刷新双窗口用量、删除和清除全部副本。

运行时 401 定向刷新原请求账户；刷新确认失效后只将该账户标记为需要重新授权，再选择备用账户。明确 quota 错误只在零模型输出、非模型测试且共享 retry budget 可用时幂等标记失败账户、切换下一可用账户并重试一次；同一旧账户的并发重复信号不会把 active 从 B 继续推进到 C。已知额度重置时间到达后账户恢复候选资格。固定 Codex 上游、`ChatGPT-Account-Id`、请求体白名单和 token 不写配置的边界保持不变。

验证通过：`go test ./internal/subscriptionauth -count=1`、`go test ./internal/backend/agent/model -count=1`、`go test ./internal/backend/server/config -count=1`、`node frontend/scripts/test-config-projection.mjs` 和 `npm run build --prefix frontend`。已重新生成并核验包含本功能的 Apple Silicon macOS `1.52.2` DMG：应用版本 `1.52.2`、Mach-O arm64、最低 macOS `10.15.0`、codesign 有效、`hdiutil verify` VALID；产物 23,070,403 bytes，SHA-256 `467bb52b1a04ef421f7df6ae1cf09e2e15cf5cf8346e594a6f38336c956d7f7d`。未使用真实订阅 token 请求外部 Codex，未做 Wails 窗口视觉点击，因此交付保持 `verified-partial`。
### 0.18 sub2api 账号选择导入与用量刷新桥接

2026-08-29 根据 Codex 接入页运行截图修复逐账户用量刷新绑定缺失，并为 Codex/Grok 接入页增加 sub2api JSON 导入。导入流程先在后端按当前页面 provider 过滤：Codex 接受 `openai/codex + oauth`，Grok 接受 `grok/xai/x.ai + oauth`，同时要求 access token 与 refresh token；随后前端弹出候选账号列表供多选，只导入用户选择且属于当前页面类型的账号。源文件保持只读，凭据只进入应用私有账号池，重复账号更新原记录且后续导入不抢占当前激活账号。

验证通过：订阅认证 Go 测试（包含用户提供 sub2api 文件的只读解析、provider 过滤、选择导入和源文件不变）、Wails bindings 生成、前端配置投影和生产构建。Wails 生成结果确认公开了 `RefreshSubscriptionAccountUsage`、`PreviewSub2APIImport` 与 `ImportSub2APIAccounts`；桥接包完整测试因大型生成文件编译超过两分钟后终止。未使用真实外部订阅请求，未做 Wails 窗口点击，因此状态为 `verified-partial`。

### 0.19 本地编译 macOS v1.52.3

2026-08-29 按用户要求将版本号升到 `1.52.3` 并编译本机 macOS 包。`build/config.yml`、darwin Info.plist/Info.dev.plist、Windows/Linux 构建元数据、`release-notes.md` 与 `releaselog/1.52.3.md` 已对齐。未发布 GitHub Release，未改 README 当前稳定发布。

使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成 `task build`，产物为 `bin/release/1.52.3/cursor-byok-1.52.3-macos-arm64.dmg`（24,019,575 bytes）。挂载后 Info.plist 版本为 `1.52.3`，可执行文件为 Mach-O arm64 且最低 macOS `10.15.0`，codesign 校验有效，`hdiutil verify` VALID，SHA-256 `1b63be249e0bafb6955d2deedf1e29d81d17309123b4597d489ddbcd9c11f635`。本版本包含 Codex 多账户池、sub2api JSON 多选导入和逐账户用量刷新桥接修复。未做 Developer ID 签名或 notarization，未构建 Intel 包。
### 0.20 订阅账号列表滚动与 sub2api 弹窗关闭修复

2026-08-29 根据 `1.52.3` 运行截图修复两个前端问题：Codex/Grok 账号列表在固定高度授权卡片中改为独立纵向滚动区，标题和底部操作栏保持可见；sub2api 确认导入成功后直接重置并关闭选择弹窗，不再调用会被 `busy=true` 拦截的手动关闭函数。验证结果以 `task/todo.md` 的 `subscription-account-list-modal-fix-20260829` 为准；未重新打包 macOS DMG。

### 0.21 Gateway 可用性测试与 managed Codex 请求体边界

2026-08-29 本轮在上一轮 1.52.3 功能基础上补齐两处改动，随 1.52.4 提交。

Gateway 接入卡片新增「测试可用性」：启动 Gateway 后自动对本机入口执行一次真实 HTTP 探测（`GET /v1/models`，带 Bearer token），返回监听地址、公开模型数量与毫秒延迟；卡片状态区实时显示入口可用性与极简使用引导，token 复制/轮换改走 Wails 原生剪贴板 `Clipboard.SetText` 并等待写入结果，失败给出可读错误。桥接层新增 `TestGateway`、`RefreshSubscriptionAccountUsage`、`PreviewSub2APIImport` 与 `ImportSub2APIAccounts` 绑定。

managed Codex ChatGPT Responses 请求体白名单不再透传 `previous_response_id`：该 ID 与签发它的 ChatGPT 账号绑定，托管场景无法验证归属，按 fail-closed 剥离；static OpenAI Responses 保持原请求体不变。回归覆盖 managed 剥离与 static 保留两条路径。订阅模型测试改为可注入的 stream 函数，验证 Codex/Grok 固定上游归一化后凭据元数据正确注入且不写回原 adapter。

验证结果以 `task/todo.md` 的 `macos-build-1.52.4-20260829` 为准。使用 Go 1.25.0、`GOMAXPROCS=2` 和 `GOFLAGS=-p=1` 完成 `task build`，产物归档为 `bin/release/1.52.4/cursor-byok-1.52.4-macos-arm64.dmg`（24,015,566 bytes）；应用短版本与 bundle 版本均为 `1.52.4`，最低 macOS `10.15.0`，Mach-O arm64，codesign 校验有效，`hdiutil verify` VALID，SHA-256 `ba3321f551a74127dd760a9a40703e22492a5c6a93b5887eaf64ac41185c6a62`，`SHA256SUMS` 校验通过。已提交（`9c4db4f`）并 push 到 `gateway` 分支。未做 Developer ID 签名或 notarization，未构建 Intel 包，未发布 GitHub Release。

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

### 0.24 本地编译 macOS v1.60.0

2026-09-07 完成 `v1.60.0` Apple Silicon macOS 本地构建。版本号统一为 `1.60.0`（纠正提交 `f6ce0b0` 中 `0.060.0` 的写法，与历史 `0.0.x.y` 四段格式一致），`releaselog/0.060.0.md` 重命名为 `1.60.0.md`。使用 Go 1.26.1、Task 3.53.1、`PATH=~/go/bin:$PATH task build` 生成 `bin/macos-arm64.dmg`（24,225,414 bytes），并归档为 `bin/release/1.60.0/cursor-byok-1.60.0-macos-arm64.dmg`。挂载核验：Info.plist 短版本与 bundle 版本均为 `1.60.0`，二进制注入版本 `1.60.0`，Mach-O 64-bit arm64，adhoc codesign 有效；`hdiutil verify` VALID；SHA-256 `7e0d86254c84e9f8211337e5feb96866e0343a083b751c3e6fa083e8325d641f`，`SHA256SUMS` 复核通过。构建同时再生成 i18n catalog 行号引用（97 处 line 值刷新，无文案变化）。未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布；版本号修正与构建证据未提交，待用户确认。

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

- **2026-10-21**：加固自动更新的网络鲁棒性。用户反馈另一台 macOS 点更新偶发 `更新失败 / unexpected EOF`、多点几次才成功；本机 `netproxy` 日志确认走系统代理（`http://127.0.0.1:7890`），该错误只在响应体未读满即被断开时出现，而原实现单次请求、无重试、无续传，任何一次瞬时截断都会整体失败。改为：manifest 最多 3 次指数退避重试（仅网络错误/5xx/429，语义错误不重试）；更新包最多 4 次尝试，传输中断保留残片并用 `Range` 续传、服务器忽略 Range 时回退整包重下，大小或 SHA-256 不一致则从零重下（哈希改为下载后从文件重算）；错误文案改为中文并区分截断/代理/校验/超时；错误事件新增 `phase`，前端错误弹窗按阶段提供「重试下载/重试安装/重新检查」。`go test ./internal/updater` 全部通过（新增 5 个用例），临时 `httptest` 探针真实复现截断并在第二次请求续传成功，`go build ./...` 与 `npm run build` 通过。未在报错实机跑真实更新，留待实机复核。详见 `task/todo.md` 的「自动更新偶发 unexpected EOF 的重试与续传加固」。

- **2026-08-31**：完成 Provider 历史净化。目标感知来源身份，跨身份剥离 opaque reasoning/Responses item 元数据但保留文本和工具链；400 脱敏摘要进入诊断；32MiB 最终编码请求体预检且 `request_build` 不重试/fallback。复审修复 provider done origin 清零落盘丢失、tool flush 顺序和 safety suppression。定向 `go test`/`go vet` 与 `git diff --check` 通过。详见 `task/todo.md` 的 `provider-history-sanitize-20260831`。

- **2026-08-30**：完成 Rule P0 与 managed Codex affinity 基础检查点。Rule 私有权限、symlink/非普通文件拒绝、原子有界持久化、docs index 来源隔离与 reconciliation 已通过定向/race/vet 和 Windows 交叉构建；Windows 实机 ACL 仍待发布门禁。Codex 已接入稳定账号隔离、独立私密密钥、四字段 HMAC 域分离、精确 ChatGPT Responses 注入及 `control | prompt_key | full` profile，继续删除 `previous_response_id`。fake upstream、缓存观测和 A/B 尚未完成，不宣称性能收益；Rule journal 与手动同步尚未开始。

- **2026-08-30**：完成上游 `v0.1.5@b807608` 与最新 `upstream/main@9120b90` 深度审阅。隔离 worktree 合并本地 `main@305b108` 发生真实跨架构冲突后已安全中止；`main`、`gateway` 未变，无提交、无 push。确定优先加固 Rule 持久化，再独立补 managed Codex 缓存亲和；Rule 离线同步必须显式 opt-in；不整体移植 Deno/Rust runtime。详细结论见差异 PRD §16。

- **2026-08-30**：完成 `v1.52.5` Apple Silicon macOS 本地构建。版本元数据与发布说明已对齐，产物归档为 `bin/release/1.52.5/cursor-byok-1.52.5-macos-arm64.dmg`（24,031,370 bytes）。构建首次因误用 Go 1.26.3 且清空编译缓存，在 8GB 内存/swap 压力下大型生成文件 `aiserver_v1.connect.go` 编译停滞；改用项目 Go 1.25.0 并加 `GOGC=20` 控制编译器内存后成功。已核验 `1.52.5` 版本（Info.plist 与二进制注入）、arm64 架构、adhoc 签名、`hdiutil verify` 与 SHA-256 `b131348e7afd154dd05eae6f00968d971401d92f10b26efeb109ba49b2ee3153`。未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布。

- **2026-08-29**：实现 Codex 多账户管理与安全轮换。模型配置仍只选择 Codex 凭据来源；旧单账户私有文件兼容迁移为账户池，接入页支持激活、逐账户用量和删除。401 定向刷新失败后切换备用账户，明确 quota 仅在零输出安全窗口内幂等轮换并单次重试。自动化验证范围见 `task/todo.md`；未使用真实 token、未做 Wails 视觉点击或发布包重构建。

- **2026-08-29**：完成 `v1.52.2` Apple Silicon macOS 本地构建，产物为 `bin/release/1.52.2/cursor-byok-1.52.2-macos-arm64.dmg`；版本元数据与发布说明已对齐，DMG 校验、版本、架构、签名和 SHA-256 已核验。未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布。

- **2026-08-29**：修复订阅模型测试沿用用户填写接口地址的问题。Codex 订阅统一使用 ChatGPT Codex Responses 官方上游，Grok 订阅统一使用 xAI 官方上游；前后端保存、重载和测试采用同一归一化规则，订阅模式下接口地址与协议端点只读。后端端点、Codex 请求协议、客户端测试链路、前端投影和生产构建通过；未使用用户真实 token 请求外部上游，未重新打包 macOS 安装包。

- **2026-08-29**：完成 `v1.52.1` Apple Silicon macOS 本地构建。版本元数据与发布说明已对齐，带版本号的产物位于 `bin/release/1.52.1/cursor-byok-1.52.1-macos-arm64.dmg`；已核验 `1.52.1` 版本、arm64 架构、adhoc 签名、DMG 完整性和 SHA-256。未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布。

- **2026-08-29**：完成 `v1.52.0` Apple Silicon macOS 本地构建。版本元数据与发布说明已对齐，`bin/macos-arm64.dmg` 的版本、arm64 架构、adhoc 签名、DMG 完整性及 SHA-256 已验证；未做 Developer ID 签名、notarization、Intel 构建或 GitHub 发布。

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