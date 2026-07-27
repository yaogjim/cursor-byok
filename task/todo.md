# 活动任务

> 本文件是项目活动任务的唯一真值源。`.cursor/plans/*.plan.md` 的 frontmatter todo 仅作阶段索引，不代表任务已满足 Definition of Ready。

## 当前执行队列

- [completed] `analyzer-sqlite-workspace`：建立分析器临时 SQLite workspace、schema、权限与生命周期。
- [completed] `analyzer-stream-ingest`：将输入发现与 JSONL 解析改为批量流式导入。
- [completed] `analyzer-stream-analysis`：将分析规则改为 SQLite 游标驱动的有界 Go trace reducer。
- [completed] `analyzer-stream-reports`：将 JSON、HTML 和诊断 ZIP 改为数据库游标流式输出并接入 CLI。
- [completed] `analyzer-sqlite-verification`：完成兼容性、大规模内存、故障注入、跨平台构建与发布隔离验证。
  - 已完成：旧 `contract.Dataset.Events` / `load.Dataset` 全量生产链路已移除；CLI 已接入临时 SQLite workspace → 流式导入 → 有界 reducer → 流式 report staging/publish。
  - 评审修复：单 trace 高基数 pair/tool scratch 改为分页读取；trace labels 报告改为嵌套流式输出；baseline comparison 改为 SQLite join 生成；publish rollback 删除已发布的新 final；diagnostic warning 扩大绝对路径清洗范围。
  - 已验证：`GOPROXY=off GOSUMDB=off go test ./...`、`go test -race ./...`、`go vet ./...`、CLI smoke、20,000 events / 1,000 traces under `GOMEMLIMIT=96MiB`、`task release:verify:analyzer-isolation`、`CGO_ENABLED=0` darwin/linux/windows 构建、`git diff --check`。

## 已完成支撑任务

- [completed] `agents-init-locate`：定位并校验 AGENTS 全局母版与项目接入状态。
- [completed] `agents-init-interview`：收集并确认项目上下文、权威入口与覆盖规则。
- [completed] `agents-init-onboard`：生成或升级项目 AGENTS 接入文件并完成本机登记。
- [completed] `enhance-sqlite-plan`：依据项目规范增强 SQLite 内存优化计划。
- [completed] `analyzer-sqlite-design-gate`：将 SQLite 工作区与流式管线合同写入系统 Design，并完成 Design Gate。

## 当前范围约束

- 仅执行“分析器临时 SQLite 内存优化”计划。
- 仅修改 `tools/log-analyzer` 及已确认的任务/设计文档状态。
- 不触碰客户端、relay、日志采集协议、发布归档或无关评审修复。
- 未经用户明确要求，不使用 subagent，不提交、不推送。