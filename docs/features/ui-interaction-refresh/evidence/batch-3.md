# 第 3 批：请求列表交互执行证据

- 日期：2026-11-18
- 执行：ui-executor；主控已独立复核通过（2026-11-18），本批记录主控结论。
- 回报：PASS。实现与自测通过；主控独立复跑 test-request-list / test-config-projection，核对 diff、截图与 Home 调用方无缺陷。真实桌面链路按用户「暂缓真实验证」处理，不作为本批缺口。
- 依据：work-orders/batch-3.md、plan.md §7 已确认边界；仅本页排序，不改变后端分页契约。

## 改动文件

| 文件 | 改动 |
|---|---|
| frontend/src/components/RequestListPanel.vue | 时间自适应；五列原生按钮三态排序及 aria-sort；本页排序提示；筛选重置/翻页保持；行与展开按钮、Enter/Space、详情区 |
| frontend/src/utils/requestList.js | 时间格式、稳定且非变异排序、缺失值末尾、行身份及固定详情字段 |
| frontend/scripts/test-request-list.mjs | 无新增依赖的逻辑回归与 Vite SSR 真实 SFC 渲染测试 |
| task/todo.md | 本批进度与证据索引 |

未改 Home/appState/后端/路由/保存作用域；未新增依赖，未 commit/push，未操作真实保存、导入、启停或重启用户应用。

## TDD 与命令

1. 先写 request-list 测试，模块不存在时 ERR_MODULE_NOT_FOUND（RED）；实现后通过。
2. 收口补空日期排序用例，RED：null 被 new Date(null) 当作 1970 年，升序得到 empty/old/new/bad；修正空值判断后 GREEN。
3. 无 ID 行的兜底身份使用源数组索引，而非排序后索引，避免排序使展开状态串行。
4. SSR 服务器使用 configFile:false，仅加载 Vue 插件，避免项目 i18n 插件注入 Wails 浏览器运行时；这不等于验证生产插件或 Wails 链路。

最终实际执行（frontend/）：

```text
node scripts/test-request-list.mjs
request list logic tests passed
request list render tests passed

npm run build
✓ built in 3.11s

node scripts/test-config-projection.mjs
config projection tests passed
```

三项退出码均为 0，原始输出：frontend/.ui-refresh-evidence/batch3-after/{request-list,build,projection}.txt。

逻辑覆盖：today/24h/7d/30d、补零/空/非法日期、三态/跨列、稳定排序、0、缺失/非法值两方向末尾、源数组不变、身份兜底、详情缺失与 0。SSR 覆盖真实 SFC 渲染、提示、五个 aria-sort、展开按钮、时间格式、props 数组不变。SSR 不覆盖事件交互；筛选与翻页状态用浏览器合成状态检查补充，不冒充自动化交互回归。

## 浏览器证据

环境：Vite 127.0.0.1:5199，Chrome CDP 9223，1440×900。仅注入无敏感合成记录，不调用真实业务服务。无 Wails 后端时 404 与“服务错误”属于预览环境限制。

已执行：

- today 显示 08:15:09；30d 显示 10-24 07:01、11-05 09:02，能区分日期。
- 鼠标点耗时：升序→降序→无，核对 0、120、900、2400、5200、缺失值及相同值顺序；恢复服务端原序。跨列输入从升序起，原耗时列恢复 none。
- 鼠标点击行/展开按钮，多行可分别展开；详情行 colspan=12，错误分类 upstream_error、状态码 502、状态 failed、req-1002、请求模型、上游模型、缓存读取 12 与写入 0 均显示；aria-expanded/aria-controls 对应。
- 聚焦 demo-request-g 行后真实 Enter：expanded=true、详情存在、焦点仍在行；真实 Space 后 expanded=false。
- 合成多页 props 更新：page 1→2 且替换当页数组，耗时 ascending 保留，展开从 1→0。此项不是真实分页 API 或下一页按钮端到端验收（主控判定真实分页与本批代码无交互，按用户暂缓，不计缺口）。
- 分别变更 range/provider/model/status 合成状态：五个 aria-sort 全部 none，展开全部清空。
- 两主题截图保存并查看：light-today.png、light-30d.png、light-sort-asc.png、light-expanded.png、dark-expanded.png，目录 frontend/.ui-refresh-evidence/batch3-after/。截图为临时证据，不声称已入库。

后续构建后预览切回趋势/错误态；此时再尝试排序按钮键盘操作，目标按钮已不在页面，未取得该项运行证据，不记录为执行 pane 已独立验证。主控复核判定：排序按钮为原生 button，沿用既有全局 `:focus-visible` 焦点环，键盘可激活且 `aria-sort` 已由 SSR 渲染测试覆盖，整页平台键盘回归已通过，该项不列为本批缺口。未擅自重启或关闭用户应用/登录标签。

## grep 与边界

实际运行：

```text
rg -n '\.at\(|v-html|Array\.prototype' src/components/RequestListPanel.vue src/utils/requestList.js
退出码 1（无匹配）
```

未复跑前批全部测试，仅执行本批工单要求的 build/projection/request-list。

## 缺口与剩余覆盖边界

- data gap：现有请求记录无错误全文，按用户确认降级，不伪造；本批不新增后端字段。
- 排序按钮键盘：不作为本批缺口（依据见上节主控判定）。执行 pane 未单独取得该按钮的浏览器键盘激活证据，不冒充已验证。
- 分页组合：真实分页依赖 Wails 后端，与本批纯本地排序计算无交互，按用户「暂缓真实验证」处理，不作为本批缺口；现有证据仍为合成 page/result 变化，不代表真实分页链路通过。
- 剩余覆盖边界：SSR 只验证结构、提示与初始渲染，不覆盖事件交互；筛选重置与翻页保持的浏览器证据来自合成 appState 状态变更，无持久自动化交互回归；合成预览不能证明 Wails 真实请求链路。
- env gap：真实桌面验证按用户要求暂缓，不得用合成预览证明真实业务链路通过。
- 当前状态：accepted（本批范围）。主控已独立复跑 test-request-list / test-config-projection，核对 diff、截图与 Home 调用方，未发现本批实现缺陷。
- 回退仅撤销本批工作区差异，不使用 git reset/checkout。

## 主控独立复核（2026-11-18）

- 实际执行 `node scripts/test-request-list.mjs`、`node scripts/test-config-projection.mjs`、`npm run build`（exit 0，仅既有 chunk 体积提示）。
- 复核 `RequestListPanel.vue` 与 `utils/requestList.js`：三态排序仅对 props 副本排序、缺失值两方向末尾、同值稳定；翻页 watch 清空展开、四类筛选 watch 重置排序与展开；渲染层 `displayRows` 按源索引回填行身份，展开详情使用固定字段且 `break-words` 完整换行；键盘展开忽略来自子按钮的事件冒泡。
- 复核 Home 调用方：`range-change` 仅 `RequestListPanel` 消费，筛选由面板自身 watch 重置，无重复触发；本批未改 Home 第 3 批相关逻辑。
- 审阅 `light-today / light-30d / light-sort-asc / light-expanded / dark-expanded` 截图：时间格式、排序箭头、本页排序提示、详情两列布局与两主题可读性符合预期。
- 未独立重做浏览器交互；交互证据沿用执行 pane 的合成数据浏览器记录。

## 用户真机反馈后的筛选工具条排版（2026-11-18，批次交付后补充）

用户提供新版真机截图，要求优化请求明细红框区域的布局与字体。仅调整 `RequestListPanel.vue` 渲染与局部样式，未改筛选、排序、分页、投影或后端语义。

### 改动

- 筛选控件统一放进 `request-filter-bar`：时间段与状态分段按钮、两个下拉同高 32px、同字号 12px；分段按钮内高 26px、圆角 7px，下拉圆角 8px，消除原先 11px 粗体分段文字与 14px 下拉文字混排、控件高度不一致的问题。
- 摘要与排序说明合并为一行 `request-meta`：摘要 12px，数值改用 `request-meta-value`（等宽数字、600 字重、正文色），不再是整行 11px 弱化文本；排序说明移到该行右侧 `request-meta-hint`（11.5px、faint 色），删除独立第三行，减少一段垂直空白。
- 摘要从字符串数组改为带 `prefix/suffix` 的对象，保证数字与单位样式分离；文案不变。

### TDD 与命令

- 先在 `scripts/test-request-list.mjs` 增加结构断言（工具条、统一下拉类、元信息行、数值强调、排序说明位置、旧独立行已移除），实跑 RED（`class="request-filter-bar"` 不匹配）。
- 实现后 `node scripts/test-request-list.mjs` PASS。另复跑 `test-ui-density`、`test-config-projection`、`test-ui-feedback`、`check-contrast`（全部 36 项 PASS）与 `npm run build`（3.46s，exit 0，仅既有 chunk 体积提示），`git diff --check` 无输出。

### 浏览器核对（合成状态）

- 1280 宽浅色与深色：工具条整体 32px 高，分段与下拉顶边对齐；摘要与右侧排序说明同一行（18px 高）；表格与分页不受影响。
- 1024 与 760 宽：工具条不换行、无横向溢出（`bar w=958/674,h=32`，页面 `scrollWidth==clientWidth`），摘要与说明仍同行，说明文字未被截断。
- 截图（浅色）：`frontend/.ui-refresh-evidence/batch5/light-request-toolbar.png`、`light-request-toolbar-1024.png`、`light-request-toolbar-760.png`；深色：`dark-request-toolbar.png`。
- 请求数据与计数为合成注入（276 条、30 天），页面顶部「加载请求列表失败」提示是无 Wails 后端的预览现象，不代表真实链路；未做真实请求查询。

### 状态与缺口

- 本次补充为 **verified-partial**：结构、排版与响应式已用预览证据核对，真实 Wails 请求列表未复测，不把预览当作真实链路验收。
- 回退仅撤销 `RequestListPanel.vue`、`global.css` 与本测试对应的本次差异；未 commit/push，未修改 execution.md。

### 24 小时范围仍待确认

- 本次只改展示层，未改 `internal/requestlog` 的范围窗口或数据。用户真机已能在 30 天范围看到数据，说明索引链路在真实运行中可用；但先前的「24 小时无、7 天有」现象仍无根因结论，需在真实请求时间与筛选条件下继续对照，不因本次排版改动标记为已解决。

## 请求模型显示名称（2026-11-18，用户真机反馈）

### 问题与事实

- 真机请求列表「请求模型」列显示 `eeb727d5b08b1fb8` 这类编号。只读核对确认：索引 `request_model` 记录的是 `stream.ModelID`，即 Cursor 下发的模型编号；转发层另有 `stream.ModelName`（来自 `ModelDetails.DisplayName`），本地 `history/*/context.json` 中同一编号对应 `"model_name": "glm5.3"`，但此前未写入索引、也未经桥接暴露到界面。
- 旧索引记录只有编号，无法回填名称；新记录才带名称。

### 实现（用户选择「名称优先，悬停看编号」）

- `internal/requestlog`：`Record` 新增可选 `request_model_name` 字段（JSON omitempty，向后兼容）；归一化去空白；`Distinct` 的模型候选同时包含名称；`matchesModel` 同时匹配实际模型、请求编号与名称。
- `internal/backend/forwarder/request_index.go`：写入 `RequestModelName = stream.ModelName`。
- `internal/bridge`：`RequestListRow` 新增 `requestModelName` 并透传。
- 前端：`requestModelLabel` 优先名称、缺失回落编号；列表列显示名称、`title` 保留原始编号；展开详情新增「请求模型名称」，保留「原始请求模型」；`appState` 归一化新增字段。

### TDD 与验证

- Go：新增 `TestRequestModelNameRoundTripAndFilter`（先编译 RED），`TestListRequestsCarriesRequestModelName`；`go test ./internal/requestlog ./internal/bridge` 通过；`go build ./...` exit 0；`go vet` 无输出（仅既有 macOS 链接版本 warning）。
- 前端：先在 `test-request-list` 增加 `requestModelLabel`、详情字段与 SSR 渲染断言（RED：缺少导出），实现后转绿；`test-ui-density`、`test-config-projection`、`test-ui-feedback`、`npm run build`（exit 0）与 `git diff --check` 均通过。
- 浏览器（合成状态）：名称行显示 `glm5.3`、`title=eeb727d5b08b1fb8`；无名称行回落编号；浅/深主题截图已查看。截图 `frontend/.ui-refresh-evidence/batch5/light-request-model-name.png`、`dark-request-model-name.png`。

### 状态与缺口

- **verified-partial**：字段链路已实现并有单元/SSR/预览证据，但未在真实桌面链路验证新请求写入名称；需重新构建应用、产生新请求后确认「请求模型」显示名称（env gap）。旧记录保持编号是已确认行为，不是缺陷。
- 本次涉及后端索引结构与桥接字段（新增可选字段，不改既有字段语义）；未改范围窗口与筛选交互，未 commit/push，未修改 execution.md。
