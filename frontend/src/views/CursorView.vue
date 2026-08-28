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
  reloadUserConfig,
  ROUTE_MODE_OPTIONS,
  syncServiceState,
  toUserError,
  toggleService,
} from "@/state/appState";

defineProps({
  embedded: {
    type: Boolean,
    default: false,
  },
});

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

async function handleReloadPage() {
  try {
    await reloadUserConfig();
  } catch (error) {
    showActionError("重新加载失败", toUserError(error));
  }
}
</script>

<template>
  <Card
    :padded="false"
    class="compact-card h-full p-0"
  >
    <div v-if="!embedded" class="page-title-row px-4 pt-4">
      <h2 class="page-title">Cursor 集成</h2>
    </div>
    <div class="compact-card-body">
      <div class="card-h">
        <div>
          <h2 class="card-title">Cursor 深度集成</h2>
          <div class="card-sub">本地代理、Backend 与控制面账号</div>
        </div>
        <span
          class="status-pill"
          :class="appState.serviceRunning ? 'is-ok' : (appState.serviceLastError ? 'is-err' : 'is-off')"
        >
          <i aria-hidden="true" />
          {{ appViewState.serviceStatusText }}
        </span>
      </div>

      <div class="grid2">
        <div class="field">
          <span class="field-l">本地服务</span>
          <div class="row-inline min-h-10 flex-wrap">
            <span class="mono-chip">{{ cursorProxyAddr() }}</span>
            <span class="mono-chip">{{ cursorBackendAddr() }}</span>
          </div>
        </div>
        <label class="field">
          <span class="field-l">路由模式</span>
          <Select
            v-model="appState.routingMode"
            :options="routeModeOptions"
            placeholder="选择模式"
          />
        </label>
      </div>

      <div
        v-if="appState.serviceLastError"
        class="mt-3 rounded-[8px] border border-[var(--color-error-border)] bg-[var(--color-error-bg)] px-3 py-2 text-sm text-[var(--color-error-text)]"
      >
        {{ appState.serviceLastError }}
      </div>
      <div
        v-if="configSectionDirty.cursor && !appState.serviceRunning"
        class="mt-2 text-xs text-[var(--color-warning-text)]"
      >
        请先保存本页
      </div>

      <div class="hr" />

      <div class="setting-row">
        <div class="setting-l">
          <div class="setting-t">Cursor 设置</div>
          <div class="setting-s">
            {{ appState.cursorSettingsApplied ? "本地代理设置已应用" : "本地代理设置尚未应用" }}
          </div>
        </div>
        <span
          class="status-pill"
          :class="appState.cursorSettingsApplied ? 'is-ok' : 'is-off'"
        >
          {{ appState.cursorSettingsApplied ? "已应用" : "未应用" }}
        </span>
      </div>

      <CursorAccountCard flush />
    </div>

    <div class="config-action-bar">
      <div class="config-action-buttons">
        <Button
          class="btn-sm"
          :class="appState.serviceRunning ? 'btn-risk' : ''"
          :disabled="appState.serviceBusy || (configSectionDirty.cursor && !appState.serviceRunning)"
          @click="handleToggleService"
        >
          {{ appState.serviceRunning ? "停止服务" : "启动服务" }}
        </Button>
        <Button class="btn-sm" @click="handleRefreshState">刷新</Button>
      </div>
      <span
        class="config-action-status spread"
        :class="configSectionDirty.cursor ? 'is-dirty' : ''"
      >
        {{ configSectionStatusText("cursor") }}
      </span>
      <div class="config-action-buttons">
        <Button variant="text" class="btn-sm" :disabled="appState.configSaving" @click="handleReloadPage">
          重新加载
        </Button>
        <Button variant="primary" class="btn-sm" :disabled="appState.configSaving || !configSectionDirty.cursor" @click="handleSavePage">
          {{ appState.configSaving ? "保存中..." : "保存 Cursor 配置" }}
        </Button>
      </div>
    </div>
  </Card>
</template>