import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  buildGatewayPublicModelOverrides,
  listGatewayPublicModelRows,
} from "../src/state/configProjection.js";

const read = (relativePath) => readFileSync(new URL(`../src/${relativePath}`, import.meta.url), "utf8");

// ── 公开模型：清空输入回到默认名，不留下空覆盖 ──
{
  const adapters = [{ id: "ad-1", displayName: "gpt-x", enabled: true }];
  const stored = [{ id: "alias", targetAdapterID: "ad-1" }];
  const rows = listGatewayPublicModelRows(adapters, stored);
  assert.equal(rows[0].hasCustom, true, "自定义名称行标记 hasCustom");
  assert.equal(rows[0].effectiveID, "alias", "自定义名称行对外名称");
  const cleared = rows.map((row) => ({ ...row, publicID: "" }));
  assert.deepEqual(
    buildGatewayPublicModelOverrides(adapters, stored, cleared),
    [],
    "清空对外名称后回到默认名，不产生空覆盖",
  );
  const defaults = listGatewayPublicModelRows(adapters, []);
  assert.equal(defaults[0].hasCustom, false, "默认行不带自定义标记");
  assert.equal(defaults[0].publicID, "gpt-x", "默认行 publicID 为模型名称");
}

// ── 总览：卡内启停回归、禁用原因、文案精炼、紧凑布局 ──
const home = read("views/Home.vue");
for (const token of ["startService", "stopService", "startGateway", "stopGateway"]) {
  assert(home.includes(token), `Home 需接回 ${token}`);
}
for (const label of ["启动服务", "停止服务", "启动 Gateway", "停止 Gateway"]) {
  assert(home.includes(label), `Home 需保留「${label}」`);
}
assert(home.includes("请先保存 Cursor 配置"), "Cursor 启动禁用原因");
assert(home.includes("需先启用并保存配置"), "Gateway 启动禁用原因");
assert(home.includes("disabled-reason"), "禁用原因走 Button disabledReason");
assert(home.includes("打开 Cursor"), "保留打开 Cursor");
assert(home.includes("配置 →") && home.includes("去配置 →"), "保留配置入口");
assert(home.includes("home-service-card"), "总览服务卡片使用局部紧凑类");
assert(!home.includes("接入页启动"), "总览不再使用接入页启动按钮");
assert(!home.includes("深度集成"), "删除 Cursor 旧副标题");
assert(!home.includes("供外部 AI 客户端使用"), "删除 Gateway 旧副标题");
assert(!home.includes("需先在接入页把 Gateway 配置为启用并保存"), "删除 Gateway 常驻说明行");
assert(!home.includes("未配置启用"), "删除未配置启用提示");

// ── 公开模型行：三列、名称列、输入框占位与禁用 ──
const gatewayCard = read("components/GatewayCard.vue");
assert(gatewayCard.includes("public-model-row"), "公开模型行使用专用三列布局类");
assert(gatewayCard.includes("hasCustom"), "名称列区分默认名与自定义名");
assert(gatewayCard.includes("默认："), "默认名显示为占位");
assert(gatewayCard.includes("留空使用默认名"), "区段标题说明默认与覆盖规则");
assert(!gatewayCard.includes("对外名称 {{ row.effectiveID }}"), "删除旧的外名称辅助行");
assert(gatewayCard.includes(":title=\"row.displayName || row.adapterID\""), "名称超长保留 title");
assert(
  gatewayCard.includes(":disabled=\"appState.configSaving || !row.published\""),
  "未公开或保存中禁用输入框",
);

// ── 模型列表：移除浮层菜单，改为行内操作 ──
const modelConfig = read("views/ModelConfig.vue");
assert(!modelConfig.includes("model-more-menu"), "模型列表移除更多操作浮层");
assert(!modelConfig.includes("mdi--dots-vertical"), "移除 ⋮ 图标");
assert(modelConfig.includes("复制") && modelConfig.includes("删除"), "复制与删除回到行内");
assert((modelConfig.match(/handleDuplicateModelAdapter/g) || []).length >= 3, "列表与卡片都保留复制入口");
assert(modelConfig.includes("inline"), "卡片视图使用 inline 状态行");
assert(modelConfig.includes("model-card"), "卡片使用局部密度类");

// ── 总览启停配色：启动深绿主按钮、停止红字风险样式（用户 2026-11-18 决策） ──
assert(
  home.includes(':variant="cursorStartDisabled ? \'default\' : \'primary\'"'),
  "总览启动保持深绿主按钮",
);
assert(home.includes("btn-sm btn-risk"), "总览停止保持红字风险样式");

// ── 测试状态卡：inline 模式与完整卡并存 ──
const testCard = read("components/ModelAdapterTestCard.vue");
assert(testCard.includes("inline"), "测试卡提供 inline 模式");
assert(testCard.includes("v-if=\"inline\""), "inline 模式单独渲染");
assert(testCard.includes("查看详情"), "失败状态提供查看详情入口");
assert(
  testCard.includes('rawResponseText.value ? "失败 · 查看详情" : "失败"'),
  "仅在有原始返回时承诺查看详情",
);
assert(
  /if \(props\.stale\) \{\s*return "bg-\[var\(--color-warning-text\)\]"/.test(testCard),
  "需重测状态点使用 warning 色",
);
assert(testCard.includes("rawResponse"), "inline 仍保留原始返回工具提示");
assert(testCard.includes("stale"), "inline 仍标记需重测");
assert(!testCard.includes('return "需重测";'), "需重测不再覆盖 inline 摘要");
assert(testCard.includes('v-if="stale"'), "需重测以标记形式与摘要并存");
assert(testCard.includes("inlineSurface"), "inline 支持浅底状态行");

// ── 编辑弹窗：标题栏开关、字段分组、底部固定栏 ──
const contentModal = read("components/ui/ContentModal.vue");
const editor = read("components/ModelEditor.vue");
assert(contentModal.includes("headerless"), "ContentModal 支持由内容组件自绘标题栏");
assert(modelConfig.includes("headerless"), "编辑弹窗使用 headerless 标题栏");
for (const group of ["连接", "模型", "高级"]) {
  assert(editor.includes(group), `字段分组包含「${group}」`);
}
assert(editor.includes("保存后生效"), "底部左侧保留启用状态文案");
assert(editor.includes("headerless") || editor.includes("model-editor-header"), "编辑器渲染标题栏");
for (const binding of [
  "draft.baseURL",
  "draft.apiKey",
  "draft.displayName",
  "draft.modelID",
  "draft.openAIEndpoint",
  "draft.reasoningEffort",
  "maxCompletionTokensInput",
  "draft.openAIExtraParamsJSON",
  "draft.customHeadersJSON",
  "draft.outboundProxy.url",
  "draft.providerFallback.enabled",
  "draft.tooltipData",
]) {
  assert(editor.includes(binding), `字段绑定保留：${binding}`);
}
assert(editor.includes("inline"), "编辑器测试结果使用 inline 单行");
assert(editor.includes("inline-surface"), "编辑器测试结果带浅底状态行");
assert(!editor.includes(':show-metrics="true"'), "编辑器不再渲染大指标块");
assert(editor.includes(':stale="modelTestResultStale"'), "编辑器仍传递需重测状态");

// ── 全局样式：局部密度类与行内操作样式 ──
const css = read("style/global.css");
assert(css.includes(".home-service-card"), "总览紧凑布局仅作用于服务卡片");
assert(css.includes(".public-model-row"), "公开模型行样式");
assert(css.includes("padding: 8px 16px"), "模型列表行高收紧");
assert(!css.includes(".model-more-menu"), "移除浮层菜单样式");
assert(css.includes(".model-card-foot"), "卡片操作区固定在底部");

// ── 接入中心：Anthropic 占位不可接入 ──
const access = read("views/AccessView.vue");
assert(access.includes(':disabled="!item.supported"'), "不支持的接入方式按钮禁用");
assert(access.includes("!selected?.supported"), "选择入口兜底阻止不支持项");
assert(access.includes("clients.value.find("), "脚本内读取 computed 的 value，支持项保持可切换");
assert(!access.includes("计划中"), "移除不支持项的「计划中」徽标");
assert(!access.includes("Anthropic 暂为接入占位"), "移除占位说明句");
assert(css.includes(".access-client:disabled"), "接入项提供禁用态样式");

console.log("ui density structure tests passed");

// ── 设置页广告项：内容服务未接入，保留位置但灰化不可点 ──
const settings = read("views/SettingsView.vue");
assert(settings.includes('label="广告内容"'), "保留广告内容开关位置");
assert(
  /label="广告内容"[\s\S]{0,260}?disabled/.test(settings),
  "广告内容开关灰化不可点击",
);
assert(settings.includes("内容服务暂未接入"), "说明广告内容暂不可用");
console.log("ads settings structure tests passed");
