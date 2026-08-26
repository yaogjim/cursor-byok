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
  rotateGatewayToken,
  startGateway,
  stopGateway,
  toUserError,
} from "@/state/appState";
import { DEFAULT_GATEWAY_LISTEN_ADDR, gatewayPublicModelInvalid } from "@/state/configProjection";
import copyTextToClipboard from "copy-text-to-clipboard";
import { computed } from "vue";

const message = useMessage();
const gatewayDirty = computed(() => configSectionDirty.gateway);

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
</script>

<template>
  <Card>
    <div class="flex flex-col gap-4">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-[var(--color-text)]">配置状态</h2>
          <div class="text-sm text-[var(--color-text-secondary)]">
            保存后允许 Gateway 启动；不等于当前进程已经运行
          </div>
        </div>
        <Switch
          label="配置为启用 Gateway"
          description="这是配置意图，需要保存后才会生效"
          enabled-text="已配置"
          disabled-text="未配置"
          :enabled="appState.gatewayEnabled"
          :disabled="appState.configSaving"
          @change="appState.gatewayEnabled = $event"
        />
      </div>

      <div class="rounded-[7px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 text-sm">
        <div class="flex flex-wrap items-center gap-2">
          <span
            class="h-2.5 w-2.5 rounded-full"
            :class="appState.gatewayRunning ? 'bg-emerald-500' : 'bg-[var(--color-text-muted)]'"
          />
          <span class="font-medium">{{ appState.gatewayRunning ? "运行中" : "未运行" }}</span>
          <span class="text-[var(--color-text-secondary)]">
            {{ gatewayDisplayAddr }}
          </span>
        </div>
        <div class="mt-3 flex flex-wrap items-center gap-2">
          <Button
            v-if="!appState.gatewayRunning"
            :disabled="appState.gatewayBusy || appState.configSaving || !appState.gatewayEnabled || gatewayDirty"
            @click="handleGatewayStart"
          >启动 Gateway</Button>
          <Button
            v-else
            :disabled="appState.gatewayBusy"
            @click="handleGatewayStop"
          >停止 Gateway</Button>
          <span
            v-if="gatewayDirty && !appState.gatewayRunning"
            class="text-xs text-amber-700 dark:text-amber-300"
          >
            请先保存本页
          </span>
        </div>
        <div v-if="appState.gatewayLastError" class="mt-2 text-xs text-amber-700 dark:text-amber-300">
          Gateway 错误：{{ appState.gatewayLastError }}
        </div>
      </div>

      <div class="flex flex-col gap-3">
        <div class="text-sm font-medium">接入安全</div>
        <label class="flex flex-col gap-2 text-sm">
          <span class="font-medium text-[var(--color-text)]">监听地址</span>
          <Input
            v-model="appState.gatewayListenAddr"
            :disabled="appState.configSaving"
            :placeholder="DEFAULT_GATEWAY_LISTEN_ADDR"
          />
        </label>
        <div class="flex flex-wrap items-center gap-2">
          <Button :disabled="appState.configSaving" @click="handleCopyToken">复制 token</Button>
          <Button :disabled="appState.configSaving" @click="handleRotateToken">轮换 token</Button>
          <span class="text-xs text-[var(--color-text-muted)]">
            {{ appState.gatewayTokenConfigured ? "已生成，仅可通过复制/轮换读取" : "保存启用配置时由后端生成" }}
          </span>
        </div>
      </div>

      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between gap-3">
          <div>
            <div class="text-sm font-medium">公开模型别名</div>
            <div class="text-xs text-[var(--color-text-muted)]">
              只经显式映射解析，不会回落到 provider modelID 或内部 hash。目标适配器变化后需重选。
            </div>
          </div>
          <Button :disabled="appState.configSaving" @click="addPublicModel">添加映射</Button>
        </div>
        <div
          v-for="(item, index) in appState.gatewayPublicModels"
          :key="index"
          class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]"
        >
          <Input v-model="item.id" :disabled="appState.configSaving" placeholder="公开别名，如 grok" />
          <Select
            v-model="item.targetAdapterID"
            :options="adapterOptions"
            placeholder="选择目标适配器"
          />
          <Button :disabled="appState.configSaving" @click="removePublicModel(index)">删除</Button>
          <div
            v-if="gatewayPublicModelInvalid(item, appState.modelAdapters)"
            class="sm:col-span-3 text-xs text-amber-700 dark:text-amber-300"
          >
            映射已失效，请重新选择目标适配器
          </div>
        </div>
      </div>

      <div class="rounded-[7px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3 text-xs leading-relaxed text-[var(--color-text-secondary)]">
        <div class="font-medium text-[var(--color-text)]">作用范围</div>
        <div class="mt-1">
          只影响 Gateway 的本机 HTTP 接口、Bearer token 和公开模型映射；不改变 Cursor 18080/18090、Cursor 会话或工具桥。
        </div>
        <div class="mt-2 font-medium text-[var(--color-text)]">保存范围</div>
        <div class="mt-1">
          本页保存只包含 Gateway 启用状态、监听地址和公开模型映射；token 由后端保留或生成。
        </div>
      </div>

      <div class="rounded-[7px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 text-sm">
        <div class="font-medium">客户端接入信息</div>
        <div class="mt-2 text-[var(--color-text-secondary)]">
          Base URL：{{ gatewayBaseURL }}
        </div>
        <div class="mt-1 text-xs text-[var(--color-text-muted)]">
          支持 Chat Completions、流式 SSE、公开模型别名。Token 明文不写入页面常驻状态、配置投影或日志。
        </div>
      </div>

      <div class="flex justify-end gap-2">
        <span
          class="self-center text-xs"
          :class="gatewayDirty ? 'text-amber-700 dark:text-amber-300' : 'text-[var(--color-text-muted)]'"
        >
          {{ configSectionStatusText("gateway") }}
        </span>
        <Button variant="primary" :disabled="appState.configSaving" @click="handleSaveGateway">
          {{ appState.configSaving ? "保存中..." : "保存本页" }}
        </Button>
      </div>
    </div>
  </Card>
</template>