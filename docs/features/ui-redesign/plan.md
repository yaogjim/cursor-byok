# UI 重设计 · 落地实施计划

状态：`planned`（Design 已定稿，本文件待用户确认后进入实施）
设计真源：[design-analysis.md](design-analysis.md) ｜ 视觉定稿：[finals/](finals/) ｜ 索引预览：[preview/index.html](preview/index.html)
适用规范：`docs/agents/project.md` 未接入，本计划按母版通用纪律执行（TDD、接线即交付、不主动提交）。

---

## 1. 目标与边界

### 1.1 目标结果

用户在真实桌面应用里看到与定稿一致的界面，并且能在设置页独立选择**外观**（跟随系统 / 亮色 / 暗色）和**配色**（宣纸·朱砂 / 暖白·墨绿 / 冷白·松针 / 中性·靛蓝），选择持久化、重启保持、原生窗口底色同步。

可观察结果（端到端链路）：

```text
设置页选择配色
  -> appState.appearancePalette
  -> document.documentElement[data-palette]
  -> global.css 令牌生效（四页同步换色，版式不变）
  -> 持久化 appearance.palette
  -> 重启后读取并恢复
  -> 原生窗口底色与纸色一致（无白闪）
```

### 1.2 本次范围

| 范围 | 内容 |
|------|------|
| 令牌体系 | 新语义令牌 + `--color-*` 兼容映射；亮暗 × 四配色共 8 组合 |
| 外观与配色 | `appearance.palette` 配置、归一化、持久化、设置页控件、原生窗口底色 |
| 外壳 | 顶栏、导航、页面边距、字体层级、响应式断点 |
| 四页 | 总览、接入、模型、设置的结构与视觉重排 |
| 模型页来源归组 | 按 `credentialSource`（codex / grok / static）归组与失效态 |
| 图表 | 五条序列改读令牌，亮暗重绘 |
| 测试 | 令牌/投影/对比度/密度测试同步更新，行为断言保留 |

### 1.3 明确不做（本次）

1. **订阅→模型自动同步**（定稿稿里的「自动同步 / 新增 2 / 无下线 / 立即检查更新」）——现有后端没有模型发现能力，见 §7 `SYNC` 独立立项。
2. 「本周用量」对 **Claude** 的展示——`internal/subscriptionauth/usage.go` 只有 codex/grok 解析器，Claude 入口保持「暂不支持」。
3. 多语言新文案的 en/ja/ru 人工翻译——沿用现有机制回退中文，翻译另批。
4. 广告位、请求列表、模型编辑器字段与校验逻辑，一律不动。
5. 任何配置默认值的语义变更（除新增 `appearance.palette`）。

---

## 2. 现状核实（事实锚点）

| 事实 | 锚点 | 对计划的影响 |
|------|------|------------|
| 现有主题只有 light/dark/system | `internal/appearance/theme.go`；`frontend/src/state/appState.js:1518` | 配色是新增维度，不能复用 theme 字段 |
| 外观已持久化 | `internal/backend/server/config/types.go:121`（`AppearanceConfig`）、`:292`（归一化）；`frontend/src/state/appState.js:1921` | 新增 palette 走同一条链路，改动面小 |
| 原生窗口底色按 theme 二选一 | `internal/bridge/window.go:199` | 必须改签名或新增按配色取值，否则切配色后窗口边缘露白 |
| 窗口 1100×720，最小 980×640 | `internal/app/runner.go:238-241` | 240px rail + 内容在 980 下偏紧，必须有断点 |
| 硬编码颜色集中在两处 | `frontend/src/style/global.css`（65 处）、`charts/HomeTrendChart.vue`（13 处） | 换肤以 global.css 为主战场，其余组件多数已用 token |
| 图表颜色不读主题 | `HomeTrendChart.vue:64-105` 五条序列写死 | 需要映射到 `--c-*`，否则暗色下刺眼 |
| 组件库已存在 | `components/ui/{Button,Switch,Select,Card,Modal}.vue` | 新控件（分段控件、配色样本）可基于既有 Button 语义实现，不引入依赖 |
| 模型有来源字段 | `config/types.go:88` `credentialSource: static\|codex\|grok` | 「按来源」归组可落地，无需新后端字段 |
| 订阅账号状态与用量可用 | `internal/subscriptionauth/{service,usage}.go` | 「已连接 / 凭据过期 / 本周用量」对 codex、grok 可真实取数 |
| i18n 自动抽取中文 | `frontend/plugins/static-i18n-plugin.js` | 新中文文案自动入 catalog；其他语言回退中文 |
| 现有测试大量依赖旧标记 | `frontend/scripts/test-ui-density.mjs`、`test-ui-feedback.mjs` | 重排会让这些断言变红，必须显式迁移，不得删除掩盖 |
| 对比度脚本只解析两套主题 | `frontend/scripts/check-contrast.mjs` | 需参数化为 8 组合 |

---

## 3. 令牌落地策略（先定，避免返工）

### 3.1 两层令牌

```css
/* 兼容层：旧变量指向新令牌，组件零改动即可换肤 */
:root {
  --paper: #f7f3ea; --paper-raised: #fbf8f1;
  --ink: #1b1610; --ink-2: #4e473c; --ink-3: #8a8272;
  --line: #e0d8c6; --line-soft: #e9e2d2;
  --accent: #9b3c2c; --accent-soft: #f3e4de;
  --warn: #8a5a12; --err: #8e2a1e; --err-text: #8e2a1e; --idle: #bdb49f;

  --color-app-bg: var(--paper);
  --color-surface: var(--paper-raised);
  --color-text: var(--ink);
  /* …其余 --color-* 逐项映射 */
}
```

好处：`P1` 一次提交即可全站换肤，`P4–P7` 再逐页把组件直接改用新令牌；任何一页没排完时，界面仍是完整可用的新皮肤。

### 3.2 选择器顺序

```css
:root, [data-palette="xuanshi"] { /* 亮色默认 */ }
[data-palette="nuanbai"] { … }         /* 同为 0,1,0，靠顺序覆盖 */
[data-theme="dark"] { /* 暗色基础与信号色 */ }
[data-theme="dark"][data-palette="nuanbai"] { … }  /* 0,2,0 最高 */
```

### 3.3 兼容性硬约束

- **禁止 `color-mix()`**：`browserslist` 声明 `Safari >= 13`，`color-mix` 需 Safari 16.2。定稿稿里 4 处 `color-mix`（开关轨道、按钮悬停边框、主按钮悬停、危险按钮边框）在生产里改为**每个配色显式给出** `--track`、`--btn-line-hover`、`--primary-hover`、`--danger-line`。
- 暗色 `color-scheme: dark` 保留，影响原生滚动条与表单控件。
- 图表 canvas 读值必须走 `getComputedStyle(document.documentElement).getPropertyValue('--c-*')`，并在 `data-theme`/`data-palette` 变化后重绘。

---

## 4. 阶段计划

每个阶段都按「目标 / 范围 / 验收 / 测试 / 文档 / 回退」六要素执行；阶段之间串行，阶段内任务可并行（仅限互不触碰同一文件的任务）。

### P0 · 冻结基线（0.5 人日）

- **目标**：拿到可对比的"改前"证据，避免"看起来没变"的争议。
- **范围**：四页 × 2 主题截图（1440×900、980×720），保存到 `docs/features/ui-redesign/evidence/before/`；记录当前测试基线结果。
- **验收**：截图齐全且能按页面对上；`npm run build`、`node scripts/test-config-projection.mjs`、`check-contrast.mjs`、`test-ui-density.mjs`、`test-ui-feedback.mjs`、`test-request-list.mjs` 全绿基线记录在案。
- **回退**：无代码改动。
- **产出**：`evidence/p0-baseline.md`（命令与结果原文，不复制到其他地方）。

### P1 · 令牌与配色地基（2 人日）

| ID | 任务 | 文件 | 验收 |
|----|------|------|------|
| P1-1 | 新增语义令牌与兼容映射；暗色与四套配色的 8 组合令牌 | `frontend/src/style/global.css` | 只改 CSS，不改模板；组件渲染与改前一致（除色值） |
| P1-2 | 删除 `color-mix` 依赖，改为每配色显式变量 | 同上 | `grep color-mix frontend/src` 为 0 |
| P1-3 | 图表五序列改读 `--c-*`，主题/配色变化后重绘 | `components/charts/HomeTrendChart.vue`、`CacheHitRateChart.vue` | 切换外观与配色后无需刷新即重绘，暗色可读 |
| P1-4 | 对比度脚本参数化为 8 组合，阈值 4.5:1（正文）；大字号 3:1 | `frontend/scripts/check-contrast.mjs` | 8 组合全部 PASS，失败时打印组合名与 token |
| P1-5 | 增加令牌存在性断言（防止某配色漏定义 `--line` 之类） | `check-contrast.mjs` 内或新脚本 | 缺 token 时报错并指出组合 |

- **测试**：`node scripts/check-contrast.mjs`；`npm run build`。
- **文档**：令牌表补进 `design-analysis.md` §5bis（已存在，补 `--track` 等新变量）。
- **回退**：单提交可整体 revert；兼容映射保证任意时点界面可用。

### P2 · 外观与配色设置端到端（1.5 人日）

| ID | 任务 | 文件 | 验收 |
|----|------|------|------|
| P2-1 | Go 配置新增 `appearance.palette`，白名单归一化，缺省 `xuanshi` | `internal/backend/server/config/types.go`（`AppearanceConfig`、`NormalizeConfig`）、`internal/appearance/palette.go`（新） | 旧配置无该键 → `xuanshi`；非法值 → 默认，不报错 |
| P2-2 | 原生窗口底色按配色取纸色 | `internal/bridge/window.go`、`internal/app/runner.go` | 亮暗 × 四配色下窗口底色与 `--paper` 一致 |
| P2-3 | 前端归一化与生效：`normalizePalette`、`resolveEffectivePalette`、`applyAppearancePalette` | `state/configProjection.js`、`state/appState.js` | `documentElement.dataset.palette` 与实际选择一致 |
| P2-4 | 持久化与读取贯通 | `state/appState.js`（`persistUserConfig`、配置载入路径） | 重启后保持；切页不丢失 |
| P2-5 | 设置页控件：外观分段 + 配色样本 | `views/SettingsView.vue`、`components/ui/SegmentedControl.vue`（新，无依赖） | 键盘可达、`aria-pressed` 正确、即时生效 |
| P2-6 | 测试 | `internal/backend/server/config/*_test.go`、`internal/appearance/*_test.go`、`scripts/test-config-projection.mjs` | 先 RED 后 GREEN；覆盖缺省、非法、往返持久化 |

- **验收证据**：真实运行实例中切换 8 组合各一次并截图；重启一次验证保持；导出配置检查 `appearance.palette`。
- **回退**：`palette` 是新增可选字段，移除读取逻辑即回到 light/dark 行为。

### P3 · 外壳与导航（1 人日）

| ID | 任务 | 文件 |
|----|------|------|
| P3-1 | 顶栏结构：品牌、主导航、右侧状态；导航下划线选中态 | `layouts/MainLayout.vue`、`style/global.css` |
| P3-2 | 页面边距与栅格：桌面 32px、窄窗 24px；内容最大宽 1040px 左对齐 | `style/global.css` |
| P3-3 | 响应式断点：≤1080px 收起徽标数字、rail 收窄；980px 不横向溢出 | 同上 |
| P3-4 | 排版层级：kicker / h1 / h2 / 数字字体 | 同上 |

- **验收**：1440、1100、980 三个宽度无横向滚动；导航键盘可达；徽标与脏标记不丢。
- **测试**：`test-ui-density.mjs` 中与导航/边距相关的断言迁移（保留行为断言，改标记断言）。
- **回退**：MainLayout 单独提交，可单独 revert。

### P4 · 设置页（1 人日）

- **范围**：200px 分区导航 + 单列文档 + 编号分组 + 全宽分隔线；外观/配色行接入 P2；其余行保持既有控件与文案语义。
- **验收**：四个分区可切换；每行「标签 / 说明 / 控件」对齐；禁用项（广告）保持禁用并给出原因；保存栏保留既有脏态行为。
- **测试**：沿用 `test-ui-feedback.mjs`；新增结构断言（编号分组、行对齐类名）；真实运行截图。
- **回退**：`SettingsView.vue` 单文件回退，不影响 P1/P2。

### P5 · 总览页（2 人日）

- **范围**：服务状态一行 + Hero 92.22% + 四列 KPI + 五线趋势图 + 250px 右栏（本周配额 / 最近请求 / 运行信息）。
- **必须保留的行为**（逐条验收）：卡内启动/停止、`disabledReason` 文案、脏态门槛、打开 Cursor、配置入口、重置统计、请求范围切换。
- **验收**：运行态/停止态各截图；数据为空时 KPI 显示一致（不出现 `NaN`）；图表在暗色下可读；右栏在 980 宽下折叠到内容下方。
- **测试**：`test-ui-density.mjs`（Home 段）与 `test-ui-feedback.mjs` 迁移；图表颜色断言改为读 token。
- **回退**：`Home.vue` + `HomeMetricsCard.vue` + `HomeAnalyticsPanel.vue` 三者同批提交。

### P6 · 模型页与来源归组（2 人日）

- **范围**：左侧来源 rail + 右侧按来源分组清单 + 「按来源 / 按厂商」切换 + 行内测试状态与开关。
- **来源映射（唯一真源）**：

| rail 项 | 判定 |
|--------|------|
| ChatGPT 订阅 | `credentialSource === "codex"` |
| xAI 订阅 | `credentialSource === "grok"` |
| API 密钥 | `credentialSource === "static"` |
| Claude 订阅 | 本次**不出现**（无来源）；接入页保留「暂不支持」占位 |

- **失效态**：来源账号全部失效 → 分组标题标「凭据已过期 · 去接入页重新登录」，行内标「不可用」，开关禁用但配置不删除（`Switch` 用 disabled，不写配置）。
- **验收**：数量一致（rail 计数 = 分组行数之和 = 模型总数）；无来源模型落入「API 密钥」；切换按厂商后分组正确；编辑器打开/保存行为不变。
- **测试**：`test-config-projection.mjs` 增加 `groupModelsBySource` 纯函数用例（先 RED）。
- **回退**：新增纯函数与视图层改动，可单独 revert；不改模型数据结构。

### P7 · 接入页（1.5 人日）

- **范围**：左 rail 两组（对外提供服务：共享入口、Cursor 接入；接入模型来源：ChatGPT 订阅、xAI 订阅、Claude 订阅〔暂不支持〕、API 密钥）；右栏按 01 账号 / 02 模型关联 / 03 已接入模型 分段。
- **行为保留**：Gateway 启停与公开模型编辑、Cursor 配置写入与运行状态、订阅登录/退出/导入、脏态提示与离开保护（`router/access.js` 不动）。
- **不做**：右栏「同步到模型页」开关与「新增/下线」计数——替换为「模型关联」只读说明 + 「到模型页查看」，避免承诺未实现能力。
- **验收**：六个 rail 项都可选中并切换右栏；订阅过期态与未支持态视觉可分；从模型页「去接入页」跳转落在正确项。
- **测试**：`test-ui-feedback.mjs` 相关断言迁移；真实运行截图（codex 已连接 / 过期两种）。
- **回退**：`AccessView.vue` + 右栏子组件同批提交。

### P8 · 收口（1 人日）

- 全量验证：`go build ./...`、相关包 `go test`、`npm run build`、五个脚本全绿。
- 真实运行验收：8 组合切换、四页巡检、重启保持、最小窗口无溢出、控制台无错误。
- 文档：`design-analysis.md` 状态、`task/todo.md` 状态、`docs/process.md` 证据索引。
- 证据等级：没有真实运行证据的部分保持 `verified-partial`，不写 `accepted`。

---

## 5. 任务与依赖

```text
P0 ─┬─> P1 ─┬─> P2 ─┬─> P4
    │       │       ├─> P6（依赖来源映射）
    │       │       └─> P7
    └───────┴─> P3 ──┴─> P5
```

- P1 是硬前置：没有令牌，后续每页都要返工。
- P2 与 P3 可并行（文件不重叠）。
- P5、P6、P7 彼此独立，可分三批交付；每批完成后即可用、可回退。
- `P6` 的来源映射纯函数先写测试再接 UI。

---

## 6. 追踪矩阵

| 链路环节 | 需求锚点 | 设计锚点 | 任务 | 验收证据 | 状态 |
|---------|---------|---------|------|---------|------|
| 全站换肤 | 定稿令牌 | design-analysis §5/§5bis | P1-1…P1-3 | 8 组合截图 + 对比度脚本 | planned |
| 配色可选可持久 | 用户 2026-11-18 追加 | §5bis 决策节 | P2-1…P2-6 | 重启保持实拍 + 配置导出 | planned |
| 外壳一致 | §7 决策表 | 定稿四页 | P3-1…P3-4 | 三宽度截图 | planned |
| 设置页可读 | 同上 | `finals/settings.html` | P4 | 四分区截图 | planned |
| 总览可决策 | 同上 | `finals/overview.html` | P5 | 运行/停止截图 + 图表暗色 | planned |
| 模型按来源定位 | 订阅与模型联动 | §7、六条规则 1/4/5 | P6 | rail 计数一致性 + 过期态截图 | planned |
| 接入分清方向 | 接入两组决策 | §7 接入行 | P7 | 六项切换截图 | planned |
| 订阅自动同步 | 六条规则 1/2 | — | **未排期** | — | out-of-scope |

---

## 7. 独立事项：订阅→模型自动同步（建议单独立项）

定稿稿里出现的「自动同步 / 本次发现新增 2 / 无下线 / 立即检查更新」在现有代码中**没有任何后端支撑**：`subscriptionauth` 只保存账号与用量，不提供"该账号可用的模型清单"接口。要落地需要：

1. 为 codex / grok 各定义一个"可用模型清单"读取路径（协议探针，只读）；
2. 定义差异合并规则（新增默认停用、下线保留、用户设置优先、来源失效不删除）；
3. 定义写入模型适配器的边界（是否自动建 adapter、凭据如何引用）。

这属于新能力，建议作为 `docs/features/subscription-model-sync/` 独立立项，先出只读探针结果再定实现。本次 UI 只呈现"已接入的模型"，不承诺自动同步。

---

## 8. 验证与门禁

| 层 | 命令/方式 | 通过标准 |
|----|----------|---------|
| Go | `go build ./...`；`go test ./internal/appearance/... ./internal/backend/server/config/... ./internal/bridge/...` | 全绿 |
| 前端构建 | `npm run build` | 成功，无新增告警 |
| 投影/纯函数 | `node scripts/test-config-projection.mjs` | 全绿（含 palette 用例） |
| 对比度 | `node scripts/check-contrast.mjs` | 8 组合全 PASS |
| 密度/反馈/请求列表 | `test-ui-density.mjs`、`test-ui-feedback.mjs`、`test-request-list.mjs` | 迁移后全绿，且**无断言被删除**（只允许改写为等价或更强断言） |
| 浏览器 | chrome-devtools：1440×900 与 980×720；亮暗 × 四配色 | 无横向滚动、无控制台报错、关键交互可达 |
| 真实运行 | 构建并安装后用真实应用巡检 | 重启保持、窗口底色一致、图表重绘 |

**证据纪律**：每阶段只在 `docs/process.md` 留一处证据；`task/todo.md` 记状态与缺口；跨对象不借证据（浏览器截图不能证明配置已持久化）。

---

## 9. 风险与回退

| 风险 | 触发信号 | 处理 |
|------|---------|------|
| 980px 下双栏拥挤 | 出现横向滚动或文字换行破版 | P3-3 断点收窄 rail；必要时把 MinWidth 提到 1040（需用户决定） |
| 旧测试断言大面积变红 | 迁移批次出现 >20 条失败 | 按"行为断言保留、标记断言改写"逐条处理，禁止批量删除 |
| 硬编码颜色漏改 | 暗色下出现浅色块 | P1 后用 `grep -E '#[0-9a-f]{6}'` 巡检；剩余项逐个替换 |
| 图表不重绘 | 切换后仍是旧色 | 在 `applyAppearanceTheme/Palette` 里触发 chart `update()`；加断言 |
| 原生窗口白闪 | 切配色或缩放窗口露白边 | P2-2 与前端纸色取同一组值 |
| Safari 13 兼容 | `color-mix`、`:has` 等新语法 | P1-2 清零；新增 CSS 只使用已验证语法 |
| 配色对比度不足 | 脚本 FAIL | 调 `--ink-3` / `--accent`，不改阈值 |

**整体回退**：按阶段提交，任意阶段可独立 revert；P1 的兼容映射保证回退到 P0 前状态时组件无需同步回退。

---

## 10. 需要用户确认的三项决策

| 编号 | 决策 | 建议 | 影响 |
|------|------|------|------|
| D1 | 模型页是否保留「自动同步 / 立即检查更新」入口 | **不保留**，改为「到接入页管理账号」 | 保留会承诺未实现能力；去掉不影响本期视觉 |
| D2 | 最小窗口宽度是否从 980 提到 1040 | 先保持 980，靠断点适配 | 提宽度影响老用户窗口体验；断点方案已验证可行 |
| D3 | 「Claude 订阅」在模型页是否显示占位分组 | **不显示**，只在接入页标「暂不支持」 | 模型页显示空分组会让人以为模型丢了 |

确认后按 P0 → P1 顺序开工；未确认前不进入编码。
