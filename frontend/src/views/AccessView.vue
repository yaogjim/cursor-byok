<script setup>
import GatewayCard from "@/components/GatewayCard.vue";
import CursorView from "@/views/CursorView.vue";
import UnsupportedClientPanel from "@/views/UnsupportedClientPanel.vue";
import { appState, configSectionDirty } from "@/state/appState";
import {
  accessRouteLocation,
  parseAccessClient,
} from "@/router/access";
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";

const route = useRoute();
const router = useRouter();

const activeClient = computed(() => parseAccessClient(route.query.client));

const clients = computed(() => [
  {
    id: "gateway",
    name: "共享入口",
    meta: gatewayMeta.value,
    status: gatewayStatus.value,
    supported: true,
    dirty: Boolean(configSectionDirty.gateway),
    icon: "icon-[mdi--web]",
    iconClass: "is-gateway",
  },
  {
    id: "cursor",
    name: "Cursor",
    meta: cursorMeta.value,
    status: cursorStatus.value,
    supported: true,
    dirty: Boolean(configSectionDirty.cursor),
    icon: "icon-[mdi--cursor-default-click-outline]",
    iconClass: "is-cursor",
  },
  {
    id: "codex",
    name: "Codex",
    meta: "深度集成 · 尚未支持",
    status: "unsupported",
    supported: false,
    dirty: false,
    icon: "icon-[bxl--openai]",
    iconClass: "is-codex",
  },
  {
    id: "claude",
    name: "Claude Code",
    meta: "深度集成 · 尚未支持",
    status: "unsupported",
    supported: false,
    dirty: false,
    icon: "icon-[logos--claude-icon]",
    iconClass: "is-claude",
  },
]);

const gatewayStatus = computed(() => {
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

const cursorStatus = computed(() => {
  if (appState.serviceLastError && !appState.serviceRunning) {
    return "error";
  }
  if (appState.serviceRunning) {
    return "running";
  }
  return "stopped";
});

const gatewayMeta = computed(() => {
  if (!appState.gatewayEnabled) {
    return "Gateway · 未启用";
  }
  if (appState.gatewayRunning) {
    return "Gateway · 运行中";
  }
  return "Gateway · 已启用";
});

const cursorMeta = computed(() => {
  if (appState.serviceRunning) {
    return "深度集成 · 运行中";
  }
  return "深度集成 · 未启动";
});

function selectClient(client) {
  const next = parseAccessClient(client);
  if (next === activeClient.value) {
    return;
  }
  void router.replace(accessRouteLocation(next));
}
</script>

<template>
  <div class="page-shell page-shell--fill gap-3 text-[var(--color-text)]">
    <div class="page-title-row">
      <div class="page-title-block">
        <h2 class="page-title">接入中心</h2>
      </div>
    </div>

    <div class="access-split min-h-0 flex-1">
      <aside class="access-master" aria-label="接入方式">
        <button
          v-for="item in clients"
          :key="item.id"
          type="button"
          class="access-client"
          :class="activeClient === item.id ? 'is-active' : ''"
          :aria-pressed="activeClient === item.id"
          @click="selectClient(item.id)"
        >
          <span class="access-client-icon" :class="item.iconClass" aria-hidden="true">
            <span :class="item.icon" />
          </span>
          <span class="access-client-info">
            <span class="access-client-name">
              <span class="truncate">{{ item.name }}</span>
              <span
                v-if="item.dirty"
                class="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--color-warning-text)]"
                aria-label="有未保存更改"
              />
            </span>
            <span class="access-client-meta truncate">{{ item.meta }}</span>
          </span>
          <span
            v-if="item.status === 'unsupported'"
            class="badge-soon"
          >
            计划中
          </span>
          <span
            v-else
            class="status-pill is-dot"
            :class="{
              'is-ok': item.status === 'running',
              'is-err': item.status === 'error',
              'is-off': item.status !== 'running' && item.status !== 'error',
            }"
            :aria-label="
              item.status === 'running'
                ? '运行中'
                : item.status === 'error'
                  ? '异常'
                  : item.status === 'disabled'
                    ? '未启用'
                    : '未启动'
            "
          >
            <i aria-hidden="true" />
          </span>
        </button>
        <p class="note plain mt-2">
          Codex 与 Claude Code 仅作入口占位，不会加载示例账号或发起授权。
        </p>
      </aside>

      <section
        class="access-detail min-h-0 overflow-hidden"
        :class="activeClient === 'cursor' ? 'is-embedded-pane' : ''"
        :aria-label="`${activeClient} 配置`"
      >
        <GatewayCard v-if="activeClient === 'gateway'" />
        <CursorView v-else-if="activeClient === 'cursor'" embedded />
        <UnsupportedClientPanel v-else :client="activeClient" />
      </section>
    </div>
  </div>
</template>