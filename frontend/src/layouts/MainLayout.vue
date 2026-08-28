<script setup>
import { Browser, Window } from "@wailsio/runtime";
import LocaleSelect from "@/components/LocaleSelect.vue";
import { useMessage } from "@/composables/useMessage";
import { ACCESS_PATH, accessRouteLocation, parseAccessClient } from "@/router/access";
import { isMainWindowPath } from "@/router";
import {
  appState,
  checkForAppUpdates,
  configSectionDirty,
  syncServiceState,
  updateViewState,
} from "@/state/appState";
import { isWindows } from "@/utils/isWindows";
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { RouterLink, useRoute } from "vue-router";

const route = useRoute();
const message = useMessage();
const directlyClose = computed(() => route.meta.directlyClose === true);
const showFooter = computed(() => isMainWindowPath(route.path));
const AUTHOR_REPOSITORY_URL = "https://github.com/leookun/cursor-byok";
const AUTHOR_LABEL = "@leookun";
const usageDocsURL = "https://docs.leokun.cn";
let proxyStateTimer = null;
const proxyStatePollIntervalMs = 10000;
const netProxyEndpoint = computed(
  () => appState.netProxyHttps || appState.netProxyHttp || "",
);
const proxyBadgeText = computed(() => {
  if (appState.netProxyUsingSystem) {
    return "已识别系统代理";
  }
  return "";
});
const proxyBadgeTitle = computed(() => {
  if (appState.netProxyUsingSystem) {
    return netProxyEndpoint.value
      ? `当前出站请求使用系统代理：${netProxyEndpoint.value}`
      : "当前出站请求使用系统代理";
  }
  if (appState.netProxyUsingEnv) {
    return netProxyEndpoint.value
      ? `当前出站请求使用环境变量代理：${netProxyEndpoint.value}`
      : "当前出站请求使用环境变量代理";
  }
  if (appState.netProxyPacIgnored) {
    return "检测到系统 PAC/自动代理，当前版本按直连处理";
  }
  return "当前出站请求未使用系统代理";
});

const accessConnectedCount = computed(() => {
  let count = 0;
  if (appState.serviceRunning) {
    count += 1;
  }
  if (appState.gatewayRunning) {
    count += 1;
  }
  return count;
});

const lastStateRefreshAt = ref(null);
const lastStateRefreshText = computed(() => {
  const value = lastStateRefreshAt.value;
  if (!(value instanceof Date)) {
    return "";
  }
  const hour = String(value.getHours()).padStart(2, "0");
  const minute = String(value.getMinutes()).padStart(2, "0");
  return `状态更新于 ${hour}:${minute}`;
});
const lastAccessClient = ref(parseAccessClient(route.query.client));

watch(
  () => (route.path === ACCESS_PATH ? parseAccessClient(route.query.client) : ""),
  (client) => {
    if (client) {
      lastAccessClient.value = client;
    }
  },
  { immediate: true },
);

const accessTabTo = computed(() => accessRouteLocation(lastAccessClient.value));

const navItems = computed(() => [
  {
    path: "/",
    to: "/",
    label: "总览",
    title: "数据概览",
    icon: "icon-[mdi--home-outline]",
  },
  {
    path: ACCESS_PATH,
    to: accessTabTo.value,
    label: "接入",
    title: "接入中心",
    icon: "icon-[mdi--lightning-bolt-outline]",
    badge: accessConnectedCount.value > 0 ? `${accessConnectedCount.value} 已接入` : "",
    dirty: Boolean(configSectionDirty.access),
  },
  {
    path: "/models",
    to: "/models",
    label: "模型",
    title: "上游模型",
    icon: "icon-[mdi--server-outline]",
    badge: String((appState.modelAdapters || []).length),
    dirty: Boolean(configSectionDirty.models),
  },
  {
    path: "/settings",
    to: "/settings",
    label: "设置",
    title: "系统设置",
    icon: "icon-[mdi--cog-outline]",
    dirty: Boolean(configSectionDirty.settings),
  },
]);

function isNavActive(item) {
  return route.path === item.path;
}

async function minimizeWindow() {
  await Window.Minimise();
}

async function closeWindow() {
  if (directlyClose.value) {
    await Window.Close();
    return;
  }
  await new Promise((resolve) => setTimeout(resolve, 200));
  await Window.Hide();
}

async function handleCheckForUpdates() {
  if (updateViewState.footerBusy || updateViewState.footerDownloading) {
    return;
  }
  const loadingMessageID = message("检查更新中...", { duration: 0 });
  try {
    await checkForAppUpdates();
  } finally {
    if (loadingMessageID) {
      message.remove(loadingMessageID);
    }
  }
}

function showActionError(title, error) {
  const detail = String(error || "操作失败").trim() || "操作失败";
  message(`${title}：${detail}`);
}

async function handleOpenAuthorHome() {
  try {
    await Browser.OpenURL(AUTHOR_REPOSITORY_URL);
  } catch (error) {
    showActionError("打开作者地址失败", error);
  }
}

async function handleOpenUsageDocs() {
  try {
    await Browser.OpenURL(usageDocsURL);
  } catch (error) {
    showActionError("打开使用教程失败", error);
  }
}

async function syncStateWithTimestamp() {
  await syncServiceState();
  lastStateRefreshAt.value = new Date();
}

async function handleRefreshState() {
  try {
    await syncStateWithTimestamp();
  } catch (error) {
    showActionError("刷新失败", error);
  }
}

onMounted(() => {
  void syncStateWithTimestamp().catch(() => {});
  proxyStateTimer = window.setInterval(() => {
    if (showFooter.value) {
      void syncStateWithTimestamp().catch(() => {});
    }
  }, proxyStatePollIntervalMs);
});

onUnmounted(() => {
  if (proxyStateTimer) {
    window.clearInterval(proxyStateTimer);
    proxyStateTimer = null;
  }
});
</script>

<template>
  <div class="flex h-screen w-screen min-h-0 min-w-0 overflow-hidden flex-col bg-[var(--color-app-bg)]">
    <header
      class="app-tabbar"
      :class="{ 'app-tabbar--mac': !isWindows }"
      style="--wails-draggable: drag"
    >
      <div class="app-brand shrink-0">
        <div class="app-brand-mark" aria-hidden="true">C</div>
        <div class="app-brand-name">Cursor 助手</div>
      </div>

      <nav class="app-tabs relative z-10" aria-label="主导航" style="--wails-draggable: no-drag">
        <RouterLink
          v-for="item in navItems"
          :key="item.path"
          :to="item.to"
          :title="item.title"
          class="app-tab"
          :class="isNavActive(item) ? 'is-active' : ''"
          :aria-current="isNavActive(item) ? 'page' : undefined"
        >
          <span :class="[item.icon, 'text-[17px]']" aria-hidden="true" />
          <span>{{ item.label }}</span>
          <span v-if="item.badge" class="app-tab-tag" :class="item.path === ACCESS_PATH && accessConnectedCount > 0 ? 'is-live' : ''">
            {{ item.badge }}
          </span>
          <span
            v-if="item.dirty"
            class="app-tab-dirty"
            aria-label="有未保存更改"
          />
        </RouterLink>
      </nav>

      <div class="app-tabbar-r relative z-10" style="--wails-draggable: no-drag">
        <span v-if="lastStateRefreshText" class="app-state-updated">
          {{ lastStateRefreshText }}
        </span>
        <button
          type="button"
          class="app-icon-btn"
          title="刷新运行状态"
          aria-label="刷新运行状态"
          @click="handleRefreshState"
        >
          <span class="icon-[mdi--refresh] text-[18px]" aria-hidden="true" />
        </button>
        <div
          v-if="isWindows"
          class="ml-1 flex items-center gap-[1px]"
        >
          <button
            type="button"
            class="text-[20px] center-row justify-center w-[30px] h-[23px] rounded-[4px] text-[var(--color-text-muted)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)] cursor-pointer"
            aria-label="最小化"
            @click="minimizeWindow"
          >
            <span class="icon-[ic--round-minus]" />
          </button>
          <button
            type="button"
            class="text-[20px] center-row justify-center w-[30px] h-[23px] rounded-[4px] text-[var(--color-text-muted)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)] cursor-pointer"
            aria-label="关闭"
            @click="closeWindow"
          >
            <span class="icon-[ic--round-close]" />
          </button>
        </div>
      </div>
    </header>

    <main class="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
      <router-view />
    </main>

    <footer
      v-if="showFooter"
      class="app-statusbar"
    >
      <div
        v-if="proxyBadgeText"
        class="center-row gap-[2px] px-[0px] py-[3px] leading-none"
        aria-live="polite"
        :title="proxyBadgeTitle"
      >
        <span class="icon-[mdi--wifi] text-[15px]" />
        <span class="truncate">{{ proxyBadgeText }}</span>
      </div>
      <button
        v-if="!updateViewState.footerDownloading"
        type="button"
        class="center-row shrink-0 gap-[6px] cursor-pointer rounded-[6px] px-[6px] py-[3px] transition-colors duration-150 hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
        :disabled="updateViewState.footerBusy"
        @click="handleCheckForUpdates"
      >
        <span>{{ updateViewState.footerVersionLabel }}</span>
        <span>检查更新</span>
      </button>
      <button
        type="button"
        class="center-row shrink-0 gap-[2px] cursor-pointer rounded-[6px] px-[6px] py-[3px] transition-colors duration-150 hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
        @click="handleOpenUsageDocs"
      >
        <span class="icon-[mdi--file-document-outline] text-[15px]" />
        <span>使用教程</span>
      </button>
      <button
        type="button"
        class="center-row shrink-0 gap-[6px] cursor-pointer rounded-[6px] px-[6px] py-[3px] transition-colors duration-150 hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
        @click="handleOpenAuthorHome"
      >
        <span class="icon-[mdi--github] text-[14px]" />
        <span>{{ AUTHOR_LABEL }}</span>
      </button>
      <div
        v-if="updateViewState.footerDownloading"
        class="flex min-w-0 flex-1 items-center gap-[10px]"
      >
        <span class="shrink-0">{{ updateViewState.footerVersionLabel }}</span>
        <div class="center-row min-w-0 gap-[8px]">
          <div
            class="h-[6px] w-[120px] overflow-hidden rounded-full bg-[var(--color-surface-muted)]"
          >
            <div
              class="h-full rounded-full bg-gradient-to-r from-[var(--color-primary)] to-[var(--color-primary-hover)]"
              :style="updateViewState.footerProgressStyle"
            />
          </div>
          <span class="shrink-0 text-[var(--color-text)]">{{
            updateViewState.footerProgressText
          }}</span>
        </div>
      </div>
      <div class="ml-auto flex shrink-0 items-center gap-[8px]">
        <LocaleSelect
          :border="false"
          aria-label="界面语言"
          wrapper-class="w-auto"
          button-class="h-[24px] bg-transparent px-1.5 text-[12px] !text-[var(--color-text-secondary)] !hover:text-[var(--color-text)]"
          menu-class="text-[12px]"
        />
      </div>
    </footer>
  </div>
</template>