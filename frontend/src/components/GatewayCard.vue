<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Input from "@/components/ui/Input.vue";
import Select from "@/components/ui/Select.vue";
import Switch from "@/components/ui/Switch.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  appState,
  configSectionDirty,
  configSectionStatusText,
  copyGatewayToken,
  persistScopedUserConfig,
  reloadUserConfig,
  rotateGatewayToken,
  startGateway,
  stopGateway,
  toUserError,
} from "@/state/appState";
import { DEFAULT_GATEWAY_LISTEN_ADDR, gatewayPublicModelInvalid } from "@/state/configProjection";
import copyTextToClipboard from "copy-text-to-clipboard";
import { computed, ref } from "vue";

const message = useMessage();
const gatewayDirty = computed(() => configSectionDirty.gateway);
const scopeOpen = ref(false);

const adapterOptions = computed(() =>
  (appState.modelAdapters || []).map((adapter) => ({
    label: adapter.displayName || adapter.id,
    value: adapter.id,
  })),
);

const gatewayDisplayAddr = computed(() =>
  appState.gatewayRuntimeListenAddr || appState.gatewayListenAddr || DEFAULT_GATEWAY_LISTEN_ADDR,
);

const gatewayBaseURL = computed(() => `http://${gatewayDisplayAddr.value}/v1`);
const publicModelCount = computed(() =>
  Array.isArray(appState.gatewayPublicModels) ? appState.gatewayPublicModels.length : 0,
);
const gatewayIntentText = computed(() =>
  appState.gatewayEnabled ? "配置意图：已启用" : "配置意图：未启用",
);
const gatewayStatusText = computed(() => {
  if (appState.gatewayRunning) {
    return "运行中";
  }
  if (appState.gatewayLastError) {
    return "异常";
  }
  return "未运行";
});
const tokenHint = computed(() =>
  appState.gatewayTokenConfigured
    ? "已生成，仅可通过复制或重新生成读取"
    : "保存启用配置时由后端生成",
);

function showActionError(title, error) {
  const detail = String(error || "服务错误").trim() || "服务错误";
  message(`${title}：${detail}`);
}

function copyTokenValue(token) {
  const text = String(token || "").trim();
  if (!text) {
    return false;
  }
  return Boolean(copyTextToClipboard(text));
}

async function handleCopyBaseURL() {
  const text = String(gatewayBaseURL.value || "").trim();
  if (!text) {
    showActionError("复制失败", "Base URL 不可用");
    return;
  }
  if (!copyTokenValue(text)) {
    showActionError("复制失败", "复制被 WebView 拒绝");
    return;
  }
  message("Base URL 已复制");
}

async function persistGateway() {
  const result = await persistScopedUserConfig("gateway");
  if (!result.ok) {
    showActionError("保存失败", result.error);
    return false;
  }
  return true;
}

async function handleCopyToken() {
  try {
    const token = await copyGatewayToken();
    if (!token) {
      showActionError("复制失败", "尚未生成 Gateway token");
      return;
    }
    if (!copyTokenValue(token)) {
      showActionError("复制失败", "复制被 WebView 拒绝");
      return;
    }
    message("Gateway token 已复制");
  } catch (error) {
    showActionError("复制失败", toUserError(error));
  }
}

async function handleRotateToken() {
  const confirmed = await showModal({
    title: "轮换 Gateway token",
    content: "轮换后旧 token 立即失效。新 token 只会返回这一次，请立即复制。",
    confirmText: "确认轮换",
  });
  if (!confirmed) {
    return;
  }
  try {
    const token = await rotateGatewayToken();
    if (!token) {
      showActionError("轮换失败", "尚未生成 Gateway token");
      return;
    }
    if (!copyTokenValue(token)) {
      showActionError("复制失败", "Gateway token 已轮换，但复制被 WebView 拒绝");
      return;
    }
    message("Gateway token 已轮换并复制");
  } catch (error) {
    showActionError("轮换失败", toUserError(error));
  }
}

async function handleGatewayStart() {
  if (gatewayDirty.value) {
    message("请先保存本页");
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

function addPublicModel() {
  if ((appState.gatewayPublicModels || []).length >= 32) {
    message("公开模型最多 32 个");
    return;
  }
  appState.gatewayPublicModels = [
    ...appState.gatewayPublicModels,
    { id: "", targetAdapterID: "" },
  ];
}

function removePublicModel(index) {
  appState.gatewayPublicModels = appState.gatewayPublicModels.filter((_, current) => current !== index);
}

async function handleSaveGateway() {
  if (!appState.gatewayListenAddr) {
    appState.gatewayListenAddr = DEFAULT_GATEWAY_LISTEN_ADDR;
  }
  const ok = await persistGateway();
  if (ok) {
    message("Gateway 配置已保存");
  }
}

async function handleReloadGateway() {
  try {
    await reloadUserConfig();
  } catch (error) {
    showActionError("重新加载失败", toUserError(error));
  }
}
</script>

<template>
  <Card :padded="false" class="compact-card h-full">
    <div class="compact-card-body">
      <div class="card-h">
        <div>
          <h2 class="card-title">共享入口</h2>
          <div class="card-sub">为外部 AI 客户端提供 OpenAI 兼容的本机 HTTP 接口</div>
        </div>
        <div class="row-inline">
          <span
            class="status-pill"
            :class="appState.gatewayRunning ? 'is-ok' : (appState.gatewayLastError ? 'is-err' : 'is-off')"
          >
            <i aria-hidden="true" />
            {{ gatewayStatusText }}
          </span>
          <Switch
            standalone
            label="配置为启用 Gateway"
            :enabled="appState.gatewayEnabled"
            :disabled="appState.configSaving"
            @change="appState.gatewayEnabled = $event"
          />
        </div>
      </div>

      <div class="grid2">
        <label class="field">
          <span class="field-l">监听地址</span>
          <Input
            v-model="appState.gatewayListenAddr"
            :disabled="appState.configSaving"
            :placeholder="DEFAULT_GATEWAY_LISTEN_ADDR"
          />
          <span class="field-h">默认仅允许本机访问</span>
        </label>
        <div class="field">
          <span class="field-l">运行控制</span>
          <div class="row-inline min-h-10">
            <Button
              v-if="!appState.gatewayRunning"
              class="btn-sm"
              :disabled="appState.gatewayBusy || appState.configSaving || !appState.gatewayEnabled || gatewayDirty"
              :title="!appState.gatewayEnabled || gatewayDirty ? '需先启用并保存配置' : ''"
              @click="handleGatewayStart"
            >
              启动 Gateway
            </Button>
            <Button
              v-else
              class="btn-sm btn-risk"
              :disabled="appState.gatewayBusy"
              @click="handleGatewayStop"
            >
              停止 Gateway
            </Button>
            <span class="card-sub !mt-0">{{ gatewayIntentText }}</span>
          </div>
          <span
            v-if="gatewayDirty && !appState.gatewayRunning"
            class="field-h text-[var(--color-warning-text)]"
          >
            请先保存本页
          </span>
          <span v-else-if="appState.gatewayLastError" class="field-h text-[var(--color-error-text)]">
            {{ appState.gatewayLastError }}
          </span>
        </div>
      </div>

      <div class="hr" />

      <div class="setting-row">
        <div class="setting-l">
          <div class="setting-t">Bearer Token</div>
          <div class="setting-s">{{ tokenHint }}</div>
        </div>
        <div class="row-inline">
          <Button class="btn-sm" :disabled="appState.configSaving" @click="handleCopyToken">
            <span class="icon-[mdi--content-copy] text-[13px]" aria-hidden="true" />
            复制 Token
          </Button>
          <Button class="btn-sm btn-risk" :disabled="appState.configSaving" @click="handleRotateToken">
            重新生成
          </Button>
        </div>
      </div>

      <div class="setting-row">
        <div class="setting-l">
          <div class="setting-t">公开模型别名</div>
          <div class="setting-s">
            {{ publicModelCount }} 个映射；只经显式映射解析，不回落到内部 modelID
          </div>
        </div>
        <Button class="btn-sm" :disabled="appState.configSaving" @click="addPublicModel">
          <span class="icon-[mdi--plus] text-[14px]" aria-hidden="true" />
          添加映射
        </Button>
      </div>
      <div
        v-for="(item, index) in appState.gatewayPublicModels"
        :key="index"
        class="setting-row"
      >
        <div class="grid w-full min-w-0 grid-cols-[1fr_1fr_auto] gap-2">
          <Input v-model="item.id" :disabled="appState.configSaving" placeholder="公开别名，如 grok" />
          <Select
            v-model="item.targetAdapterID"
            :options="adapterOptions"
            placeholder="选择目标适配器"
          />
          <Button class="btn-sm" :disabled="appState.configSaving" @click="removePublicModel(index)">
            删除
          </Button>
          <div
            v-if="gatewayPublicModelInvalid(item, appState.modelAdapters)"
            class="col-span-3 text-xs text-[var(--color-warning-text)]"
          >
            映射已失效，请重新选择目标适配器
          </div>
        </div>
      </div>

      <div class="setting-row">
        <div class="setting-l">
          <div class="setting-t">客户端接入</div>
          <div class="setting-s">
            Base URL <span class="mono-chip">{{ gatewayBaseURL }}</span>
          </div>
        </div>
        <Button class="btn-sm" @click="handleCopyBaseURL">复制 Base URL</Button>
      </div>

      <div class="ui-collapse" :class="{ 'is-open': scopeOpen }">
        <button
          type="button"
          class="ui-collapse-toggle"
          :aria-expanded="scopeOpen"
          @click="scopeOpen = !scopeOpen"
        >
          <span class="icon icon-[mdi--chevron-right]" aria-hidden="true" />
          作用范围与安全说明
        </button>
        <div v-show="scopeOpen" class="ui-collapse-body">
          <div class="note plain">
            只影响 Gateway 的 HTTP 接口、Token 与公开模型映射；不改变 Cursor 18080/18090 链路。Token 明文不写入页面常驻状态、配置投影或日志。本页保存只包含启用状态、监听地址和公开模型映射。
          </div>
        </div>
      </div>
    </div>

    <div class="config-action-bar">
      <span
        class="config-action-status"
        :class="gatewayDirty ? 'is-dirty' : ''"
      >
        {{ configSectionStatusText("gateway") }}
      </span>
      <div class="config-action-buttons spread">
        <Button
          variant="text"
          class="btn-sm"
          :disabled="appState.configSaving"
          @click="handleReloadGateway"
        >
          重新加载
        </Button>
        <Button variant="primary" class="btn-sm" :disabled="appState.configSaving || !gatewayDirty" @click="handleSaveGateway">
          {{ appState.configSaving ? "保存中..." : "保存 Gateway 配置" }}
        </Button>
      </div>
    </div>
  </Card>
</template>