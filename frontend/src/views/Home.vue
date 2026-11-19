<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import HomeMetricsCard from "@/components/HomeMetricsCard.vue";
import HomeAnalyticsPanel from "@/components/HomeAnalyticsPanel.vue";
import { useMessage } from "@/composables/useMessage";
import { showActionError } from "@/composables/useActionError";
import { showModal } from "@/composables/useModal";
import { accessRouteLocation } from "@/router/access";
import { getAdRuntime, getHomeMetricsReport, launchCursor } from "@/services/clientApi";
import {
  appState,
  appViewState,
  configSectionDirty,
  resetHomeMetrics,
  setRequestListPage,
  startGateway,
  startService,
  stopGateway,
  stopService,
  syncHomeMetricsReport,
  syncRequestList,
  toUserError,
  updateRequestListFilters,
} from "@/state/appState";
import { normalizeHomeMetricsReport } from "@/state/homeMetrics";
import { DEFAULT_GATEWAY_LISTEN_ADDR, countPublishedGatewayModels } from "@/state/configProjection";
import { Events } from "@wailsio/runtime";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();
const AD_UPDATED_EVENT = "ad:updated";
const OPEN_AD_EVENT = "cursor:open-ad";
const message = useMessage();

const adRuntime = ref(null);
let unsubscribeAdUpdated = null;
const analyticsView = ref("trend");
const activityDaily = ref([]);
const activityLoading = ref(false);
const activityError = ref("");
const launchCursorBusy = ref(false);

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
  asString(appState.proxyListenAddr) || asString(appState.configProxyListenAddr) || "127.0.0.1:18080",
);
const cursorBackendAddr = computed(() =>
  asString(appState.backendListenAddr) || asString(appState.configBackendListenAddr) || "127.0.0.1:18090",
);
const gatewayListenAddr = computed(() =>
  asString(appState.gatewayRuntimeListenAddr)
  || asString(appState.gatewayListenAddr)
  || DEFAULT_GATEWAY_LISTEN_ADDR,
);
const gatewayPublicModelCount = computed(() => (
  countPublishedGatewayModels(appState.modelAdapters, appState.gatewayPublicModels)
));
const cursorStartDisabled = computed(() =>
  appState.serviceBusy || (configSectionDirty.cursor && !appState.serviceRunning),
);
const gatewayStartDisabled = computed(() =>
  appState.gatewayBusy
  || !appState.gatewayEnabled
  || configSectionDirty.gateway,
);
const cursorStartDisabledReason = computed(() => (
  configSectionDirty.cursor && !appState.serviceRunning ? "请先保存 Cursor 配置" : ""
));
const gatewayStartDisabledReason = computed(() => (
  !appState.gatewayEnabled || configSectionDirty.gateway ? "需先启用并保存配置" : ""
));

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


function goAccess(client) {
  router.push(accessRouteLocation(client));
}

async function loadActivityDaily() {
  activityLoading.value = true;
  activityError.value = "";
  try {
    const report = normalizeHomeMetricsReport(await getHomeMetricsReport("all"));
    activityDaily.value = report.daily;
  } catch (error) {
    activityDaily.value = [];
    activityError.value = toUserError(error);
  } finally {
    activityLoading.value = false;
  }
}

async function handleRefreshMetrics() {
  const result = await syncHomeMetricsReport();
  await loadActivityDaily();
  if (result.ok) {
    message("刷新成功", { tone: "success" });
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

function requestFailureMessage(result) {
  if (!result || result.ok) {
    return;
  }
  showActionError("加载请求列表失败", result.error);
}

async function loadRequestList() {
  requestFailureMessage(await syncRequestList());
}

async function loadRequestListWithOptions() {
  requestFailureMessage(await updateRequestListFilters({}));
}

async function handleRequestRangeChange(range) {
  requestFailureMessage(await updateRequestListFilters({ range }));
}

async function handleRequestProviderChange(provider) {
  requestFailureMessage(await updateRequestListFilters({ provider }));
}

async function handleRequestModelChange(model) {
  requestFailureMessage(await updateRequestListFilters({ model }));
}

async function handleRequestStatusChange(status) {
  requestFailureMessage(await updateRequestListFilters({ status }));
}

async function handleRequestPreviousPage() {
  requestFailureMessage(await setRequestListPage(Number(appState.requestListResult?.page || 1) - 1));
}

async function handleRequestNextPage() {
  requestFailureMessage(await setRequestListPage(Number(appState.requestListResult?.page || 1) + 1));
}

async function handleAnalyticsViewChange(view) {
  analyticsView.value = view;
  if (view === "requests") {
    await loadRequestListWithOptions();
  }
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
  await loadActivityDaily();
  if (result.ok) {
    message("统计已重置", { tone: "success" });
    return;
  }
  showActionError("重置失败", result.error);
}

async function handleCursorStart() {
  const result = await startService();
  if (result.cancelled) {
    return;
  }
  if (result.partial) {
    message(result.error, { tone: "error" });
    return;
  }
  if (!result.ok) {
    showActionError("启动失败", result.error);
  }
}

async function handleCursorStop() {
  const result = await stopService();
  if (!result.ok) {
    showActionError("停止失败", result.error);
  }
}

async function handleGatewayStart() {
  if (!appState.gatewayEnabled) {
    message("需先启用并保存配置", { tone: "info" });
    return;
  }
  const result = await startGateway();
  if (!result.ok) {
    showActionError("启动失败", result.error);
  }
}

async function handleGatewayStop() {
  const result = await stopGateway();
  if (!result.ok) {
    showActionError("停止失败", result.error);
  }
}

async function handleLaunchCursor() {
  if (launchCursorBusy.value) {
    return;
  }
  launchCursorBusy.value = true;
  try {
    await launchCursor();
    message("已请求打开 Cursor", { tone: "success" });
  } catch (error) {
    showActionError("打开 Cursor 失败", toUserError(error));
  } finally {
    launchCursorBusy.value = false;
  }
}

onMounted(() => {
  unsubscribeAdUpdated = Events.On(AD_UPDATED_EVENT, handleAdUpdated);
  void syncAdRuntimeQuietly();
  void syncHomeMetricsReport();
  void loadActivityDaily();
});

onBeforeUnmount(() => {
  if (unsubscribeAdUpdated) {
    unsubscribeAdUpdated();
  }
});
</script>

<template>
  <div class="page-shell flex h-full min-h-0 flex-col gap-4 overflow-y-auto scroll-shadow-bottom text-[var(--color-text)]">
    <div class="grid grid-cols-2 gap-3 max-[760px]:grid-cols-1">
      <Card :padded="false" class="home-service-card flex h-full flex-col">
        <div class="ui-card-body flex flex-1 flex-col gap-2">
          <div class="flex items-center justify-between gap-3">
            <div>
              <h3 class="card-title">Cursor</h3>
              <div class="card-sub">本地代理与 Backend</div>
            </div>
            <span
              class="status-pill"
              :class="appState.serviceRunning ? 'is-ok' : (appState.serviceLastError ? 'is-err' : 'is-off')"
            >
              <i aria-hidden="true" />
              {{ appViewState.serviceStatusText }}
            </span>
          </div>
          <div class="flex flex-nowrap gap-2 overflow-hidden">
            <span class="mono-chip">代理 {{ cursorProxyAddr }}</span>
            <span class="mono-chip">Backend {{ cursorBackendAddr }}</span>
          </div>
          <div
            v-if="appState.serviceLastError"
            class="text-xs text-[var(--color-error-text)]"
          >
            {{ appState.serviceLastError }}
          </div>
          <div
            v-if="configSectionDirty.cursor && !appState.serviceRunning"
            class="text-xs text-[var(--color-warning-text)]"
          >
            请先保存 Cursor 配置
          </div>
        </div>
        <div class="ui-card-foot mt-auto">
          <Button
            v-if="appState.serviceRunning"
            class="btn-sm btn-risk"
            :disabled="appState.serviceBusy"
            @click="handleCursorStop"
          >
            {{ appState.serviceBusy ? "关闭中..." : "停止服务" }}
          </Button>
          <Button
            v-else
            class="btn-sm"
            :variant="cursorStartDisabled ? 'default' : 'primary'"
            :disabled="cursorStartDisabled"
            :disabled-reason="cursorStartDisabledReason"
            @click="handleCursorStart"
          >
            {{ appState.serviceBusy ? "启动中..." : "启动服务" }}
          </Button>
          <Button class="btn-sm" :disabled="launchCursorBusy" @click="handleLaunchCursor">
            {{ launchCursorBusy ? "打开中..." : "打开 Cursor" }}
          </Button>
          <Button variant="text" class="btn-sm ml-auto" @click="goAccess('cursor')">配置 →</Button>
        </div>
      </Card>

      <Card :padded="false" class="home-service-card flex h-full flex-col">
        <div class="ui-card-body flex flex-1 flex-col gap-2">
          <div class="flex items-center justify-between gap-3">
            <div>
              <h3 class="card-title">Gateway</h3>
              <div class="card-sub">本机 HTTP 入口 · 外部客户端</div>
            </div>
            <span
              class="status-pill"
              :class="appState.gatewayRunning ? 'is-ok' : (appState.gatewayLastError ? 'is-err' : 'is-off')"
            >
              <i aria-hidden="true" />
              {{
                appState.gatewayRunning
                  ? "运行中"
                  : appState.gatewayEnabled
                    ? "未运行"
                    : "未启用"
              }}
            </span>
          </div>
          <div class="flex flex-nowrap gap-2 overflow-hidden">
            <span class="mono-chip">{{ gatewayListenAddr }}</span>
            <span class="mono-chip">公开模型 {{ gatewayPublicModelCount }} 个</span>
          </div>
          <div
            v-if="appState.gatewayLastError"
            class="text-xs text-[var(--color-error-text)]"
          >
            {{ appState.gatewayLastError }}
          </div>
          <div
            v-if="configSectionDirty.gateway && !appState.gatewayRunning"
            class="text-xs text-[var(--color-warning-text)]"
          >
            请先保存 Gateway 配置
          </div>
        </div>
        <div class="ui-card-foot mt-auto">
          <Button
            v-if="appState.gatewayRunning"
            class="btn-sm btn-risk"
            :disabled="appState.gatewayBusy"
            @click="handleGatewayStop"
          >
            {{ appState.gatewayBusy ? "关闭中..." : "停止 Gateway" }}
          </Button>
          <Button
            v-else
            class="btn-sm"
            :variant="gatewayStartDisabled ? 'default' : 'primary'"
            :disabled="gatewayStartDisabled"
            :disabled-reason="gatewayStartDisabledReason"
            @click="handleGatewayStart"
          >
            {{ appState.gatewayBusy ? "启动中..." : "启动 Gateway" }}
          </Button>
          <Button variant="text" class="btn-sm ml-auto" @click="goAccess('gateway')">
            {{ appState.gatewayEnabled ? "配置 →" : "去配置 →" }}
          </Button>
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
    >
      <template #analytics>
        <HomeAnalyticsPanel
          :report="appState.homeMetricsReport"
          :activity-daily="activityDaily"
          :range="appState.homeMetricsRange"
          :view="analyticsView"
          :loading="appState.homeMetricsReportLoading"
          :error="appState.homeMetricsReportError"
          :activity-loading="activityLoading"
          :activity-error="activityError"
          :request-list="appState.requestListResult"
          :request-options="appState.requestListOptions"
          :request-range="appState.requestListRange"
          :request-provider="appState.requestListProvider"
          :request-model="appState.requestListModel"
          :request-status="appState.requestListStatus"
          :request-loading="appState.requestListLoading"
          :request-error="appState.requestListError"
          @range-change="handleRangeChange"
          @view-change="handleAnalyticsViewChange"
          @request-range-change="handleRequestRangeChange"
          @request-provider-change="handleRequestProviderChange"
          @request-model-change="handleRequestModelChange"
          @request-status-change="handleRequestStatusChange"
          @request-previous-page="handleRequestPreviousPage"
        @request-next-page="handleRequestNextPage"
          @request-retry="loadRequestList"
          @retry="handleRetryAnalytics"
          @retry-activity="loadActivityDaily"
        />
      </template>
    </HomeMetricsCard>
  </div>
</template>