# 批次 4 工单：总览启停回归与界面密度打磨

- 授权：用户 2026-11-18 确认「全部按建议稿执行」；本批撤销 plan D2「总览只读」，改为卡内启停 + 紧凑布局。
- 建议稿（视觉真源）：`frontend/.ui-refresh-evidence/mockups/home.png`、`models.png`、`editor.png`；HTML 源同目录。
- 依据：`../plan.md` §0.2、`../execution.md` 共同纪律。
- 范围：`frontend/` 前端布局与样式；不改字段、校验、保存作用域、路由、i18n 运行时与后端。

## 1. 总览（views/Home.vue）

1. 恢复卡内启停，逻辑从 `git show HEAD:frontend/src/views/Home.vue` 取回并接回当前文件：
   - 重新引入 `startService/stopService/startGateway/stopGateway` 与对应处理函数、禁用计算（`cursorStartDisabled`、`gatewayStartDisabled`）。
   - Cursor：运行中显示「停止服务」（`btn-risk`，busy 时「关闭中...」）；未运行显示「启动服务」（primary，busy 时「启动中...」）。
   - Gateway：运行中显示「停止 Gateway」；未运行显示「启动 Gateway」。
   - 保留「打开 Cursor」「配置 → / 去配置 →」。
   - 启停行为、scope dirty 门槛与改造前一致（§10.14 第 589 行）；提示统一走 `message(..., { tone })` / `showActionError`，不新增文案语义。
2. 禁用原因：删除 Gateway 常驻说明行与「未配置启用」，改为 `Button` 的 `disabledReason`：
   - Cursor 未保存且未运行：`请先保存 Cursor 配置`；
   - Gateway 未启用或未保存：`需先启用并保存配置`。
   - 保留错误行和 dirty 警告行（仅在触发时出现）。
3. 文案精炼（用户已批准）：
   - Cursor 副标题改为「本地代理与 Backend」；
   - Gateway 副标题改为「本机 HTTP 入口 · 外部客户端」；
   - 删除「需先在接入页把 Gateway 配置为启用并保存」。
4. 紧凑布局：只作用于总览服务卡片（不要改全局 `.ui-card-body` 默认值）。目标两张卡等高、高度约 148px；body 内边距与 gap 收紧，chips 行保持单行不换行。

## 2. 接入中心 · 公开模型（components/GatewayCard.vue）

1. 行布局改为 `开关 | 名称 | 对外名称输入` 三列（窄屏可改为两行），行高目标约 50px，保留分隔线。
2. 名称列显示 `displayName || adapterID`，超长截断并保留 `title`；`hasCustom` 时在名称下显示 11px 的「对外：{effectiveID}」，默认行不再显示第二行。
3. 输入框：
   - `hasCustom` 为 true 时显示 `publicID`；
   - 为 false 时值显示为空，placeholder 为 `默认：{publicID}`；
   - 继续使用 `setPublicModelID`；清空输入应回到默认名，不产生空覆盖（用现有 `buildGatewayPublicModelOverrides` 语义验证）。
   - 未公开或保存中保持禁用。
4. 区段标题：`{enabledModelCount} 个已启用 · 默认按模型名称公开；留空使用默认名，填写后覆盖对外名称`（用户已批准的新文案），删除每行「对外名称 ...」辅助行。
5. 不改 Token、监听地址、保存栏与任何保存字段。

## 3. 模型管理 · 列表（views/ModelConfig.vue + style/global.css）

1. 删除行尾 `<details class="model-more-menu">` 及 `.model-more-menu*` 样式（若确认无其他引用），改为行内操作：测试、编辑（描边按钮）+ 复制（文字）+ 删除（文字、红色）。
2. 行高目标约 58px：收紧 `.model-row` 上下内边距；列对齐保持现有五列结构，测试状态不换行；保留拖拽手柄、启用开关、禁用态与既有响应式规则。
3. 操作区禁止换行；删除入口继续走原确认流程。

## 4. 模型管理 · 卡片（views/ModelConfig.vue + components/ModelAdapterTestCard.vue）

1. 为 `ModelAdapterTestCard` 增加 `inline` 模式（默认 false，保持编辑器内现有完整卡不变）：单行渲染状态点 + 摘要（未测试/测试中/通过 + 延迟与 token/失败 + 查看详情），原始返回与错误详情通过现有 `Tooltip` 可查看，不丢信息。
2. 卡片：删除大块灰色测试区，改用 inline 状态行；收紧 body gap 与 padding；操作区固定在卡底、上方分隔线；同排卡片等高。目标高度约 165px。
3. 停用卡片保持降低不透明度；点击照常可用。保留测试/编辑/复制/删除原处理函数。

## 5. 编辑模型配置（components/ModelEditor.vue，必要时 ContentModal.vue）

1. 启用开关移出独立整行，放到弹窗标题栏右侧（可与关闭按钮同区）。实现方式自选：给 `ContentModal` 增加 `#header-actions` 插槽、或等价的低风险方案；不得改写 draft 状态归属、校验或保存逻辑。若确有实现障碍，可退为类型标签同一行的右侧紧凑开关，并在证据中说明偏差。
2. 字段按「连接 / 模型 / 高级」分组，使用轻分隔（无边框盒子）：标签 12px、组内两列、字段间距约 10px。允许组内重排（接口端点归连接、推理强度归模型），但不得删除、隐藏或改变任何字段的绑定。
3. 底部操作栏固定在滚动区之外：左侧状态文案，右侧「取消 / 保存并测试 / 保存」，主次关系与现有处理函数不变。
4. 保持 Esc、Tab 困囚、焦点归还与初始聚焦行为不回归。

## 6. 验证与证据

- 执行：`npm run build`、`node scripts/test-config-projection.mjs`；因共享 Button/ContentModal/CSS，复跑 `node scripts/test-ui-feedback.mjs`、`node scripts/test-request-list.mjs`、`node scripts/check-contrast.mjs`。
- 浏览器（预览 5199 + CDP 9223，合成无敏感数据）：
  - 总览两种状态（运行/未运行）的按钮与禁用原因，浅/深主题；
  - 公开模型默认名、自定义名、停用行三态；
  - 模型列表行内操作、无浮层裁切；
  - 卡片三态（未测试/通过/停用）等高；
  - 编辑弹窗分组、标题栏开关、底部固定栏、Esc/Tab。
  - 明确记录哪些状态来自合成注入，不代表 Wails 真实链路。
- 不新增依赖，不使用 Safari 13 不支持的 API；不 commit/push；不重启用户应用；范围外问题只报告。
- 证据写 `../evidence/batch-4.md`，截图放 `frontend/.ui-refresh-evidence/batch4-*`；更新 `task/todo.md` 本任务状态，标注待主控复核。
