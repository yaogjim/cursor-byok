# 第 2 批执行证据

- 执行：`ui-executor`，2026-11-17；工单 `../work-orders/batch-2.md`，方案 §6。
- 回报：**BATCH 2: BLOCKED**（rev1：已合入主控复核提出的 Safari 13 兼容修复）；delivery_status=`verified-partial`。本地代码与下列检查通过，但真实成功保存、桌面导入路径验收未闭合，不能标记整批 PASS。完整配置导入按钮缺失经主控判定为 N/A。
- 未 commit/push；未新增依赖；未修改后端、路由、保存作用域、接口契约或真实应用安装。未进入第 3 批。
- 截图/原始输出统一在 `frontend/.ui-refresh-evidence/batch2-baseline/` 与 `batch2-after/`（以下以这两个目录简称）。截图为 1440×900，中文；预览地址 `http://127.0.0.1:5199/`，CDP 9223。

## 1. 实施顺序与变更

### 2.1 token 与对比度

先创建 `frontend/scripts/check-contrast.mjs` 并记录原状态失败（`batch2-baseline/contrast.txt`），再改 token 与 Button primary。

- 浅色 muted=`#5b6675`；浅色 faint 最终为 **`#69707d`**，不是建议值 `#6b7280`：后者在 `surface-muted=#f3f4f6` 上只有 **4.393:1**。微调后最低值 **4.528:1**，仍比 muted 浅，未放宽断言或改变背景。
- 深色 muted=`#9a9a9a`、faint=`#919191`。
- 两主题 strong=`#047857`、strong-hover=`#065f46`；Button primary 只用这两个填充/边框 token。强调色、`.text-on-primary` 白字不变。
- 脚本解析实际 CSS token，检查两主题 × 四种文字 × 四种底色及白字对 strong/hover，共 36 项。缺 token 也失败。
- 进入 2.2 前：脚本 exit 0；浏览器计算样式确认首页两个主按钮白字 + `rgb(4,120,87)` 背景。截图 `light-home-2.1.png`。主按钮审美取舍仍交主控/用户，不自行换色。

### 2.2 弹窗共享能力

新增 `src/composables/useDialogFocus.js`，由 Modal 与 ContentModal 共同使用。记录触发元素、初始聚焦、Esc、Tab 循环、关闭/卸载监听清理及焦点归还。模块级打开栈仅允许最上层打开实例处理键盘事件；隐藏实例不注册监听。

- Modal 增加 dialog/aria-modal/aria-label，初始聚焦首个可用按钮（取消优先，无取消则确认）；无可聚焦按钮时聚焦面板。
- ContentModal 保留首个输入字段优先、否则关闭按钮，以及 closeOnEscape/closeDisabled/遮罩既有规则；关闭按钮 hover 改用文字 token。
- 遮罩行为未改变。拖拽手柄局部深色底未动。
- 进入 2.3 前实际键盘自测：取消初始聚焦 → Shift+Tab 到确认 → Tab 回取消 → Esc → 焦点回到触发按钮。额外挂载两个 Modal（一开一关），Esc 回调记录仅 `["A"]`，关闭后卸载测试实例。
- 正式路由守卫复验：设置页改动「启动时隐藏主窗口」→ 用总览标签触发确认 → 初始焦点「留下」；Shift+Tab 为「离开」，Tab 回「留下」；Esc 后 URL 保持 `#/settings`，焦点回「总览」，可见 dialog 数 0。截图 `dark-dirty-modal.png`；实际背景 `rgb(41,41,41)`。

### 2.3 toast 与错误收敛

- useMessage 默认 info；success/info 默认 2400ms；error 默认 duration=0；显式 duration 优先。仍是单条替换，不新增队列。
- MessageProvider 有语义色边框/背景与左侧图标；success/info 为 status，error 为 alert/assertive；错误关闭按钮可点击、可 Tab 聚焦，pointer-events-auto。
- `useActionError.js` 导出统一 `showActionError`。默认空详情为「服务错误」。八处本地实现移除，useConfigTransfer 保持参数注入。
- MainLayout 保留原「操作失败」空错误详情兜底：在调用统一函数前传入原归一化结果。SubscriptionAuthPanel 保留 lastError 页面状态，在调用点先赋值后交给统一函数，未丢掉原页面错误显示。
- `ModelEditor` 的校验错误和 `result.error`、Cursor partial.error 均明确 error。中性提示未改文案/语气。
- 进入 2.4 前运行 `node scripts/test-ui-feedback.mjs` → `UI feedback tests passed`：覆盖默认 tone、两种 2400ms、error 无定时器、显式 900ms 覆盖、显式 0、清除、错误兜底与具体详情。测试使用真实模块与 Vue reactive，只替换时钟。
- 浏览器真实操作新增模型保存失败，alert 显示「模型 1 的访问密钥不能为空」及后续显示名称校验错误，数次工具操作后仍在；鼠标关闭成功。另一次纯键盘保存失败为「模型 1 的接口地址不能为空」，Esc 关编辑器后 Tab 到 toast 关闭按钮，Enter 关闭成功。
- **success 视觉和计时是组件级探针，不是真实保存成功证据**：通过当前 Home 实例注入的 message API 展示「刷新成功」；截图显式 duration=0 以稳定捕获。另用默认时长：400ms 时 status 存在，2900ms 时消失，精确 2400ms 由回归测试验证。

### 全部 message(...) 调用点分类

下表以函数/内容唯一定位，文件均在 `frontend/src/`。`useMessage.js` 中的导出函数定义不是调用点，不计入；所有 `showActionError(...)` 调用最终均经表末统一 error 展示。

| 文件 | 调用点 / 内容 | tone | 分类依据 |
|---|---|---|---|
| views/Home.vue | handleRefreshMetrics：刷新成功 | success | 刷新完成 |
| views/Home.vue | handleResetMetrics：统计已重置 | success | 重置完成 |
| views/Home.vue | handleLaunchCursor：已请求打开 Cursor | success | 发起操作完成，未夸大为 Cursor 已打开 |
| views/SettingsView.vue | handleSaveConfig：本页设置已保存 | success | 保存完成 |
| views/ModelConfig.vue | handleSaveConfig：本页配置已保存 | success | 保存完成 |
| views/ModelConfig.vue | LOGICAL_ROUTING_RUNTIME_VERIFY_HINT | info（默认） | 运行时验证提示 |
| views/ModelConfig.vue | testAllUnavailableText.value | info（默认） | 没有物理模型可测试的提示 |
| views/CursorView.vue | result.partial 的 result.error | error | 部分失败 |
| views/CursorView.vue | handleSavePage：本页配置已保存 | success | 保存完成 |
| layouts/MainLayout.vue | 检查更新中...，duration=0 | info（默认） | 进行中，保留手动移除 |
| components/ModelEditor.vue | singleCheck | error | 模型校验失败 |
| components/ModelEditor.vue | result.error | error | 保存失败 |
| components/ModelEditor.vue | handleSave 的运行时提示 | info（默认） | 逻辑路由需运行时验证 |
| components/ModelEditor.vue | handleTest 的运行时提示 | info（默认） | 同上，不是失败 |
| components/GatewayCard.vue | Base URL 已复制 | success | 复制完成 |
| components/GatewayCard.vue | Gateway token 已复制 | success | 复制完成 |
| components/GatewayCard.vue | Gateway token 已轮换并复制 | success | 轮换复制完成 |
| components/GatewayCard.vue | Gateway 可用 | success | 测试成功 |
| components/GatewayCard.vue | 请先保存本页 | info（默认） | 操作前置条件提示 |
| components/GatewayCard.vue | Gateway 配置已保存 | success | 保存完成 |
| components/SubscriptionAuthPanel.vue | 已导入 Codex 认证副本，未修改原始 auth.json | success | 导入完成 |
| components/SubscriptionAuthPanel.vue | 文件中没有可导入的 Codex/Grok OAuth 账号 | info（默认） | 空候选结果，非异常对象 |
| components/SubscriptionAuthPanel.vue | 已导入 N 个 Codex/Grok 账号 | success | 导入完成 |
| components/SubscriptionAuthPanel.vue | 已清除全部 Codex 认证副本 | success | 清理完成 |
| components/SubscriptionAuthPanel.vue | Codex/Grok 设备授权完成 | success | 授权成功 |
| components/SubscriptionAuthPanel.vue | 请在浏览器完成授权，用户码... | info（默认） | 等待用户操作 |
| composables/useConfigTransfer.js | 配置已导出到... | success | 导出完成 |
| composables/useConfigTransfer.js | 配置导入成功，共导入... | success | 导入完成 |
| composables/useConfigTransfer.js | 模型已合并到草稿...保存本页后才会写入配置 | success | 草稿合并完成，保留未落盘说明 |
| composables/useActionError.js | 标题：normalizedDetail | error | 所有统一错误入口 |

### 2.4 焦点环与禁用原因

- 用 `:root :is(button,a,[role="button"],input,select,textarea,[tabindex]):focus-visible` 替换四条局部重复规则；2px primary outline + 2px offset，优先级实际高于后加载 `.outline-none`。
- Button 增加 disabled/disabledReason，两个 variant 分支都保留原生 disabled，禁用且有原因时输出 title/aria-disabled。
- Cursor scope 脏且未运行：沿用「请先保存 Cursor 配置」。Gateway 未启用或 scope 脏：把原 title「需先启用并保存配置」迁入 disabledReason，移除旧 title 以免覆盖。busy 等无准确既有原因的状态不编造。
- **完整配置导入按钮缺失（主控判定 N/A）**：全量搜索确认只有 useConfigTransfer 内保留 handleImportConfig 定义/返回值，ModelConfig 只取 handleImportModelAdapters；无完整配置导入按钮调用点。主控已判定该工单验收项不适用，不恢复入口，也不把模型导入错误禁用为完整导入。
- AccessView 已有 button、广告卡既有 role/tabindex/Enter/Space 保持；未提前实现请求列表键盘展开。
- `global.css` prefers-reduced-motion 规则仍在，transition/animation 为 0.01ms !important，未修改。

## 1.1 主控复核追加：Safari 13 兼容性修复

主控独立复跑 build/projection/UI-feedback/contrast 全绿后指出：浏览器目标为 Safari ≥13，`Array.prototype.at` 不可用，而本批新增的 `useDialogFocus.js` 使用了 `.at(-1)`。

- 实际受影响为 **4 处**（主控所述「三处」按逻辑位置计；文本上为 3 处 `dialogs.at(-1)` + 1 处 `elements.at(-1)`）：打开栈顶层判断 2 处、卸载/归还时顶层判断 1 处、可聚焦元素取末项 1 处。
- 全部改为 `arr[arr.length - 1]`，未新增 polyfill 或依赖，未改动其它文件与行为。
- 修复后 `rg -n '\.at\(' src/composables/useDialogFocus.js src/components/ui/Modal.vue src/components/ui/ContentModal.vue` 无输出（exit 1）；`git diff --check` 无输出。

复跑（原始输出：`batch2-after/{build,projection,feedback,contrast}-after-at-fix.txt`，键盘记录 `keyboard-after-at-fix.json`）：

| 检查 | 结果 |
|---|---|
| `npm run build` | exit 0，`✓ 139 modules transformed`，`✓ built in 3.10s`（仅既有 warning） |
| `node scripts/test-config-projection.mjs` | exit 0：`config projection tests passed` |
| `node scripts/test-ui-feedback.mjs` | exit 0：`UI feedback tests passed` |
| `node scripts/check-contrast.mjs` | exit 0：36 项 PASS |
| `keyboard-check.mjs`（真实 Tab/Shift+Tab/Enter/Escape） | exit 0：全部记录点 focusVisible=true，保存失败 alert 出现，编辑器 Esc 后焦点归还「新增模型」，设置面板切换生效 |

补充的弹窗专项复验（真实按键）：

- ContentModal 初始聚焦接口地址输入框；从首个可聚焦元素「关闭」Shift+Tab 环绕到末项「保存」，再 Tab 回到「关闭」；Esc 关闭且焦点回到「新增模型」。
- 路由守卫 Modal：初始聚焦「留下」，Esc 保持 `#/settings`、焦点回「总览」、可见 dialog 数 0。
- 双实例挂载（一开一关）：Esc 仅记录 `["A"]`。
- 一次键盘脚本复跑在「新增模型」Enter 步骤未打开弹窗而中断；同页用真实按键重试可正常打开，随后完整脚本再次 exit 0。判定为脚本注入的瞬时问题，不是应用行为回归，未改产品代码。

## 2. 浏览器证据与覆盖边界

### 截图

- baseline：`light/dark-{home,access,models,settings}.png` 共 8 张；另 `light/dark-modal.png`、`light/dark-success-toast.png`、`light/dark-error-toast.png`，均在代码修改前采集。toast 基线是通过原 API 手动展示的视觉样本，不是业务成功证据。
- after：`light/dark-{home,access,models,settings}.png` 共 8 张；`light/dark-focus.png`；`light/dark-success-toast.png`；`light-error-toast.png`、`dark-error-modal.png`、`dark-save-error.png`、`dark-dirty-modal.png`、`dark-disabled-reason.png`。
- 已实际打开检查截图：基线 light-error-toast、after dark-success-toast、after light-focus，页面与控件可见，未以空白截图充数。
- `visual-probes.json` 记录八页面实际 token 与两主题 focus 计算样式。浅色 outline=`rgb(16,185,129) solid 2px`；深色=`rgb(16,173,93) solid 2px`；offset 均 2px；搜索输入确实带 `.outline-none` 且 matches(':focus-visible')=true。

### 纯键盘路径

`batch2-after/keyboard-check.mjs` 通过本机 CDP Input.dispatchKeyEvent 发送真实 Tab/Shift+Tab/Enter/Escape；初始只定位起点，后续遍历不调用 click/focus。最终原始记录在 `keyboard.json`：

1. 四个主标签 Tab 可达、Enter 切页，焦点环均可见。
2. 接入页客户端 button 可达、Enter 选择。
3. 模型页 Tab 到新增模型，Enter 打开，初始聚焦接口地址输入框，焦点环可见。
4. Tab 遍历到保存，Enter 触发空模型校验失败 alert。
5. Esc 关闭编辑器，焦点回新增模型按钮；Tab 到错误提示关闭按钮，Enter 关闭。
6. Tab 到设置标签并进入，Tab 到「会话与日志」，Enter 后可见内容包含「日志维护」，焦点不落空。

探针调试失败未当作产品失败：首次 CDP Enter 缺少字符事件，按钮未激活；补齐 `text='\r'` 后通过。最初面板观测用了不存在的「日志占用空间」文案，改为实际可见 innerText「日志维护」，最终 true。起点跨出文档的问题通过在文档内 Tab/Shift+Tab 定位修正；最终每个记录点 focusVisible 均 true。

### 禁用悬停与真实失败

- Gateway 未启用按钮实际 hover=true、disabled=true、title="需先启用并保存配置"、aria-disabled="true"；已截图。原生 title 文本与属性已核实，不把浏览器原生 tooltip 是否被截图捕获当作已证实。
- 设置页真实点击保存 → **保存失败：服务错误**（error alert），截图 `dark-save-error.png`。这证明失败接线，不证明成功链。
- 测试用设置草稿已通过反向切换恢复，未保存真实配置；未发送真实模型请求或读取真实凭据。
- 控制台存在 Wails browser preview 警告、API 404（SetAppearanceTheme/GetState/LoadUserConfig/GetHomeMetricsReport/GetAppVersion 等），不能声称控制台全绿。未启动/重启用户桌面服务来绕开权限边界。

## 3. 最终命令与原始输出

以下最后一次检查均退出 0；详尽原始文件也保留在 `batch2-after/`。`git diff --check` 无输出。

### npm run build

```text
> frontend@0.0.0 build
> node ./scripts/run-vite-build.mjs --scan --mode production

vite v7.3.1 building client environment for production...
(node:47024) Warning: `--localstorage-file` was provided without a valid path
(Use `node --trace-warnings ...` to show where the warning was created)
transforming...
Browserslist: browsers data (caniuse-lite) is 6 months old. Please run:
  npx update-browserslist-db@latest
  Why you should do it regularly: https://github.com/browserslist/update-db#readme
✓ 139 modules transformed.
rendering chunks...
computing gzip size...
dist/index.html                                0.38 kB │ gzip:   0.28 kB
dist/assets/HFKos-R-DhlAw8FH.ttf              30.26 kB
dist/assets/PingFang-Medium-BJ5FLqaG.ttf  10,968.36 kB
dist/assets/index-D0amN5VW.css                96.51 kB │ gzip:  22.26 kB
dist/assets/index-etK3xJC6.js              1,323.90 kB │ gzip: 340.76 kB

(!) Some chunks are larger than 500 kB after minification. Consider:
- Using dynamic import() to code-split the application
- Use build.rollupOptions.output.manualChunks to improve chunking: https://rollupjs.org/configuration-options/#output-manualchunks
- Adjust chunk size limit for this warning via build.chunkSizeWarningLimit.
✓ built in 3.17s
```

未因构建 warning 新增依赖、拆包或更新 Browserslist。build 自动更新 catalog/四语言包，未删除 i18n 运行时。

### node scripts/test-config-projection.mjs

```text
config projection tests passed
```

本批没有改动该测试文件；沿用主控已修复的「登录时启动」断言及第 1 批已有修改。

### node scripts/test-ui-feedback.mjs

```text
UI feedback tests passed
```

### grep -rn "function showActionError" src

```text
（无输出，匹配数 0；grep 无匹配退出码 1 属预期）
```

统一实现使用导出的 const 函数，不存在上述本地函数定义；八个组件/页面已 import 统一实现。

### node scripts/check-contrast.mjs

```text
PASS light text-faint / app-bg: 4.809:1 (#69707d / #fafbfc)
PASS light text-faint / surface: 4.983:1 (#69707d / #ffffff)
PASS light text-faint / surface-muted: 4.528:1 (#69707d / #f3f4f6)
PASS light text-faint / card-foot: 4.860:1 (#69707d / #fcfcfd)
PASS light text-muted / app-bg: 5.627:1 (#5b6675 / #fafbfc)
PASS light text-muted / surface: 5.830:1 (#5b6675 / #ffffff)
PASS light text-muted / surface-muted: 5.297:1 (#5b6675 / #f3f4f6)
PASS light text-muted / card-foot: 5.686:1 (#5b6675 / #fcfcfd)
PASS light text-secondary / app-bg: 7.294:1 (#4b5563 / #fafbfc)
PASS light text-secondary / surface: 7.557:1 (#4b5563 / #ffffff)
PASS light text-secondary / surface-muted: 6.867:1 (#4b5563 / #f3f4f6)
PASS light text-secondary / card-foot: 7.371:1 (#4b5563 / #fcfcfd)
PASS light text / app-bg: 16.098:1 (#1a1d2e / #fafbfc)
PASS light text / surface: 16.678:1 (#1a1d2e / #ffffff)
PASS light text / surface-muted: 15.155:1 (#1a1d2e / #f3f4f6)
PASS light text / card-foot: 16.267:1 (#1a1d2e / #fcfcfd)
PASS light white / primary-strong: 5.484:1 (#ffffff / #047857)
PASS light white / primary-strong-hover: 7.684:1 (#ffffff / #065f46)
PASS dark text-faint / app-bg: 5.578:1 (#919191 / #191919)
PASS dark text-faint / surface: 4.616:1 (#919191 / #292929)
PASS dark text-faint / surface-muted: 4.987:1 (#919191 / #232323)
PASS dark text-faint / card-foot: 4.987:1 (#919191 / #232323)
PASS dark text-muted / app-bg: 6.248:1 (#9a9a9a / #191919)
PASS dark text-muted / surface: 5.170:1 (#9a9a9a / #292929)
PASS dark text-muted / surface-muted: 5.585:1 (#9a9a9a / #232323)
PASS dark text-muted / card-foot: 5.585:1 (#9a9a9a / #232323)
PASS dark text-secondary / app-bg: 6.970:1 (#a3a3a3 / #191919)
PASS dark text-secondary / surface: 5.767:1 (#a3a3a3 / #292929)
PASS dark text-secondary / surface-muted: 6.231:1 (#a3a3a3 / #232323)
PASS dark text-secondary / card-foot: 6.231:1 (#a3a3a3 / #232323)
PASS dark text / app-bg: 16.411:1 (#f7f7f7 / #191919)
PASS dark text / surface: 13.580:1 (#f7f7f7 / #292929)
PASS dark text / surface-muted: 14.671:1 (#f7f7f7 / #232323)
PASS dark text / card-foot: 14.671:1 (#f7f7f7 / #232323)
PASS dark white / primary-strong: 5.484:1 (#ffffff / #047857)
PASS dark white / primary-strong-hover: 7.684:1 (#ffffff / #065f46)
对比度检查：PASS
```

## 4. 缺口与交回主控事项

| 分类 | 缺口 | 补齐标准 |
|---|---|---|
| env / test gap | 无 Wails 后端，真实保存返回服务错误；纯键盘覆盖保存失败，尚无真实成功保存 | 在获准的桌面验收环境实际保存 → success toast → 自动消失；复核成功保存后关编辑器/离页无错误脏点 |
| env / test gap | 桌面文件选择器及真实导入弹窗未走通 | 桌面环境打开导入流程，确认深色背景、Esc/Tab/关闭与原行为一致 |
| product-decision gap | 完整配置导入按钮缺失（工单 2.4 指定补禁用原因） | **N/A：主控已判定不适用，不恢复入口** |
| product-decision gap | 主按钮视觉人工确认 | **已关闭（2026-11-18）：用户确认保留当前深绿 `#047857`（白字 5.48:1、hover 7.68:1），不改配色** |

下一步仅需主控复核本批 diff/截图与上述验收边界，安排获准环境补成功保存/导入证据。不以组件探针或构建绿色替代真实业务链，不把本记录提升为 accepted。
