# 批次 4 执行与自测证据

- 日期：2026-11-18。
- 执行者：`ui-executor`。
- 工单：`../work-orders/batch-4.md`；用户确认建议稿并撤销 D2「总览只读」。
- 执行结果：**PASS（实施与本地自测通过，待主控独立复核）**。
- `delivery_status=verified-partial`：前端已接线并完成合成状态浏览器检查，不代表真实 Wails 桌面启停、保存或模型调用验收；不自行标记 accepted。

## 1. 本批改动

| 文件 | 改动 |
| --- | --- |
| `frontend/src/views/Home.vue` | 恢复卡内 Cursor/Gateway 启停处理与既有 scope dirty 门槛；错误走统一提示入口；保留打开 Cursor 与配置跳转；禁用原因通过 Button 提示，服务卡局部紧凑化 |
| `frontend/src/components/GatewayCard.vue` | 公开模型改开关、名称、输入三列；默认名称用 placeholder，自定义名保留二级说明；未公开或保存中禁用输入 |
| `frontend/src/views/ModelConfig.vue` | 列表去掉 ⋮ 浮层，测试/编辑/复制/删除行内显示；卡片改 inline 状态与底部操作区；编辑器使用自绘标题栏 |
| `frontend/src/components/ModelAdapterTestCard.vue` | 增加默认关闭的 inline 模式；保留完整模式、原始返回 Tooltip 与过期状态 |
| `frontend/src/components/ModelEditor.vue` | 启用开关入标题栏；字段按连接/模型/高级分组，收紧标签与间距；底部状态和原操作按钮位于滚动区外 |
| `frontend/src/components/ui/ContentModal.vue` | 增加默认 false 的 headerless，允许编辑器自绘标题栏，沿用既有焦点与关闭控制 |
| `frontend/src/style/global.css` | 局部密度类、三列公开模型行、卡片底栏；移除 model-more-menu 样式 |
| `frontend/scripts/test-ui-density.mjs` | 本批投影语义与结构回归断言 |

未改后端、字段语义、校验、保存作用域、路由或新增依赖；未主动修改 i18n 语料/运行时。工作区包含其他批次既有差异，不能把整个工作区 diff 归为本批。未修改主控维护的 `execution.md`。

## 2. TDD 与命令证据

先新增密度回归断言，再实施界面：首次执行实际 RED 为 `Home 需接回 startService`。实现后逐项转绿。过程中修正两项断言/标记：补上编辑器专用标题栏 class；最大输出字段断言使用既有 computed `maxCompletionTokensInput`，不改字段绑定。

在 `frontend/` 执行：

| 命令 | 实际结果 |
| --- | --- |
| `node scripts/test-ui-density.mjs` | PASS，`ui density structure tests passed` |
| `npm run build` | PASS，退出 0，Vite 构建 3.26s |
| `node scripts/test-config-projection.mjs` | PASS |
| `node scripts/test-ui-feedback.mjs` | PASS |
| `node scripts/test-request-list.mjs` | PASS |
| `node scripts/check-contrast.mjs` | PASS |
| `git diff --check` | PASS，无输出 |

新增语义断言调用现有 `listGatewayPublicModelRows` / `buildGatewayPublicModelOverrides`：自定义名有 hasCustom，默认名无 hasCustom，清空自定义名称得到空覆盖数组（回到默认名）。其余密度断言主要为源码结构检查，不冒充组件交互测试或真实后端测试。

## 3. 浏览器检查

入口为 Vite `http://127.0.0.1:5199/`，通过 Chrome DevTools 操作独立浏览器上下文 `batch4`，使用合成、无敏感的内存状态。浅/深主题均截图并查看。未点击真实服务启停、成功保存或向真实模型发送测试请求。

### 总览

- 未运行：Cursor「启动服务」禁用原因 `请先保存 Cursor 配置`；Gateway「启动 Gateway」禁用原因 `需先启用并保存配置`；打开 Cursor 与配置入口保留。
- 运行：分别显示「停止服务」「停止 Gateway」，运行状态标签与按钮对应。
- 两卡等高，实测约 **140px**（工单目标约 148px，属近似密度目标的尺寸偏差，交主控视觉复核）；chips 单行。
- 状态、dirty 与运行标记来自合成注入，不是实际启动/停止成功证据。

### 公开模型

- 默认名 `smartdk`：输入为空、placeholder `默认：smartdk`。
- 自定义名 `gpt-6.2-sol-211`：二级说明 `对外：sol-fast`，输入值 `sol-fast`。
- 未公开 `gpt-6-astra-211`：开关关闭、输入禁用。
- 三列对齐、分隔线及名称截断/title 保留；清空回默认名由上述真实投影函数测试覆盖，未做 Wails 保存/重载验证。

### 模型列表与卡片

- 列表行高实测 **54–55px**（工单约 58px）；保留五列、拖拽手柄、开关与测试状态。
- 行内显示测试/编辑/复制/删除，操作区 nowrap，无 ⋮ 菜单与浮层裁切；删除保持红色并沿用原确认处理函数，未实际删除配置。
- 卡片实测三张均 **171px**（工单约 165px），底栏对齐；未测试、通过和停用三态可见，停用降低不透明度。
- 通过态合成摘要为 `128 t/s | 首字 820 毫秒` 与 `128 tokens`，不是一次实际模型测速。

### 编辑弹窗

- 标题栏包含标题、类型/模型摘要、启用开关、关闭按钮。
- 连接/模型/高级分组可见；高级字段通过滚动可达；原字段绑定与完整测试卡保留。
- 滚动容器滚至约 scrollTop=672 时底部状态与取消/保存并测试/保存仍固定可见。
- 实际发送 Tab 按键检查焦点留在弹窗内；Esc 关闭并将焦点归还到触发编辑按钮。初始焦点沿用首个可用输入。
- 未成功保存，未替换 draft 所属或保存处理函数。

### 控制台与环境边界

本次查看控制台，存在 Wails `Browser Environment Detected` 提示、桥接资源 404、`GetState` / `LoadUserConfig` / `GetModelAdapterTestResults` / `FetchModelAdapterModels` 等失败，以及主题桥接失败伴随 rejected promise。它们属于无 Wails 后端的预览环境现象，本次未据此扩项修改桥接或异常机制；**不声称控制台全绿**。合成状态仅用于可见布局与交互检查，不替代真实桌面链路。

## 4. 截图索引

全部位于 `frontend/.ui-refresh-evidence/batch4-after/`：

- 总览：`light-home-stopped.png`、`light-home-running.png`、`dark-home-stopped.png`、`dark-home-running.png`。
- 公开模型：`light-public-models.png`、`dark-public-models.png`。
- 模型列表：`light-model-list.png`、`dark-model-list.png`。
- 模型卡片：`light-model-cards.png`、`dark-model-cards.png`。
- 编辑器：`light-model-editor.png`、`light-model-editor-advanced.png`、`dark-model-editor.png`。
- 响应式（主控复核补充）：`responsive-1024-public-models.png`、`responsive-1024-model-list.png`、`responsive-760-public-models.png`、`responsive-760-model-list.png`、`responsive-760-public-models-long-name.png`。

## 5. 缺口、回退与交接

- **test/env gap**：未验证真实 Wails 成功启停、成功保存/重载、实际模型测试、桌面 WebView 与其他平台；不冒充真实链路完成。
- **test gap**：inline 失败/运行中/过期态保留实现与结构断言，本批未分别截图或逐个交互；未覆盖全部窄屏断点及所有模型类型字段组合。工单要求的未测试/通过/停用样本已覆盖。
- **待独立复核**：主控核对 diff、命令与截图，判断约定密度下的尺寸差异是否接受；执行 pane 不自行 accepted。
- 回退：仅撤销本批对应差异与新增测试，不使用 reset/checkout，不覆盖其他批次或主控差异；无迁移。
- 预览进程 `byavsauyo` 已停止；未重启/替换用户应用，未 commit/push。

## 6. 主控复核后修复（2026-11-18）

### 6.1 测试卡 inline 两处缺陷

- 失败态：`inlineText` 改为仅当 `rawResponseText` 非空时显示「失败 · 查看详情」，否则显示「失败」，不再承诺不存在的详情入口。
- 需重测态：`dotClass` 增加 stale 分支，使用 `--color-warning-text`，与 stale 文案的 warning 文字色一致，不再出现绿点配黄字。
- 先补两条结构断言并实跑 RED（`仅在有原始返回时承诺查看详情` 失败），实现后复绿。
- 覆盖边界：两条修复以源码结构与既有 `rawResponseText`/`stale` 计算逻辑断言验证，未在浏览器单独渲染失败无详情、stale 两种状态（test gap）；完整模式（非 inline）行为未改。

### 6.2 响应式核对（1024 / 760 宽度）

浏览器独立上下文，合成三行公开模型（默认名、自定义名 `sol-fast`、未公开）与三个模型，浅色主题；尺寸取自元素几何测量：

- **1024×860**：公开模型行宽 706px，三列为 40 / 422 / 220，行高 48px，无横向溢出；模型列表行高 54–55px，`overflow=false`，四个行内操作单行显示。
- **760×1100**：公开模型行宽 678px，三列为 40 / 394 / 220，行高 48px，无横向溢出、名称列未被压缩到 0；模型列表进入既有窄屏规则（隐藏提供商/测试状态/端点列），行高 66–67px，名称与测试/编辑/复制/删除同排显示，页面 `scrollWidth=760`，无整页横向滚动。
- 长名称压力：将默认名替换为 92 字符名称后，名称列 `scrollWidth=611 > clientWidth=394` 正常省略号截断，`title` 保留完整名称，行与页面均无溢出；输入框占位符同样截断。
- 结论：三列公开模型行与四按钮操作区在 1024 / 760 下均不溢出，名称被截断而非压没，**未做响应式修正**。截图见上索引；状态为合成注入，仍不代表 Wails 真实链路。

## 7. 用户真机反馈后的展示调整（2026-11-18）

### 已确认范围与实现

- 总览配色：用户选择「启动深绿、停止保持红字」，沿用 primary `#047857` 与 btn-risk，不改实现。
- 编辑器测试信息：用户选择单行状态。使用 `inline inline-surface`，取消大号总耗时/输出 Token 指标块；保留摘要、Token 数、原始返回 Tooltip；stale 改为与历史摘要并存的「需重测」标记，不再覆盖摘要。
- 接入中心：Anthropic 原生 disabled、降低透明度、禁止选择；移除「计划中」徽标及底部占位说明。脚本 guard 通过 `clients.value.find` 读取 computed，浏览器实际点击 Cursor 成功切换到 `#/access?client=cursor`。

### 验证与证据

- `node scripts/test-ui-density.mjs`、`node scripts/test-config-projection.mjs`、`node scripts/test-ui-feedback.mjs` 通过；`npm run build` 通过（3.44s，保留 Browserslist、chunk 体积及 localstorage-file 警告）；`git diff --check` 通过。
- 独立预览内注入合成 success + stale 测试结果：摘要与「需重测」同时显示，状态行实测 30px，无弹窗横向溢出；悬停信息图标能看到合成原始返回。浅深截图均已查看。未运行真实模型测试或保存。
- Anthropic 浅深主题截图已查看；禁用态不跳转，支持项 Cursor 可切换。
- 截图位于 `frontend/.ui-refresh-evidence/batch5/`：`light-editor-inline-test.png`、`dark-editor-inline-test.png`、`light-access-anthropic-disabled.png`、`dark-access-anthropic-disabled.png`。batch5 仅为截图目录名，仍归本交付任务，不新增任务体系。

### 24 小时请求列表：尚未定位，不能判为正常

- 只读核查：请求列表来自 `history/requests/requests-YYYYMMDD.jsonl`，24h 是滚动最近 24 小时；7d 范围更大，因此仅当最近 24 小时确无符合筛选的请求时，24h 空而 7d 有数据才正常。模型编辑器测速不属于聊天请求索引路径。
- 检查时本机 `~/.cursor-local-assistant-v2/history/requests/` 不存在；这只能证明该路径当时没有索引，**不能解释用户看到的 7d 列表数据，也不能据此认定用户反馈已解决**。usage.json 聚合与请求索引是不同数据源，不能相互借证据。
- 当前无已确认根因，未改后端、查询范围或真实数据。仍需运行新版本产生真实聊天请求后，对照实际请求时间、筛选与索引确认（data/env gap）。
- 先前构建的 `bin/macos-arm64.dmg` 早于本节改动，本节未重新构建 DMG；未替换/重启用户应用、未 commit/push、未修改 execution.md。交付仍为 verified-partial，待主控及桌面验收。

### 此前主控复核修复的复跑记录

`node scripts/test-ui-density.mjs`、`node scripts/test-config-projection.mjs`、`npm run build`（3.50s，保留既有 chunk 体积警告）及 `git diff --check` 全部通过。预览进程已停止；未 commit/push；未修改 `execution.md`。
