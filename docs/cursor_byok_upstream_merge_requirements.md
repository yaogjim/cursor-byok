# Cursor BYOK 上游同步与合并要求

- **文档类型**：上游同步执行说明，不是 PRD
- **原始上游仓库**：<https://github.com/leookun/cursor-byok>
- **本地 Fork（`origin`）**：<https://github.com/yaogjim/cursor-byok>
- **本地分支**：`noad`
- **本地已提交基线**：`e9b6d701d63f3cc315676afffaddc3128c7db7cc`
- **原始主线基线**：`main@799dbda`
- **决策 PRD**：[`prd_cursor_byok_工作决策基线.md`](prd_cursor_byok_工作决策基线.md)
- **功能差异 PRD**：[`prd_cursor_byok_当前功能与上游差异.md`](prd_cursor_byok_当前功能与上游差异.md)

本说明供 Cursor、其他 AI code agent 或人工维护者执行后续上游同步。它只规定如何安全获取、分析、合并和验收上游变化；产品决策和当前功能事实必须回到两个 PRD，不得由本说明自行新增产品需求。

`origin` 是本地 Fork，用于保存 `noad` 和推送提交；`upstream` 必须指向原始仓库，用于获取上游代码。禁止把 `origin/main` 自动当作原始上游基线。

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