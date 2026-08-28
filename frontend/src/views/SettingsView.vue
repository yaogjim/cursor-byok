<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import LocaleSelect from "@/components/LocaleSelect.vue";
import Select from "@/components/ui/Select.vue";
import Switch from "@/components/ui/Switch.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  appState,
  clearClosedLogSessions,
  configSectionDirty,
  configSectionStatusText,
  openLocalLogsDirectory,
  persistScopedUserConfig,
  reloadUserConfig,
  syncLogCaptureStatus,
  THEME_OPTIONS,
  toUserError,
} from "@/state/appState";
import { computed, onMounted, ref } from "vue";

const themeOptions = THEME_OPTIONS;
const settingsPanel = ref("basic");
const settingsSections = [
  { id: "basic", label: "基本设置" },
  { id: "logs", label: "会话与日志" },
  { id: "network", label: "网络与请求" },
  { id: "recovery", label: "数据与恢复" },
];
const observabilityModeOptions = [
  { label: "关闭", value: "off" },
  { label: "基础（推荐）", value: "basic" },
  { label: "完整调试", value: "full" },
];

const message = useMessage();
const settingsSaveLabel = computed(() => {
  if (appState.configSaving) {
    return "保存中...";
  }
  return settingsPanel.value === "logs" ? "保存日志设置" : "保存基本设置";
});

function showActionError(title, error) {
  const detail = String(error || "服务错误").trim() || "服务错误";
  message(`${title}：${detail}`);
}

async function handleSaveConfig() {
  const result = await persistScopedUserConfig("settings");
  if (!result.ok) {
    showActionError("保存失败", result.error);
    return;
  }
  await syncLogCaptureStatus().catch(() => {});
  message("本页设置已保存");
}

async function handleReloadConfig() {
  try {
    await reloadUserConfig();
    await syncLogCaptureStatus().catch(() => {});
  } catch (error) {
    showActionError("重新加载失败", toUserError(error));
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
    title: "清理日志",
    content: "只会删除已关闭的采集 session，不会删除当前活跃 session 和普通运行日志。",
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
  <div class="page-shell page-shell--fill gap-3 text-[var(--color-text)]">
    <div class="page-title-row">
      <div class="page-title-block">
        <h2 class="page-title">系统设置</h2>
      </div>
      <div class="seg shrink-0" role="group" aria-label="设置分组">
        <button
          v-for="section in settingsSections"
          :key="section.id"
          type="button"
          :aria-pressed="settingsPanel === section.id"
          :class="settingsPanel === section.id ? 'is-on' : ''"
          @click="settingsPanel = section.id"
        >
          {{ section.label }}
        </button>
      </div>
    </div>

    <Card v-show="settingsPanel === 'basic'" :padded="false" class="compact-card min-h-0 flex-1">
      <div class="compact-card-body">
        <div class="card-h">
          <div>
            <h2 class="card-title">基本设置</h2>
            <div class="card-sub">界面偏好、启动窗口、广告与更新</div>
          </div>
        </div>
        <div class="grid2">
          <div class="field">
            <span class="field-l">界面语言</span>
            <LocaleSelect wrapper-class="w-full" />
            <span class="field-h">切换后立即应用并保存在本机</span>
          </div>
          <div class="field">
            <span class="field-l">界面主题</span>
            <Select
              v-model="appState.appearanceTheme"
              :options="themeOptions"
              placeholder="选择主题"
            />
            <span class="field-h">保存后同步应用到所有客户端窗口</span>
          </div>
        </div>
        <div class="hr" />
        <div class="grid2" style="column-gap: 32px">
          <div>
            <div class="setting-t" style="margin-bottom: 4px">启动与窗口</div>
            <div class="setting-row is-planned" inert>
              <div class="setting-l">
                <div class="setting-t text-[var(--color-text-muted)]">
                  开机自启动 <span class="badge-soon">计划中</span>
                </div>
                <div class="setting-s">登录系统后自动启动 Cursor 助手服务</div>
              </div>
              <button type="button" class="relative inline-flex h-[22px] w-[40px] shrink-0 rounded-full bg-[var(--color-border-strong)] opacity-55" disabled aria-label="开机自启动，计划中" />
            </div>
            <div class="setting-row is-planned" inert>
              <div class="setting-l">
                <div class="setting-t text-[var(--color-text-muted)]">
                  最小化到系统托盘 <span class="badge-soon">计划中</span>
                </div>
                <div class="setting-s">关闭窗口时保持服务在后台运行</div>
              </div>
              <button type="button" class="relative inline-flex h-[22px] w-[40px] shrink-0 rounded-full bg-[var(--color-border-strong)] opacity-55" disabled aria-label="最小化到系统托盘，计划中" />
            </div>
          </div>
          <div>
            <div class="setting-t" style="margin-bottom: 4px">内容与更新</div>
            <div class="setting-row">
              <Switch
                compact
                :show-state="false"
                class="w-full"
                label="广告内容"
                description="关闭后不拉取广告，也不显示广告位与弹窗"
                :enabled="appState.advertisingEnabled"
                :disabled="appState.configSaving"
                @change="appState.advertisingEnabled = $event"
              />
            </div>
            <div class="setting-row">
              <Switch
                compact
                :show-state="false"
                class="w-full"
                label="启动时检查客户端更新"
                description="仅检查版本；下载和安装仍需分别确认"
                :enabled="appState.updateCheckOnStartup"
                :disabled="appState.configSaving"
                @change="appState.updateCheckOnStartup = $event"
              />
            </div>
          </div>
        </div>
      </div>
      <div class="config-action-bar">
        <span
          class="config-action-status"
          :class="configSectionDirty.settings ? 'is-dirty' : ''"
        >
          {{ configSectionStatusText("settings") }}
        </span>
        <div class="config-action-buttons spread">
          <Button variant="text" class="btn-sm" :disabled="appState.configSaving" @click="handleReloadConfig">
            重新加载
          </Button>
          <Button variant="primary" class="btn-sm" :disabled="appState.configSaving || !configSectionDirty.settings" @click="handleSaveConfig">
            {{ settingsSaveLabel }}
          </Button>
        </div>
      </div>
    </Card>

    <Card v-show="settingsPanel === 'logs'" :padded="false" class="compact-card min-h-0 flex-1">
      <div class="compact-card-body">
        <div class="card-h">
          <div>
            <h2 class="card-title">会话与日志</h2>
            <div class="card-sub">采集级别、容量控制与日志维护</div>
          </div>
          <span
            class="status-pill"
            :class="appState.logCaptureStatus.enabled ? 'is-ok' : 'is-off'"
          >
            <i aria-hidden="true" />
            {{ appState.logCaptureStatus.enabled ? "采集中" : "采集未运行" }}
          </span>
        </div>

        <div class="grid3">
          <label class="field">
            <span class="field-l">日志采集</span>
            <Select
              v-model="appState.observabilityMode"
              :options="observabilityModeOptions"
              placeholder="选择日志等级"
            />
            <span class="field-h">warning/error 始终保存</span>
          </label>
          <label class="field">
            <span class="field-l">保留天数</span>
            <input
              v-model.number="appState.observabilityRetentionDays"
              type="number"
              min="1"
              max="90"
              step="1"
              :disabled="appState.configSaving"
              class="h-10 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3.5 font-mono text-[var(--color-text)] outline-none focus:border-[var(--color-primary)] disabled:opacity-60"
            >
            <span class="field-h">范围 1–90 天</span>
          </label>
          <label class="field">
            <span class="field-l">最大空间</span>
            <input
              v-model.number="appState.observabilityMaxDiskMB"
              type="number"
              min="64"
              max="10240"
              step="64"
              :disabled="appState.configSaving"
              class="h-10 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3.5 font-mono text-[var(--color-text)] outline-none focus:border-[var(--color-primary)] disabled:opacity-60"
            >
            <span class="field-h">范围 64–10240 MB</span>
          </label>
        </div>

        <div
          v-if="appState.observabilityMode === 'full'"
          class="mt-3 rounded-[7px] border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm leading-relaxed text-amber-700 dark:text-amber-300"
        >
          完整调试日志可能包含 Prompt、源码片段、diff 和工具输入输出，仅应在排障期间开启。凭据仍会在写盘前强制清洗。
        </div>

        <div class="note plain mt-3">
          <span class="icon-[mdi--folder-outline] text-[15px] shrink-0" aria-hidden="true" />
          <span>
            <span class="mono-chip">{{ appState.logCaptureStatus.logsRoot || "~/.cursor-local-assistant-v2/logs" }}</span>
            达到天数或容量上限时按策略清理旧日志。
          </span>
        </div>
        <div
          v-if="appState.logCaptureStatus.payloadDegraded || appState.logCaptureStatus.lastError"
          class="mt-2 text-xs text-[var(--color-warning-text)]"
        >
          采集已降级：{{ appState.logCaptureStatus.lastError || "payload_capture_disabled" }}；丢弃事件
          {{ appState.logCaptureStatus.droppedEvents }} 条
        </div>

        <div class="setting-row">
          <div class="setting-l">
            <div class="setting-t">日志维护</div>
            <div class="setting-s">刷新采集状态、打开目录或按当前策略清理</div>
          </div>
          <div class="row-inline">
            <Button class="btn-sm" :disabled="appState.logCaptureLoading" @click="syncLogCaptureStatus">
              {{ appState.logCaptureLoading ? "刷新中..." : "刷新状态" }}
            </Button>
            <Button class="btn-sm" @click="handleOpenLogsDirectory">打开目录</Button>
            <Button class="btn-sm btn-risk" :disabled="appState.logCleanupRunning" @click="handleClearClosedSessions">
              {{ appState.logCleanupRunning ? "清理中..." : "清理日志" }}
            </Button>
          </div>
        </div>

        <div class="setting-row is-planned" inert>
          <div class="setting-l">
            <div class="setting-t text-[var(--color-text-muted)]">
              立即清空全部日志 <span class="badge-soon">计划中</span>
            </div>
            <div class="setting-s">不受保留策略影响，立即删除所有日志文件</div>
          </div>
          <Button variant="default" class="btn-sm" disabled>清空全部</Button>
        </div>
      </div>
      <div class="config-action-bar">
        <span
          class="config-action-status"
          :class="configSectionDirty.settings ? 'is-dirty' : ''"
        >
          {{ configSectionStatusText("settings") }}
        </span>
        <div class="config-action-buttons spread">
          <Button variant="text" class="btn-sm" :disabled="appState.configSaving" @click="handleReloadConfig">
            重新加载
          </Button>
          <Button variant="primary" class="btn-sm" :disabled="appState.configSaving || !configSectionDirty.settings" @click="handleSaveConfig">
            {{ settingsSaveLabel }}
          </Button>
        </div>
      </div>
    </Card>

    <Card v-show="settingsPanel === 'network'" :padded="false" class="compact-card min-h-0">
      <div class="compact-card-body flex flex-col gap-3">
        <div class="flex items-start justify-between gap-3">
          <div>
            <h2 class="card-title">网络与请求</h2>
            <div class="card-sub">上游请求链路配置</div>
          </div>
          <span class="badge-soon">计划中</span>
        </div>
        <div class="setting-row is-planned" inert>
          <div>
            <div class="text-sm font-medium text-[var(--color-text-muted)]">HTTP 代理</div>
            <div class="text-xs text-[var(--color-text-muted)]">配置 HTTP/HTTPS/SOCKS5 代理</div>
          </div>
          <Button disabled>配置</Button>
        </div>
        <div class="setting-row is-planned" inert>
          <div>
            <div class="text-sm font-medium text-[var(--color-text-muted)]">请求超时（秒）</div>
            <div class="text-xs text-[var(--color-text-muted)]">上游请求的全局超时时间</div>
          </div>
          <input class="h-10 w-20 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 text-sm" type="number" value="30" disabled>
        </div>
        <div class="setting-row is-planned" inert>
          <div>
            <div class="text-sm font-medium text-[var(--color-text-muted)]">跳过 SSL 验证</div>
            <div class="text-xs text-[var(--color-text-muted)]">仅用于开发或自签名证书</div>
          </div>
          <button type="button" class="relative inline-flex h-[22px] w-[40px] shrink-0 rounded-full bg-[var(--color-border-strong)] opacity-55" disabled aria-label="跳过 SSL 验证，计划中" />
        </div>
      </div>
    </Card>

    <Card v-show="settingsPanel === 'recovery'" :padded="false" class="compact-card min-h-0">
      <div class="compact-card-body flex flex-col gap-3">
        <div>
          <h2 class="card-title">数据与恢复</h2>
          <div class="card-sub">低频维护与恢复操作</div>
        </div>
        <div class="setting-row is-planned" inert>
          <div>
            <div class="flex items-center gap-2 text-sm font-medium text-[var(--color-text-muted)]">
              重置所有设置 <span class="badge-soon">计划中</span>
            </div>
            <div class="text-xs text-[var(--color-text-muted)]">恢复基本设置、日志与网络配置的默认值，不影响模型和接入账号授权</div>
          </div>
          <Button disabled>重置设置</Button>
        </div>
      </div>
    </Card>
  </div>
</template>