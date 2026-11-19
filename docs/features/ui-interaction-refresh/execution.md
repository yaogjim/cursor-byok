# UI 交互改造执行编排

- 状态：执行中
- 更新：2026-11-17
- 方案真源：本目录 `plan.md`（用户已确认执行）
- 用户补充决策：原「应用显示名 Cursor 助手 → Cursor Plus」一项**已撤销**，本次不做任何改名。
- 用户补充决策（2026-11-18）：**撤销 D2「总览只读」**，总览恢复卡内启停并压缩高度；追加批次 4 做公开模型行、模型列表/卡片、编辑弹窗的密度打磨（建议稿已确认）。

## 1. 角色与流程

| 角色 | 位置 | 责任 |
|---|---|---|
| 主控 | 本 pane（`w2:p1`） | 拆批、下发工单、独立复跑验收、记录状态、用户决策升级 |
| 执行 | Herdr pane 内 `ui-executor` | 按工单实施、自测、留证、回报 |

每批流程：

1. 主控下发该批工单（`work-orders/batch-N.md`）。
2. 执行完成实现与自测，把证据写入 `evidence/batch-N.md`，并在 pane 内回报 `PASS` / `BLOCKED` + 摘要 + 缺口。
3. 主控独立复核：读 diff、复跑构建/测试、执行 grep 与浏览器视觉检查。
4. 主控确认后，才下发下一批；发现范围、语义或用户决策问题立即停手升级。

## 2. 共同纪律（每批适用）

1. **不 commit / 不 push**，不修改 git 历史；直接改工作区。
2. 只做当前工单；范围外问题只记录上报，不顺手修。
3. 不新增依赖；不改 Go/后端、路由结构、i18n 语料与运行时、`build/config.yml`、发布配置。
4. 不降低既有测试覆盖。删除死代码牵动的断言，必须先确认等价行为仍有覆盖，再删除；找不到承接时停手上报。
5. 每批证据必须包含：改动文件清单、实际命令与原始输出摘要、grep 校验、未覆盖路径/缺口。
6. 构建与契约测试统一在 `frontend/` 执行：
   - `npm run build`
   - `node scripts/test-config-projection.mjs`
7. 视觉检查使用浏览器预览（仅 UI，后端调用 404 属预期）：
   - 启动：`cd frontend && npm run dev -- --port 5199 --host 127.0.0.1`
   - 本机已有无头 Chrome，CDP 地址 `http://127.0.0.1:9223`；可用 chrome-devtools MCP。
   - 中文界面：`localStorage.setItem("cursor-client:locale:v1","zh-CN")` 与 `localStorage.setItem("cursor-client:locale-source:v1","manual")` 后刷新。
   - 深色：`document.documentElement.dataset.theme = "dark"`；浅色设为 `light`。
   - 路由：`#/`（总览）、`#/access`（接入）、`#/models`（模型）、`#/settings`（设置）。
8. 证据文字记录放 `evidence/batch-N.md`；截图可放 `frontend/.ui-refresh-evidence/`（临时目录，不入库、不在证据中声称已提交）。
9. 遇到改变业务语义、数据模型、权限、接口契约、用户可见措辞或需要选型的事项：**停手上报**，不自行决定。
10. 未实际运行的检查不得写「已验证」；只覆盖部分路径时写「已验证部分路径 + 未覆盖清单」。

## 3. 批次队列

| 批次 | 主题 | 状态 | 依赖 | 备注 |
|---|---|---|---|---|
| 0 | 清理：死代码、残留视图、硬编码色下沉 | accepted（2026-11-17） | — | 含主控单独立项修复投影测试既有 6 条失败（用户决策） |
| 1 | 职责重整：总览只读、模型单一写盘、设置共用保存栏、语言选择器 | verified-partial（2026-11-18 复核修正） | 0 | 预览内交互与代码路径均已核实；plan §5 要求的真实写盘、真实启停/测试连接未执行（用户暂缓），原记 accepted 口径过宽；rev2 用户决策已实施 |
| 2 | 视觉与反馈：颜色 token、弹窗合并、toast 分语气、无障碍 | verified-partial | 0 | 主控复跑构建/投影/反馈/36项对比度全绿；Safari 13 的 Array.at 回归已修复。主按钮配色经用户确认保留；真实成功保存/桌面导入待用户手动验收 |
| 3 | 请求列表：排序、自适应时间、行展开 | verified-partial（2026-11-18） | — | 用户确认本页排序（翻页保持、筛选重置）与行详情降级；主控独立复核通过；错误全文为已接受 data gap，真实桌面验证按用户要求暂缓 |
| 4 | 总览启停回归与界面密度打磨 | verified-partial（2026-11-18） | — | 用户确认建议稿；主控复核发现两处小缺陷（inline 失败态虚假详情入口、stale 圆点配色），已由执行 pane 修复并经主控复验；真实启停/保存待用户桌面验收 |

## 4. 证据索引

| 批次 | 证据记录 | 主控复核结论 |
|---|---|---|
| 0 | `evidence/batch-0.md` | 通过：build/grep/截图逐字节一致/计算样式同源；投影测试经断言修复后全绿。缺口：Modal/Combobox 未实页截图、悬停态未截图（test gap） |
| 1 | `evidence/batch-1.md` | 通过实现复核（含 rev2）：build/投影测试绿；diff 无范围外改动；预览内入口跳转、脏态、面板切换、语言选择器副作用均已实测；payload 构造路径未变。**状态按 plan §9 修正为 verified-partial**：真实写盘、真实启停与「测试连接」未执行；模型编辑器弹窗/脏态截图、登录时启动行视觉未覆盖（test/env gap）；语言包变动为插件既有生成策略。 |
| 2 | `evidence/batch-2.md` | verified-partial：独立复跑 build/projection/feedback/contrast 通过；审阅两主题首页截图与消息分类。主控额外浏览器弹窗探针超时，未取得独立运行证据（采用执行 pane 已留存真实按键证据，不冒充独立验证）。完整配置导入按钮不存在判定 N/A；不恢复入口 |
| 3 | `evidence/batch-3.md` | verified-partial（主控复核）：复跑 `test-request-list`/`test-config-projection` 通过；核对 `RequestListPanel.vue` 与新增 `utils/requestList.js` diff、Home 调用方（`range-change` 仅此面板消费，重置 watch 不会重复触发）、两主题截图。三态排序仅本页并明示、翻页保持、四类筛选重置、展开状态清除、错误分类降级展示、原生按钮键盘与 aria-sort 均符合工单。缺口：错误全文 data gap（用户已接受）；真实 Wails 请求链路与真实分页按用户暂缓（env/test gap）；排序逻辑与渲染回归为脚本测试，交互路径为执行 pane 浏览器合成数据验证，主控未独立重做浏览器交互。 |
| 4 | `evidence/batch-4.md` | verified-partial（主控复核）：复跑 `npm run build`、`test-config-projection`、`test-ui-feedback`、`test-request-list`、`test-ui-density`、`check-contrast`（36 项 PASS）、`git diff --check` 全部通过；逐段审阅 Home/GatewayCard/ModelConfig/ModelEditor/ContentModal/ModelAdapterTestCard diff：启停接回既有处理函数与 dirty 门槛、公开模型三列且自定义名/默认名语义经真实投影函数测试、列表 ⋯ 浮层已删无悬空引用、卡片 inline 状态保留原始返回 Tooltip、编辑器字段仅重排未增删绑定、`ContentModal` 共享焦点能力未回归。截图浅/深主题各态符合建议稿；服务卡约 140px、模型卡约 171px 为近似目标。缺口：真实启停/保存/模型调用未验证（env gap，用户手动验收）；密度断言多为源码结构检查，非组件交互测试。复核修复：inline 失败态仅在存在原始返回时提示「查看详情」，stale 圆点改 warning 色（先补断言 RED 后 GREEN，主控复跑密度/投影/build 全绿）；1024/760 宽度实测公开模型三列与四按钮操作区无横向溢出、长名称省略并保留 title，未做响应式改动。窄屏仅验证这两处页面，其它页面窄屏未覆盖。 |

## 5. 第 3 批前置核对结论（主控只读取证，2026-11-17）

- 后端 `MetricsService.ListRequests` / `requestlog.Query` **没有排序参数**，固定按 `At` 倒序并在服务端分页；因此列头排序只能作用于当前页。按 plan §7/§11，需用户确认接受「本页排序」并在 UI 明示，或另立后端排序范围。
- `RequestListRow` 现有字段：`at/requestId/conversationId/modelCallId/requestModel/provider/model/inputTokens/outputTokens/cacheReadTokens/cacheWriteTokens/cacheHitRate/ttftMs/durationMs/statusCode/errorCategory/status`。**没有错误信息全文**；`requestlog.Record` 也未存储错误文本。按非目标「不新增后端字段」，行展开只能展示错误分类、状态码/状态、请求 ID、原始请求模型、上游模型、缓存读写明细；「错误信息全文」需用户决定是否接受降级或另立后端范围。
- 时间/输入/输出/耗时/首字耗时字段齐全，可在当前页做三态排序。
- 用户已确认（2026-11-18）：仅本页排序并明示，翻页保持、筛选/时间范围重置；接受上述现有字段的行详情降级，错误全文保留 data gap。边界已同步 plan §7，按 batch-3 工单实施；真实桌面验证按用户要求暂缓。

## 6. 对照 plan 的完成情况复核（2026-11-18，主控）

结论：D1–D10 的产品改动已全部落到工作区，四批实现无缺失项；按 plan §9「缺一不得声明该批完成」的严格口径，仅第 0 批满足 accepted，第 1–3 批为 verified-partial（真实桌面写盘/启停/导入按用户要求暂缓）。

| plan 锚点 | 结论 | 主要依据 |
|---|---|---|
| D1/§8.3 导航与路由 | 满足 | `router/index.js` 未改；四栏结构未动 |
| D2/§5.1 总览只读 | 满足（预览） | Home 启停按钮、处理函数与 import 已删；双入口经用户确认；grep 无残留 |
| D3/§5.2 模型单一写盘 | 部分 | 底部按钮按脏态呈现；`appState.js` 未改，`savedScope=models` 清脏点路径已核对；真实写盘/拖拽/导入未执行 |
| D4/§6.1 配色 | 满足 | 兼容层删除；36 项对比度 PASS（最低 4.528:1）；`.text-on-primary` 保持白字 |
| D5/§6.2–6.3 反馈与弹窗 | 部分 | 共享 `useDialogFocus` 接入两个弹窗；toast tone/错误常驻/关闭；本地 `showActionError` 清零；深色 ContentModal（模型详情/导入）未实页截图 |
| D6/§7 请求列表 | 满足（降级范围） | 本页五列三态排序、自适应时间、行详情/键盘展开；错误全文按用户决策为 data gap |
| D7/§6.4 无障碍 | 满足（预览） | 全局焦点环唯一规则、`disabledReason` 2 个调用点、弹窗 Esc/困囚/归还、纯键盘主流程实测 |
| D8/§5.3 设置页 | 部分 | 单一保存栏、文案统一、payload 构造路径未变；真实 `SaveSystemSettings` 未执行 |
| D9/§5.4 多语言 | 满足 | 选择器禁用+「计划中」，页脚入口按用户决策移除，i18n 运行时与语料保留 |
| §8 跨批约束 | 满足 | 无后端/依赖/路由/保存作用域改动；无 token 投影变更 |

### 复核发现（按严重度）

1. **状态口径**：第 1 批原记 accepted 缺少 plan §5.1/§5.2/§5.3 要求的真实写盘与启停验证，已修正为 verified-partial。
2. **测试缺口**：深色 ContentModal（模型详情/导入弹窗）未实页截图，仅有计算样式探针；模型编辑器弹窗、模型页脏态、登录时启动行未截图；请求列表排序/筛选无持久自动化交互测试。
3. **环境缺口**：真实保存成功 toast、桌面导入、启停与「测试连接」、真实分页链路均未执行（用户暂缓）。
4. **产品决策已闭合**：主按钮深绿配色（`#047857`）经用户确认保留（2026-11-18）；真实链路由用户手动验收。
5. **范围外既有问题（只报告）**：`CursorAccountCard.vue:44` 使用 `Array.prototype.at`，与 Safari 13 目标不一致，HEAD 已存在，非本次引入；`ModelConfig.vue`、`Combobox.vue`、`ContentModal.vue` 仍有 8 处硬编码 hex 类，均不在被删兼容层映射范围内，无本批回归。
6. **小偏差**：总览按钮文案为「接入页启动」，plan 写作「去接入页启动」；行 `aria-controls` 指向的详情节点在折叠时不存在（可访问性小瑕疵）。

### 建议下一步

真实桌面验收（保存成功/导入/启停/分页）由用户手动执行并回报；在结果到达前，第 1–3 批保持 verified-partial，不提升为 accepted，不 commit/push。
