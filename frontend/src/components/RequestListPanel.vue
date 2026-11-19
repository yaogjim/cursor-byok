<script setup>
import Select from "@/components/ui/Select.vue";
import { formatCompactInteger } from "@/utils/numberFormat";
import {
  formatRequestTime,
  nextRequestSort,
  requestDetailFields,
  requestModelLabel,
  requestRowKey,
  sortRequestRows,
} from "@/utils/requestList";
import { computed, ref, watch } from "vue";

const RANGE_OPTIONS = Object.freeze([
  { value: "today", label: "今天" },
  { value: "24h", label: "24 小时" },
  { value: "7d", label: "7 天" },
  { value: "30d", label: "30 天" },
]);

const STATUS_OPTIONS = Object.freeze([
  { value: "", label: "全部" },
  { value: "failed", label: "失败" },
]);

const props = defineProps({
  result: { type: Object, required: true },
  options: { type: Object, default: () => ({ providers: [], models: [] }) },
  range: { type: String, default: "today" },
  provider: { type: String, default: "" },
  model: { type: String, default: "" },
  status: { type: String, default: "" },
  loading: { type: Boolean, default: false },
  error: { type: String, default: "" },
});

const emit = defineEmits([
  "range-change",
  "provider-change",
  "model-change",
  "status-change",
  "previous-page",
  "next-page",
  "retry",
]);

const rows = computed(() => (Array.isArray(props.result?.items) ? props.result.items : []));
const total = computed(() => Number(props.result?.total) || 0);
const page = computed(() => Number(props.result?.page) || 1);
const pageSize = computed(() => Number(props.result?.pageSize) || 50);
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)));
const summary = computed(() => props.result?.summary || {});
const sort = ref(null);
const expandedKeys = ref(new Set());
const sortedRows = computed(() => sortRequestRows(rows.value, sort.value));
const displayRows = computed(() => (
  sortedRows.value.map((row, index) => ({ row, index, key: requestRowKey(row, rows.value.indexOf(row)) }))
));

watch(
  () => [props.range, props.provider, props.model, props.status],
  () => {
    sort.value = null;
    clearExpanded();
  },
);

function sortDirection(key) {
  return sort.value?.key === key ? sort.value.direction : "";
}

function ariaSort(key) {
  const direction = sortDirection(key);
  if (direction === "asc") {
    return "ascending";
  }
  return direction === "desc" ? "descending" : "none";
}

function sortIconClass(key) {
  const direction = sortDirection(key);
  if (direction === "asc") {
    return "icon-[mdi--arrow-up] text-[13px] text-[var(--color-primary)]";
  }
  if (direction === "desc") {
    return "icon-[mdi--arrow-down] text-[13px] text-[var(--color-primary)]";
  }
  return "icon-[mdi--unfold-more-horizontal] text-[13px] text-[var(--color-text-faint)]";
}

function toggleSort(key) {
  sort.value = nextRequestSort(sort.value, key);
}

function isExpanded(key) {
  return expandedKeys.value.has(key);
}

function clearExpanded() {
  expandedKeys.value = new Set();
}

function toggleRow(key) {
  const next = new Set(expandedKeys.value);
  if (next.has(key)) {
    next.delete(key);
  } else {
    next.add(key);
  }
  expandedKeys.value = next;
}

function detailId(index) {
  return `request-detail-${index}`;
}

function handleRowKeydown(event, key) {
  if (event.target !== event.currentTarget) {
    return;
  }
  if (event.key !== "Enter" && event.key !== " ") {
    return;
  }
  event.preventDefault();
  toggleRow(key);
}

watch(page, clearExpanded);

const providerOptions = computed(() => [
  { value: "", label: "全部供应商" },
  ...(props.options?.providers || []).map((item) => ({ value: item, label: item })),
]);
const modelOptions = computed(() => [
  { value: "", label: "全部模型" },
  ...(props.options?.models || []).map((item) => ({ value: item, label: item })),
]);

const summaryParts = computed(() => {
  const item = summary.value || {};
  return [
    { key: "requests", prefix: "", value: formatCompactInteger(item.requests || 0), suffix: " 个请求" },
    { key: "input", prefix: "输入 ", value: formatCompactInteger(item.inputTokens || 0), suffix: "" },
    { key: "output", prefix: "输出 ", value: formatCompactInteger(item.outputTokens || 0), suffix: "" },
    { key: "cacheRead", prefix: "缓存读取 ", value: formatCompactInteger(item.cacheReadTokens || 0), suffix: "" },
    { key: "cacheWrite", prefix: "缓存写入 ", value: formatCompactInteger(item.cacheWriteTokens || 0), suffix: "" },
  ];
});

function formatCompact(value) {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) {
    return "—";
  }
  return formatCompactInteger(number);
}

function formatDuration(ms) {
  const value = Number(ms);
  if (!Number.isFinite(value) || value <= 0) {
    return "—";
  }
  if (value < 1000) {
    return `${Math.round(value)} 毫秒`;
  }
  return `${(value / 1000).toFixed(1)} 秒`;
}

function formatHitRate(rate) {
  if (typeof rate !== "number" || !Number.isFinite(rate)) {
    return "—";
  }
  return `${(rate * 100).toFixed(1)}%`;
}

function formatSpeed(row) {
  const output = Number(row?.outputTokens);
  const duration = Number(row?.durationMs);
  if (!Number.isFinite(output) || output <= 0 || !Number.isFinite(duration) || duration <= 0) {
    return "—";
  }
  return `${Math.round(output / (duration / 1000))} token/秒`;
}

function isSuccess(row) {
  return String(row?.status || "") === "completed";
}

function statusLabel(row) {
  if (isSuccess(row)) {
    return "成功";
  }
  const code = Number(row?.statusCode);
  if (code > 0) {
    return `${code} · ${String(row?.status || "失败")}`;
  }
  return String(row?.status || "失败");
}

const canPrev = computed(() => page.value > 1 && !props.loading);
const canNext = computed(() => page.value < pageCount.value && !props.loading);
</script>

<template>
  <div>
    <div class="request-filter-bar">
      <div class="seg" role="group" aria-label="时间范围">
        <button
          v-for="item in RANGE_OPTIONS"
          :key="item.value"
          type="button"
          :class="range === item.value ? 'is-on' : ''"
          @click="emit('range-change', item.value)"
        >
          {{ item.label }}
        </button>
      </div>
      <Select
        :model-value="provider"
        :options="providerOptions"
        placeholder="全部供应商"
        button-class="request-filter-select min-w-[132px]"
        @update:model-value="emit('provider-change', $event)"
      />
      <Select
        :model-value="model"
        :options="modelOptions"
        placeholder="全部模型"
        button-class="request-filter-select min-w-[132px]"
        @update:model-value="emit('model-change', $event)"
      />
      <div class="seg" role="group" aria-label="状态筛选">
        <button
          v-for="item in STATUS_OPTIONS"
          :key="item.value || 'all'"
          type="button"
          :class="status === item.value ? 'is-on' : ''"
          @click="emit('status-change', item.value)"
        >
          {{ item.label }}
        </button>
      </div>
    </div>

    <div class="request-meta">
      <span v-for="(part, index) in summaryParts" :key="part.key" class="request-meta-item">
        <span v-if="index > 0" class="request-meta-sep" aria-hidden="true">·</span>
        <span v-if="part.prefix">{{ part.prefix }}</span>
        <span class="request-meta-value">{{ part.value }}</span>
        <span v-if="part.suffix">{{ part.suffix }}</span>
      </span>
      <span v-if="rows.length > 0" class="request-meta-hint">
        本页排序，翻页保留排序，切换筛选重置
      </span>
    </div>

    <div
      v-if="error"
      class="mt-3 flex items-center justify-between gap-3 rounded border border-[var(--color-error-border)] bg-[var(--color-error-bg)] px-3 py-2 text-xs text-[var(--color-error-text)]"
    >
      <span>{{ error }}</span>
      <button
        type="button"
        class="shrink-0 rounded-[6px] border border-[var(--color-error-border)] bg-[var(--color-surface)] px-2 py-1 text-xs text-[var(--color-error-text)] disabled:cursor-not-allowed disabled:opacity-55"
        :disabled="loading"
        @click="emit('retry')"
      >
        重试
      </button>
    </div>
    <div v-else-if="loading && rows.length === 0" class="mt-5 text-sm text-[var(--color-text-muted)]">
      加载中...
    </div>
    <div v-else-if="rows.length === 0" class="mt-5 text-sm text-[var(--color-text-muted)]">
      暂无请求记录
    </div>
    <div v-else class="mt-3">
      <div class="mt-2 overflow-x-auto">
        <table class="w-full min-w-[900px] border-collapse text-[12px]">
          <thead>
            <tr class="text-left text-[11px] font-semibold text-[var(--color-text-muted)]">
              <th scope="col" class="w-[30px] px-2 py-2"><span class="sr-only">展开详情</span></th>
              <th class="whitespace-nowrap px-2 py-2" :aria-sort="ariaSort('at')">
                <button type="button" class="inline-flex cursor-pointer items-center gap-1 hover:text-[var(--color-text)]" @click="toggleSort('at')">
                  时间<span :class="sortIconClass('at')" aria-hidden="true" />
                </button>
              </th>
              <th class="whitespace-nowrap px-2 py-2">请求模型</th>
              <th class="whitespace-nowrap px-2 py-2">供应商</th>
              <th class="whitespace-nowrap px-2 py-2">实际模型</th>
              <th class="whitespace-nowrap px-2 py-2 text-right" :aria-sort="ariaSort('inputTokens')">
                <button type="button" class="inline-flex cursor-pointer items-center gap-1 hover:text-[var(--color-text)]" @click="toggleSort('inputTokens')">
                  输入<span :class="sortIconClass('inputTokens')" aria-hidden="true" />
                </button>
              </th>
              <th class="whitespace-nowrap px-2 py-2 text-right" :aria-sort="ariaSort('outputTokens')">
                <button type="button" class="inline-flex cursor-pointer items-center gap-1 hover:text-[var(--color-text)]" @click="toggleSort('outputTokens')">
                  输出<span :class="sortIconClass('outputTokens')" aria-hidden="true" />
                </button>
              </th>
              <th class="whitespace-nowrap px-2 py-2 text-right">缓存命中率</th>
              <th class="whitespace-nowrap px-2 py-2 text-right" :aria-sort="ariaSort('ttftMs')">
                <button type="button" class="inline-flex cursor-pointer items-center gap-1 hover:text-[var(--color-text)]" @click="toggleSort('ttftMs')">
                  首字耗时<span :class="sortIconClass('ttftMs')" aria-hidden="true" />
                </button>
              </th>
              <th class="whitespace-nowrap px-2 py-2 text-right" :aria-sort="ariaSort('durationMs')">
                <button type="button" class="inline-flex cursor-pointer items-center gap-1 hover:text-[var(--color-text)]" @click="toggleSort('durationMs')">
                  耗时<span :class="sortIconClass('durationMs')" aria-hidden="true" />
                </button>
              </th>
              <th class="whitespace-nowrap px-2 py-2 text-right">速度</th>
              <th class="whitespace-nowrap px-2 py-2">状态</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="entry in displayRows" :key="`${entry.key}-${entry.index}`">
              <tr
                tabindex="0"
                class="cursor-pointer border-t border-[var(--color-border)] hover:bg-[var(--color-surface-hover)]"
                @click="toggleRow(entry.key)"
                @keydown="handleRowKeydown($event, entry.key)"
              >
                <td class="px-2 py-2">
                  <button
                    type="button"
                    class="center-row size-6 shrink-0 cursor-pointer rounded-[4px] text-[var(--color-text-secondary)] hover:bg-[var(--color-surface-muted)]"
                    :aria-expanded="isExpanded(entry.key)"
                    :aria-controls="detailId(entry.index)"
                    :aria-label="isExpanded(entry.key) ? '收起请求详情' : '展开请求详情'"
                    @click.stop="toggleRow(entry.key)"
                  >
                    <span
                      :class="isExpanded(entry.key) ? 'icon-[mdi--chevron-down]' : 'icon-[mdi--chevron-right]'"
                      class="text-[16px]"
                      aria-hidden="true"
                    />
                  </button>
                </td>
                <td class="whitespace-nowrap px-2 py-2 tabular-nums" style="font-family: var(--font-num)">
                  {{ formatRequestTime(entry.row.at, range) }}
                </td>
                <td class="max-w-[180px] truncate px-2 py-2" :title="entry.row.requestModel">
                  {{ requestModelLabel(entry.row) || "本地会话" }}
                </td>
                <td class="max-w-[160px] truncate px-2 py-2" :title="entry.row.provider">
                  {{ entry.row.provider || "—" }}
                </td>
                <td class="max-w-[200px] truncate px-2 py-2" :title="entry.row.model">
                  {{ entry.row.model || "—" }}
                </td>
                <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
                  {{ formatCompact(entry.row.inputTokens) }}
                </td>
                <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
                  {{ formatCompact(entry.row.outputTokens) }}
                </td>
                <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
                  {{ formatHitRate(entry.row.cacheHitRate) }}
                </td>
                <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
                  {{ formatDuration(entry.row.ttftMs) }}
                </td>
                <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
                  {{ formatDuration(entry.row.durationMs) }}
                </td>
                <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
                  {{ formatSpeed(entry.row) }}
                </td>
                <td class="whitespace-nowrap px-2 py-2">
                  <span
                    class="status-pill"
                    :class="isSuccess(entry.row) ? 'is-ok' : 'is-err'"
                    :title="entry.row.errorCategory || ''"
                  >
                    <i aria-hidden="true" />
                    {{ statusLabel(entry.row) }}
                  </span>
                </td>
              </tr>
              <tr
                v-if="isExpanded(entry.key)"
                :id="detailId(entry.index)"
                class="border-t border-[var(--color-border)] bg-[var(--color-surface-muted)]"
              >
                <td :colspan="12" class="px-3 py-3">
                  <dl class="grid grid-cols-2 gap-x-6 gap-y-1 text-[11px] max-[760px]:grid-cols-1">
                    <div
                      v-for="field in requestDetailFields(entry.row)"
                      :key="field.label"
                      class="flex min-w-0 gap-2"
                    >
                      <dt class="shrink-0 text-[var(--color-text-muted)]">{{ field.label }}</dt>
                      <dd class="min-w-0 break-words text-[var(--color-text)]">{{ field.value }}</dd>
                    </div>
                  </dl>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </div>

    <div v-if="rows.length > 0" class="mt-3 flex items-center justify-end gap-2 text-[11px] text-[var(--color-text-muted)]">
      <span>第 {{ page }} / {{ pageCount }} 页 · 共 {{ total }} 条</span>
      <button
        type="button"
        class="rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 disabled:cursor-not-allowed disabled:opacity-50"
        :disabled="!canPrev"
        @click="emit('previous-page')"
      >
        上一页
      </button>
      <button
        type="button"
        class="rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 disabled:cursor-not-allowed disabled:opacity-50"
        :disabled="!canNext"
        @click="emit('next-page')"
      >
        下一页
      </button>
    </div>
  </div>
</template>
