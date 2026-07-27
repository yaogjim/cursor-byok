<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import LocaleSelect from "@/components/LocaleSelect.vue";
import Select from "@/components/ui/Select.vue";
import Switch from "@/components/ui/Switch.vue";
import { showModal } from "@/composables/useModal";
import {
  appState,
  clearClosedLogSessions,
  openLocalLogsDirectory,
  openModelConfigWindow,
  persistUserConfig,
  reloadUserConfig,
  syncLogCaptureStatus,
  ROUTE_MODE_OPTIONS,
  THEME_OPTIONS,
  toUserError,
} from "@/state/appState";
import { onMounted } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();
const routeModeOptions = ROUTE_MODE_OPTIONS;
const themeOptions = THEME_OPTIONS;
const observabilityModeOptions = [
  { label: "基础（推荐）", value: "basic" },
  { label: "完整调试", value: "full" },
];

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
  await syncLogCaptureStatus().catch(() => {});
  await showModal({
    title: "提示",
    content: "本地配置已保存",
    showCancel: false,
  });
}

async function handleOpenModelConfig() {
  try {
    await openModelConfigWindow();
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

async function handleOpenLogsDirectory() {
  try {
    await openLocalLogsDirectory();
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

function formatBytes(value) {
  const bytes = Math.max(0, Number(value) || 0);
  if (bytes < 1024 * 1024) {
    return `${Math.round(bytes / 1024)} KB`;
  }
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

async function handleClearClosedSessions() {
  const confirmed = await showModal({
    title: "清理已关闭日志",
    content: "只会删除已关闭的采集 session；当前正在写入的 session 不会被删除。",
    confirmText: "确认清理",
  });
  if (!confirmed) {
    return;
  }
  const result = await clearClosedLogSessions();
  if (!result.ok) {
    await showActionError("清理失败", result.error);
    return;
  }
  await showModal({
    title: "清理完成",
    content: `已删除 ${result.removedSessions} 个 session，释放 ${formatBytes(result.freedBytes)}。`,
    showCancel: false,
  });
}

onMounted(async () => {
  await Promise.all([
    reloadUserConfig().catch(() => {}),
    syncLogCaptureStatus().catch(() => {}),
  ]);
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
      <div class="flex flex-col gap-5">
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-[var(--color-text)]">日志采集</h2>
            <div class="text-sm text-[var(--color-text-secondary)]">
              基础模式只保存链路元数据；完整调试模式会保存经过凭据清洗的本机业务数据
            </div>
          </div>
          <div class="w-[220px] max-w-full">
            <Select
              v-model="appState.observabilityMode"
              :options="observabilityModeOptions"
              placeholder="选择日志等级"
            />
          </div>
        </div>

        <div
          v-if="appState.observabilityMode === 'full'"
          class="rounded-[7px] border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm leading-relaxed text-amber-700 dark:text-amber-300"
        >
          完整调试日志可能包含 Prompt、源码片段、diff 和工具输入输出，仅应在排障期间开启。凭据仍会在写盘前强制清洗。
        </div>

        <div class="grid gap-4 sm:grid-cols-2">
          <label class="flex flex-col gap-2 text-sm">
            <span class="font-medium text-[var(--color-text)]">保留天数</span>
            <input
              v-model.number="appState.observabilityRetentionDays"
              type="number"
              min="1"
              max="90"
              step="1"
              :disabled="appState.configSaving"
              class="h-9 rounded-[6px] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 text-[var(--color-text)] outline-none focus:border-[var(--color-primary)] disabled:opacity-60"
            />
            <span class="text-xs text-[var(--color-text-muted)]">范围 1–90 天</span>
          </label>
          <label class="flex flex-col gap-2 text-sm">
            <span class="font-medium text-[var(--color-text)]">最大磁盘空间（MB）</span>
            <input
              v-model.number="appState.observabilityMaxDiskMB"
              type="number"
              min="64"
              max="10240"
              step="64"
              :disabled="appState.configSaving"
              class="h-9 rounded-[6px] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 text-[var(--color-text)] outline-none focus:border-[var(--color-primary)] disabled:opacity-60"
            />
            <span class="text-xs text-[var(--color-text-muted)]">范围 64–10240 MB</span>
          </label>
        </div>

        <div class="rounded-[7px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 text-sm">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="flex items-center gap-2">
              <span
                class="h-2.5 w-2.5 rounded-full"
                :class="appState.logCaptureStatus.enabled ? 'bg-emerald-500' : 'bg-[var(--color-text-muted)]'"
              ></span>
              <span class="font-medium">
                {{ appState.logCaptureStatus.enabled ? "正在采集" : "采集未运行" }}
              </span>
              <span class="text-[var(--color-text-secondary)]">
                {{ appState.logCaptureStatus.mode || appState.observabilityMode }}
              </span>
            </div>
            <div class="center-row flex-wrap gap-2">
              <Button :disabled="appState.logCaptureLoading" @click="syncLogCaptureStatus">
                {{ appState.logCaptureLoading ? "刷新中..." : "刷新状态" }}
              </Button>
              <Button @click="handleOpenLogsDirectory">打开日志目录</Button>
              <Button :disabled="appState.logCleanupRunning" @click="handleClearClosedSessions">
                {{ appState.logCleanupRunning ? "清理中..." : "清理已关闭 session" }}
              </Button>
            </div>
          </div>
          <div class="mt-2 break-all text-xs text-[var(--color-text-muted)]">
            {{ appState.logCaptureStatus.logsRoot || "~/.cursor-local-assistant-v2/logs/" }}
          </div>
          <div
            v-if="appState.logCaptureStatus.payloadDegraded || appState.logCaptureStatus.lastError"
            class="mt-2 text-xs text-amber-700 dark:text-amber-300"
          >
            采集已降级：{{ appState.logCaptureStatus.lastError || "payload_capture_disabled" }}；丢弃事件
            {{ appState.logCaptureStatus.droppedEvents }} 条
          </div>
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
