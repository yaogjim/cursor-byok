# 第 0 批工单：清理（零视觉差异）

- 状态：已下发
- 对应方案：`../plan.md` §4
- 核心要求：**行为与观感零变化**。本批只删除死代码/残留视图，并把已被全局 `!important` 覆盖的硬编码色类替换为等价 token 类。

## 约束

- 不 commit / 不 push。
- 不新增依赖，不改后端、路由、i18n 运行时与语料生成逻辑。
- 只替换 `global.css` 兼容层（约 194–270 行）中列出的选择器所对应的类；**兼容层之外的硬编码色不要动**（例如 `#10AD5D`、`#10d06f`、`#454545`、`#333333` 等），否则会产生视觉差异。
- 删代码前先确认无引用、且相关断言有等价覆盖。

## 任务

### 0.1 删除死代码 `components/ModelAdapterModal.vue`

- 代码引用为 0，仅 `src/i18n/generated/catalog.json`（生成物）有记录。
- 删除该文件。
- 同步处理 `frontend/scripts/test-config-projection.mjs`：
  - `:1364` 附近读取该文件的变量；
  - `:1714` 附近「ModelAdapterModal exposes native media generation…」断言；
  - 这两项与 `ModelEditor` 的等价断言重复（`ModelEditor exposes native media generation…`、`both editors explain…`）。删除前确认 `ModelEditor` 处断言仍保留，并在证据中写明承接关系。
- `catalog.json` 由 `static-i18n-plugin` 在 build/dev 时重新生成；跑一次 build 后确认无 `ModelAdapterModal` 残留。

### 0.2 删除无路由入口的 `views/Config.vue`

- `/config` 已 redirect 到 `/settings`，该视图无入口。
- 删除该文件。
- 同步处理 `frontend/scripts/test-config-projection.mjs` `:2033–2044` 对该文件的读取与断言（GatewayCard、diagnosticDegraded/quotaBlocked、appLogDegraded 等）。
  - 先逐项确认等价行为已由 `Home.vue` / `GatewayCard.vue` / `SettingsView.vue` 承接并在证据中写出映射；
  - 若无承接，停手上报，不静默删掉检查。
- build 后确认 `catalog.json` 无 `views/Config.vue` 引用。

### 0.3 下沉 Theme compatibility layer

文件：`frontend/src/style/global.css` 兼容层 + 实际使用这些类的组件。
当前使用任意 hex 类的组件只有 3 个：`components/ui/ContentModal.vue`、`components/ui/Combobox.vue`、`views/ModelConfig.vue`。

替换映射（仅针对兼容层中出现的类）：

| 原类 | 替换为 |
|---|---|
| `bg-[#292929]` `bg-[#242424]` `bg-[#232323]` `bg-[#202020]` `bg-[#1c1c1c]` | `bg-[var(--color-surface)]` |
| `bg-[#272727]` `bg-[#252525]` `bg-[#1f1f1f]` | `bg-[var(--color-surface-muted)]` |
| `hover:bg-[#333]` `hover:bg-[#303030]` `hover:bg-[#2f2f2f]` `hover:bg-[#2a2a2a]` `hover:bg-[#1f1f1f]` | `hover:bg-[var(--color-surface-hover)]` |
| `text-white` `text-[#e5e5e5]` `text-[#d4d4d4]` `text-[#cfcfcf]` | `text-[var(--color-text)]` |
| `text-[#a3a3a3]` `text-[#9d9d9d]` `text-[#8f8f8f]` `text-[#8A8A8A]` | `text-[var(--color-text-secondary)]` |
| `text-[#7f7f7f]` `text-[#7b7b7b]` `text-[#777]` `text-[#737373]` `text-[#727272]` `text-[#6f6f6f]` `text-[#666]` | `text-[var(--color-text-muted)]` |
| `hover:text-[#ddd]` `hover:text-[#e5e5e5]` | `hover:text-[var(--color-text)]` |
| `border-[#242424]` `border-[#343434]` `border-[#3a3a3a]` `border-[#3b3b3b]` `border-[#3f3f3f]` | `border-[var(--color-border)]` |
| `hover:border-[#4a4a4a]` `hover:border-[#4c4c4c]` | `hover:border-[var(--color-border-strong)]` |
| `border-[#14532d]` | `border-[var(--color-success-border)]` |
| `bg-[#102418]` | `bg-[var(--color-success-bg)]` |
| `text-[#86efac]` | `text-[var(--color-success-text)]` |
| `border-[#5a4314]` `border-[#6b5b1e]` `border-[#8a6d1a]` | `border-[var(--color-warning-border)]` |
| `bg-[#2f2612]` `bg-[#2c2612]` | `bg-[var(--color-warning-bg)]` |
| `text-[#fcd34d]` `text-[#f6d77a]` | `text-[var(--color-warning-text)]` |
| `border-[#164e63]` | `border-[var(--color-info-border)]` |
| `bg-[#0b2530]` | `bg-[var(--color-info-bg)]` |
| `text-[#67e8f9]` | `text-[var(--color-info-text)]` |
| `border-[#4b1d1d]` | `border-[var(--color-error-border)]` |
| `bg-[#2a1313]` | `bg-[var(--color-error-bg)]` |
| `text-[#fca5a5]` | `text-[var(--color-error-text)]` |

- 替换完成后，从 `global.css` 删除兼容层规则；**保留 `.text-on-primary`**（第 2 批会改其取值）。
- 注意：兼容层里存在但组件未使用的选择器，直接删除规则即可。
- 不要为了「更干净」去改兼容层之外的硬编码色。

## 验收与证据

必须实际执行并记录：

1. `cd frontend && npm run build` 成功（无 error）。
2. `cd frontend && node scripts/test-config-projection.mjs` 成功。
3. 残留校验（全部为 0 输出）：
   - `grep -rn "ModelAdapterModal" frontend/src frontend/scripts`
   - `grep -rn "views/Config.vue\|views/Config\b" frontend/src frontend/scripts`
   - `grep -nE '^\s*\.(bg|border|text|ring)-\\\[\#' frontend/src/style/global.css`
   - 对兼容层原始选择器逐个反查：每个被删除的原类名在 `frontend/src` 的 `.vue`/`.js` 中应无引用（写出你实际使用的命令与结果）。
4. 浏览器视觉对比（零视觉差异是本批硬要求）：
   - 改前基线：在动手前用浏览器预览抓取 4 个路由 × 浅色/深色截图；
   - 改后：同样条件再抓一遍；
   - 逐张对比，颜色、文字、边框无可见差异；
   - 记录实际覆盖的路由/主题，以及因无数据而无法呈现的组件（例如未打开的弹窗/下拉框），不要声称已覆盖。
5. 生成物核对：build 后 `src/i18n/generated/catalog.json` 不再引用被删文件。

## 回报格式（pane 内）

```
BATCH 0: PASS|BLOCKED
改动：<文件列表>
验证：build=..., projection=..., grep=...
视觉：<覆盖范围 + 结论/缺口>
缺口/风险：<无则写无>
证据：docs/features/ui-interaction-refresh/evidence/batch-0.md
```
