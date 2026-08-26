<script setup>
import { computed } from "vue";

const props = defineProps({
  report: { type: Object, required: true },
  loading: { type: Boolean, default: false },
  error: { type: String, default: "" },
});

const emit = defineEmits(["retry"]);

const daily = computed(() => Array.isArray(props.report?.daily) ? props.report.daily : []);
const maxCalls = computed(() => Math.max(1, ...daily.value.map((item) => Number(item.providerCalls) || 0)));
const calendarCells = computed(() => {
  const values = new Map(daily.value.map((item) => [item.date, Number(item.providerCalls) || 0]));
  if (!daily.value.length) return [];
  const start = new Date(`${daily.value[0].date}T00:00:00Z`);
  const end = new Date(`${daily.value[daily.value.length - 1].date}T00:00:00Z`);
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) {
    return [];
  }
  const cells = [];
  for (let pad = 0; pad < start.getUTCDay(); pad += 1) {
    cells.push({ date: `pad-${pad}`, value: 0, empty: true });
  }
  for (const cursor = new Date(start); cursor <= end; cursor.setUTCDate(cursor.getUTCDate() + 1)) {
    const date = cursor.toISOString().slice(0, 10);
    cells.push({ date, value: values.get(date) || 0, empty: false });
  }
  return cells;
});

function barStyle(value, max) {
  return { height: `${Math.max(4, Math.round((value / max) * 100))}%` };
}
function heatClass(value) {
  if (!value) return "bg-[var(--color-surface-muted)]";
  const ratio = value / maxCalls.value;
  if (ratio < 0.25) return "bg-emerald-200";
  if (ratio < 0.5) return "bg-emerald-300";
  if (ratio < 0.75) return "bg-emerald-400";
  return "bg-emerald-600";
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div class="flex items-center justify-between gap-3">
        <div>
          <h3 class="text-sm font-medium">请求趋势</h3>
          <div class="mt-1 text-xs text-[var(--color-text-muted)]">按日统计，时间基准：{{ report.timezone || "UTC" }}</div>
        </div>
        <span v-if="loading" class="text-xs text-[var(--color-text-muted)]">加载中...</span>
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
      <div v-if="!daily.length" class="mt-5 text-sm text-[var(--color-text-muted)]">暂无按日数据</div>
      <div v-else class="mt-5 flex h-32 items-end gap-1 overflow-x-auto border-b border-[var(--color-border)] pb-1">
        <div v-for="item in daily" :key="item.date" class="group flex h-full min-w-[18px] flex-1 flex-col justify-end">
          <div class="relative flex h-full items-end">
            <div class="w-full rounded-t bg-[var(--color-primary)]" :style="barStyle(item.providerCalls, maxCalls)" :title="`${item.date}: ${item.providerCalls} 次调用`"></div>
          </div>
          <div class="mt-1 truncate text-center text-[9px] text-[var(--color-text-muted)]">{{ item.date.slice(5) }}</div>
        </div>
      </div>
      <div v-if="daily.length" class="mt-2 text-xs text-[var(--color-text-secondary)]">柱高表示 LLM 调用次数；当前区间 Token：{{ daily.reduce((total, item) => total + item.requestTokens, 0).toLocaleString() }}</div>
    </div>

    <div class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div class="flex items-center justify-between gap-3">
        <div>
          <h3 class="text-sm font-medium">活跃度</h3>
          <div class="mt-1 text-xs text-[var(--color-text-muted)]">按日调用次数，少到多</div>
        </div>
        <div class="flex items-center gap-1 text-[10px] text-[var(--color-text-muted)]"><span>少</span><i class="h-3 w-3 bg-[var(--color-surface-muted)]"></i><i class="h-3 w-3 bg-emerald-200"></i><i class="h-3 w-3 bg-emerald-400"></i><i class="h-3 w-3 bg-emerald-600"></i><span>多</span></div>
      </div>
      <div v-if="!calendarCells.length" class="mt-5 text-sm text-[var(--color-text-muted)]">暂无活跃度数据</div>
      <div v-else class="mt-4 grid grid-cols-7 gap-1">
        <div
          v-for="cell in calendarCells"
          :key="cell.date"
          class="h-4 w-4 rounded-[3px]"
          :class="cell.empty ? 'bg-transparent' : heatClass(cell.value)"
          :title="cell.empty ? '' : `${cell.date}: ${cell.value} 次调用`"
        ></div>
      </div>
    </div>
  </div>
</template>