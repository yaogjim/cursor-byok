<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import HomeMetricsCard from "@/components/HomeMetricsCard.vue";
import HomeAnalyticsPanel from "@/components/HomeAnalyticsPanel.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import { getAdRuntime } from "@/services/clientApi";
import {
  appState,
  appViewState,
  resetHomeMetrics,
  syncHomeMetricsReport,
  syncServiceState,
  toUserError,
} from "@/state/appState";
import { DEFAULT_GATEWAY_LISTEN_ADDR } from "@/state/configProjection";
import { Events } from "@wailsio/runtime";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();
const AD_UPDATED_EVENT = "ad:updated";
const OPEN_AD_EVENT = "cursor:open-ad";
const message = useMessage();

const adRuntime = ref(null);
let unsubscribeAdUpdated = null;

function asString(value) {
  if (typeof value === "string") {
    return value.trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

function asBoolean(value) {
  return value === true || value === "true" || value === 1 || value === "1";
}

const homeAds = computed(() => {
  if (!appState.advertisingEnabled) {
    return [];
  }
  const runtime = adRuntime.value && typeof adRuntime.value === "object" ? adRuntime.value : {};
  const slots = Array.isArray(runtime.slots) && runtime.slots.length > 0 ? runtime.slots : [runtime];
  return slots
    .map((slot, index) => {
      const item = slot && typeof slot === "object" ? slot : {};
      const home = item.home && typeof item.home === "object" ? item.home : {};
      const title = asString(home.title);
      if (
        !title
        || !asBoolean(item.available)
        || !asBoolean(item.enabled)
        || !asString(item.packageHash)
      ) {
        return null;
      }
      return {
        id: asString(item.id) || String(index + 1),
        title,
        subtitle: asString(home.subtitle),
      };
    })
    .filter(Boolean);
});

const cursorProxyAddr = computed(() =>
  asString(appState.proxyListenAddr) || asString(appState.configProxyListenAddr),
);
const cursorBackendAddr = computed(() =>
  asString(appState.backendListenAddr) || asString(appState.configBackendListenAddr),
);
const cursorHasStatus = computed(() =>
  Boolean(
    appState.serviceRunning
    || appState.backendRunning
    || cursorProxyAddr.value
    || cursorBackendAddr.value
    || asString(appState.serviceLastError),
  ),
);
const gatewayListenAddr = computed(() =>
  asString(appState.gatewayRuntimeListenAddr)
  || asString(appState.gatewayListenAddr)
  || DEFAULT_GATEWAY_LISTEN_ADDR,
);
const gatewayPublicModelCount = computed(() =>
  Array.isArray(appState.gatewayPublicModels) ? appState.gatewayPublicModels.length : 0,
);
const gatewayHasStatus = computed(() =>
  Boolean(
    appState.gatewayEnabled
    || appState.gatewayRunning
    || asString(appState.gatewayRuntimeListenAddr)
    || asString(appState.gatewayListenAddr)
    || asString(appState.gatewayLastError)
    || gatewayPublicModelCount.value > 0,
  ),
);

async function syncAdRuntimeQuietly() {
  if (!appState.advertisingEnabled) {
    adRuntime.value = null;
    return;
  }
  try {
    adRuntime.value = await getAdRuntime();
  } catch (_error) {
    adRuntime.value = null;
  }
}

function handleAdUpdated() {
  void syncAdRuntimeQuietly();
}

function handleOpenHomeAd(slotId) {
  window.dispatchEvent(new CustomEvent(OPEN_AD_EVENT, { detail: { slotId: asString(slotId) } }));
}

function showActionError(title, error) {
  const detail = String(error || "服务错误").trim() || "服务错误";
  message(`${title}：${detail}`);
}

async function handleRefreshState() {
  const results = await Promise.allSettled([
    syncServiceState(),
    syncHomeMetricsReport(),
  ]);
  const failed = results.find((result) => result.status === "rejected");
  if (failed) {
    showActionError("刷新失败", toUserError(failed.reason));
    return;
  }
  const reportResult = results[1];
  if (reportResult.status === "fulfilled" && reportResult.value && !reportResult.value.ok) {
    showActionError("刷新失败", reportResult.value.error);
  }
}

async function handleRefreshMetrics() {
  const result = await syncHomeMetricsReport();
  if (result.ok) {
    message("刷新成功");
    return;
  }
  showActionError("刷新失败", result.error);
}

async function handleRangeChange(range) {
  const result = await syncHomeMetricsReport(range);
  if (!result.ok) showActionError("加载趋势失败", result.error);
}

async function handleRetryAnalytics() {
  await syncHomeMetricsReport();
}

async function handleResetMetrics() {
  const confirmed = await showModal({
    title: "重置会话统计",
    content: "只会清零首页会话统计，不会删除会话历史。新请求将从 0 重新累计。",
    confirmText: "确认重置",
  });
  if (!confirmed) {
    return;
  }
  const result = await resetHomeMetrics();
  if (result.ok) {
    message("统计已重置");
    return;
  }
  showActionError("重置失败", result.error);
}

onMounted(() => {
  unsubscribeAdUpdated = Events.On(AD_UPDATED_EVENT, handleAdUpdated);
  void syncAdRuntimeQuietly();
  void syncHomeMetricsReport();
});

onBeforeUnmount(() => {
  if (unsubscribeAdUpdated) {
    unsubscribeAdUpdated();
  }
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto scroll-shadow-bottom p-4 pt-0 text-[var(--color-text)]">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-medium">数据概览</h2>
        <div class="text-sm text-[var(--color-text-secondary)]">
          查看 Cursor / Gateway 是否可用，以及当前会话统计
        </div>
      </div>
      <Button @click="handleRefreshState">刷新</Button>
    </div>

    <div class="grid gap-4 md:grid-cols-2">
      <Card>
        <div class="flex h-full flex-col gap-3">
          <div class="flex items-start justify-between gap-3">
            <div>
              <h3 class="text-sm font-medium">Cursor</h3>
              <div class="mt-1 text-xs text-[var(--color-text-muted)]">本地代理与 Backend</div>
            </div>
            <Button @click="router.push('/cursor')">打开 Cursor</Button>
          </div>
          <template v-if="cursorHasStatus">
            <div class="center-row gap-2 text-sm" :class="appViewState.serviceStatusClass">
              <span
                class="h-2 w-2 rounded-full"
                :class="appState.serviceRunning ? 'bg-emerald-500' : 'bg-[var(--color-text-muted)]'"
              />
              <span>{{ appViewState.serviceStatusText }}</span>
            </div>
            <div class="text-sm text-[var(--color-text-secondary)]">
              {{ cursorProxyAddr || "127.0.0.1:18080" }} / {{ cursorBackendAddr || "127.0.0.1:18090" }}
            </div>
            <div
              v-if="appState.serviceLastError"
              class="text-xs text-[var(--color-error-text)]"
            >
              {{ appState.serviceLastError }}
            </div>
          </template>
          <div v-else class="text-sm text-[var(--color-text-muted)]">
            暂无 Cursor 运行状态
          </div>
        </div>
      </Card>

      <Card>
        <div class="flex h-full flex-col gap-3">
          <div class="flex items-start justify-between gap-3">
            <div>
              <h3 class="text-sm font-medium">Gateway</h3>
              <div class="mt-1 text-xs text-[var(--color-text-muted)]">本机 HTTP 入口</div>
            </div>
            <Button @click="router.push('/gateway')">打开网关</Button>
          </div>
          <template v-if="gatewayHasStatus">
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
              <span class="text-[var(--color-text-secondary)]">
                {{ appState.gatewayEnabled ? "已配置启用" : "未配置启用" }}
              </span>
              <span class="center-row gap-2">
                <span
                  class="h-2 w-2 rounded-full"
                  :class="appState.gatewayRunning ? 'bg-emerald-500' : 'bg-[var(--color-text-muted)]'"
                />
                <span>{{ appState.gatewayRunning ? "运行中" : "未运行" }}</span>
              </span>
            </div>
            <div class="text-sm text-[var(--color-text-secondary)]">
              {{ gatewayListenAddr }}　公开模型 {{ gatewayPublicModelCount }} 个
            </div>
            <div
              v-if="appState.gatewayLastError"
              class="text-xs text-[var(--color-error-text)]"
            >
              {{ appState.gatewayLastError }}
            </div>
          </template>
          <div v-else class="text-sm text-[var(--color-text-muted)]">
            暂无 Gateway 运行状态
          </div>
        </div>
      </Card>
    </div>

    <HomeMetricsCard
      :metrics="appState.homeMetrics"
      :loading="appState.homeMetricsLoading"
      :resetting="appState.homeMetricsResetting"
      :error="appState.homeMetricsError"
      :home-ads="homeAds"
      @refresh="handleRefreshMetrics"
      @reset="handleResetMetrics"
      @open-ad="handleOpenHomeAd"
    />

    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-sm font-medium">统计趋势</h3>
      <div class="flex flex-wrap gap-1">
        <button v-for="range in [{ value: '7d', label: '近 7 天' }, { value: '30d', label: '近 30 天' }, { value: 'all', label: '全部' }]" :key="range.value" type="button" class="rounded-[6px] border px-2 py-1 text-xs" :class="appState.homeMetricsRange === range.value ? 'border-[var(--color-primary)] text-[var(--color-primary)]' : 'border-[var(--color-border)] text-[var(--color-text-secondary)]'" @click="handleRangeChange(range.value)">{{ range.label }}</button>
      </div>
    </div>
    <HomeAnalyticsPanel
      :report="appState.homeMetricsReport"
      :loading="appState.homeMetricsReportLoading"
      :error="appState.homeMetricsReportError"
      @retry="handleRetryAnalytics"
    />
  </div>
</template>
