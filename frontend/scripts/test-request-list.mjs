import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import vue from "@vitejs/plugin-vue";
import { createSSRApp, h } from "vue";
import { renderToString } from "@vue/server-renderer";
import { createServer } from "vite";
import {
  formatRequestTime,
  nextRequestSort,
  requestDetailFields,
  requestModelLabel,
  requestRowKey,
  sortRequestRows,
} from "../src/utils/requestList.js";

// 使用本地时间构造样本，断言不依赖测试机时区。
function at(...parts) {
  return new Date(...parts);
}

// ── 时间格式 ──
const sample = at(2026, 10, 18, 8, 5, 9);
assert.equal(formatRequestTime(sample, "today"), "08:05:09");
assert.equal(formatRequestTime(sample, "24h"), "11-18 08:05");
assert.equal(formatRequestTime(sample, "7d"), "11-18 08:05");
assert.equal(formatRequestTime(sample, "30d"), "11-18 08:05");
assert.equal(formatRequestTime("", "today"), "—");
assert.equal(formatRequestTime(null, "30d"), "—");
assert.equal(formatRequestTime("not-a-date", "30d"), "—");

// ── 请求模型名称：优先名称，缺失回落编号 ──
assert.equal(requestModelLabel({ requestModelName: "glm5.3", requestModel: "eeb727d5b08b1fb8" }), "glm5.3");
assert.equal(requestModelLabel({ requestModel: "eeb727d5b08b1fb8" }), "eeb727d5b08b1fb8");
assert.equal(requestModelLabel({}), "");
{
  const fields = requestDetailFields({ requestModelName: "glm5.3", requestModel: "eeb727d5b08b1fb8" });
  assert.equal(fields.find((field) => field.label === "请求模型名称")?.value, "glm5.3");
  assert.equal(fields.find((field) => field.label === "原始请求模型")?.value, "eeb727d5b08b1fb8");
}

// ── 三态排序 ──
assert.deepEqual(nextRequestSort(null, "durationMs"), { key: "durationMs", direction: "asc" });
assert.deepEqual(nextRequestSort({ key: "durationMs", direction: "asc" }, "durationMs"), {
  key: "durationMs",
  direction: "desc",
});
assert.equal(nextRequestSort({ key: "durationMs", direction: "desc" }, "durationMs"), null);
assert.deepEqual(nextRequestSort({ key: "durationMs", direction: "desc" }, "ttftMs"), {
  key: "ttftMs",
  direction: "asc",
});

// ── 排序：基本、稳定、0、缺失、非变异 ──
const rows = [
  { requestId: "a", durationMs: 300, at: at(2026, 10, 18, 8, 0, 0) },
  { requestId: "b", durationMs: 0, at: at(2026, 10, 18, 9, 0, 0) },
  { requestId: "c", durationMs: 120, at: at(2026, 10, 18, 7, 0, 0) },
  { requestId: "d", durationMs: 120, at: at(2026, 10, 18, 6, 0, 0) },
  { requestId: "e", durationMs: null, at: at(2026, 10, 18, 5, 0, 0) },
  { requestId: "f", durationMs: Number.NaN, at: at(2026, 10, 18, 4, 0, 0) },
];
const originalOrder = rows.map((row) => row.requestId);
assert.deepEqual(sortRequestRows(rows, null).map((r) => r.requestId), originalOrder, "无排序恢复原顺序");
assert.deepEqual(
  sortRequestRows(rows, { key: "durationMs", direction: "asc" }).map((r) => r.requestId),
  ["b", "c", "d", "a", "e", "f"],
  "升序包含 0，相同值保持原序，缺失/非法放末尾",
);
assert.deepEqual(
  sortRequestRows(rows, { key: "durationMs", direction: "desc" }).map((r) => r.requestId),
  ["a", "c", "d", "b", "e", "f"],
  "降序同样把缺失/非法放末尾，相同值保持原序",
);
assert.deepEqual(rows.map((row) => row.requestId), originalOrder, "排序不修改源数组");

const timeRows = [
  { requestId: "old", at: at(2026, 10, 1, 0, 0, 0) },
  { requestId: "new", at: at(2026, 10, 18, 0, 0, 0) },
  { requestId: "bad", at: "?" },
  { requestId: "empty", at: null },
];
assert.deepEqual(
  sortRequestRows(timeRows, { key: "at", direction: "asc" }).map((r) => r.requestId),
  ["old", "new", "bad", "empty"],
);
assert.deepEqual(
  sortRequestRows(timeRows, { key: "at", direction: "desc" }).map((r) => r.requestId),
  ["new", "old", "bad", "empty"],
);

// ── 行身份：不仅依赖日期，缺失时仍唯一 ──
assert.equal(requestRowKey({ modelCallId: "mc-1", requestId: "r-1" }, 0), "mc-1");
assert.equal(requestRowKey({ requestId: "r-1" }, 0), "r-1");
assert.notEqual(requestRowKey({ at: "2026-11-18T00:00:00Z" }, 0), requestRowKey({ at: "2026-11-18T00:00:00Z" }, 1));
assert.equal(requestRowKey({ at: "2026-11-18T00:00:00Z" }, 0), requestRowKey({ at: "2026-11-18T00:00:00Z" }, 0), "同一行身份稳定");

// ── 详情字段：缺失为 —，0 正常显示 ──
const detail = Object.fromEntries(
  requestDetailFields({
    errorCategory: "",
    statusCode: 0,
    status: "",
    requestId: "req-0",
    requestModel: "",
    model: "upstream-0",
    cacheReadTokens: 0,
    cacheWriteTokens: 12,
  }).map((field) => [field.label, field.value]),
);
assert.equal(detail["错误分类"], "—");
assert.equal(detail["状态码"], "—");
assert.equal(detail["状态"], "—");
assert.equal(detail["请求 ID"], "req-0");
assert.equal(detail["原始请求模型"], "—");
assert.equal(detail["上游模型"], "upstream-0");
assert.equal(detail["缓存读取 token"], "0");
assert.equal(detail["缓存写入 token"], "12");
assert.equal(detail["请求模型名称"], "—");
assert.equal(requestDetailFields({}).length, 9, "详情字段集合固定");

// ── 真实组件渲染（Vite SSR，不新增依赖） ──
// 测试服务器不加载项目构建插件，只编译 Vue SFC；因此不触发 i18n 注入与 Wails 运行时。
const root = fileURLToPath(new URL("..", import.meta.url));
const vite = await createServer({
  configFile: false,
  root,
  server: { middlewareMode: true },
  appType: "custom",
  logLevel: "error",
  optimizeDeps: { noDiscovery: true },
  plugins: [vue()],
  resolve: { alias: { "@": path.join(root, "src") } },
});
let rendered = "";
try {
  const { default: RequestListPanel } = await vite.ssrLoadModule("/src/components/RequestListPanel.vue");
  const items = [
    { requestId: "req-a", at: at(2026, 10, 18, 8, 5, 9), durationMs: 900, inputTokens: 10, outputTokens: 5, ttftMs: 100, status: "completed", requestModelName: "glm5.3", requestModel: "eeb727d5b08b1fb8" },
    { requestId: "req-b", at: at(2026, 10, 17, 7, 4, 3), durationMs: 1500, inputTokens: 20, outputTokens: 8, ttftMs: 200, status: "failed", statusCode: 502 },
  ];
  const before = items.map((item) => item.requestId);
  const app = createSSRApp({
    render: () => h(RequestListPanel, {
      result: { items, total: 2, page: 1, pageSize: 50, summary: { requests: 2 } },
      range: "30d",
    }),
  });
  rendered = await renderToString(app);
  assert.deepEqual(items.map((item) => item.requestId), before, "渲染不修改传入的当页数组");
  assert.match(rendered, /本页排序/, "标注本页排序");
  assert.match(rendered, /翻页保留排序，切换筛选重置/, "标注排序与翻页/筛选关系");
  assert.match(rendered, /class="request-filter-bar"/, "筛选控件统一在同一工具条");
  assert.equal((rendered.match(/request-filter-select/g) || []).length, 2, "两个下拉使用统一尺寸类");
  assert.match(rendered, /class="request-meta"/, "摘要与排序说明合并为一行元信息");
  assert.match(rendered, /class="request-meta-value"/, "摘要数字单独强调");
  assert.match(rendered, /class="request-meta-hint"/, "排序说明位于元信息行右侧");
  assert.doesNotMatch(rendered, /<div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-\[11px\]/, "移除独立的排序说明行");
  assert.match(rendered, />glm5\.3</, "请求模型列优先显示名称");
  assert.match(rendered, /11-18 08:05/, "30 天范围显示 MM-DD HH:mm");
  assert.match(rendered, /title="eeb727d5b08b1fb8"/, "名称列保留原始编号悬停信息");
  assert.doesNotMatch(rendered, /<td[^>]*>eeb727d5b08b1fb8<\/td>/, "名称存在时列内不再直接显示编号");
  assert.equal((rendered.match(/aria-sort="none"/g) || []).length, 5, "五列可排序且初始 aria-sort=none");
  assert.equal((rendered.match(/aria-label="展开请求详情"/g) || []).length, 2, "每行有展开按钮");
  assert.equal((rendered.match(/aria-expanded="false"/g) || []).length, 4, "展开按钮与筛选下拉均正确暴露折叠状态");
  for (const label of ["时间", "输入", "输出", "首字耗时", "耗时"]) {
    assert.match(rendered, new RegExp(`>\\s*${label}(?=<)`), `列头 ${label} 可点`);
  }
  assert.match(rendered, /<th[^>]*scope="col"[^>]*><span class="sr-only">展开详情<\/span><\/th>/, "首列保留可访问的展开说明");
} finally {
  await vite.close();
}
console.log("request list logic tests passed");
console.log("request list render tests passed");
