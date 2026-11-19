# 第 1 批证据记录（职责重整）

- 日期：2026-11-17
- 执行：`ui-executor`
- 状态：实现完成（含 rev2 用户决策补充）；`npm run build` 通过、`node scripts/test-config-projection.mjs` 通过；真实后端写盘/启停为 `test/env gap`
- 版本：rev1 初稿 + rev2（页脚语言选择器移除、设置页提示文案更新、总览双入口保留确认，见 §10）
- 工作区：未 commit / 未 push

## 1. 改动文件清单

| 文件 | 改动 |
|---|---|
| `views/Home.vue` | 删除 Cursor/Gateway 卡片启停按钮、`handleCursorStart/Stop`、`handleGatewayStart/Stop`、`cursorStartDisabled`、`gatewayStartDisabled` 及 `startService/stopService/startGateway/stopGateway` import；卡片底部新增 primary「接入页启动」（复用 `goAccess`） |
| `views/ModelConfig.vue` | 底部保存按钮改为 `v-if="configSectionDirty.models"`：干净态仅「重新加载」，脏态显示「保存模型配置」 |
| `views/SettingsView.vue` | 删除基本/日志/网络三个面板内的 `config-action-bar`，在四面板之外（页面底部）新增唯一保存栏；`settingsSaveLabel` 去掉按面板切换文案（统一「保存设置」）；语言提示改为「多语言界面将在后续版本提供」（rev2，用户批准） |
| `layouts/MainLayout.vue` | rev2：删除页脚 `LocaleSelect` 渲染、import 与其包装容器（组件本身保留） |
| `components/LocaleSelect.vue` | 选择器改为 `disabled` + 「计划中」`badge-soon`，移除 `setLocale` 绑定（现仅设置页使用） |
| `scripts/test-config-projection.mjs` | 两处等价承接调整（详见 §6）：Home 的 partial/cancelled 断言改挂 `CursorView`；页脚 LocaleSelect 断言按用户决策改为「不再渲染」 |
| 生成物 `i18n/generated/catalog.json`、`i18n/locales/*.json` | build 重新生成：移除已删组件文案，新增「接入页启动」；无其它语义变更 |

未改动：`views/CursorView.vue`、`components/GatewayCard.vue`、`state/appState.js`、路由与保存接口。

## 2. 1.1 总览只读状态 + 分析

- `grep -n "启动服务\|停止服务\|启动 Gateway\|停止 Gateway" frontend/src/views/Home.vue` → 无输出（exit 1）。
- 保留：状态 pill、代理/Backend/监听地址、公开模型数、错误与「请先保存 …配置」提示、`打开 Cursor`、`配置 →`、全部使用分析区块与广告区。
- 新增主入口：`Home.vue:288`（Cursor）、`Home.vue:341`（Gateway），文案统一「接入页启动」，`goAccess("cursor"|"gateway")`。
- 浏览器验证（预览 1440×900，浅/深色）：
  - 点 Cursor 卡片「接入页启动」→ `#/access?client=cursor`；
  - 点 Gateway 卡片「接入页启动」→ `#/access?client=gateway`；
  - 总览无任何启停控件（截图 `batch1-after/light-home.png`、`dark-home.png`）。
- 接入页启停未改动：`git status` 显示 `CursorView.vue` 无修改；`GatewayCard.vue` 无修改。
- **用户决策（rev2）**：保留双入口（「接入页启动」+「配置 →」），现状即符合，无需改动；primary 按钮 vs 文字链接的层级保持不变。

## 3. 1.2 模型页单一写盘入口

- 代码核对（只读）：`persistConfigPayload(..., { modelAdaptersOnly: true })` → `applyConfigToState(..., { modelAdaptersOnly: true, savedScope: "models" })`；`modelAdaptersOnly` 分支中 `preserveModelsDraft` 对 `savedScope === "models"` 为 false，因此 `appState.modelAdapters` 取持久化值并 `captureConfigSectionSnapshot("models")` 刷新基线；编辑器 `persistDraft()` 成功后另调 `setModelsEditorDraftDirty(false)`。**脏点归零已生效，未改 `appState.js`。**
- 底部操作栏按脏态呈现（`ModelConfig.vue:760`）：
  - 干净态（预览初始、0 模型）：`已保存 · 共 0 个模型` + 仅「重新加载」（`batch1-after/light-models.png`）。
  - 脏态（打开「新增模型」并输入显示名称）：`有未保存更改 · 共 0 个模型` + 「重新加载」「保存模型配置」。
  - 编辑器关闭（未保存）后草稿丢弃、脏点回到干净态，底部保存按钮消失。
  - 脏态下切换路由：出现「有未保存更改 / 离开后将丢弃本页未保存的修改」确认；点「留下」停留原页且草稿保留（实际触发并验证）。
- 未覆盖（`test/env gap`）：
  - 「打开已有模型 → 改名 → 编辑器内保存 → 关闭」真实写盘链路：预览无后端（404），无既有模型数据，无法端到端验证；脏点归零逻辑仅做代码核对。
  - 拖拽排序、导入合并后的脏点/保存：预览无 ≥2 个模型且导入依赖真实文件对话框，未验证。
  - 真实 `SaveModelAdapters` 写盘未执行。

## 4. 1.3 设置页共用保存栏

- 结构：`grep -c "config-action-bar" SettingsView.vue` = 1，位于四个 `Card` 之后、页面容器底部；四个面板只保留内容区。
- 文案：`settingsSaveLabel` 仅保留保存中/「保存设置」两态；「保存日志设置」分支删除。
- 浏览器验证（浅色，1440×900）：
  - 基本设置勾选「广告内容」→ 底部状态变「有未保存更改」、保存按钮启用（`batch1-after/light-settings-basic.png` 为干净态；操作记录见本节）。
  - 切到「会话与日志」再切回「基本设置」：勾选状态保留、底部仍为单一栏且显示脏点（未触发保存）。
  - 「数据与恢复」面板无保存字段：底部栏仍常驻可见（`light-settings-recovery.png`）。
  - 四个面板截图：`light-settings-{basic,logs,network,recovery}.png`。
- payload 字段集合核对（只读）：`handleSaveConfig` 仍调用 `persistScopedUserConfig("settings")` → `persistScopedConfig("settings")`，构造路径仍为 `buildConfigPayloadFromState()` + 全局代理校验 + `saveSystemSettings`；本次未改 `appState.js`，字段集合与改造前一致，无新增/丢失。
- 未覆盖（`test/env gap`）：点击保存的真实 `SaveSystemSettings` 写盘（预览无后端）未执行。

## 5. 1.4 多语言选择器「计划中」

- `LocaleSelect.vue`：`Select` 增加 `disabled`；右侧渲染 `<span class="badge-soon">计划中</span>`；移除 `@update:model-value="setLocale"`。
- rev2 用户决策「只留设置页标识」：删除 `MainLayout.vue` 页脚渲染、import 与 `ml-auto` 包装容器（`LocaleSelect.vue` 组件、`i18n/runtime.js`、四个语言包、`catalog.json` 均保留）。现仅 `SettingsView.vue:205` 一个使用点。
- 浏览器验证：
  - 设置页选择器按钮 a11y 状态 `disabled`；点击无 `[role="listbox"]` 出现（0 个）。
  - 点击前后 `localStorage.cursor-client:locale:v1 = zh-CN`、`locale-source:v1 = manual` 不变；`document.documentElement.lang = "zh-CN"` 不变；页面文案不变。
  - 页脚不再渲染语言选择器（`footer button[aria-label="界面语言"]` 不存在；a11y 树 footer 仅剩版本/检查更新），页脚左侧内容与高度正常（截图 `batch1-after/light-footer.png`、`dark-footer.png`）。
  - 截图：`batch1-after/light-settings-basic.png`、`dark-settings-basic.png`。
- 设置页提示（rev2 用户批准改这一处）：`切换后立即应用并保存在本机` → `多语言界面将在后续版本提供`；其余文案未动。
  - 生成物同步：四个 `locales/*.json` 均新增同一消息 id `84caad6b78bef424`（zh-CN 值为新文案；en-US/ja-JP/ru-RU 为插件生成的空值，与既有新键 `接入页启动` 的处理一致），旧 id `3870b850ec101a01` 在四个文件中均已移除；`catalog.json` 含新文案、无旧文案。四个语言包键数均为 745，本次仅这一对键的替换，没有其它增删。

## 6. 测试脚本等价承接（两处，均已在证据中说明）

1）Home 启停处理已删除，以下断言失去对象：

```
Home surfaces partial Cursor launch success
Home does not treat restart refusal as start failure
```

等价行为承接在接入页 `CursorView.vue` 的 `handleToggleService()`（`result.partial` / `result.cancelled`，同语义）；断言改为：

- `test-config-projection.mjs:2404` `CursorView surfaces partial Cursor launch success`（原已存在）
- `test-config-projection.mjs:2405` `CursorView does not treat restart refusal as start failure`（新增承接）

2）rev2 按用户决策移除页脚 LocaleSelect，原断言「shell keeps locale select」的对象被有意撤销。改为反向断言 `assert(!layoutSource.includes("LocaleSelect"), "shell no longer renders locale select until multi-language ships")`；设置页仍有 `settingsSource.includes("LocaleSelect")` 断言（`language remains immediate LocaleSelect`）覆盖组件存在性。

未改动主控修复的「登录时启动」相关断言。

## 7. 构建 / 测试（rev2 最终状态实际执行）

```
$ cd frontend && npm run build
✓ built in 2.87s          # BUILD_EXIT=0（完整日志 .ui-refresh-evidence/batch1-build.log）

$ cd frontend && node scripts/test-config-projection.mjs
config projection tests passed   # PROJ_EXIT=0（完整日志 .ui-refresh-evidence/batch1-projection.log）
```

## 8. 视觉基线 / 改后截图

- 基线（rev1 改动前，历史留档）：`frontend/.ui-refresh-evidence/batch1-baseline/`
- 改后（rev2 覆盖后）：`frontend/.ui-refresh-evidence/batch1-after/`
- rev2 重抓并覆盖：总览浅/深（`light-home.png`、`dark-home.png`）、设置页基本设置浅/深（`light-settings-basic.png`、`dark-settings-basic.png`）、页脚浅/深（`light-footer.png`、`dark-footer.png`，旧 `*-footer-locale.png` 已删除）。
- 其它保留的改后图：`light-models.png`、`dark-models.png`、`light-settings-{logs,network,recovery}.png`。
- 条件：`npm run dev -- --port 5199 --host 127.0.0.1`，1440×900，`zh-CN`，主题经 `document.documentElement.dataset.theme` 切换。
- 页脚核对：删除选择器后右侧包装容器一并移除，页脚仅剩左侧版本/检查更新，高度与对齐正常（元素截图与全页截图均确认）。
- 未覆盖：真实模型编辑器弹窗、模型页脏态截图（以文本/状态读数为证）；登录时启动行因浏览器预览 `platform` 非 darwin 未渲染，未做视觉核对。

## 9. 缺口 / 风险汇总

- `test/env gap`：所有真实写盘（模型编辑保存、设置保存）、真实启停、拖拽排序/导入、后端数据下的脏点链路均无法在浏览器预览验证，未声称已验证。
- 总览双入口：经用户确认「保留两个（接入页启动 + 配置 →）」，现状即符合；两张卡片按钮文案与层级无需再调（rev2 确认，未改代码）。
- 语言提示失真问题已由 rev2 文案修改消除。
- 页脚移除语言选择器后，用户不再能从页脚看到语言入口；与 D9「本次不做多语言」一致，组件与语料保留，后续接入时再恢复入口。

## 10. rev2 用户决策后的补充改动

主控复核后转达用户三项决策，rev2 已按此完成：

1. **页脚语言选择器「只留设置页标识」**：删除 `MainLayout.vue` 的 LocaleSelect 渲染、import 与 `ml-auto` 包装容器；`LocaleSelect.vue` / i18n 运行时 / 语言包 / catalog 全部保留。页脚右侧不再有空容器，左侧版本与检查更新布局正常（`batch1-after/{light,dark}-footer.png`）。
2. **设置页提示文案**：`切换后立即应用并保存在本机` → `多语言界面将在后续版本提供`（仅此一句）；build 后四个语言包与 catalog 已同步（新 id `84caad6b78bef424` 在四个文件均存在，旧 id `3870b850ec101a01` 均移除，四文件键数 745 不变，无其它键增删）。
3. **总览双入口保留**：未改代码，确认两卡按钮文案与层级为最终状态。

rev2 重新执行：`npm run build`（exit 0，2.87s）、`node scripts/test-config-projection.mjs`（passed）。截图已覆盖旧图（页脚文件名由 `*-footer-locale.png` 改为 `light-footer.png` / `dark-footer.png`）。
