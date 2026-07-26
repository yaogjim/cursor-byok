<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import LocaleSelect from "@/components/LocaleSelect.vue";
import Select from "@/components/ui/Select.vue";
import Switch from "@/components/ui/Switch.vue";
import { showModal } from "@/composables/useModal";
import {
  appState,
  openModelConfigWindow,
  persistUserConfig,
  reloadUserConfig,
  ROUTE_MODE_OPTIONS,
  THEME_OPTIONS,
  toUserError,
} from "@/state/appState";
import { onMounted } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();
const routeModeOptions = ROUTE_MODE_OPTIONS;
const themeOptions = THEME_OPTIONS;

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || "服务错误").trim() || "服务错误",
  });
}

async function handleBackHome() {
  await router.push("/");
}

async function handleSaveConfig() {
  const result = await persistUserConfig();
  if (!result.ok) {
    await showActionError("保存失败", result.error);
    return;
  }
  await showModal({
    title: "提示",
    content: "本地配置已保存",
  });
}

async function handleOpenModelConfig() {
  try {
    await openModelConfigWindow();
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

onMounted(async () => {
  await reloadUserConfig().catch(() => {});
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[var(--color-text)]">
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-[var(--color-text)]">本地配置</h2>
          <div class="text-sm text-[var(--color-text-secondary)]">
            可配置运行模式和模型渠道；运行日志位于 <code>~/.cursor-local-assistant-v2/logs/</code>
          </div>
        </div>
        <div class="center-row gap-2">
          <Button variant="default" @click="handleBackHome">返回首页</Button>
          <Button variant="primary" :disabled="appState.configSaving" @click="handleSaveConfig">
            {{ appState.configSaving ? "保存中..." : "保存配置" }}
          </Button>
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-[var(--color-text)]">运行模式</h2>
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
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-[var(--color-text)]">界面语言</h2>
          <div class="text-sm text-[var(--color-text-secondary)]">
            切换当前界面显示语言，设置会立即生效并保存在本机
          </div>
        </div>
        <LocaleSelect wrapper-class="w-[220px] max-w-full" />
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-[var(--color-text)]">界面主题</h2>
          <div class="text-sm text-[var(--color-text-secondary)]">
            浅色为默认主题，保存后会同步应用到所有客户端窗口
          </div>
        </div>
        <div class="w-[220px] max-w-full">
          <Select
            v-model="appState.appearanceTheme"
            :options="themeOptions"
            placeholder="选择主题"
          />
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex flex-col gap-4">
        <Switch
          label="广告内容"
          description="关闭后不拉取广告、不显示顶部广告位和广告弹窗"
          enabled-text="已允许广告内容"
          disabled-text="已关闭广告内容"
          :enabled="appState.advertisingEnabled"
          :disabled="appState.configSaving"
          @change="appState.advertisingEnabled = $event"
        />
        <div class="border-t border-[var(--color-border)]"></div>
        <Switch
          label="启动时检查客户端更新"
          description="开启后仅检查版本；下载和安装仍分别需要确认"
          enabled-text="启动时会检查版本"
          disabled-text="仅手动检查更新"
          :enabled="appState.updateCheckOnStartup"
          :disabled="appState.configSaving"
          @change="appState.updateCheckOnStartup = $event"
        />
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-[var(--color-text)]">模型配置</h2>
          <div class="text-sm text-[var(--color-text-secondary)]">
            已配置 {{ appState.modelAdapters.length }} 个模型适配器
          </div>
        </div>
        <Button variant="primary" @click="handleOpenModelConfig">打开模型配置</Button>
      </div>
    </Card>
  </div>
</template>
