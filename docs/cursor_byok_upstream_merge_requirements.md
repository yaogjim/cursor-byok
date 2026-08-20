# Cursor BYOK 上游同步与合并要求

- **文档类型**：上游同步安全策略与冲突要求，不是 PRD
- **端到端操作手册（推荐入口）**：[`cursor_byok_upstream_sync_release_runbook.md`](cursor_byok_upstream_sync_release_runbook.md)  
  常规流程：`upstream/main` → `main` → `noad` → 窄范围测试 → push origin → 三平台构建（无 Linux）→ 仅发布 `yaogjim/cursor-byok`。  
  **本文件**负责冲突分类、必须保留的本地行为与停止条件；命令级顺序与发版步骤以 Runbook 为准。
- **原始上游仓库**：<https://github.com/leookun/cursor-byok>
- **本地 Fork（`origin`）**：<https://github.com/yaogjim/cursor-byok>
- **本地分支**：`noad`
- **已提交 `noad` 基线 / 目标**：`9e6936f15fc45a351e9b53e2e99f321aa1b79ac1`
- **本次合并来源**：本地 `main@305b108e8cb44f68c15672809fc579acc65a9835`（含本地 `upstream/main@564f2bdcaec790863aca86403cedbfc77191bd43`）
- **共同祖先**：`cbf7cb4030e24ddfbd366c0e87bea969ba5e2421`
- **原始上游核对基线**：本地锁定 `upstream/main@564f2bdcaec790863aca86403cedbfc77191bd43`；最终 `git fetch upstream --prune --no-tags` 已通过，远端仍为此 SHA
- **当前同步状态**：隔离 worktree `sync/main-305b108-into-noad-20260820-004943` 已解决冲突并完成可执行门禁；用户已确认创建本地 merge commit 并推进 `noad`；**仍未 push**
- **待提交 index**：`git diff --cached --name-only` 当前 **59** 个文件；即将创建本地 merge，**仍未 push**
- **原始主线历史基线**：`main@799dbda`
- **决策 PRD**：[`prd_cursor_byok_工作决策基线.md`](prd_cursor_byok_工作决策基线.md)
- **功能差异 PRD**：[`prd_cursor_byok_当前功能与上游差异.md`](prd_cursor_byok_当前功能与上游差异.md)

本说明供 Cursor、其他 AI code agent 或人工维护者在合并时遵守安全边界。产品决策和当前功能事实必须回到两个 PRD，不得由本说明自行新增产品需求。

`origin` 是本地 Fork，用于保存 `main`/`noad` 和推送提交；`upstream` 必须指向原始仓库，用于获取上游代码。禁止把 `origin/main` 自动当作原始上游基线。

**流程优化（与历史 §3.1 的关系）：** 默认不再把 `upstream/main` 直接 merge 进 `noad`。应先更新 fork 的 `main` 作为上游镜像，再语义合并进 `noad`，最后按 Runbook 做三平台发布。§3.1 保留为「仅 noad 应急直合同步」参考；`git fetch upstream` 一律加 `--no-tags`；验证优先 `go test ./internal/...` 等窄范围门禁，避免并行 `go test ./...`。

## 1. 合并前硬性约束

1. 先读取两个配套 PRD、当前适用的路线图和当前工作树状态。
2. 在独立同步分支执行，不直接在 `noad` 工作分支上 pull 或 merge。
3. 保留当前 HEAD、当前工作树 patch 和上游目标 commit 的可恢复引用。
4. 当前工作树有未提交客户端体验治理改动时，禁止使用会覆盖本地修改的命令；先由维护者决定暂存、提交、导出 patch 或保持工作树。
5. 禁止读取、复制或提交真实 Token、API Key、Cookie、Authorization、真实 Prompt、真实 workspace、历史会话正文和生产审计原始文件。
6. 不停止或替换正在使用的 `18080/18090` 唯一代理，不在没有维护窗口时做运行中实例交接。

推荐的只读核对顺序：

```bash
git status --short
git branch --show-current
git log --oneline --decorate -20
git remote -v
git ls-remote --heads https://github.com/leookun/cursor-byok.git
```

## 2. 上游变更分类

### A. 可优先合并

- 不触碰出口、鉴权、审计、配置、广告和更新状态机的独立 UI 修复。
- 有明确测试覆盖、没有新增外部目标、没有扩大敏感数据采集的通用 bug fix。
- 与当前 provider adapter 兼容，且不改变默认 Endpoint、重试或 replay 语义的模型支持。

### B. 必须人工审查

- `internal/backend/host.go`、`internal/backend/server/upstream/**`、`internal/backend/server/route.go`。
- `internal/backend/agent/model/**`、`internal/backend/forwarder/**`、projector、retry、context/state/replay。
- `internal/backend/server/config/**`、`frontend/src/state/appState.js`、`configProjection.js`。
- `internal/ads/**`、`internal/updater/**`、`internal/bridge/**`、全局 CSS 和应用启动流程。
- protobuf、RPC 路由注册、Connect envelope、FileSync/Repository/Docs handler。

这些文件不能用“上游版本覆盖本地版本”的方式解决冲突，必须比较职责、数据流、状态机、证据和测试。

### C. 默认暂停并请求确认

出现任一行为时暂停 merge：

- 新增或修改 `tab.leokun.cn`、其他 relay、未知硬编码外部域名或未在配置中显式选择的目标。
- local 失败自动 fallback 到 Cursor 官方或第三方 upstream。
- 将 Tab/Cpp/FileSync/Git/Repository/Docs 请求正文、headers、Prompt、源码、diff 或凭据写入日志、artifact、debug 文件或错误信息。
- 将默认关闭的审计改为默认开启，扩大审计范围，弱化 `0600`、自动过期或事件上限，或持久化 canary 原值。
- 广告关闭时仍发送请求，或通过旧缓存继续展示广告。
- 更新器自动下载、跳过用户确认、接受任意资源 URL、允许空 checksum、取消大小限制或退出后保留临时包。
- 把 Repository/Docs/Upload 的兼容 success 当成真实索引、抓取、分块或持久化完成。
- 删除或泛化 `ForceBackgroundShell` replay 的专项门控，导致所有孤立工具结果都被放行。

## 3. 推荐合并流程

### 阶段 1：建立可回滚同步点

1. 记录原始仓库目标 commit、本地 HEAD、工作树状态和当前测试结果。
2. 获取上游引用，但不要直接修改 `noad` 工作树。
3. 创建独立同步分支，并为现有未提交改动建立可恢复 patch 或临时引用。
4. 按 commit 和文件范围阅读上游差异，先标记涉及外部目标、凭据、审计、配置和状态机的变化。

### 阶段 2：先合并低风险增量

1. 优先处理无路由、无鉴权、无配置语义变化的独立提交。
2. 每合并一个逻辑主题就运行对应测试，不把多类冲突一次性混在一起。
3. 对模型协议、provider retry、context/state 和 replay 变化，使用现有 fixture 与功能特征测试验证输入输出和顺序。

### 阶段 3：逐项处理高风险冲突

建议顺序：

1. 配置契约与迁移；
2. provider、Agent、retry/replay 稳定性；
3. 审计和 upstream forwarding；
4. Tab、FileSync、Git 路由；
5. 广告 gate；
6. 更新器状态机；
7. 前端状态投影与 UI。

每个冲突在继续前必须回答：

- 谁拥有这段代码的职责？
- 输入、状态和输出是否改变？
- 是否新增外发目标或敏感字段暴露？
- 是否有独立测试和回滚点？

## 3.1 后续从 GitHub 上游同步的正确流程（AI code agent 执行入口）

后续 AI code agent 读取本文件后，必须按以下顺序执行。除非用户明确要求，不得跳过检查、直接在 `noad` 上 `pull`、`merge` 或推送。

### 3.1.1 先确认工作树和文档基线

```bash
git status --short --untracked-files=all
git branch --show-current
git log --oneline --decorate -20
git remote -v
git diff --check
```

同时读取：

- 本文件；
- `docs/prd_cursor_byok_工作决策基线.md`；
- `docs/prd_cursor_byok_当前功能与上游差异.md`；
- 当前适用的能力、Tab/relay、隐私和客户端体验路线图。

如果工作树有任何未提交修改或未解释的用户文件，立即停止，不得使用会覆盖本地修改的命令。先由维护者决定提交、暂存、导出 patch 或保留工作树。

### 3.1.2 配置并获取原始上游

检查 `upstream` 是否指向原始仓库：

```bash
git remote get-url upstream
```

如果不存在才添加：

```bash
git remote add upstream https://github.com/leookun/cursor-byok.git
```

只获取引用，不修改当前工作树：

```bash
git fetch upstream --prune
UPSTREAM_SHA="$(git rev-parse upstream/main)"
LOCAL_SHA="$(git rev-parse noad)"
printf 'local=%s upstream=%s\\n' "$LOCAL_SHA" "$UPSTREAM_SHA"
```

不得使用 `git pull` 代替上述步骤，也不得从 `origin/main` 推断原始上游状态。

### 3.1.3 建立可恢复引用和独立同步分支

在 `noad` 干净时执行：

```bash
STAMP="$(date +%Y%m%d-%H%M%S)"
git branch "backup/noad-before-upstream-${STAMP}" noad
git switch -c "sync/upstream-${UPSTREAM_SHA:0:12}" noad
```

如果需要保存非空工作树，不能自行 stash 或导出其中可能包含的敏感数据；必须先暂停并请求维护者决定。

### 3.1.4 先分析差异，再合并

```bash
git log --oneline --reverse noad..upstream/main
git diff --stat noad..upstream/main
git diff --name-status noad..upstream/main
```

重点人工审查：

- 出口、鉴权、relay、`tab.leokun.cn` 和其他外部目标；
- provider、Agent、retry、context/state、replay 和 RunSSE；
- 配置迁移、前端状态投影、广告和更新器；
- protobuf、Connect envelope、RPC 路由、FileSync、Repository、Docs；
- Prompt、源码、headers、Token、Cookie、API Key 和日志/artifact 的数据流。

若发现新增外部目标、自动 fallback、敏感正文落盘、默认打开审计、广告关闭仍请求、更新跳过用户确认，或兼容 success 被改成真实业务成功，立即停止并请求确认。

### 3.1.5 以不自动提交的方式合并

确认没有触发停止条件后，在同步分支执行：

```bash
git merge --no-ff --no-commit upstream/main
```

合并后立即检查：

```bash
git status --short
git diff --check
git diff --cached --stat
git diff --cached --name-status
```

有冲突时：

```bash
git status --short
git diff --name-only --diff-filter=U
```

高风险文件不得直接选择 `ours` 或 `theirs`，必须按职责、数据流、状态机、外发边界和测试逐段处理。无法证明合并安全时执行：

```bash
git merge --abort
```

然后提交失败报告，不得提交“看起来能编译”的结果。

### 3.1.6 合并后验证

```bash
git diff --check
go test ./...
go build ./...
cd frontend && yarn test:config-projection && yarn build
```

额外确认：

- 旧配置迁移不丢失模型、路由和监听地址；
- 默认配置为 `light / false / false`；
- 广告关闭时启动、聚焦和定时刷新不发广告请求；
- 更新检查、下载、安装分别经过明确确认；
- 更新 URL、大小、SHA-256 和临时文件清理有效；
- 审计关闭时不解析或落盘原始 body；
- `tab.leokun.cn`、local/official/relay 路由和 Agent replay 行为没有未经记录的变化；
- 差异中没有真实 Token、API Key、Cookie、Authorization、Prompt、workspace、历史正文或 synthetic 原始产物。

### 3.1.7 记录结果并推进本地分支

验证通过后，在同步分支上更新 `docs/prd_cursor_byok_当前功能与上游差异.md`，记录：

- `upstream/main` 目标 commit 和同步时间；
- 保留、移植、重做和拒绝的上游功能；
- 受影响的目标、凭据、审计、配置和状态机；
- 测试结果、运行时证据和未验证边界。

然后提交同步分支：

```bash
git add docs/prd_cursor_byok_当前功能与上游差异.md
git commit -m "docs: 记录上游同步结果"
```

确认同步分支完整后，才允许快进回 `noad`：

```bash
git switch noad
git merge --ff-only "sync/upstream-${UPSTREAM_SHA:0:12}"
git push origin noad
git status --short
```

推送前不得推送 `upstream`，不得强制推送，不得覆盖远程已有提交。若同步分支需要评审，先推送同步分支并等待维护者确认，再快进 `noad`。

### 3.1.8 失败报告格式

任何停止、回滚或验证失败都必须报告：

- 原始上游目标 commit、本地 HEAD 和同步分支；
- 冲突文件及逻辑主题；
- 已通过和失败的测试；
- 是否触及外部目标、凭据、审计、配置或状态机；
- 是否执行了 `git merge --abort`；
- 推荐的人工决策。

## 4. 必须保留的本地行为

- `tab.leokun.cn` 默认路由不因 pull 自动改变。
- local、official、relay 之间不允许隐式 fallback。
- 审计默认关闭，且不记录正文、Token、API Key、Authorization、Cookie、路径和 canary 原值。
- Agent 的 `context.json`、`state.json`、tool replay、reasoning signature、RunSSE 终态以及取消/重试行为必须保持回归通过。
- `ForceBackgroundShell` 只有在明确专项条件下允许无 reasoning replay；其他孤立 `tool_result` 继续受原门控。
- 默认主题为 `light`，仍支持 `dark`。
- 默认广告关闭；本地关闭必须阻断广告请求和展示。
- 默认更新完全手动；检查、下载、安装分阶段确认。
- 更新资源必须验证 GitHub Release 归属、HTTPS、大小和 SHA-256；退出时清理临时文件。
- Repository/Docs/Upload 的实际能力不能由 success 响应虚增。

## 5. Merge 后验证门禁

### 5.1 静态与单元验证

```bash
git diff --check
go test ./...
go build ./...
cd frontend && yarn test:config-projection && yarn build
```

若上游变更涉及特定模块，必须额外执行相应包测试和现有功能特征测试；失败时不得删除测试或放宽断言来“修复”合并。

### 5.2 配置与客户端体验

- 旧 `config.yaml` 补齐 `appearance/theme`、`advertising/enabled`、`updates/checkOnStartup` 时不丢失模型、路由和监听地址。
- 默认配置得到 `light / false / false`。
- 保存模型或路由时不覆盖客户端偏好。
- 广告默认关闭时启动、聚焦、定时刷新都不产生广告 HTTP 请求。
- 更新默认不检查；手动检查只请求 manifest；未确认下载时不请求资源；校验失败不进入 `ready`；退出清理临时包。

### 5.3 路由、隐私与状态机

- 17 条明确 relay 路由仍有清晰 route name 和审计标记，其余 `DirectAction` 不被泛化审计。
- 审计关闭时不解析 protobuf、不记录原始 body。
- 审计开启时 JSONL 只有允许的 metadata，权限为 `0600`，且不出现正文、凭据、UUID 原值、路径或 canary 原值。
- synthetic 验证只能使用隔离 workspace 和一次性 canary；不读取真实会话和生产数据。
- Agent provider、Tab/FileSync、Git 的目标 host、状态和请求序列必须与当前基线一致，或在功能差异 PRD 中记录新的证据。

### 5.4 运行时验收

运行时验收必须在明确维护窗口进行，且不能同步停止唯一代理。若需要切换实例：

1. 回复发送后延迟交接；
2. 等待旧请求稳定；
3. 启动候选实例；
4. 检查 `18080/18090` 和 `/healthz`；
5. 失败自动回退；
6. 恢复后删除 synthetic 明细和临时二进制。

## 6. 失败处理与停止条件

出现以下情况立即停止 merge：

- 测试、构建或前端构建失败，且不能证明是上游独立问题。
- Agent、文件工具、provider、Cursor 启动或设置恢复回归。
- 发现自动 fallback、未选择的外部目标、重试风暴、入口消失或持久化损坏。
- 发现真实 Prompt、源码、凭据或历史正文落盘。
- 无法判断某个 success 是否是 Cursor compatibility gate。
- 无独立回滚路径，或当前工作树存在未解释的用户改动。

失败报告至少包含：

- 上游目标 commit和本地 HEAD；
- 冲突文件和逻辑主题；
- 已通过/失败的测试；
- 是否触及外部目标、凭据、审计、配置或状态机；
- 推荐的人工决策。

不得直接提交一个“看起来能编译”的合并结果。

## 7. 合并完成后的记录

每次成功合并后，必须更新 [`prd_cursor_byok_当前功能与上游差异.md`](prd_cursor_byok_当前功能与上游差异.md)：

- 原始仓库新的 commit 和同步时间；
- 实际保留、重做、拒绝的上游功能；
- 新增或变化的外部目标；
- 新的运行时证据和未验证边界；
- 测试与构建结果；
- 是否需要用户重新确认路由、token、隐私或更新策略。

没有这些记录，不把 merge 标记为完成。

## 8. `main@b438237` 同步记录

- **同步时间**：`2026-07-26T18:07:14-07:00`
- **同步来源**：本地 `main@b438237e08ed42f871dd7de2f05ee1dd9689a72a`
- **原始上游核对点**：`leookun/cursor-byok main@9e057399690bff78cd7571dba2e7a14e12767585`
- **本地同步前基线**：`noad@841a49c0963a911b4c838157438e074ef930b33b`
- **安全 merge commit**：`74c1c38ec3e68ed0f40fbefceaedd41c69616065`
- **拓扑**：双父分别为 `841a49c` 与 `b438237`，使用真实 merge，未使用 cherry-pick。

### 8.1 接受与整合

- 接受 provider artifact session 释放与请求摘要化，完整 provider payload 不再保留在 active session。
- 接受 Agent replay 对连续 assistant text/reasoning/tool-call 的归并兼容修复，并保留 reasoning signature、Responses 状态、tool-call ID 和结果顺序。
- 接受俄语 locale、静态 i18n scanner、Linux GTK3/WebKit 4.1 依赖、`0.0.41` 版本元数据、贡献指南和 PR 模板。
- 保留本地主题、广告、更新配置事实源；广告仍默认关闭，只有本地显式开启后才允许所有 locale 展示。
- `.vscode/settings.json` 未进入索引；仓库只新增对应 `.gitignore` 规则。

### 8.2 重做与安全收紧

- 将 scanner 新增的 22 个本地中文源 key 补齐为 `en-US`、`ja-JP`、`ru-RU` 共 66 个非空翻译，四个 locale 的 key 与 placeholder 集合一致。
- provider/conversation atomic 文件与 provider debug JSONL 的创建权限收紧为 `0600`。
- 增加 artifact 成功、错误、取消、sink 提前退出清理测试，以及 payload 不驻留和文件权限测试。
- 增加多 tool-call replay、sanitizer 幂等和 `ForceBackgroundShell` 唯一无 reasoning 例外测试。
- 修复两处既有 protobuf 锁值拷贝，使 `go vet ./...` 成为可通过门禁。
- 发布说明收敛为真实代码差异，并加入禁止在聊天、日志或截图中粘贴真实 API Key 的提示。

### 8.3 明确拒绝

- 拒绝 Windows 安装器固定 `RequestExecutionLevel admin` 和固定写入 `HKLM`；保留 `WAILS_INSTALL_SCOPE` 驱动的 user/machine 双模式、HKCU/HKLM 对称卸载及最小权限能力，仅同步版本号。
- 拒绝发布说明中让 AI 读取、处理或接收真实 API Key 的操作引导。
- 拒绝发布说明中无法由本次真实代码差异证明的模型、图片识别、搜索 fallback 和 Windows 行为声明。

### 8.4 自动化证据

以下门禁在 merge commit 前通过：

- `git diff --cached --check` 与冲突标记扫描；
- `go vet ./...`；
- `go test ./...`；
- `go test -race ./internal/backend/forwarder`；
- `go build ./...`；
- `cd frontend && yarn test:config-projection`；
- `cd frontend && yarn build` 连续两次，catalog 与四个 locale 哈希稳定；
- i18n 完整性校验：`246` 个 key × `4` 个 locale，无空值、无新增中文 fallback、placeholder 集合一致。

仅观察到既有非阻断告警：macOS linker 目标版本警告与 Vite chunk 大小警告。

### 8.5 未验证边界

- 本轮未停止或替换正在使用的 `18080/18090` 代理，因此没有执行候选实例交接、窗口首屏、`/healthz` 或真实网络零请求验收。
- 广告关闭零请求、更新完整交互序列、旧配置实机迁移和发布资源实机安装仍需在明确维护窗口验证；本记录不把这些事项声明为已完成。
- 本轮只更新本地分支，不执行 `git push`。

## 9. `0.0.49` 冲突分类（用户已确认本地 merge）

- **同步时间**：`2026-08-20T02:40:22-0700`
- **源**：`main@305b108e8cb44f68c15672809fc579acc65a9835`（包含本地锁定 `upstream/main@564f2bdcaec790863aca86403cedbfc77191bd43`）
- **目标**：`noad@9e6936f15fc45a351e9b53e2e99f321aa1b79ac1`
- **共同祖先**：`cbf7cb4030e24ddfbd366c0e87bea969ba5e2421`
- **同步分支**：`sync/main-305b108-into-noad-20260820-004943`
- **备份**：`backup/noad-before-main-20260820-004943` @ `9e6936f`
- **方式**：`git merge --no-ff --no-commit main`；无 `ours/theirs` 批量裁决。
- **状态**：冲突已解决，自动化门禁已跑完，最终 review 已修 P1；用户已确认创建本地 merge commit 并推进 `noad`。`git diff --cached --name-only` 当前 **59** 个文件；即将创建本地 merge，**仍未 push**。

### 9.1 八个文本冲突

`MERGE_MSG` 列出的文本冲突及裁决：

| 文件 | 分类 | 裁决 | 验证 |
| --- | --- | --- | --- |
| `frontend/src/i18n/locales/zh-CN.json` | B 人工审查 | 与 en/ja/ru 做 key/译文并集，保留 noad 仍在用的文案 | 前端扫描构建；catalog 两次哈希稳定 |
| `frontend/src/i18n/locales/en-US.json` | B | 同上 | 同上 |
| `frontend/src/i18n/locales/ja-JP.json` | B | 同上 | 同上 |
| `frontend/src/i18n/locales/ru-RU.json` | B | 同上 | 同上 |
| `frontend/src/i18n/generated/catalog.json` | B | 不手改；由 `npm run build --prefix frontend` 扫描重生 | 连续两次 PASS，SHA-256 `3aea2eec12cde08f5b791defffab23455545d3b85c7edbbd3bfb4cc4f9ffed31` |
| `frontend/src/state/appState.js` | B | 以 noad 配置投影为底，接入导入导出 API/状态 | `test-config-projection` PASS；默认 `light/false/false/basic` |
| `internal/backend/forwarder/transcript_adapter_test.go` | B | 按最终 transcript 契约改写；删除启动回填断言 | Agent/forwarder 专项与根 `go test ./internal/...` PASS |
| `release-notes.md` | B | 拒绝含糊上游说明，按 noad 风格重写 v0.0.49 | 人工核对发布身份与保留策略 |

### 9.2 关键无文本语义冲突

这些文件不一定出现冲突标记，但属于第 2 节 B 类，禁止按任一侧覆盖。

| 主题 | 主要文件 | 停止条件风险 | 裁决 |
| --- | --- | --- | --- |
| Agent 主链路 | `openai.go`、`router.go`、`projector.go`、`transcript_adapter.go`、`service.go`、`host.go`、`upstream/client.go` | replay/retry/截断、审计、路径沙箱、`ForceBackgroundShell` | 接受 orphan output、交错 tool-call、replay trim、transcript 新语义与 `IsConnected` mock/statsig；保留 noad retry/截断/审计/MITM/observability/路径沙箱；**不恢复** transcript 启动回填 |
| 配置导入导出与 routing | `config_transfer.go*`、`config.go`、`lifecycle.go`、`service.go`、`bridge/proxy.go` | legacy `routing` 重复键、配置事实源被前端覆盖 | 接受导入导出与锁分层；YAML 为事实源；去掉 duplicated routing key；修复旧 `Config.Log` 测试 |
| 前端导出 API 日志契约 | `frontend/src/services/clientApi.js` | 三参数导出调用把 noad 两参数 `withApiLogging(name, runner)` 打成 TypeError，导出到不了后端 | 最终 review 发现并修复为两参数；新增静态契约检查覆盖 24 个调用点 |
| Windows 安装 scope | `build/windows/nsis/wails_tools.nsh` | 固定 `RequestExecutionLevel admin` / 只写 HKLM | 拒绝上游固定管理员安装；恢复 `WAILS_INSTALL_SCOPE` user/machine 双模式，仅升到 `0.0.49` |
| prompt 身份 | `prompt/**` | 把运行时身份改写成 `leookun/cursor-byok` | 接受 prompt 更新；删除 leookun 身份覆盖；作者署名与 updater 负向 fixture 不改 |
| 发布身份 | `internal/buildinfo/buildinfo.go`、`Taskfile.yml`、updater 测试 | 更新 URL 回写上游 Release | 保持 `yaogjim/cursor-byok` |

### 9.3 验证门禁（本轮已执行）

- proto / 官方 bindings 生成成功。
- 协议/Agent、配置、产品专项通过。
- `node frontend/scripts/test-config-projection.mjs` PASS。
- `npm run build --prefix frontend` 连续两次 PASS；catalog SHA-256 `3aea2eec12cde08f5b791defffab23455545d3b85c7edbbd3bfb4cc4f9ffed31` 两次稳定。四个 locale 与 catalog 均由扫描生成。
- 新增 `frontend/scripts/test-client-api-logging.mjs`：24 个调用点全部为两参数，PASS。P1 修复后 `test-config-projection` 与 frontend build 重新 PASS，catalog SHA-256 不变。
- `GOPACKAGESDRIVER=off go test -p=1 ./internal/...` PASS。
- `GOPACKAGESDRIVER=off go vet -p=1 ./internal/...` PASS。
- `cursor-tab-server`：`go test -p=1 ./...`、`go test -p=1 -race ./...`、`go vet ./...` PASS。
- `tools/log-analyzer`：`go test -p=1 ./...`、`go test -p=1 -race ./...`、`go vet ./...` PASS。该独立模块本轮无 merge diff。
- macOS 正式构建 `task build` PASS，约 8m12s。本地 ignored `bin/macos-arm64.dmg`：`23577563` bytes，SHA-256 `6105a69a0d1ea48fa4b822a5ea90fc496e5c2d5e75efaed04939e9662e46cd7b`；Mach-O arm64、non-fat/非 universal；Info.plist `0.0.49`、Bundle ID `com.cursor.wuxianxubei`；adhoc 签名且 `codesign --verify --deep --strict` PASS。DMG 不进入 index。
- 最终代码 review 其余范围无阻塞问题。
- `git diff --check`、`git diff --cached --check` 通过；无 unmerged。

**未运行 / 不得写成通过：** `task release:verify:analyzer-isolation`（无真实 `0.0.49` release 归档；本地 ignored DMG 不能代替发布资产）。

### 9.4 未验证边界与确认点

- 最终 `git fetch upstream --prune --no-tags` 已通过；`upstream/main` 仍为锁定 SHA `564f2bdcaec790863aca86403cedbfc77191bd43`。此前一次 fetch 曾因 `Could not resolve host: github.com` 失败，不得再写成待 fetch。
- 未做广告零请求、更新交互、Windows 安装/启动、Wails 实机验收；未影响 `18080/18090`。
- 本地 `task build` 仅产出 ignored `bin/macos-arm64.dmg`，**未** notarize、**未** 生成 release 归档、**未** 做 Gatekeeper 首次启动实机验收。
- 仍缺 Wails runtime mock 下的真实导出调用单测；当前只有静态两参数契约检查。
- 非阻塞残余测试风险：缺少 `IsConnected` 精确路由测试；缺少 `ForceBackgroundShell` × replay trim 交叉测试。
- 未用 `-tags gui` 编译分析器 GUI 入口。
- 用户已确认创建本地 merge commit 并推进 `noad`。仍未 push；运行时未验证边界仍成立。
