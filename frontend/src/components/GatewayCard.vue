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
  copyGatewayToken,
  persistUserConfig,
  rotateGatewayToken,
  startGateway,
  stopGateway,
  toUserError,
} from "@/state/appState";
import { DEFAULT_GATEWAY_LISTEN_ADDR, gatewayPublicModelInvalid } from "@/state/configProjection";
import { computed } from "vue";

const message = useMessage();

const adapterOptions = computed(() =>
  (appState.modelAdapters || []).map((adapter) => ({
    label: adapter.displayName || adapter.id,
    value: adapter.id,
  })),
);

function showActionError(title, error) {
  const detail = String(error || "服务错误").trim() || "服务错误";
  message(`${title}：${detail}`);
}

async function persistGateway() {
  const result = await persistUserConfig();
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
    if (navigator?.clipboard?.writeText) {
      await navigator.clipboard.writeText(token);
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
    if (navigator?.clipboard?.writeText && token) {
      await navigator.clipboard.writeText(token);
    }
    message("Gateway token 已轮换并复制");
  } catch (error) {
    showActionError("轮换失败", toUserError(error));
  }
}

async function handleGatewayStart() {
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
  await persistGateway();
}
</script>

<template>
  <Card>
    <div class="flex flex-col gap-4">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-[var(--color-text)]">Chat Gateway</h2>
          <div class="text-sm text-[var(--color-text-secondary)]">
            默认关闭。独立监听 127.0.0.1:18091，仅 loopback + Bearer；可单独启停，不改变 Cursor 的 18080/18090。
          </div>
        </div>
        <Switch
          label="启用 Gateway"
          description="保存后可独立启动；失败不会回滚 Cursor"
          enabled-text="已启用"
          disabled-text="已关闭"
          :enabled="appState.gatewayEnabled"
          :disabled="appState.configSaving"
          @change="appState.gatewayEnabled = $event"
        />
      </div>

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

      <div class="rounded-[7px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 text-sm">
        <div class="flex items-center gap-2">
          <span
            class="h-2.5 w-2.5 rounded-full"
            :class="appState.gatewayRunning ? 'bg-emerald-500' : 'bg-[var(--color-text-muted)]'"
          />
          <span class="font-medium">{{ appState.gatewayRunning ? "Gateway 运行中" : "Gateway 未运行" }}</span>
          <span class="text-[var(--color-text-secondary)]">
            {{ appState.gatewayRuntimeListenAddr || appState.gatewayListenAddr }}
          </span>
        </div>
        <div class="mt-3 flex gap-2">
          <Button
            v-if="!appState.gatewayRunning"
            :disabled="appState.serviceBusy || appState.configSaving || !appState.gatewayEnabled"
            @click="handleGatewayStart"
          >启动 Gateway</Button>
          <Button
            v-else
            :disabled="appState.serviceBusy"
            @click="handleGatewayStop"
          >停止 Gateway</Button>
        </div>
        <div v-if="appState.gatewayLastError" class="mt-2 text-xs text-amber-700 dark:text-amber-300">
          Gateway 错误：{{ appState.gatewayLastError }}
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

      <div class="flex justify-end">
        <Button variant="primary" :disabled="appState.configSaving" @click="handleSaveGateway">
          {{ appState.configSaving ? "保存中..." : "保存 Gateway" }}
        </Button>
      </div>
    </div>
  </Card>
</template>