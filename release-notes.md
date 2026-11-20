# cursor-byok v1.95.0

本版本是 v1.94.0 之后的界面重做基线:四批 UI 交互改造落地、请求列表增强、广告内容服务停用旧远端,并修复压缩摘要在上游以 `length` 结束 `response.incomplete` 时未能按输出触顶重试的问题。

## UI 交互改造(批次 0–4)

- 清理死代码:删除 `ModelAdapterModal.vue` 与残留的 `views/Config.vue`;`global.css` 的旧主题兼容层下沉为 token 类,零视觉差异。
- 颜色与对比度:重构全局颜色 token,新增 `scripts/check-contrast.mjs` 对 36 组前景/背景配对做对比度校验;主按钮 `#047857` 保持不变。
- 弹窗与键盘:新增 `composables/useDialogFocus.js`,多层弹窗仅最上层响应 Escape/Tab,焦点在面板内循环并在关闭后归还;`Modal`/`ContentModal`/`Combobox` 接入该能力。实现按 Safari 13 目标写,避免使用 `.at(-1)`。
- 反馈口径:新增 `composables/useActionError.js` 作为统一错误入口,toast 增加 `success`/`info`/`error` 语气;各视图移除各自的 `showActionError` 副本;禁用按钮通过 `disabled-reason` 说明原因。
- 总览:服务卡压缩高度、地址 chips 单行不换行;恢复卡内启停并保留 dirty 门槛;启动按钮按可用性切换 primary/default;Gateway 未启用提示收敛到 `disabled-reason`。
- 设置页:四个面板共用底部单一「保存设置」栏,移除各面板重复操作条;语言选择器改为禁用并提示「多语言界面将在后续版本提供」,页脚选择器移除;「登录时启动」接真实 `setLaunchAtLogin`/`appState.loginItemStatus`;「广告内容」开关保留位置但置灰不可点。
- 使用分析请求列表:五列三态排序、自适应时间、行详情与 Enter/Space 展开;翻页保持排序并清空展开,四类筛选可重置;排序、详情、时间格式化抽到 `utils/requestList.js`;筛选工具条与摘要排版收紧(控件统一 32px/12px),1024/760 宽度无横向溢出。
- 模型页:公开模型三列、模型列表行内四操作(测试/编辑/复制/删除,替代旧「更多操作」弹层);`ModelAdapterTestCard` 新增 inline 形态与 stale「需重测」标记;编辑弹窗标题栏开关、字段分组与固定底栏,测速展示压为单行;Anthropic 订阅入口禁用并移除「计划中」占位。
- i18n:按静态扫描同步 `catalog.json` 与 en-US/ja-JP/ru-RU/zh-CN 文案,移除随界面调整下线的旧条目。
- 前端测试:新增 `test-request-list.mjs`、`test-ui-density.mjs`、`test-ui-feedback.mjs`、`check-contrast.mjs`;`test-config-projection.mjs` 改为校验新版设置页、总览登录项与「外壳不再渲染语言选择器」。

## 请求模型显示名称

- 请求索引记录新增可选 `request_model_name`,模型筛选与 `Distinct` 同时匹配实际模型、原始请求模型与显示名称。
- 列表优先显示名称、悬停看原始编号,行详情两者都保留;旧记录回落显示编号。

## 广告内容服务停用旧远端

- 清空硬编码广告位并删除旧远端地址常量;未配置广告位时 `LoadAsset` 直接返回空,旧缓存不再出现在界面。
- 新增回归:默认无远端位、无位刷新不拉取、未配置位不出资源。设计文档见 `docs/features/ad-content-service/design.md`。

## 压缩摘要输出触顶识别

- Responses 规范以 `incomplete_details.reason=max_output_tokens` 表示输出额度耗尽,但部分聚合网关沿用 chat 风格的 `length`;此前只识别前者,导致摘要未按输出触顶扩大额度重试一次。
- 现在 `incomplete` 终态下 `max_output_tokens` 与 `length` 均按输出触顶处理,其余终态语义不变;新增真实 HTTP 适配器回归,断言两次请求的输出预算分别为 4096 与 8192。

## 文档

- 新增 `docs/features/ui-redesign/`(B · Paper 方案的设计分析、实施计划、四页亮/暗定稿、配色对照与预览截图索引),作为后续界面重设计的真源;本次只入库文档,不含生产代码。

## 明确限制

- 本批界面改动以浏览器预览与静态断言为证据;真实 Wails 桌面链路下的成功保存/导入、真实启停与桌面 WebView 渲染仍属未验收范围。
- 请求列表的「24 小时范围为空而 7 天有数据」现象未定位根因,保留为数据缺口,未改数据层。
- 新增的请求模型显示名称需新构建产生真实请求后才能验证写入。
- 桌面四项设置项(登录时启动、关闭窗口后继续运行、Dock 图标、清空全部日志)的真机验收沿用 v1.94.0 记录的缺口,未因本次发布改变。
- UI 重设计稿为设计资料,尚未进入编码落地。

## 验证边界

- `go build ./...`、`go test ./internal/ads ./internal/requestlog ./internal/bridge` 与 `go test ./internal/backend/forwarder`(含新增压缩回归)通过;`go vet`、`gofmt -l`、`git diff --check` 通过。
- `frontend` 侧 `test-config-projection`、`test-request-list`、`test-ui-density`、`test-ui-feedback`、`check-contrast` 全部通过,`npm run build` 成功。
- macOS、Windows、Linux 安装包由 tag 触发的远端 CI 构建并随 Release 发布,本地未重新构建 DMG。