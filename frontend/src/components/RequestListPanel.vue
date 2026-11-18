<script setup>
import Select from "@/components/ui/Select.vue";
import { formatCompactInteger } from "@/utils/numberFormat";
import { computed } from "vue";

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
    `${formatCompactInteger(item.requests || 0)} 个请求`,
    `输入 ${formatCompactInteger(item.inputTokens || 0)}`,
    `输出 ${formatCompactInteger(item.outputTokens || 0)}`,
    `缓存读取 ${formatCompactInteger(item.cacheReadTokens || 0)}`,
    `缓存写入 ${formatCompactInteger(item.cacheWriteTokens || 0)}`,
  ];
});

function formatCompact(value) {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) {
    return "—";
  }
  return formatCompactInteger(number);
}

function formatTime(value) {
  const text = String(value || "");
  if (!text) {
    return "—";
  }
  const date = new Date(text);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  return date.toLocaleTimeString("zh-CN", { hour12: false });
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
    <div class="flex flex-wrap items-center gap-2">
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
      <div class="flex flex-wrap items-center gap-2">
        <Select
          :model-value="provider"
          :options="providerOptions"
          placeholder="全部供应商"
          button-class="min-w-[132px]"
          @update:model-value="emit('provider-change', $event)"
        />
        <Select
          :model-value="model"
          :options="modelOptions"
          placeholder="全部模型"
          button-class="min-w-[132px]"
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
    </div>

    <div class="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-[var(--color-text-muted)]">
      <span v-for="(part, index) in summaryParts" :key="part">
        <span v-if="index > 0" class="mr-2 text-[var(--color-text-faint)]">·</span>
        {{ part }}
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
    <div v-else class="mt-3 overflow-x-auto">
      <table class="w-full min-w-[900px] border-collapse text-[12px]">
        <thead>
          <tr class="text-left text-[11px] font-semibold text-[var(--color-text-muted)]">
            <th class="whitespace-nowrap px-2 py-2">时间</th>
            <th class="whitespace-nowrap px-2 py-2">请求模型</th>
            <th class="whitespace-nowrap px-2 py-2">供应商</th>
            <th class="whitespace-nowrap px-2 py-2">实际模型</th>
            <th class="whitespace-nowrap px-2 py-2 text-right">输入</th>
            <th class="whitespace-nowrap px-2 py-2 text-right">输出</th>
            <th class="whitespace-nowrap px-2 py-2 text-right">缓存命中率</th>
            <th class="whitespace-nowrap px-2 py-2 text-right">首字耗时</th>
            <th class="whitespace-nowrap px-2 py-2 text-right">耗时</th>
            <th class="whitespace-nowrap px-2 py-2 text-right">速度</th>
            <th class="whitespace-nowrap px-2 py-2">状态</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in rows"
            :key="row.modelCallId || row.at"
            class="border-t border-[var(--color-border)]"
          >
            <td class="whitespace-nowrap px-2 py-2 tabular-nums" style="font-family: var(--font-num)">
              {{ formatTime(row.at) }}
            </td>
            <td class="max-w-[180px] truncate px-2 py-2" :title="row.requestModel">
              {{ row.requestModel || "本地会话" }}
            </td>
            <td class="max-w-[160px] truncate px-2 py-2" :title="row.provider">
              {{ row.provider || "—" }}
            </td>
            <td class="max-w-[200px] truncate px-2 py-2" :title="row.model">
              {{ row.model || "—" }}
            </td>
            <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
              {{ formatCompact(row.inputTokens) }}
            </td>
            <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
              {{ formatCompact(row.outputTokens) }}
            </td>
            <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
              {{ formatHitRate(row.cacheHitRate) }}
            </td>
            <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
              {{ formatDuration(row.ttftMs) }}
            </td>
            <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
              {{ formatDuration(row.durationMs) }}
            </td>
            <td class="px-2 py-2 text-right tabular-nums" style="font-family: var(--font-num)">
              {{ formatSpeed(row) }}
            </td>
            <td class="whitespace-nowrap px-2 py-2">
              <span
                class="status-pill"
                :class="isSuccess(row) ? 'is-ok' : 'is-err'"
                :title="row.errorCategory || ''"
              >
                <i aria-hidden="true" />
                {{ statusLabel(row) }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
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
