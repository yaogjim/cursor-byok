# UI 交互改造实施方案（四栏 IA 内重整）

- 状态：已确认执行（2026-11-17，按批次验收推进）
- 日期：2026-11-17
- 范围：`frontend/`（Vue 3 + Tailwind，不含 Go 后端能力新增）
- 目标读者：执行实施的 agent（按批次独立交付，每批可单独验收与回退）

---

## 0. 已确认决策（不再讨论）

| 编号 | 决策 | 结论 |
|---|---|---|
| D1 | 改造边界 | 保留「总览 / 接入 / 模型 / 设置」四栏，只重整职责与内容，不改导航结构 |
| D2 | 总览内容 | 只留只读状态 + 使用分析；**移除启停按钮**，改为跳转接入页 |
| D3 | 模型编辑器保存 | 编辑器「保存」即写盘；成功后同步清除脏点 |
| D4 | 配色深度 | 调整颜色 token 使对比度达标，并清理 `global.css` 中的硬编码 hex 覆盖 |
| D5 | 反馈与弹窗 | 合并 Modal / ContentModal 为一套；toast 区分成功/失败/信息；`showActionError` 收敛为共享实现 |
| D6 | 请求列表 | 加列头排序、时间按范围自适应、行可展开看详情 |
| D7 | 无障碍 | 关键路径：全局焦点环、禁用原因、可点元素键盘支持、弹窗 Esc + 焦点困囚 + 焦点归还 |
| D8 | 设置页 | 保留四个 seg 面板，共用一套保存栏与脏点管理 |
| D9 | 多语言 | 本次不做；选择器标记为「计划中」并禁用；语言包与运行时保留，不删除 |
| D10 | 分批 | 四批递进：清理 → 职责重整 → 视觉与反馈 → 列表交互 |

### 0.1 由 D3 推导出的补充规则（实施时须遵守）

核对发现模型页除编辑器外，**复制 / 删除 / 拖拽排序 / 导入合并**都只修改前端草稿、不写盘；且决策基线 §10.14 第 593 行明确要求模型页导入「合并当前模型草稿，点击保存后仅写模型作用域」。因此：

- 保留页面底部的保存按钮，但改为**按脏状态呈现**：`configSectionDirty.models === false` 时显示「重新加载」，为 `true` 时显示「保存模型配置」。
- 编辑器保存成功后必须让脏点回到干净态（见 2.1 的验收项）。
- 不得把复制/删除/排序改为自动写盘，也不得为导入恢复「立即写盘」路径。

### 0.2 后续用户修订（2026-11-18，覆盖 D2）

- **撤销 D2「总览只读」**：总览 Cursor / Gateway 卡片恢复「启动服务 / 停止服务」「启动 Gateway / 停止 Gateway」按钮，启停逻辑复用改造前实现；「配置 → / 去配置 →」保留。
- 总览两张服务卡片压缩高度（目标约 148px），Gateway 副标题与说明文案精炼；未启用/未保存时用按钮禁用原因替代常驻说明行。
- 追加界面打磨批（见 `work-orders/batch-4.md`）：公开模型行、模型列表/卡片、编辑模型弹窗的密度与布局优化。
- 不改字段、校验、保存作用域、路由与后端契约；不新增依赖。

---

## 1. 现状事实与问题清单（均已在代码中核实）

### 1.1 职责重叠

- 服务启停有三个入口：总览卡片（`views/Home.vue:345,353` 停止/启动服务，`:413,422` 停止/启动 Gateway）、接入页 Cursor 面板（`views/CursorView.vue:154-162` 操作栏、`:41` `toggleService()`）、Gateway 卡片（`components/GatewayCard.vue:178,187`）。
- 总览同时承担运行控制、指标、广告、趋势、活跃度、请求列表，是「监控 + 操作」混合页。
- 模型页两段式保存：`components/ModelEditor.vue:281` 调 `saveModelAdapterAt`，该函数在 `state/appState.js:2042-2067` 内实际执行 `persistConfigPayload(..., { modelAdaptersOnly: true })`，即**已经写盘**；而 `views/ModelConfig.vue:329` 的 `handleSavePage` 再写一次盘。用户看到的效果是「已写盘但页面仍显示未保存」。
- 设置页四个面板各自复制一条 `config-action-bar`（`views/SettingsView.vue:304,443,518`，第四个面板无操作栏），保存文案却按面板变化（`:46`，日志面板为「保存日志设置」）。

### 1.2 配色与对比度

- `--color-text-faint` 浅色为 `#9ca3af`（`style/global.css:56`），在 `#fafbfc` 背景上约 2.45:1，低于 WCAG AA 4.5:1。
- 深色 `--color-text-muted: #7b7b7b`（`:85`）在 `#191919` 上约 4.16:1，同样不达标。
- `.text-on-primary` 为 `#fff !important`（`:190`），配 `--color-primary: #10b981` 约 2.27:1。
- `global.css:198-270` 有一段「Theme compatibility layer」，把 `bg-[#202020]`、`border-[#14532d]` 等约 20 组 Tailwind 任意色类用 `!important` 映射回 token。这些是历史补丁，组件里仍在写硬编码色。
- `components/ui/ContentModal.vue:114,119,123` 硬编码 `border-[#3a3a3a] bg-[#202020] text-white`、`hover:bg-[#303030]`，深色弹窗在浅色主题下不跟随。

### 1.3 反馈与弹窗

- 三套弹窗实现：`ui/Modal.vue`（无 Esc、无焦点困囚、无焦点归还）、`ui/ContentModal.vue`（具备上述能力，但颜色硬编码）、`ModelAdapterModal.vue`（**零引用，死代码**）。
- 提示条 `ui/MessageProvider.vue` 单条、居中、`role="status"`，无法区分成功与失败；`composables/useMessage.js` 用固定 2400ms 与固定样式。
- `showActionError` 在 `components/GatewayCard.vue`、`components/SubscriptionAuthPanel.vue`、`views/ModelConfig.vue`、`views/CursorView.vue`、`views/SettingsView.vue`、`views/Home.vue`、`composables/useConfigTransfer.js:23-128` 等至少 9 处重复定义或重复拼装标题+错误的模式。

### 1.4 无障碍

- `global.css` 中 `focus-visible` 规则仅 4 条（`:1383-1386`：`.app-tab`、`.access-client`、`.seg button`、`.app-icon-btn`）。
- `ui/Button.vue` 无焦点环，禁用时只有 `disabled:opacity-55`，没有 `title` 说明原因。
- `HomeMetricsCard.vue:266-267` 的可点广告卡有 `role="button" tabindex="0"` 与键响应，是正面样板；请求列表行、接入页客户端项等未统一。

### 1.5 请求列表

- `components/RequestListPanel.vue:203` 固定 11 列、`min-w-[900px]`；`:83` 时间仅 `toLocaleTimeString`，跨天范围分不清日期；无排序；错误详情仅靠 `title`（`:259`）。

### 1.6 其它

- `views/Config.vue` 仍被 `i18n/generated/catalog.json` 记录，但已不在路由表中（四页 IA 替代），属待清理残留。
- `i18n/runtime.js` 的 `$ls/$lt` 全仓调用次数为 **0**；四个语言包各 777 条，与页面中文文案一一对应但未被引用。

---

## 2. 目标与非目标

### 目标

1. 每个动作只有一个入口，每个配置只有一个写盘点，用户能明确判断「改了没生效」的原因。
2. 浅色与深色主题下正文、辅助文字、主按钮文字对比度均 ≥ 4.5:1。
3. 键盘可完成主流程：切页、开合弹窗、操作列表、启停服务。
4. 失败有明确、可区分的反馈；不再出现「已保存却显示未保存」。

### 非目标（本次明确不做）

- 不改导航结构、不改路由映射、不合并页面。
- 不新增后端接口或 Go 侧能力。
- 不做完整多语言接入（D9），不改动任何中文文案的 i18n 抽取。
- 不做屏幕阅读器全量审计，不改信息架构层级。
- 不改 `SaveCursorConfig` / `SaveGatewayConfig` / `SaveModelAdapters` / `SaveSystemSettings` 的分作用域语义（决策基线 §10.14 红线）。
- 不主动 `git commit` / `git push`。

---

## 3. 批次总览

| 批次 | 主题 | 是否改变观感 | 可独立回退 |
|---|---|---|---|
| 第 0 批 | 清理：死代码、残留路由视图、硬编码色 | 否（应为零视觉差异） | 是 |
| 第 1 批 | 职责重整：总览只读、模型单一写盘入口、设置共用保存栏 | 是（页内） | 是 |
| 第 2 批 | 视觉与反馈：颜色 token、弹窗合并、toast 分语气、无障碍 | 是（全局） | 是 |
| 第 3 批 | 列表交互：排序、自适应时间、行展开 | 是（总览内） | 是 |

依赖：第 2 批的颜色 token 调整依赖第 0 批清掉硬编码覆盖（否则会出现改 token 局部不生效）；第 3 批与其余批无依赖。

---

## 4. 第 0 批：清理（零视觉差异）

### 任务

1. 删除死代码 `components/ModelAdapterModal.vue`（全仓零引用），同时移除 `changePlan5` 中与之配套的未使用 import（如有）。
2. 删除已无路由入口的 `views/Config.vue`，并清理 `i18n/generated/catalog.json` 中指向它的条目（该文件为生成物，若重新生成会自然消失，则只删视图）。
3. 把 `global.css:198-270` 的「Theme compatibility layer」逐条下沉到组件：组件里写 `bg-[#202020]` 之类的地方改为对应的 token 类（`bg-[var(--color-surface)]` 等），然后删除对应的 `!important` 覆盖。
   - 完成后 `grep -nE '^\s*\.(bg|border|text|ring)-\\\[\#' frontend/src/style/global.css` 应无输出。
   - 注意保留 `.text-on-primary` 这类语义类，第 2 批会改它的取值。
4. 清理已完成项时不要顺手动其它样式。

### 验收标准

- `cd frontend && npm run build` 通过。
- `node scripts/test-config-projection.mjs` 通过。
- 浅色/深色两套主题下逐页截图对比，颜色与文字无可见差异（这是本批唯一的观感要求：**必须相同**）。
- `grep` 校验：无 `ModelAdapterModal` 引用、`global.css` 无硬编码 hex 类覆盖。

### 回退

单批独立提交（经授权后）；回退即 `git revert` 本批提交，不影响其它批。

---

## 5. 第 1 批：职责重整

### 5.1 总览改为只读状态 + 分析（D2）

**文件**：`views/Home.vue`

- 删除两组启停按钮：Cursor 卡片 `:345,353`、Gateway 卡片 `:413,422`，以及对应的 `startService` / `stopService` / `startGateway` / `stopGateway` import 与调用（若首页不再需要）。
- 服务卡片保留：运行状态、监听地址/端口、公开模型数、`配置 →` 跳转。
- 增加主行动按钮「去接入页启动」，跳转 `accessRouteLocation('cursor')` / `('gateway')`；复用已有的 `goAccess`（`views/Home.vue:135` 附近）。
- 保留全部使用分析区块（指标卡、趋势、活跃度、请求列表）与广告位。

**验收**

- 总览页无任何启停控件；`grep -n "启动服务\|停止服务\|启动 Gateway\|停止 Gateway" views/Home.vue` 无输出。
- 从总览点「去接入页启动」落到 `/access?client=cursor`（或 gateway），接入页对应面板可直接启停。
- 启停行为与改造前一致（启动后用 `curl` 或应用内「测试连接」验证代理可用）。
- 决策基线 §10.14 第 589 行仍满足：启停只用已保存配置，scope dirty 时提示先保存（该逻辑在接入页，未改动）。

### 5.2 模型页单一写盘入口（D3 + 0.1）

**文件**：`views/ModelConfig.vue`、`components/ModelEditor.vue`、`state/appState.js`

- 保留编辑器「保存」的即时写盘行为（`saveModelAdapterAt` → `persistConfigPayload({ modelAdaptersOnly: true })`）。
- 编辑器保存成功后，确保模型作用域脏点归零。核对 `applyConfigToState` 的 `savedScope: "models"` 分支是否已清 `configSectionDirty.models`；若已清则无需改动，若未清则补齐（只在确实不生效时改）。
- `views/ModelConfig.vue` 底部操作栏改为按脏状态呈现：干净 → 「重新加载」；脏 → 「保存模型配置」+「重新加载」。删除重复的两段式语义文案。
- 不得改动复制 / 删除 / 排序 / 导入的草稿语义（见 0.1）。

**验收**

- 打开已有模型 → 改名 → 编辑器内保存 → 关闭编辑器：底部不再显示「保存模型配置」，直接关闭页面**不**弹「有未保存更改」。
- 拖拽排序后：底部出现「保存模型配置」，离开页面弹出确认，确认后草稿被丢弃、重新进入恢复原顺序。
- 导入合并后：底部出现保存按钮，点击后写盘并清脏点。
- `persistConfigPayload` 的 `modelAdaptersOnly` 路径未被破坏：`node scripts/test-config-projection.mjs` 通过。

### 5.3 设置页四面板共用保存栏（D8）

**文件**：`views/SettingsView.vue`

- 抽出单一 `config-action-bar`，放在四个面板之外（页面底部固定），四个 `Card` 只保留内容区。
- 文案统一为「保存设置」；删除 `:46` 按面板切换文案的逻辑。
- 脏点提示、保存中状态、错误提示由同一处渲染。
- 面板切换（`seg`）不触发保存、不丢失草稿。
- `settingsPanel === 'recovery'` 面板若确实没有可保存字段，则其面板内不显示保存按钮的既有行为改为：全局保存栏常驻，但保存内容仍只包含已接通的 settings 字段（保持后端契约不变）。

**验收**

- 四个面板任一改动 → 底部出现脏点与「保存设置」；切换面板再切回，改动仍在。
- 点击保存 → 一次写入，四个面板的改动同时生效；无「日志面板保存不了基本设置」的情况。
- 检查 `SaveSystemSettings` 提交的字段集合与改造前一致（对比请求 payload），未新增或丢失字段。

### 5.4 多语言选择器标记计划中（D9）

**文件**：`components/LocaleSelect.vue`、`views/SettingsView.vue:205`、`layouts/MainLayout.vue:312`

- 选择器改为禁用态 + 「计划中」标识，样式与设置页既有「计划中」行一致。
- 保留 `i18n/runtime.js`、四个语言包与 `i18n/generated/catalog.json`，不删除。

**验收**

- 点击选择器无下拉、无语言切换副作用（`localStorage` 不被改写）。
- `document.documentElement.lang` 保持默认值，页面文案不变。

---

## 6. 第 2 批：视觉与反馈

### 6.1 颜色 token 与对比度（D4）

**文件**：`frontend/src/style/global.css`

- 浅色主题：
  - `--color-text-faint`：`#9ca3af` → `#6b7280`（对 `#fafbfc` 约 4.73:1）。
  - 相应地加深 `--color-text-muted` 以保持层级（建议 `#5b6675` 量级，实施时以脚本测量为准）。
  - `--color-text-secondary` 保持 `#4b5563`（约 7.4:1）。
- 深色主题：
  - `--color-text-muted`：`#7b7b7b` → 满足 ≥4.5:1 的值（`#9a9a9a` 对 `#191919` 约 6.2:1）。
  - `--color-text-faint`：`#6b6b6b` → 同步提高，保持比 muted 略低但 ≥4.5:1。
- 主按钮：白字配 `#10b981` 仅约 2.27:1。采用「加深填充色」方案：新增/调整 `--color-primary-strong` 作为实心按钮背景（建议 `#047857`，白字约 5.5:1），hover 用更深一档（`#065f46`）；`#10b981` 继续作为强调色（图标、焦点环、进度），不用于承载白字的实心块。
- `.text-on-primary` 保持「白色」，只改它所在的背景 token。

**验收（必需，不可口头声称）**

- 写一个一次性测量脚本（可放在 `frontend/scripts/` 下或直接内联 node 命令），对下列组合逐一输出比值并断言 ≥4.5：
  - 浅色：faint/muted/secondary/正文 对 `--color-app-bg` 与 `--color-surface`
  - 深色：同上
  - 主按钮白字对 `--color-primary-strong`
- 脚本作为本批验收证据的一部分，输出留档。
- 视觉走查：浅色/深色各过一遍四个页面，确认没有出现「文字看不清」或「按钮变土」的区域；截图对比。

### 6.2 弹窗合并（D5）

**文件**：`components/ui/Modal.vue`、`components/ui/ContentModal.vue`、`App.vue`、`composables/useModal.js`

- 把 `ContentModal.vue` 的 Esc 关闭、Tab 焦点困囚、关闭后焦点归还、`role="dialog"` / `aria-modal` / `aria-label` 能力抽到一个共享实现（composable 或基座组件），供两处复用。
- `ui/Modal.vue` 接入该能力：开启后 `Esc` = 取消，`Tab` 在弹窗内循环，关闭后焦点回到触发元素。
- `ContentModal.vue` 的硬编码深色（`:114,119,123`）改为 token：`bg-[var(--color-surface)]`、`border-[var(--color-border)]`、`text-[var(--color-text)]`、`hover:bg-[var(--color-surface-hover)]`、`text-[var(--color-text-muted)]`。
- 遮罩点击行为保持现状（`Modal` 点遮罩 = 取消，与现有 `App.vue` 绑定一致），不新增行为。

**验收**

- 触发一次「离开页面放弃未保存修改」确认框：按 `Esc` 等于「留下」，焦点回到触发的标签/按钮。
- 弹窗内连续按 `Tab` 不会跑到背景页面元素。
- 深色主题下模型详情弹窗、导入弹窗的背景色为 `--color-surface`（截图确认），不再是固定 `#202020`。

### 6.3 toast 分语气与 `showActionError` 收敛（D5）

**文件**：`composables/useMessage.js`、`components/ui/MessageProvider.vue`、新增 `composables/useActionError.js`（或并入现有 composable）、各调用点

- `useMessage` 增加 `tone`：`success` / `error` / `info`（默认 `info`，向后兼容现有 `message(...)` 调用）。
- `MessageProvider` 按 tone 渲染：左侧图标 + 边框/背景使用 `--color-success-*` / `--color-error-*` / `--color-info-*`；`role="status"` 保留，error 用 `role="alert"`。
- 时长：`success` 2.4s；`error` 默认不自动消失，提供关闭按钮（点击关闭），避免错误一闪而过。
- 新增统一 `showActionError(title, detail)`：拼成「标题：详情」，以 `error` 语气展示，并集中处理 `toUserError` 为空时的兜底文案。
- 替换全部重复定义：`GatewayCard.vue`、`SubscriptionAuthPanel.vue`、`ModelConfig.vue`、`CursorView.vue`、`SettingsView.vue`、`Home.vue`、`useConfigTransfer.js` 中的本地实现删掉，统一 import。
- 区分成功路径：现有 `message("...已保存")` 之类改为 `message(..., { tone: 'success' })`。

**验收**

- 制造一次保存失败（例如把模型 baseURL 改成非法值）→ 提示为红色、带图标、不自动消失、可关闭。
- 保存成功 → 绿色提示，2.4s 后消失。
- `grep -rn "function showActionError" frontend/src` 输出为 0。
- 所有原先调用点行为等价（成功路径不再被红色错误样式污染）。

### 6.4 无障碍关键路径（D7）

**文件**：`style/global.css`、`components/ui/Button.vue`、`components/ui/Modal.vue`、`views/AccessView.vue`、`components/RequestListPanel.vue`

- 全局焦点环：定义统一的 `:focus-visible` 规则（2px 描边 + 2px offset，颜色用 `--color-primary`），覆盖 `button`、`a`、`[role="button"]`、`input`、`select`、`[tabindex]`；删除零散的 4 条局部规则中重复的部分（保留必要特例）。
- `Button.vue`：增加 `disabledReason` prop，禁用时输出 `title` 与 `aria-disabled`；对确有原因的调用点（如「服务运行中不能导入」）补传原因。
- 可点元素键盘支持：`AccessView` 客户端项已是 `button`（保持）；请求列表行展开（第 3 批实现时必须同时是键盘可操作）；广告卡保持现有 `role/tabindex/Enter/Space`。
- 弹窗 Esc / 焦点困囚 / 焦点归并在 6.2 已覆盖，本批只验收不重复实现。

**验收**

- 全流程纯键盘走一遍：切四个标签 → 接入页选客户端 → 打开模型编辑 → 保存 → 关闭 → 打开设置 → 切换面板。每一步焦点可见，不落空。
- 禁用按钮悬浮显示原因。
- `prefers-reduced-motion: reduce` 仍生效（`global.css:1526` 附近未被破坏）。

---

## 7. 第 3 批：请求列表交互（D6）

**文件**：`components/RequestListPanel.vue`、`views/Home.vue`（若需要把排序参数透传给后端）

### 前置核对（必须先做）

- 确认请求列表数据来源是否支持排序：若 `result.items` 是一次性取回的当页数据，则排序只能作用于当前页，需在 UI 上明确标为「本页排序」；若后端接口支持 `sort` 参数，则透传并在切换时重新请求。
- 确认行详情所需字段（错误分类 `errorCategory`、请求 ID、上游模型）在 `result.items` 中已存在，避免为展示新增后端字段。

### 已确认的实施边界（2026-11-18）

- 用户确认仅做**本页排序**，UI 明示「本页排序」；翻页保持排序字段与方向，切换时间范围或供应商/模型/状态筛选重置排序。
- 用户确认接受行详情降级：展示错误分类、状态码与状态、请求 ID、原始请求模型、上游模型、缓存读写明细；现有记录没有错误信息全文，本批不新增后端字段，不以分类冒充错误全文。下述任务与验收中的错误全文要求以本条为准。
- 切换筛选/时间范围或翻页清空行展开状态。不改变后端分页、接口与保存作用域。

### 任务

1. 时间列按 `range` 自适应格式：`today` → `HH:mm:ss`；`24h` / `7d` → `MM-DD HH:mm`；`30d` → `MM-DD HH:mm`。保持 `font-family: var(--font-num)`。
2. 列头排序：时间、输入、输出、耗时（`durationMs`）、首字耗时（`ttftMs`）可点，三态循环（无 → 升序 → 降序），箭头指示方向，`aria-sort` 正确。
3. 行展开：点击行（或行首展开按钮）展示详情区，内容为错误分类与错误信息全文、请求 ID、原始请求模型、上游模型、缓存读写明细。展开状态为本地状态，切换筛选/翻页时清空。
4. 键盘：行可聚焦，`Enter` / `Space` 展开折叠，展开按钮有 `aria-expanded`。
5. 不为列表新增依赖（不引入表格库）。

### 验收

- 选「30 天」后时间显示为 `MM-DD HH:mm`，能区分不同日期。
- 点「耗时」列头：升序 → 降序 → 恢复，数据顺序正确（与手工核对首行一致）。
- 展开一条失败请求：能看到错误分类与完整错误文本（不得被截断）。
- 纯键盘可展开/折叠任意一行。
- 排序与筛选、翻页组合使用时状态一致（翻页后排序保持，或明确重置——二者择一并在 UI 上说明）。

---

## 8. 跨批约束（不得违反）

1. 配置保存作用域不得合并：继续使用 `SaveCursorConfig` / `SaveGatewayConfig` / `SaveModelAdapters` / `SaveSystemSettings`，禁止新增「保存全部配置」的接口路径（决策基线 §10.14 第 587 行）。
2. 启停只使用已保存配置；当前 scope 脏时必须提示先保存（§10.14 第 589 行）。本方案不改变该逻辑所在位置（接入页）。
3. 路由守卫的 scope 映射（`/access?client=cursor` → `cursor`、`gateway` → `gateway`、`/models` → `models`、`/settings` → `settings`）保持不变（`router/index.js:39-104`）。
4. 不把 token、API Key、Cursor token 写入日志、`localStorage` 或普通 JSON 投影（§10.14 第 488 行）。
5. 不改中文文案的语义；第 1 批只改控件与布局，不顺手改措辞。

---

## 9. 验证与证据要求

每批交付必须附带下列证据，缺一不得声明该批完成：

| 项目 | 证据 |
|---|---|
| 构建 | `cd frontend && npm run build` 的实际输出（无 error） |
| 投影/契约 | `node scripts/test-config-projection.mjs` 输出 |
| 视觉 | 浅色 + 深色两套主题下受影响页面的截图 |
| 交互 | 受影响路径的实际操作记录（点击/键盘/输入），含失败路径 |
| 对比度（仅第 2 批） | 测量脚本输出，逐项 ≥4.5:1 |
| 零残留 | 对应 `grep` 校验（死代码、`showActionError`、硬编码 hex） |

状态用语要求：未跑过的检查不得写「已验证」；只跑了部分页面时写「已验证部分路径 + 未覆盖页面清单」。

---

## 10. 风险与应对

| 风险 | 影响 | 应对 |
|---|---|---|
| 清理硬编码色后出现局部颜色回归 | 中 | 第 0 批要求「零视觉差异」，逐页截图对比；发现差异即回退该条并单独处理 |
| token 调整使某些灰字层级看不清 | 中 | 用测量脚本代替肉眼；同步调整相邻 token 保持层级 |
| 总览移除启停导致用户少一步操作变多 | 低 | 加显式跳转按钮并在卡片上标注当前状态；用户已确认此取舍 |
| 模型页脏点语义变化引起守卫误弹 | 中 | 5.2 的验收显式覆盖「编辑器保存后关闭页面不弹确认」 |
| toast 改 error 不自动消失造成堆积 | 低 | 单条替换 + 关闭按钮；必要时限制同时一条 |
| 列表排序误以为全量排序 | 中 | 7 的前置核对决定文案与实现，UI 标明范围 |

---

## 11. 需要人工确认的后续事项

- 第 2 批主按钮配色（加深填充 vs 保留品牌绿改深色文字）若视觉上不可接受，需回到用户处再定一次。
- 第 3 批排序范围（本页 vs 全量）取决于后端接口能力，实施时若发现不支持，需回到用户处确认是否接受「本页排序」。
- 多语言真正接入不在本次范围，若后续启动，需先评估 682 行含中文字符串的抽取成本。
