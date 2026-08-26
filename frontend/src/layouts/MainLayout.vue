<script setup>
import { Browser, Window } from "@wailsio/runtime";
import LocaleSelect from "@/components/LocaleSelect.vue";
import { useMessage } from "@/composables/useMessage";
import { isMainWindowPath } from "@/router";
import {
  appState,
  checkForAppUpdates,
  syncServiceState,
  updateViewState,
} from "@/state/appState";
import { isWindows } from "@/utils/isWindows";
import { computed, onMounted, onUnmounted } from "vue";
import { RouterLink, useRoute } from "vue-router";

const route = useRoute();
const message = useMessage();
const title = computed(() => route.meta.title ?? "数据概览");
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

const cursorNavStatus = computed(() => {
  if (appState.serviceLastError && !appState.serviceRunning) {
    return "error";
  }
  if (appState.serviceRunning) {
    return "running";
  }
  return "stopped";
});

const gatewayNavStatus = computed(() => {
  if (!appState.gatewayEnabled) {
    return "disabled";
  }
  if (appState.gatewayLastError && !appState.gatewayRunning) {
    return "error";
  }
  if (appState.gatewayRunning) {
    return "running";
  }
  return "stopped";
});

const navItems = computed(() => [
  { path: "/", label: "概览", title: "数据概览" },
  { path: "/cursor", label: "Cursor", title: "Cursor 集成", status: cursorNavStatus.value },
  {
    path: "/gateway",
    label: "网关",
    title: "网关集成",
    status: gatewayNavStatus.value,
    badge: gatewayNavStatus.value === "disabled" ? "未启用" : "",
  },
  { path: "/models", label: "模型", title: "上游模型" },
  { path: "/settings", label: "设置", title: "系统设置" },
]);

function statusDotClass(status) {
  if (status === "running") {
    return "bg-emerald-500";
  }
  if (status === "error") {
    return "bg-[var(--color-solid-error)]";
  }
  if (status === "disabled") {
    return "border border-[var(--color-border-strong)] bg-transparent";
  }
  return "bg-[var(--color-text-muted)]";
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

onMounted(() => {
  proxyStateTimer = window.setInterval(() => {
    if (showFooter.value) {
      void syncServiceState().catch(() => {});
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
  <div class="flex h-screen w-screen min-h-0 min-w-0 overflow-hidden flex-col">
    <div
      class="fixed top-0 w-screen h-[40px] z-9999 w-full"
      style="--wails-draggable: drag"
    ></div>

    <header
      class="flex h-[40px] center-row px-[20px] w-full min-h-0 shrink-0 justify-between relative"
      style="--wails-draggable: drag"
      :class="{ '!justify-center': !isWindows }"
    >
      <div class="center-row gap-2" style="font-family: var(--font-num);">
        <div>{{ title }}</div>
      </div>
      <div
        v-if="isWindows"
        class="absolute right-[10px] top-[8px] z-99999 center-row gap-[1px]"
      >
        <button
          class="text-[20px] center-row justify-center w-[30px] h-[23px] rounded-[4px] text-[var(--color-text-muted)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)] cursor-pointer"
          @click="minimizeWindow"
        >
          <span class="icon-[ic--round-minus]"></span>
        </button>
        <button
          class="text-[20px] center-row justify-center w-[30px] h-[23px] rounded-[4px] text-[var(--color-text-muted)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)] cursor-pointer"
          @click="closeWindow"
        >
          <span class="icon-[ic--round-close]"></span>
        </button>
      </div>
    </header>

    <div class="flex min-h-0 min-w-0 flex-1">
      <aside
        class="flex w-[184px] shrink-0 flex-col gap-1 border-r border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-3"
      >
        <RouterLink
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          :title="item.title"
          class="center-row h-[34px] gap-2 rounded-[8px] px-3 text-sm transition-colors duration-150"
          :class="route.path === item.path
            ? 'bg-[var(--color-success-bg)] text-[var(--color-text)]'
            : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'"
        >
          <span class="min-w-0 flex-1 truncate">{{ item.label }}</span>
          <span
            v-if="item.badge"
            class="shrink-0 text-[10px] leading-none text-[var(--color-text-muted)]"
          >{{ item.badge }}</span>
          <span
            v-else-if="item.status"
            class="h-2 w-2 shrink-0 rounded-full"
            :class="statusDotClass(item.status)"
          />
        </RouterLink>
      </aside>

      <main class="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
        <router-view />
      </main>
    </div>

    <footer
      v-if="showFooter"
      class="flex !pr-1 h-[30px] shrink-0 items-center gap-[8px] border-t border-[var(--color-border)] px-[14px] text-[12px] text-[var(--color-text-secondary)]"
    >
      <div
        v-if="proxyBadgeText"
        class="center-row  border-none gap-[2px]  border-none  px-[0px] py-[3px] leading-none "
        aria-live="polite"
        :title="proxyBadgeTitle"
      >
        <span class="icon-[mdi--wifi] text-[15px]"></span>
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
        class="center-row shrink-0 gap-[2px]  cursor-pointer rounded-[6px] px-[6px] py-[3px] transition-colors duration-150 hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
        @click="handleOpenUsageDocs"
      >
        <span class="icon-[mdi--file-document-outline] text-[15px]"></span>
        <span>使用教程</span>
      </button>
      <button
        type="button"
        class="center-row shrink-0 gap-[6px] cursor-pointer rounded-[6px] px-[6px] py-[3px] transition-colors duration-150 hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
        @click="handleOpenAuthorHome"
      >
        <span class="icon-[mdi--github] text-[14px]"></span>
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
            ></div>
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
