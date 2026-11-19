# 第 1 批工单：职责重整

- 状态：待下发（依赖第 0 批验收通过）
- 对应方案：`../plan.md` §5（含 §0.1 补充规则）
- 核心要求：每个动作一个入口；模型页单一写盘入口；设置页共用保存栏；语言选择器标记「计划中」。只改控件与布局，不顺手改中文措辞。

## 共同约束

- 不 commit / 不 push；不新增依赖；不改后端/Go、路由映射、i18n 运行时与语言包。
- 配置保存作用域不得合并：继续使用现有 `persistScopedUserConfig("cursor"|"gateway"|"models"|"settings")` / 对应保存接口。
- 启停只使用已保存配置；scope 脏时提示先保存（该逻辑在接入页，保持不动）。
- 复制 / 删除 / 拖拽排序 / 导入仍只改前端草稿，不改为自动写盘。

## 任务

### 1.1 总览改为只读状态 + 分析

文件：`frontend/src/views/Home.vue`

- 删除 Cursor 卡片与 Gateway 卡片上的启停按钮（停止服务/启动服务/停止 Gateway/启动 Gateway），以及仅被它们使用的处理函数与 import（确认无其它引用后再删）。
- 保留：运行状态、监听地址/端口、公开模型数、错误/未启用提示、「配置 →」跳转、打开 Cursor（如它不属于启停）。
- 增加主行动入口「去接入页启动」，复用 `goAccess("cursor")` / `goAccess("gateway")`；按钮文案与放置位置需让用户一眼看出「启停已移到接入页」。
- 保留全部使用分析区块（指标卡、趋势、活跃度、请求列表）与广告位。
- 不修改接入页启停逻辑。

验收：

- `grep -n "启动服务\|停止服务\|启动 Gateway\|停止 Gateway" frontend/src/views/Home.vue` 无输出。
- 总览页无任何启停控件；点击「去接入页启动」落到对应 `/access?client=cursor|gateway`。
- 接入页启停行为未被改动（对比 diff 仅 Home 相关）。
- 浏览器预览可验证：无启停控件、跳转正确、使用分析区块仍渲染（无数据时显示既有空态）。

### 1.2 模型页单一写盘入口（D3 + §0.1）

文件：`frontend/src/views/ModelConfig.vue`、`frontend/src/components/ModelEditor.vue`、`frontend/src/state/appState.js`

- 保留编辑器「保存」的即时写盘（`saveModelAdapterAt` → `persistConfigPayload({ modelAdaptersOnly: true })`）。
- 编辑器保存成功后，模型作用域脏点必须归零。先核对 `applyConfigToState` 的 `modelAdaptersOnly` / `savedScope: "models"` 分支是否已清脏点；**已生效则不改**，未生效才补最小修复。
- 底部操作栏按脏状态呈现：
  - `configSectionDirty.models === false`：只显示「重新加载」；
  - `true`：显示「保存模型配置」+「重新加载」。
- 删除重复的两段式语义文案；不改变复制 / 删除 / 排序 / 导入的草稿语义。

验收：

- 打开已有模型 → 改名 → 编辑器内保存 → 关闭编辑器：底部不再显示「保存模型配置」；直接离开页面不弹「有未保存更改」。
- 拖拽排序后：底部出现「保存模型配置」；离开页面弹确认；确认后草稿丢弃，重进恢复原顺序。
- 导入合并后：底部出现保存按钮；点击后写盘并清脏点。
- `node scripts/test-config-projection.mjs` 通过。
- 浏览器预览：脏/净两态操作栏切换可验证；真实写盘需后端，若无法在预览中验证，明确记为 test/env gap，不得声称已端到端验证。

### 1.3 设置页四面板共用保存栏（D8）

文件：`frontend/src/views/SettingsView.vue`

- 抽出单一 `config-action-bar`，放到四个面板之外（页面底部），四个面板只保留内容区。
- 文案统一为「保存设置」；删除按面板切换文案的逻辑（`settingsSaveLabel` 中 logs 分支）。
- 脏点提示、保存中状态、错误提示由同一处渲染。
- 面板切换（`seg`）不触发保存、不丢失草稿。
- recovery 面板无保存字段时：全局保存栏仍常驻；保存内容仍只包含已接通的 settings 字段，保持后端契约不变。
- 保存后仍通过现有 settings 保存接口，字段集合不得新增或丢失。

验收：

- 四个面板任一改动 → 底部出现脏点与「保存设置」；切换面板再切回，改动仍在。
- 点击保存 → 一次写入；检查提交 payload 的字段集合与改造前一致（对照 `serializeConfigPayload` / 保存接口调用参数，写出核对结论）。
- 日志面板的改动也能通过同一保存栏保存（不再出现「日志面板保存不了基本设置」的语义误导）。
- 浏览器预览：脏点、面板切换、草稿保留可验证；真实写盘如无法验证，记为 gap。

### 1.4 多语言选择器标记「计划中」（D9）

文件：`frontend/src/components/LocaleSelect.vue`、`frontend/src/views/SettingsView.vue:205`、`frontend/src/layouts/MainLayout.vue:312`

- 两处选择器改为禁用态 + 「计划中」标识，样式与设置页既有 `badge-soon` / `is-planned` 行一致。
- 保留 `i18n/runtime.js`、四个语言包、`i18n/generated/catalog.json`，不删除。
- 不新增「切换语言」行为，不修改默认语言逻辑。

验收：

- 点击选择器无下拉、无语言切换副作用；`localStorage` 的 `cursor-client:locale:v1` / `cursor-client:locale-source:v1` 不被改写。
- `document.documentElement.lang` 保持当前值，页面文案不变。
- 浏览器预览可完整验证。

## 证据与回报

- 实际执行：`cd frontend && npm run build`、`node scripts/test-config-projection.mjs`。
- 交互证据：浏览器预览下的脏点/净态、面板切换、禁用选择器、总览跳转；截图或步骤记录。
- 真实后端写盘、启停代理可用性若无法执行，单独列 `test/env gap`，不得写成已验证。
- 报告写入 `evidence/batch-1.md`；pane 内回报 `BATCH 1: PASS|BLOCKED` + 改动/验证/缺口。
