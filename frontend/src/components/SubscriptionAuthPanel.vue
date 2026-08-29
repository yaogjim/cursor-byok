<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  activateSubscriptionAccount,
  clearCodexAuth,
  deleteSubscriptionAccount,
  getCodexAuthStatus,
  importCodexAuth,
  listSubscriptionAccounts,
  pollCodexDeviceAuth,
  pollGrokDeviceAuth,
  refreshSubscriptionUsage,
  startCodexDeviceAuth,
  startGrokDeviceAuth,
} from "@/services/clientApi";
import { toUserError } from "@/state/appState";
import { Browser, Dialogs } from "@wailsio/runtime";
import { computed, onMounted, onUnmounted, ref } from "vue";

const props = defineProps({
  provider: {
    type: String,
    default: "codex",
    validator: (value) => value === "codex" || value === "grok",
  },
});

const message = useMessage();
const busy = ref(false);
const lastError = ref("");
const codexStatus = ref({ state: "missing", provider: "codex" });
const grokAccounts = ref([]);
const deviceChallenge = ref(null);
const deviceProvider = ref("");
let pollTimer = null;

const JSON_FILE_FILTER = [{ DisplayName: "JSON", Pattern: "*.json" }];

function asAccount(value) {
  const raw = value && typeof value === "object" ? value : {};
  return {
    accountId: String(raw.accountId || ""),
    provider: String(raw.provider || ""),
    state: String(raw.state || "missing"),
    email: String(raw.email || ""),
    displayName: String(raw.displayName || ""),
    planLabel: String(raw.planLabel || ""),
    chatgptAccountId: String(raw.chatgptAccountId || ""),
    lastRefresh: raw.lastRefresh || "",
    expiresAt: raw.expiresAt || "",
    hasRefreshToken: Boolean(raw.hasRefreshToken),
    remainingPercent: Number(raw.remainingPercent || 0),
    usedPercent: Number(raw.usedPercent || 0),
    resetAt: raw.resetAt || "",
    sessionRemainingPercent: Number(raw.sessionRemainingPercent || 0),
    sessionResetAt: raw.sessionResetAt || "",
    limitReached: Boolean(raw.limitReached),
    active: Boolean(raw.active),
    error: String(raw.error || ""),
  };
}

const grokActive = computed(() => grokAccounts.value.find((item) => item.active) || null);
const codexReady = computed(() => codexStatus.value.state === "ready");
const isCodex = computed(() => props.provider === "codex");
const accountCount = computed(() => {
  if (isCodex.value) {
    return codexStatus.value.state === "missing" ? 0 : 1;
  }
  return grokAccounts.value.length;
});
const panelTitle = computed(() => isCodex.value ? "Codex 接入" : "Grok 接入");
const panelSubtitle = computed(() => isCodex.value
  ? "管理 ChatGPT / Codex 订阅授权，支持导入 auth.json 或设备码授权"
  : "管理 Grok / xAI 订阅授权与账号切换");

function stateLabel(state) {
  switch (state) {
    case "ready":
      return "已就绪";
    case "auth_required":
      return "需要重新授权";
    case "quota_exhausted":
      return "配额已用尽";
    case "pending":
      return "等待授权";
    case "error":
      return "异常";
    default:
      return "未配置";
  }
}

function showActionError(title, error) {
  lastError.value = String(error || title);
  message(`${title}：${lastError.value}`);
}

async function refreshAll() {
  try {
    if (isCodex.value) {
      codexStatus.value = asAccount(await getCodexAuthStatus());
    } else {
      const grok = await listSubscriptionAccounts("grok");
      grokAccounts.value = Array.isArray(grok) ? grok.map(asAccount) : [];
    }
    lastError.value = "";
  } catch (error) {
    showActionError("读取订阅认证失败", toUserError(error));
  }
}

async function handleImportCodex() {
  try {
    const path = await Dialogs.OpenFile({
      Title: "导入 Codex auth.json",
      Filters: JSON_FILE_FILTER,
      CanChooseFiles: true,
      CanChooseDirectories: false,
      AllowsMultipleSelection: false,
      AllowsOtherFiletypes: false,
    });
    if (!path) {
      return;
    }
    busy.value = true;
    const status = await importCodexAuth(path);
    codexStatus.value = asAccount(status);
    message("已导入 Codex 认证副本，未修改原始 auth.json");
  } catch (error) {
    showActionError("导入失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

async function handleClearCodex() {
  const confirmed = await showModal({
    title: "清除 Codex 认证副本",
    content: "只删除本应用保存的认证副本，不会修改 ~/.codex/auth.json。",
    confirmText: "清除",
    cancelText: "取消",
    showCancel: true,
  });
  if (!confirmed) {
    return;
  }
  try {
    busy.value = true;
    codexStatus.value = asAccount(await clearCodexAuth());
    message("已清除 Codex 认证副本");
  } catch (error) {
    showActionError("清除失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

function stopPolling() {
  if (pollTimer) {
    window.clearTimeout(pollTimer);
    pollTimer = null;
  }
}

async function pollOnce() {
  const challenge = deviceChallenge.value;
  const provider = deviceProvider.value;
  if (!challenge?.pollToken || !provider) {
    return;
  }
  try {
    const result = provider === "codex"
      ? await pollCodexDeviceAuth({ pollToken: challenge.pollToken, userCode: challenge.userCode })
      : await pollGrokDeviceAuth({ pollToken: challenge.pollToken, userCode: challenge.userCode });
    if (result?.status === "success") {
      stopPolling();
      deviceChallenge.value = null;
      deviceProvider.value = "";
      await refreshAll();
      message(provider === "codex" ? "Codex 设备授权完成" : "Grok 设备授权完成");
      return;
    }
    if (result?.status === "pending" || result?.status === "slow_down") {
      const waitMs = Math.max(3, Number(result.retryAfterSeconds || challenge.interval || 5)) * 1000;
      pollTimer = window.setTimeout(() => { void pollOnce(); }, waitMs);
      return;
    }
    stopPolling();
    deviceChallenge.value = null;
    deviceProvider.value = "";
    showActionError("设备授权未完成", result?.error || result?.status || "未知状态");
    await refreshAll();
  } catch (error) {
    stopPolling();
    deviceChallenge.value = null;
    deviceProvider.value = "";
    showActionError("轮询设备授权失败", toUserError(error));
  }
}

async function handleStartDevice(provider) {
  try {
    busy.value = true;
    const challenge = provider === "codex"
      ? await startCodexDeviceAuth()
      : await startGrokDeviceAuth();
    deviceProvider.value = provider;
    deviceChallenge.value = challenge;
    const uri = challenge.verificationUriComplete || challenge.verificationUri;
    if (uri) {
      try {
        await Browser.OpenURL(uri);
      } catch (_error) {
        // keep the on-screen code even if the browser helper fails
      }
    }
    message(`请在浏览器完成授权，用户码 ${challenge.userCode || ""}`);
    stopPolling();
    pollTimer = window.setTimeout(() => { void pollOnce(); }, Math.max(3, Number(challenge.interval || 5)) * 1000);
  } catch (error) {
    showActionError("启动设备授权失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

async function handleActivate(accountID) {
  try {
    busy.value = true;
    await activateSubscriptionAccount(accountID);
    await refreshAll();
  } catch (error) {
    showActionError("激活失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

async function handleDelete(accountID) {
  const confirmed = await showModal({
    title: "删除订阅账号",
    content: "删除后无法恢复该账号在本应用中的认证副本。",
    confirmText: "删除",
    cancelText: "取消",
    showCancel: true,
  });
  if (!confirmed) {
    return;
  }
  try {
    busy.value = true;
    await deleteSubscriptionAccount(accountID);
    await refreshAll();
  } catch (error) {
    showActionError("删除失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

async function handleRefreshUsage(provider) {
  try {
    busy.value = true;
    await refreshSubscriptionUsage(provider);
    await refreshAll();
  } catch (error) {
    showActionError("刷新用量失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

onMounted(() => {
  void refreshAll();
});

onUnmounted(() => {
  stopPolling();
});
</script>

<template>
  <div class="subscription-access-panel">
    <div class="subscription-access-head">
      <div class="min-w-0">
        <h2 class="subscription-access-title">{{ panelTitle }}</h2>
        <p class="subscription-access-subtitle">{{ panelSubtitle }}</p>
      </div>
      <Button
        v-if="isCodex"
        variant="primary"
        class="shrink-0"
        :disabled="busy"
        @click="handleImportCodex"
      >
        <span class="icon-[mdi--file-import-outline] text-[15px]" aria-hidden="true" />
        导入 auth.json
      </Button>
      <Button
        v-else
        variant="primary"
        class="shrink-0"
        :disabled="busy"
        @click="handleStartDevice('grok')"
      >
        <span class="icon-[mdi--plus] text-[15px]" aria-hidden="true" />
        添加授权
      </Button>
    </div>

    <Card :padded="false" class="subscription-mode-card">
      <div class="subscription-mode-row">
        <span class="subscription-mode-label">接入方式</span>
        <div class="subscription-mode-select text-sm text-[var(--color-text-secondary)]">
          {{ isCodex ? "ChatGPT / Codex" : "Grok / xAI" }}
        </div>
        <Button :disabled="busy" @click="handleStartDevice(provider)">设备码授权</Button>
        <Button
          :disabled="busy || (isCodex ? !codexReady : !grokActive)"
          @click="handleRefreshUsage(provider)"
        >
          刷新用量
        </Button>
      </div>
      <div class="subscription-mode-hint">
        授权凭据只保存在本机私有副本中，不会写入模型配置或前端状态。
      </div>
    </Card>

    <Card :padded="false" class="subscription-account-card">
      <div class="subscription-account-head">
        <h3 class="subscription-account-title">订阅授权</h3>
        <p class="subscription-account-subtitle">
          <span>{{ accountCount }} 个账号</span>
          {{ isCodex ? "可导入现有 auth.json 或重新进行设备授权" : "可添加、激活或删除 Grok 授权账号" }}
        </p>
      </div>

      <div v-if="isCodex && accountCount" class="flex flex-1 flex-col justify-center gap-3 px-7 py-6">
        <div class="flex items-center justify-between gap-3">
          <div class="min-w-0">
            <div class="truncate text-sm font-semibold text-[var(--color-text)]">
              {{ codexStatus.email || codexStatus.displayName || "ChatGPT / Codex" }}
            </div>
            <div class="mt-1 text-xs text-[var(--color-text-secondary)]">
              {{ stateLabel(codexStatus.state) }}
              <span v-if="codexStatus.planLabel"> · {{ codexStatus.planLabel }}</span>
              <span v-if="codexStatus.chatgptAccountId"> · {{ codexStatus.chatgptAccountId }}</span>
            </div>
            <div v-if="codexReady" class="mt-1 text-xs text-[var(--color-text-secondary)]">
              剩余 {{ Math.round(codexStatus.remainingPercent) }}%
              <span v-if="codexStatus.sessionResetAt">
                · 会话剩余 {{ Math.round(codexStatus.sessionRemainingPercent) }}%
              </span>
            </div>
          </div>
          <Button variant="text" :disabled="busy" @click="handleClearCodex">清除副本</Button>
        </div>
      </div>

      <ul v-else-if="!isCodex && grokAccounts.length" class="flex flex-1 flex-col gap-2 px-7 py-6">
        <li
          v-for="account in grokAccounts"
          :key="account.accountId"
          class="flex items-center justify-between gap-2 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3"
        >
          <div class="min-w-0">
            <div class="truncate text-sm font-semibold text-[var(--color-text)]">
              {{ account.displayName || account.email || account.accountId }}
            </div>
            <div class="mt-1 text-xs text-[var(--color-text-secondary)]">
              {{ account.active ? "当前使用" : stateLabel(account.state) }}
              <span v-if="account.planLabel"> · {{ account.planLabel }}</span>
              <span v-if="account.remainingPercent"> · 剩余 {{ Math.round(account.remainingPercent) }}%</span>
            </div>
          </div>
          <div class="flex shrink-0 gap-2">
            <Button variant="text" :disabled="busy || account.active" @click="handleActivate(account.accountId)">激活</Button>
            <Button variant="text" :disabled="busy" @click="handleDelete(account.accountId)">删除</Button>
          </div>
        </li>
      </ul>

      <div v-else class="subscription-account-empty">
        <span class="subscription-account-empty-icon icon-[mdi--account-outline]" aria-hidden="true" />
        <div>
          <div class="subscription-account-empty-title">暂无授权账号</div>
          <div class="subscription-account-empty-copy">
            {{ isCodex ? "导入 auth.json 或使用设备码完成 ChatGPT / Codex 授权。" : "使用设备码添加 Grok / xAI 授权账号。" }}
          </div>
        </div>
      </div>

      <div v-if="deviceChallenge" class="mx-7 mb-4 note">
        正在等待 {{ isCodex ? "Codex" : "Grok" }} 授权，用户码
        <strong>{{ deviceChallenge.userCode }}</strong>
      </div>
      <div v-if="lastError" class="mx-7 mb-4 note">{{ lastError }}</div>

      <div class="subscription-account-footer">
        <span class="config-action-status">
          {{ isCodex ? stateLabel(codexStatus.state) : (grokActive ? stateLabel(grokActive.state) : "未配置") }}
        </span>
        <div class="config-action-buttons">
          <Button
            v-if="isCodex"
            :disabled="busy"
            @click="handleImportCodex"
          >
            导入 auth.json
          </Button>
          <Button
            variant="primary"
            :disabled="busy"
            @click="handleStartDevice(provider)"
          >
            设备码授权
          </Button>
        </div>
      </div>
    </Card>
  </div>
</template>