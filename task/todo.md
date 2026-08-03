# 活动任务

> 本文件是项目活动任务的唯一真值源。`.cursor/plans/*.plan.md` 的 frontmatter todo 仅作阶段索引，不代表任务已满足 Definition of Ready。

## 当前执行队列

- [completed] `semantic-design-contract`：更新 PRD、系统 Design 与任务真值源，冻结日志 v2、项目隐私、语义枚举、案例和 AI 包合同。
- [completed] `analysis-project-engine`：抽取可复用 analysis project 生命周期，并保持现有 CLI 参数与报告语义兼容。
- [completed] `semantic-capture-v2`：实现 v1/v2 兼容及客户端 project/turn/capability/operation/outcome/build 指纹采集。
- [completed] `interactive-query-source`：实现 SQLite 分页、组合检索 DSL、保存查询、App 日志索引及安全 payload 按需访问。
- [completed] `enhanced-diagnostics`：增加能力状态机、compat/partial、证据缺口、百分位和多维 baseline 诊断。
- [pending] `investigation-case-library`：实现持久调查案例库、脱敏证据快照、状态机、版本关联和修复后复验。
- [pending] `ai-evidence-bundle`：实现外部 AI 调查包导出、结构化分析结果导入和不可信日志数据边界。
- [completed] `standalone-analyzer-gui`：完成首版独立 Wails/Vue GUI，接通项目生命周期、检索与分页、Finding/指标/App 日志/payload/保存查询/既有报告导出及安全 session 管理；案例、AI 包和复验入口保持明确未启用，归属后续独立切片。首版 macOS arm64 `.app` 已通过原生启动/退出 smoke、ad-hoc 签名、DMG/tar.gz 结构及 SHA-256 校验。
- [completed] `analyzer-default-log-autoload`：GUI 启动后由后端按稳定路径合同解析并异步自动加载客户端默认日志目录；目录不存在、无可分析日志或加载失败时预填默认路径并保留手动选择。打开事务支持取消和最新请求发布，关闭项目/应用不会等待大目录分析结束；不建立对客户端运行时的模块依赖。
- [pending] `client-analyzer-launcher`：在客户端日志采集区接入跨平台分析器检测、启动按钮和未安装引导。
- [pending] `distribution-verification`：建立分析器独立发布、客户端归档隔离及跨版本/跨模块/跨平台验证证据；补充真实大默认日志根的完成耗时门禁（当前 `1.7 GB` 数据集 14 分钟内未完成，已验证可取消但尚未通过全量性能验收）。

## 已完成支撑任务

- [completed] `analyzer-sqlite-workspace`：建立分析器临时 SQLite workspace、schema、权限与生命周期。
- [completed] `analyzer-stream-ingest`：将输入发现与 JSONL 解析改为批量流式导入。
- [completed] `analyzer-stream-analysis`：将分析规则改为 SQLite 游标驱动的有界 Go trace reducer。
- [completed] `analyzer-stream-reports`：将 JSON、HTML 和诊断 ZIP 改为数据库游标流式输出并接入 CLI。
- [completed] `analyzer-sqlite-verification`：完成兼容性、大规模内存、故障注入、跨平台构建与发布隔离验证。
  - 旧 `contract.Dataset.Events` / `load.Dataset` 全量生产链路已移除；CLI 已接入临时 SQLite workspace → 流式导入 → 有界 reducer → 流式 report staging/publish。
  - 已验证：`GOPROXY=off GOSUMDB=off go test ./...`、`go test -race ./...`、`go vet ./...`、CLI smoke、20,000 events / 1,000 traces under `GOMEMLIMIT=96MiB`、`task release:verify:analyzer-isolation`、`CGO_ENABLED=0` darwin/linux/windows 构建、`git diff --check`。
- [completed] `agents-init-locate`：定位并校验 AGENTS 全局母版与项目接入状态。
- [completed] `agents-init-interview`：收集并确认项目上下文、权威入口与覆盖规则。
- [completed] `agents-init-onboard`：生成或升级项目 AGENTS 接入文件并完成本机登记。
- [completed] `enhance-sqlite-plan`：依据项目规范增强 SQLite 内存优化计划。
- [completed] `analyzer-sqlite-design-gate`：将 SQLite 工作区与流式管线合同写入系统 Design，并完成 Design Gate。

## 当前范围约束

- 按 `semantic-design-contract → analysis-project-engine → semantic-capture-v2 → query/diagnostics → case/bundle → GUI/launcher → distribution` 的依赖顺序增量实施；每个切片保持可测试、可回退。
- 客户端只采集并通过受限启动器打开独立分析器；不读取历史日志、不分析、不生成报告。
- 第一版分析器不调用 AI、不修改仓库、不运行外部命令；只导出脱敏调查包并导入结构化分析结果。
- `basic` 不记录正文或项目路径；full payload 默认不索引、不复制、不导出。调查案例默认只保存脱敏证据快照。
- 分析器保持独立 module、独立进程和独立发布，不进入客户端二进制或更新归档。
- 未经用户明确要求，不使用 subagent，不提交、不推送。