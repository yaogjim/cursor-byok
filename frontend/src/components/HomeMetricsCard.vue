<script setup>
import CacheHitRateChart from "@/components/charts/CacheHitRateChart.vue";
import Switch from "@/components/ui/Switch.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { appState, saveIncludeCacheWriteInHitRate } from "@/state/appState";
import { formatCompactInteger, formatInteger } from "@/utils/numberFormat";
import { computed, ref } from "vue";

const emit = defineEmits(["refresh", "reset", "open-ad"]);

const TOKEN_PRICE_PER_MILLION = {
  input: 5,
  output: 25,
  cacheRead: 0.5,
  cacheWrite: 6.25,
};

const props = defineProps({
  metrics: {
    type: Object,
    required: true,
  },
  loading: {
    type: Boolean,
    default: false,
  },
  resetting: {
    type: Boolean,
    default: false,
  },
  error: {
    type: String,
    default: "",
  },
  homeAd: {
    type: Object,
    default: null,
  },
  homeAds: {
    type: Array,
    default: () => [],
  },
});

const homeMetricsConfigSaving = ref(false);
const homeMetricsConfigError = ref("");

function normalizeNumber(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return 0;
  }
  return Math.round(number);
}

function formatMetricValue(value) {
  const full = formatInteger(value);
  const compact = formatCompactInteger(value);
  return full === compact ? full : `${full} (${compact})`;
}

function formatRateLabel(value) {
  const rate = Number(value);
  if (!Number.isFinite(rate)) {
    return "暂无数据";
  }
  return `${(Math.max(0, Math.min(1, rate)) * 100).toFixed(2)}%`;
}

function calculateRate(numerator, denominator) {
  const top = normalizeNumber(numerator);
  const bottom = normalizeNumber(denominator);
  if (bottom <= 0) {
    return null;
  }
  return top / bottom;
}

function priceTokens(tokens, pricePerMillion) {
  return (normalizeNumber(tokens) / 1_000_000) * pricePerMillion;
}

function formatUSD(value) {
  const amount = Number(value);
  if (!Number.isFinite(amount)) {
    return "$0.00";
  }
  if (amount > 0 && amount < 0.01) {
    return "<$0.01";
  }
  return `$${amount.toFixed(2)}`;
}

const cacheReadTokensTotal = computed(() => normalizeNumber(props.metrics?.cacheReadTokens));
const cacheWriteTokensTotal = computed(() => normalizeNumber(props.metrics?.cacheWriteTokens));

const inputTokensTotal = computed(() => {
  const promptTokensTotal = normalizeNumber(props.metrics?.promptTokensTotal);
  return Math.max(0, promptTokensTotal - cacheReadTokensTotal.value - cacheWriteTokensTotal.value);
});

const defaultCacheHitRate = computed(() =>
  calculateRate(cacheReadTokensTotal.value, cacheReadTokensTotal.value + inputTokensTotal.value),
);

const cacheReuseRate = computed(() =>
  calculateRate(
    cacheReadTokensTotal.value,
    cacheReadTokensTotal.value + cacheWriteTokensTotal.value + inputTokensTotal.value,
  ),
);

const includeCacheWriteInHitRate = computed(() => appState.includeCacheWriteInHitRate);

const selectedCacheHitRate = computed(() =>
  includeCacheWriteInHitRate.value ? cacheReuseRate.value : defaultCacheHitRate.value,
);

const selectedCacheRateModeLabel = computed(() =>
  includeCacheWriteInHitRate.value ? "计入缓存创建" : "默认口径",
);

const validTurnsRate = computed(() => {
  const turnsTotal = normalizeNumber(props.metrics?.turnsTotal);
  if (turnsTotal <= 0) {
    return null;
  }
  return normalizeNumber(props.metrics?.validTurnsTotal) / turnsTotal;
});

const completionTokensTotal = computed(() => {
  const requestTokensTotal = normalizeNumber(props.metrics?.requestTokensTotal);
  const promptTokensTotal = normalizeNumber(props.metrics?.promptTokensTotal);
  return Math.max(0, requestTokensTotal - promptTokensTotal);
});

const estimatedTokenCost = computed(() => {
  const input = priceTokens(inputTokensTotal.value, TOKEN_PRICE_PER_MILLION.input);
  const output = priceTokens(completionTokensTotal.value, TOKEN_PRICE_PER_MILLION.output);
  const cacheRead = priceTokens(cacheReadTokensTotal.value, TOKEN_PRICE_PER_MILLION.cacheRead);
  const cacheWrite = priceTokens(cacheWriteTokensTotal.value, TOKEN_PRICE_PER_MILLION.cacheWrite);
  return {
    input,
    output,
    cacheRead,
    cacheWrite,
    total: input + output + cacheRead + cacheWrite,
  };
});

const cacheTooltipContent = computed(() => {
  const formula = includeCacheWriteInHitRate.value
    ? "缓存读取 /（缓存读取 + 缓存创建 + 非缓存输入）"
    : "缓存读取 /（缓存读取 + 非缓存输入）";
  return [
    `当前：${formatRateLabel(selectedCacheHitRate.value)}`,
    `公式：${formula}`,
    `默认 ${formatRateLabel(defaultCacheHitRate.value)} / 计入创建 ${formatRateLabel(cacheReuseRate.value)}`,
  ].join("\n");
});

const turnsTooltipContent = computed(() =>
  [
    "按历史记录里扫描到的回合 summary 汇总，不是上游 LLM 调用次数。",
    "",
    `总轮次：${formatMetricValue(props.metrics?.turnsTotal)}`,
    `有效轮次：${formatMetricValue(props.metrics?.validTurnsTotal)}`,
    `异常轮次：${formatMetricValue(props.metrics?.invalidTurnsTotal)}`,
    `有效占比：${formatRateLabel(validTurnsRate.value)}`,
  ].join("\n"),
);

const providerCallsTooltipContent = computed(() =>
  [
    "按实际上游 LLM 调用次数汇总，与对话轮次不是同一口径。",
    "",
    `LLM 调用：${formatMetricValue(props.metrics?.providerCallsTotal)}`,
  ].join("\n"),
);

const tokensTooltipContent = computed(() =>
  [
    "总请求 Token 包含 Prompt 和模型输出。",
    "",
    `总请求：${formatMetricValue(props.metrics?.requestTokensTotal)}`,
    `Prompt：${formatMetricValue(props.metrics?.promptTokensTotal)}`,
    `输出推算：${formatMetricValue(completionTokensTotal.value)}`,
    `非缓存输入：${formatMetricValue(inputTokensTotal.value)}`,
    `缓存读取：${formatMetricValue(cacheReadTokensTotal.value)}`,
    `缓存写入：${formatMetricValue(cacheWriteTokensTotal.value)}`,
    "",
    "缓存读写已计入 Prompt 侧统计。",
  ].join("\n"),
);

const costTooltipContent = computed(() =>
  [
    "按 Claude Opus 4.7 价格估算。",
    `缓存统计策略：${selectedCacheRateModeLabel.value}（${formatRateLabel(selectedCacheHitRate.value)}）`,
    "",
    `普通输入：${formatMetricValue(inputTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.input}/1M = ${formatUSD(estimatedTokenCost.value.input)}`,
    `模型输出：${formatMetricValue(completionTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.output}/1M = ${formatUSD(estimatedTokenCost.value.output)}`,
    `缓存读取：${formatMetricValue(cacheReadTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.cacheRead}/1M = ${formatUSD(estimatedTokenCost.value.cacheRead)}`,
    `缓存写入：${formatMetricValue(cacheWriteTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.cacheWrite}/1M = ${formatUSD(estimatedTokenCost.value.cacheWrite)}`,
    "",
    `合计：${formatUSD(estimatedTokenCost.value.total)}`,
  ].join("\n"),
);

function normalizeHomeAd(item, index) {
  const source = item && typeof item === "object" ? item : {};
  const title = typeof source.title === "string" ? source.title.trim() : "";
  if (!title) {
    return null;
  }
  return {
    id: typeof source.id === "string" && source.id.trim() ? source.id.trim() : String(index + 1),
    title,
    subtitle: typeof source.subtitle === "string" ? source.subtitle.trim() : "",
  };
}

async function toggleIncludeCacheWriteInHitRate(value) {
  const nextValue = Boolean(value);
  homeMetricsConfigSaving.value = true;
  homeMetricsConfigError.value = "";
  try {
    const result = await saveIncludeCacheWriteInHitRate(nextValue);
    if (!result?.ok) {
      homeMetricsConfigError.value = result?.error || "保存失败";
    }
  } catch (error) {
    homeMetricsConfigError.value = error?.message || "保存失败";
  } finally {
    homeMetricsConfigSaving.value = false;
  }
}

const normalizedHomeAds = computed(() => {
  const list = Array.isArray(props.homeAds) && props.homeAds.length > 0 ? props.homeAds : [props.homeAd];
  return list.map(normalizeHomeAd).filter(Boolean);
});

const hasHomeAd = computed(() => normalizedHomeAds.value.length > 0);
</script>

<template>
  <div>
    <div class="flex flex-col gap-3">
      <div class="page-title-row">
        <div class="page-title-block">
          <h2 class="page-title">使用分析</h2>
        </div>
        <div
          class="flex min-w-0 flex-wrap items-center justify-end gap-2"
        >
          <div
            v-if="hasHomeAd"
            class="grid min-w-0 grid-cols-3 gap-2"
          >
            <div
              v-for="ad in normalizedHomeAds"
              :key="ad.id"
              style="font-family: var(--font-num)"
              class="center-row h-[42px] min-w-0 cursor-pointer gap-[8px] rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-[8px] pr-[10px] text-left transition-colors duration-150 hover:border-[var(--color-border-strong)] hover:bg-[var(--color-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-400/50"
              role="button"
              tabindex="0"
              :title="ad.subtitle ? `${ad.title}\n${ad.subtitle}` : ad.title"
              @click="emit('open-ad', ad.id)"
              @keydown.enter.prevent="emit('open-ad', ad.id)"
              @keydown.space.prevent="emit('open-ad', ad.id)"
            >
              <div
                class="center-row h-[20px] w-[20px] shrink-0 justify-center text-[20px] text-amber-400"
              >
                <span class="icon-[cil--badge]"></span>
              </div>
              <div class="min-w-0 flex-1">
                <div class="truncate text-[13px] font-semibold leading-[16px] text-[var(--color-text)]">
                  {{ ad.title }}
                </div>
                <div
                  v-if="ad.subtitle"
                  class="mt-[2px] center-row min-w-0 gap-[2px] text-[11px] leading-[12px] text-[var(--color-text-secondary)]"
                >
                  <span class="truncate">{{ ad.subtitle }}</span>
                </div>
              </div>
            </div>
          </div>
          <button
            type="button"
            class="center-row h-8 w-8 justify-center rounded-[9px] border border-[var(--color-border)] bg-[var(--color-surface)] text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-strong)] hover:text-[var(--color-text)] disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="loading || resetting"
            :title="loading ? '刷新中' : '刷新统计'"
            @click="emit('refresh')"
          >
            <span
              class="icon-[mdi--refresh] text-[16px]"
              :class="{ '!animate-spin': loading }"
            ></span>
          </button>
          <button
            type="button"
            class="btn-risk center-row h-8 justify-center rounded-[9px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 text-[12px] font-semibold text-[var(--color-text-secondary)] transition-colors duration-150 hover:text-[var(--color-error-text)] disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="loading || resetting"
            :title="resetting ? '重置中' : '重置统计'"
            @click="emit('reset')"
          >
            {{ resetting ? "重置中" : "重置" }}
          </button>
        </div>
      </div>

      <div class="ui-card ui-card--flush">
        <div
          class="ui-card-body grid grid-cols-2 gap-4 min-[900px]:grid-cols-5"
        >
        <div class="flex min-h-0 min-w-0 flex-col justify-between gap-1">
          <div class="center-row justify-start gap-1 text-[11px] font-semibold text-[var(--color-text-muted)]">
            <span>缓存命中率</span>
            <Tooltip>
              <div class="w-[280px] space-y-3">
                <div class="border-b border-[var(--color-border)] pb-3">
                  <Switch
                    compact
                    label="计入缓存创建"
                    description="开启后把缓存创建纳入分母"
                    enabled-text="当前按复用率口径显示"
                    disabled-text="当前按默认命中率口径显示"
                    :enabled="includeCacheWriteInHitRate"
                    :busy="homeMetricsConfigSaving"
                    :disabled="homeMetricsConfigSaving"
                    @change="toggleIncludeCacheWriteInHitRate"
                  />
                </div>
                <div class="whitespace-pre-wrap">{{ cacheTooltipContent }}</div>
                <div v-if="homeMetricsConfigError" class="text-[11px] text-[var(--color-error-text)]">
                  {{ homeMetricsConfigError }}
                </div>
              </div>
            </Tooltip>
          </div>
          <CacheHitRateChart :rate="selectedCacheHitRate" />
        </div>

        <div class="flex min-h-0 min-w-0 flex-col justify-between gap-1">
          <div class="center-row justify-start gap-1 text-[11px] font-semibold text-[var(--color-text-muted)]">
            <span>对话轮次</span>
            <Tooltip :content="turnsTooltipContent" />
          </div>
          <div>
            <div
              class="metric-value"
              style="font-family: var(--font-num)"
              :title="formatInteger(metrics.turnsTotal)"
            >
              {{ formatCompactInteger(metrics.turnsTotal) }}
            </div>
            <div class="mt-1 text-[11px] leading-4 text-[var(--color-text-faint)]">
              有效
              <span :title="formatInteger(metrics.validTurnsTotal)">
                {{ formatCompactInteger(metrics.validTurnsTotal) }}
              </span>
              / 异常
              <span :title="formatInteger(metrics.invalidTurnsTotal)">
                {{ formatCompactInteger(metrics.invalidTurnsTotal) }}
              </span>
            </div>
          </div>
        </div>

        <div class="flex min-h-0 min-w-0 flex-col justify-between gap-1">
          <div class="center-row justify-start gap-1 text-[11px] font-semibold text-[var(--color-text-muted)]">
            <span>LLM 调用</span>
            <Tooltip :content="providerCallsTooltipContent" />
          </div>
          <div>
            <div
              class="metric-value truncate"
              style="font-family: var(--font-num)"
              :title="formatInteger(metrics.providerCallsTotal)"
            >
              {{ formatCompactInteger(metrics.providerCallsTotal) }}
            </div>
            <div class="mt-1 text-[11px] leading-4 text-[var(--color-text-faint)]">
              上游调用次数
            </div>
          </div>
        </div>

        <div class="flex min-h-0 min-w-0 flex-col justify-between gap-1">
          <div class="center-row justify-start gap-1 text-[11px] font-semibold text-[var(--color-text-muted)]">
            <span>Token 消耗</span>
            <Tooltip :content="tokensTooltipContent" />
          </div>
          <div>
            <div
              class="metric-value truncate"
              style="font-family: var(--font-num)"
              :title="formatInteger(metrics.requestTokensTotal)"
            >
              {{ formatCompactInteger(metrics.requestTokensTotal) }}
            </div>
            <div class="mt-1 text-[11px] leading-4 text-[var(--color-text-faint)]">
              Prompt
              <span :title="formatInteger(metrics.promptTokensTotal)">
                {{ formatCompactInteger(metrics.promptTokensTotal) }}
              </span>
            </div>
          </div>
        </div>

        <div class="flex min-h-0 min-w-0 flex-col justify-between gap-1">
          <div class="center-row justify-start gap-1 text-[11px] font-semibold text-[var(--color-text-muted)]">
            <span>价值估算</span>
            <Tooltip :content="costTooltipContent" />
          </div>
          <div>
            <div
              class="metric-value is-brand truncate"
              style="font-family: var(--font-num)"
              :title="formatUSD(estimatedTokenCost.total)"
            >
              {{ formatUSD(estimatedTokenCost.total) }}
            </div>
            <div class="mt-1 text-[11px] leading-4 text-[var(--color-text-faint)]">
              缓存读写
              <span :title="formatUSD(estimatedTokenCost.cacheRead + estimatedTokenCost.cacheWrite)">
                {{ formatUSD(estimatedTokenCost.cacheRead + estimatedTokenCost.cacheWrite) }}
              </span>
            </div>
          </div>
        </div>
      </div>
      <div
        v-if="error"
        class="px-[var(--card-pad)] pb-3"
      >
        <div
          class="rounded-[10px] border border-[var(--color-error-border)] bg-[var(--color-error-bg)] px-3 py-2 text-xs text-[var(--color-error-text)]"
        >
          {{ error }}
        </div>
      </div>
      <div
        v-if="$slots.analytics"
        class="border-t border-[var(--color-border)] px-[var(--card-pad)] py-4"
      >
        <slot name="analytics" />
      </div>
      </div>
    </div>
  </div>
</template>

<style scoped></style>
