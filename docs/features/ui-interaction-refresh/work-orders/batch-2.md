# 第 2 批工单：视觉与反馈

- 状态：已下发
- 对应方案：`../plan.md` §6（含 §2 目标 2/4、§9 证据要求）
- 核心要求：正文/辅助文字/主按钮文字在浅深两主题均 ≥4.5:1；弹窗能力合并；toast 区分语气；关键路径键盘可用。

## 共同约束

- 不 commit / 不 push；不新增依赖；不改后端/Go、路由、保存作用域与接口契约。
- 不改中文文案语义（toast 文案本身可沿用现有句子）；不删除 i18n 运行时与语言包。
- 颜色只改 token 与使用 token 的类；兼容层之外的组件硬编码品牌色（`#10AD5D` 等）保持为强调色，不当作主按钮底色。
- 所有颜色结论必须由脚本测量，不接受肉眼判断。

## 任务

### 2.1 颜色 token 与对比度（D4）

文件：`frontend/src/style/global.css`、`components/ui/Button.vue`

浅色：
- `--color-text-faint`: `#9ca3af` → `#6b7280`
- `--color-text-muted`: `#6b7280` → `#5b6675`（保持层级：faint 比 muted 浅）
- `--color-text-secondary` 保持 `#4b5563`

深色：
- `--color-text-muted`: `#7b7b7b` → `#9a9a9a`
- `--color-text-faint`: `#6b6b6b` → `#919191`（深色 surface `#292929` 上 ≥4.5；低于 muted 保持层级）

主按钮：
- 新增 `--color-primary-strong: #047857` 与 `--color-primary-strong-hover: #065f46`（两主题同一取值）。
- `Button.vue` 的 `variant="primary"` 背景与边框改用 strong/strong-hover；`.text-on-primary` 保持白色。
- `--color-primary` / `--color-primary-hover` 继续作为强调色（图标、焦点环、进度等），不承载白字实心块。

新增测量脚本：`frontend/scripts/check-contrast.mjs`
- 从 `src/style/global.css` 解析 `:root` 与 `:root[data-theme="dark"]` 的 token 值，计算 WCAG 对比度。
- 检查项（逐项输出比值并断言 ≥4.5）：浅色 faint/muted/secondary/text 对 `--color-app-bg` 与 `--color-surface`；深色同上；白字对 `--color-primary-strong`；白字对 hover strong。（`--color-surface-muted`、`--color-card-foot` 也纳入文字底色检查，避免漏掉常驻区域。）
- 任一项 <4.5 时脚本 `exit 1`；脚本输出原样写入证据。

### 2.2 弹窗合并（D5）

文件：`components/ui/Modal.vue`、`components/ui/ContentModal.vue`、新增 `composables/useDialogFocus.js`（名称可等价，需说明）

- 把 ContentModal 现有能力抽到共享 composable：打开时记录触发元素、Esc 关闭、Tab 焦点困囚、关闭后焦点归还、清理监听。
- `Modal.vue` 接入：`role="dialog"`、`aria-modal="true"`、`:aria-label="title"`；Esc = 取消；Tab 在弹窗内循环；关闭后焦点回到触发元素；初始焦点放在取消按钮（无取消时放确认按钮），不要默认聚焦危险确认。
- `ContentModal` 保持现有初始聚焦策略（首个输入框，否则关闭按钮）与 `closeOnEscape` / `closeDisabled` / 遮罩行为不变。
- 遮罩点击行为保持现状（Modal 点遮罩 = 取消），不新增行为。
- 修掉 `ContentModal.vue` 关闭按钮 `hover:text-white`（浅色 `--color-surface-hover` 上不可读）→ `hover:text-[var(--color-text)]`。`ModelConfig.vue:679` 拖拽手柄的 `#333333` 深色底 + 白图标是组件局部样式，保持不动。

验收：

- 触发「离开页面放弃未保存修改」：Esc = 留下，焦点回到触发元素。
- 连续 Tab 不跑出弹窗；初始焦点在取消按钮。
- 深色下模型详情/导入弹窗背景为 `--color-surface`（截图）。
- 两个 Modal 实例同时挂载时，只有可见那个响应 Esc/Tab。

### 2.3 toast 分语气与 `showActionError` 收敛（D5）

文件：`composables/useMessage.js`、`components/ui/MessageProvider.vue`、新增 `composables/useActionError.js`、各调用点

- `message(content, options)` 增加 `tone: "success" | "error" | "info"`，默认 `info`。
- 默认时长：success 2400ms；info 2400ms；error 不自动消失（`duration: 0`），提供关闭按钮。显式 `duration` 优先。
- `MessageProvider` 按 tone 渲染：左侧图标 + 语义色边框/背景（用 `--color-success-* / --color-error-* / --color-info-*`）；success/info `role="status"`，error `role="alert"`；关闭按钮需要 `pointer-events-auto`，键盘可聚焦。
- 新增 `showActionError(title, detail)`：统一拼「标题：详情」，detail 为空时兜底「服务错误」，以 error 语气展示。
- 删除以下本地实现并改为统一 import：`GatewayCard.vue`、`SubscriptionAuthPanel.vue`、`CursorAccountCard.vue`、`MainLayout.vue`、`ModelConfig.vue`、`CursorView.vue`、`SettingsView.vue`、`Home.vue`；`useConfigTransfer.js` 的调用点改传统一实现（保留其参数注入形式即可）。
- 审计全部 `message(...)` 调用并给出分类表（证据中逐条列出）：动作已完成 → success；失败/错误对象 → error（或走 showActionError）；中性提示/进行中 → info。
- 现有 `message(result.error)` 这类错误路径不得继续使用默认 info；`请先保存本页`、`检查更新中...`、运行时提示等保持 info。

验收：

- 制造保存失败（如模型 baseURL 非法）→ 红色、带图标、`role="alert"`、不自动消失、可点击/键盘关闭。
- 保存成功 → 绿色、2.4s 后消失。
- `grep -rn "function showActionError" frontend/src` 输出为 0。
- 所有原调用点成功/失败语义与证据分类表一致。

### 2.4 无障碍关键路径（D7）

文件：`style/global.css`、`components/ui/Button.vue`、`views/AccessView.vue`、`components/RequestListPanel.vue`（本批只做现有可达元素）

- 全局焦点环：统一 `:root :focus-visible`（或等价更高优先级选择器）覆盖 `button`、`a`、`[role="button"]`、`input`、`select`、`textarea`、`[tabindex]`；2px 描边 + 2px offset，颜色 `--color-primary`。删除现有 4 条局部重复规则（`.app-tab`/`.access-client`/`.seg button`/`.app-icon-btn`），保留确有必要且不可被全局覆盖的特例（写明理由）。
- 注意 `.outline-none` 工具类由 tailwind.css 后加载；统一规则必须实际生效（用浏览器验证，不是只看源码）。
- `Button.vue` 增加 `disabledReason` prop：禁用且有原因时输出 `title` 与 `aria-disabled="true"`；对确有原因的调用点补传（至少覆盖：服务运行中不能导入完整配置、Gateway 未启用/配置未保存不能启动、scope 脏时不能启动等现有禁用原因）。无原因的禁用不强行编造文案。
- 可点元素键盘可达：AccessView 客户端项已是 `button`，确认 Tab 可达且焦点可见；广告卡保持现有 `role/tabindex/Enter/Space`；请求列表行键盘展开归第 3 批，本批不提前实现。
- 确认 `prefers-reduced-motion` 规则仍生效。

验收（必须在浏览器中实际操作）：

- 纯键盘走：切四个标签 → 接入页选客户端 → 打开模型编辑 → 保存 → 关闭 → 打开设置 → 切换面板；每一步焦点可见、不落空。
- 禁用按钮悬浮显示原因（至少验证一个新增 `disabledReason` 的点）。
- 弹窗 Esc/Tab/焦点归还按 2.2 验收。
- 焦点环在浅/深两主题下都可见（截图）。

## 证据与回报

- `cd frontend && npm run build`、`node scripts/test-config-projection.mjs`、`node scripts/check-contrast.mjs`（新脚本）实际输出。
- 浅/深 × 四个页面截图；主按钮、成功 toast、错误 toast、弹窗、焦点环各至少一张。
- 键盘流程与 Esc/Tab 的操作记录（包含实际观察结果，不只是预期）。
- `grep -rn "function showActionError" frontend/src` = 0。
- 报告写入 `evidence/batch-2.md`；pane 内回报 `BATCH 2: PASS|BLOCKED` + 摘要 + 缺口。
- 主按钮 `#047857` 若你认为视觉不可接受，先按方案完成并截图，由主控交给用户决定，不要自行换色。
