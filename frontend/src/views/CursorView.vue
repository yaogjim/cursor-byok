<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Select from "@/components/ui/Select.vue";
import CursorAccountCard from "@/components/CursorAccountCard.vue";
import { useMessage } from "@/composables/useMessage";
import {
  appState,
  appViewState,
  configSectionDirty,
  configSectionStatusText,
  persistScopedUserConfig,
  ROUTE_MODE_OPTIONS,
  syncServiceState,
  toUserError,
  toggleService,
} from "@/state/appState";

const message = useMessage();
const routeModeOptions = ROUTE_MODE_OPTIONS;

const cursorProxyAddr = () =>
  appState.proxyListenAddr || appState.configProxyListenAddr || "127.0.0.1:18080";
const cursorBackendAddr = () =>
  appState.backendListenAddr || appState.configBackendListenAddr || "127.0.0.1:18090";

function showActionError(title, error) {
  const detail = String(error || "服务错误").trim() || "服务错误";
  message(`${title}：${detail}`);
}

async function handleToggleService() {
  const result = await toggleService();
  if (!result.ok) {
    showActionError("服务操作失败", result.error);
  }
}

async function handleRefreshState() {
  try {
    await syncServiceState();
  } catch (error) {
    showActionError("刷新失败", toUserError(error));
  }
}

async function handleSavePage() {
  const result = await persistScopedUserConfig("cursor");
  if (!result.ok) {
    showActionError("保存失败", result.error);
    return;
  }
  message("本页配置已保存");
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto scroll-shadow-bottom p-4 pt-0 text-[var(--color-text)]">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-medium">Cursor 集成</h2>
        <div class="text-sm text-[var(--color-text-secondary)]">
          Cursor 的本地代理和 Backend 运行控制
        </div>
      </div>
      <div class="center-row gap-2">
        <span
          class="text-xs"
          :class="configSectionDirty.cursor ? 'text-amber-700 dark:text-amber-300' : 'text-[var(--color-text-muted)]'"
        >
          {{ configSectionStatusText("cursor") }}
        </span>
        <Button @click="handleRefreshState">刷新</Button>
        <Button variant="primary" :disabled="appState.configSaving" @click="handleSavePage">
          {{ appState.configSaving ? "保存中..." : "保存本页" }}
        </Button>
      </div>
    </div>

    <Card>
      <div class="flex flex-col gap-4">
        <div class="center-row justify-between gap-4">
          <div class="flex min-w-0 flex-col gap-2">
            <div class="center-row gap-2">
              <span
                class="h-2.5 w-2.5 rounded-full"
                :class="appState.serviceRunning ? 'bg-emerald-500' : 'bg-[var(--color-text-muted)]'"
              />
              <div class="text-sm" :class="appViewState.serviceStatusClass">
                {{ appViewState.serviceStatusText }}
              </div>
            </div>
            <div class="text-sm text-[var(--color-text-secondary)]">
              代理 {{ cursorProxyAddr() }}　Backend {{ cursorBackendAddr() }}
            </div>
          </div>
          <Button
            variant="primary"
            :disabled="appState.serviceBusy || (configSectionDirty.cursor && !appState.serviceRunning)"
            @click="handleToggleService"
          >
            <span class="icon-[mdi--pause] text-[16px]" v-if="appState.serviceRunning"></span>
            <span class="icon-[mdi--play] text-[16px]" v-else></span>
            <span>{{ appState.serviceRunning ? "停止 Cursor 服务" : "启动 Cursor 服务" }}</span>
          </Button>
        </div>
        <div
          v-if="configSectionDirty.cursor && !appState.serviceRunning"
          class="text-xs text-amber-700 dark:text-amber-300"
        >
          请先保存本页
        </div>

        <div
          v-if="appState.serviceLastError"
          class="rounded-[8px] border border-[var(--color-error-border)] bg-[var(--color-error-bg)] px-3 py-2 text-sm text-[var(--color-error-text)]"
        >
          {{ appState.serviceLastError }}
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex flex-col gap-4">
        <div class="flex flex-wrap items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium">连接与路由</h2>
            <div class="text-sm text-[var(--color-text-secondary)]">
              控制白名单主链路请求走本地服务，还是回到原始 Cursor 上游地址
            </div>
          </div>
          <div class="w-[220px] max-w-full">
            <Select
              v-model="appState.routingMode"
              :options="routeModeOptions"
              placeholder="选择模式"
            />
          </div>
        </div>
        <div class="text-sm text-[var(--color-text-secondary)]">
          本地代理：{{ cursorProxyAddr() }}　Backend：{{ cursorBackendAddr() }}　Cursor 设置：{{
            appState.cursorSettingsApplied ? "已应用" : "未应用"
          }}
        </div>
        <div class="text-xs text-[var(--color-text-muted)]">
          Gateway 不改变上述 Cursor 链路，使用独立端口 127.0.0.1:18091。
        </div>
      </div>
    </Card>

    <CursorAccountCard />
  </div>
</template>