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
    limitReached: Boolean(raw.limitReached),
    active: Boolean(raw.active),
    error: String(raw.error || ""),
  };
}

const grokActive = computed(() => grokAccounts.value.find((item) => item.active) || null);
const codexReady = computed(() => codexStatus.value.state === "ready");

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
    const [codex, grok] = await Promise.all([
      getCodexAuthStatus(),
      listSubscriptionAccounts("grok"),
    ]);
    codexStatus.value = asAccount(codex);
    grokAccounts.value = Array.isArray(grok) ? grok.map(asAccount) : [];
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
  <Card class="subscription-auth-panel">
    <div class="flex flex-col gap-3">
      <div class="page-title-block">
        <h3 class="text-[15px] font-semibold text-[var(--color-text)]">上游订阅认证</h3>
        <p class="note plain">
          Codex / Grok 订阅用于上游模型请求，与接入中心的 Codex 客户端占位、Cursor 登录无关。Token 只保存在本机私有副本中。
        </p>
      </div>

      <section class="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3">
        <div class="mb-2 flex items-center justify-between gap-2">
          <strong>ChatGPT / Codex</strong>
          <span class="text-xs text-[var(--color-text-secondary)]">{{ stateLabel(codexStatus.state) }}</span>
        </div>
        <p class="text-sm text-[var(--color-text-secondary)]">
          {{ codexStatus.email || codexStatus.displayName || "尚未导入认证副本" }}
        </p>
        <p v-if="codexStatus.chatgptAccountId" class="mt-1 text-xs text-[var(--color-text-secondary)]">
          账号 {{ codexStatus.chatgptAccountId }}
        </p>
        <div class="mt-3 flex flex-wrap gap-2">
          <Button variant="primary" :disabled="busy" @click="handleImportCodex">导入 auth.json</Button>
          <Button :disabled="busy" @click="handleStartDevice('codex')">设备码授权</Button>
          <Button :disabled="busy || !codexReady" @click="handleRefreshUsage('codex')">刷新用量</Button>
          <Button :disabled="busy || codexStatus.state === 'missing'" @click="handleClearCodex">清除副本</Button>
        </div>
      </section>

      <section class="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3">
        <div class="mb-2 flex items-center justify-between gap-2">
          <strong>Grok / xAI</strong>
          <span class="text-xs text-[var(--color-text-secondary)]">
            {{ grokActive ? stateLabel(grokActive.state) : "未配置" }}
          </span>
        </div>
        <p class="text-sm text-[var(--color-text-secondary)]">
          {{ grokActive ? (grokActive.displayName || grokActive.email || grokActive.accountId) : "尚未添加 Grok 账号" }}
        </p>
        <div class="mt-3 flex flex-wrap gap-2">
          <Button variant="primary" :disabled="busy" @click="handleStartDevice('grok')">设备码授权</Button>
          <Button :disabled="busy || !grokActive" @click="handleRefreshUsage('grok')">刷新用量</Button>
        </div>
        <ul v-if="grokAccounts.length" class="mt-3 flex flex-col gap-2">
          <li
            v-for="account in grokAccounts"
            :key="account.accountId"
            class="flex items-center justify-between gap-2 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2"
          >
            <div class="min-w-0">
              <div class="truncate text-sm">{{ account.displayName || account.email || account.accountId }}</div>
              <div class="text-xs text-[var(--color-text-secondary)]">
                {{ account.active ? "当前使用" : "未激活" }}
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
      </section>

      <p v-if="deviceChallenge" class="note">
        正在等待 {{ deviceProvider === "codex" ? "Codex" : "Grok" }} 授权，用户码
        <strong>{{ deviceChallenge.userCode }}</strong>
      </p>
      <p v-if="lastError" class="note">{{ lastError }}</p>
    </div>
  </Card>
</template>