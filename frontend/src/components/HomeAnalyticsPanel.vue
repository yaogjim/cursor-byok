<script setup>
import HomeTrendChart from "@/components/charts/HomeTrendChart.vue";
import RequestListPanel from "@/components/RequestListPanel.vue";
import {
  activityLevel,
  buildActivityCells,
  HOME_METRICS_RANGE_OPTIONS,
  isActivityEmpty,
  isTrendEmpty,
} from "@/state/homeMetrics";
import { formatCompactInteger } from "@/utils/numberFormat";
import { computed } from "vue";

const props = defineProps({
  report: { type: Object, required: true },
  activityDaily: { type: Array, default: () => [] },
  range: { type: String, default: "24h" },
  view: { type: String, default: "trend" },
  loading: { type: Boolean, default: false },
  error: { type: String, default: "" },
  activityLoading: { type: Boolean, default: false },
  activityError: { type: String, default: "" },
  requestList: { type: Object, default: () => ({}) },
  requestOptions: { type: Object, default: () => ({ providers: [], models: [] }) },
  requestRange: { type: String, default: "today" },
  requestProvider: { type: String, default: "" },
  requestModel: { type: String, default: "" },
  requestStatus: { type: String, default: "" },
  requestLoading: { type: Boolean, default: false },
  requestError: { type: String, default: "" },
});

const emit = defineEmits([
  "retry",
  "retry-activity",
  "range-change",
  "view-change",
  "request-range-change",
  "request-provider-change",
  "request-model-change",
  "request-status-change",
  "request-previous-page",
  "request-next-page",
  "request-retry",
]);

const rangeOptions = HOME_METRICS_RANGE_OPTIONS;
const isRequestView = computed(() => props.view === "requests");
const panelTitle = computed(() => {
  if (props.view === "activity") {
    return "近 52 周活跃度";
  }
  if (props.view === "requests") {
    return "请求明细";
  }
  return "Token 使用趋势";
});
const granularityLabel = computed(() => (props.report?.granularity === "hour" ? "按小时" : "按日"));
const trendEmpty = computed(() => isTrendEmpty(props.report));
const activityEmpty = computed(() => isActivityEmpty(props.activityDaily));
const activityCells = computed(() => buildActivityCells(props.activityDaily));
const activityMax = computed(() => Math.max(0, ...activityCells.value.map((cell) => Number(cell.value) || 0)));
const weekdayLabels = ["日", "一", "二", "三", "四", "五", "六"];
const monthLabels = computed(() => {
  const labels = [];
  let lastMonth = "";
  for (const [index, cell] of activityCells.value.entries()) {
    if (cell.weekday !== 0) {
      continue;
    }
    const month = cell.date.slice(0, 7);
    if (month === lastMonth) {
      continue;
    }
    lastMonth = month;
    labels.push({
      date: cell.date,
      text: `${Number(cell.date.slice(5, 7))}月`,
      weekIndex: Math.floor(index / 7),
    });
  }
  return labels;
});
const heatLevelClass = {
  0: "bg-[var(--color-surface-muted)]",
  1: "bg-emerald-200 dark:bg-emerald-900",
  2: "bg-emerald-300 dark:bg-emerald-800",
  3: "bg-emerald-400 dark:bg-emerald-700",
  4: "bg-emerald-500 dark:bg-emerald-600",
  5: "bg-emerald-600 dark:bg-emerald-500",
};

const emptyTrendText = computed(() => (
  props.report?.granularity === "hour" ? "暂无小时数据" : "暂无按日趋势"
));

const metaText = computed(() => {
  const parts = [`时间基准：${props.report?.timezone || "UTC"}`];
  if (props.report?.dataVersion) {
    parts.push(`dataVersion ${props.report.dataVersion}`);
  }
  return parts.join(" · ");
});

function heatClass(value) {
  return heatLevelClass[activityLevel(value, activityMax.value)] || heatLevelClass[0];
}
</script>

<template>
  <div>
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div>
        <div class="flex flex-wrap items-center gap-2">
          <h3 class="card-title">{{ panelTitle }}</h3>
          <span
            v-if="view === 'trend'"
            class="status-pill is-info"
          >
            {{ granularityLabel }}
          </span>
        </div>
        <div v-if="!isRequestView" class="page-sub !mt-0">{{ metaText }}</div>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <div v-if="!isRequestView" class="seg" role="group" aria-label="时间范围">
          <button
            v-for="item in rangeOptions"
            :key="item.value"
            type="button"
            :class="range === item.value ? 'is-on' : ''"
            @click="emit('range-change', item.value)"
          >
            {{ item.label }}
          </button>
        </div>
        <div class="seg" role="group" aria-label="分析视图">
          <button
            type="button"
            :class="view === 'trend' ? 'is-on' : ''"
            @click="emit('view-change', 'trend')"
          >
            趋势
          </button>
          <button
            type="button"
            :class="view === 'activity' ? 'is-on' : ''"
            @click="emit('view-change', 'activity')"
          >
            活跃度
          </button>
          <button
            type="button"
            :class="view === 'requests' ? 'is-on' : ''"
            @click="emit('view-change', 'requests')"
          >
            请求
          </button>
        </div>
      </div>
    </div>

    <RequestListPanel
      v-if="isRequestView"
      :result="requestList"
      :options="requestOptions"
      :range="requestRange"
      :provider="requestProvider"
      :model="requestModel"
      :status="requestStatus"
      :loading="requestLoading"
      :error="requestError"
      @range-change="emit('request-range-change', $event)"
      @provider-change="emit('request-provider-change', $event)"
      @model-change="emit('request-model-change', $event)"
      @status-change="emit('request-status-change', $event)"
      @retry="emit('request-retry')"
      @previous-page="emit('request-previous-page')"
      @next-page="emit('request-next-page')"
    />

    <div v-else-if="view === 'trend'">
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
      <div v-else-if="loading" class="mt-5 text-sm text-[var(--color-text-muted)]">加载中...</div>
      <div v-else-if="trendEmpty" class="mt-5 text-sm text-[var(--color-text-muted)]">{{ emptyTrendText }}</div>
      <div v-else class="mt-4">
        <HomeTrendChart :report="report" />
      </div>
    </div>

    <div v-else>
      <div
        v-if="activityError"
        class="mt-3 flex items-center justify-between gap-3 rounded border border-[var(--color-error-border)] bg-[var(--color-error-bg)] px-3 py-2 text-xs text-[var(--color-error-text)]"
      >
        <span>{{ activityError }}</span>
        <button
          type="button"
          class="shrink-0 rounded-[6px] border border-[var(--color-error-border)] bg-[var(--color-surface)] px-2 py-1 text-xs text-[var(--color-error-text)] disabled:cursor-not-allowed disabled:opacity-55"
          :disabled="activityLoading"
          @click="emit('retry-activity')"
        >
          重试
        </button>
      </div>
      <div v-else-if="activityLoading" class="mt-5 text-sm text-[var(--color-text-muted)]">加载中...</div>
      <div v-else-if="activityEmpty" class="mt-5 text-sm text-[var(--color-text-muted)]">暂无近 52 周活跃度数据</div>
      <div v-else class="mt-4 overflow-x-auto">
        <div class="flex gap-2">
          <div class="flex flex-col justify-between py-[2px] text-[10px] leading-none text-[var(--color-text-muted)]">
            <span v-for="label in weekdayLabels" :key="label">{{ label }}</span>
          </div>
          <div>
            <div
              class="grid grid-flow-col grid-rows-7 gap-[3px]"
              role="img"
              aria-label="近 52 周每日调用次数"
            >
              <div
                v-for="cell in activityCells"
                :key="cell.date"
                class="h-3 w-3 rounded-[2px]"
                :class="heatClass(cell.value)"
                :title="`${cell.date}: ${formatCompactInteger(cell.value)} 次调用`"
              />
            </div>
            <div class="mt-2 flex gap-[3px] text-[10px] text-[var(--color-text-muted)]">
              <span
                v-for="month in monthLabels"
                :key="month.date"
                class="min-w-[12px]"
                :style="{ marginLeft: month.weekIndex === 0 ? '0' : '0' }"
              >
                {{ month.text }}
              </span>
            </div>
          </div>
        </div>
        <div class="mt-3 flex items-center justify-end gap-1 text-[10px] text-[var(--color-text-muted)]">
          <span>少</span>
          <i class="h-3 w-3 rounded-[2px] bg-[var(--color-surface-muted)]" />
          <i class="h-3 w-3 rounded-[2px] bg-emerald-200" />
          <i class="h-3 w-3 rounded-[2px] bg-emerald-400" />
          <i class="h-3 w-3 rounded-[2px] bg-emerald-600" />
          <span>多</span>
        </div>
      </div>
    </div>
  </div>
</template>