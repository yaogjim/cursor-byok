<script setup>
import { computed } from "vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { formatDuration } from "@/state/appState";

const props = defineProps({
  result: {
    type: Object,
    default: null,
  },
  stale: {
    type: Boolean,
    default: false,
  },
  compact: {
    type: Boolean,
    default: false,
  },
  showMetrics: {
    type: Boolean,
    default: false,
  },
  title: {
    type: String,
    default: "模型测试",
  },
  emptyText: {
    type: String,
    default: "尚未测试",
  },
});

const normalizedStatus = computed(() => {
  const status = String(props.result?.status || "").trim().toLowerCase();
  return ["running", "success", "error"].includes(status) ? status : "idle";
});

const summaryText = computed(() => {
  const text = String(props.result?.summaryText || "").trim();
  if (text) {
    return text;
  }
  if (normalizedStatus.value === "running") {
    return "测试中...";
  }
  if (normalizedStatus.value === "error") {
    return "测试失败,请查看原始信息";
  }
  return props.emptyText;
});

const rawResponseText = computed(() => {
  const raw = String(props.result?.rawResponse || "").trim();
  if (raw) {
    return raw;
  }
  if (normalizedStatus.value === "error") {
    return String(props.result?.error || "").trim();
  }
  return "";
});

const panelClass = computed(() => {
  if (props.stale) {
    return "border-[var(--color-warning-border)] bg-[var(--color-warning-bg)]";
  }
  if (normalizedStatus.value === "running") {
    return "border-[var(--color-info-border)] bg-[var(--color-info-bg)]";
  }
  if (normalizedStatus.value === "error") {
    return "border-[var(--color-error-border)] bg-[var(--color-error-bg)]";
  }
  if (normalizedStatus.value === "success" && props.result?.tokensEstimated) {
    return "border-[var(--color-warning-border)] bg-[var(--color-warning-bg)]";
  }
  if (normalizedStatus.value === "success") {
    return "border-[var(--color-success-border)] bg-[var(--color-success-bg)]";
  }
  return "border-[var(--color-border)] bg-[var(--color-surface-muted)]";
});

const summaryClass = computed(() => {
  if (props.stale) {
    return "text-[var(--color-warning-text)]";
  }
  if (normalizedStatus.value === "running") {
    return "text-[var(--color-info-text)]";
  }
  if (normalizedStatus.value === "error") {
    return "text-[var(--color-error-text)]";
  }
  if (normalizedStatus.value === "success" && props.result?.tokensEstimated) {
    return "text-[var(--color-warning-text)]";
  }
  if (normalizedStatus.value === "success") {
    return "text-[var(--color-success-text)]";
  }
  return "text-[var(--color-text-secondary)]";
});
</script>

<template>
  <div
    class="rounded-[8px] border-none"
    :class="[panelClass, compact ? 'px-2.5 py-2' : 'px-3 py-3']"
  >
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-1.5">
          <div
            :class="compact ? 'text-[11px] uppercase tracking-[0.08em] text-[var(--color-text-muted)]' : 'text-sm font-medium text-[var(--color-text)]'"
          >
            {{ title }}
          </div>
          <div v-if="rawResponseText" class="center-row gap-1 text-[11px] text-[var(--color-text-secondary)]">
            <span>原始返回</span>
            <Tooltip :content="rawResponseText" copyable />
          </div>
        </div>
        <div
          class="mt-1 leading-relaxed"
          :class="[summaryClass, compact ? 'text-xs' : 'text-sm']"
        >
          {{ summaryText }}
        </div>
      </div>
      <span
        v-if="stale"
        class="shrink-0 rounded-[999px] border border-[var(--color-warning-border)] px-2 py-1 text-xs text-[var(--color-warning-text)]"
      >
        需重测
      </span>
    </div>

    <div v-if="stale" class="mt-2 text-xs text-[var(--color-warning-text)]">
      配置已变更，请重新测试
    </div>

    <div
      v-if="showMetrics && normalizedStatus === 'success'"
      class="mt-3 grid grid-cols-1 gap-2 md:grid-cols-2"
    >
      <div class="rounded-[8px] bg-[var(--color-surface)] px-3 py-2">
        <div class="text-[11px] uppercase tracking-[0.08em] text-[var(--color-text-muted)]">总耗时</div>
        <div class="mt-1 text-sm text-[var(--color-text)]">{{ formatDuration(result?.totalDurationMS) }}</div>
      </div>
      <div class="rounded-[8px] bg-[var(--color-surface)] px-3 py-2">
        <div class="text-[11px] uppercase tracking-[0.08em] text-[var(--color-text-muted)]">输出 Token</div>
        <div class="mt-1 text-sm text-[var(--color-text)]">{{ result?.outputTokens ?? 0 }}</div>
      </div>
    </div>

    <div
      v-if="normalizedStatus === 'success' && result?.tokensEstimated"
      class="mt-2 text-xs text-[var(--color-text-secondary)]"
    >
      输出 Token 为估算值
    </div>
  </div>
</template>
