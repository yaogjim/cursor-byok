<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import ContentModal from "@/components/ui/ContentModal.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  activateSubscriptionAccount,
  clearCodexAuth,
  deleteSubscriptionAccount,
  importCodexAuth,
  importSub2APIAccounts,
  listSubscriptionAccounts,
  pollCodexDeviceAuth,
  pollGrokDeviceAuth,
  previewSub2APIImport,
  refreshSubscriptionAccountUsage,
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
const codexAccounts = ref([]);
const grokAccounts = ref([]);
const deviceChallenge = ref(null);
const deviceProvider = ref("");
const sub2apiOpen = ref(false);
const sub2apiPath = ref("");
const sub2apiAccounts = ref([]);
const sub2apiSelected = ref([]);
const sub2apiSkippedCount = ref(0);
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

const isCodex = computed(() => props.provider === "codex");
const accounts = computed(() => isCodex.value ? codexAccounts.value : grokAccounts.value);
const activeAccount = computed(() => accounts.value.find((item) => item.active) || null);
const accountCount = computed(() => accounts.value.length);
const panelTitle = computed(() => isCodex.value ? "Codex 接入" : "Grok 接入");
const selectedSub2APICount = computed(() => sub2apiSelected.value.length);

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

function accountStateLabel(account) {
  if (account?.active && account.state === "ready") {
    return "当前使用";
  }
  if (!account?.active && account?.state === "ready") {
    return "备用";
  }
  return stateLabel(account?.state);
}

function showActionError(title, error) {
  lastError.value = String(error || title);
  message(`${title}：${lastError.value}`);
}

async function refreshAll() {
  try {
    const providerAccounts = await listSubscriptionAccounts(props.provider);
    const normalized = Array.isArray(providerAccounts) ? providerAccounts.map(asAccount) : [];
    if (isCodex.value) {
      codexAccounts.value = normalized;
    } else {
      grokAccounts.value = normalized;
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
    await importCodexAuth(path);
    await refreshAll();
    message("已导入 Codex 认证副本，未修改原始 auth.json");
  } catch (error) {
    showActionError("导入失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

function resetSub2APIImport() {
  sub2apiOpen.value = false;
  sub2apiPath.value = "";
  sub2apiAccounts.value = [];
  sub2apiSelected.value = [];
  sub2apiSkippedCount.value = 0;
}

function closeSub2APIImport() {
  if (busy.value) {
    return;
  }
  resetSub2APIImport();
}

function toggleAllSub2APIAccounts() {
  if (sub2apiSelected.value.length === sub2apiAccounts.value.length) {
    sub2apiSelected.value = [];
    return;
  }
  sub2apiSelected.value = sub2apiAccounts.value.map((account) => account.accountId);
}

async function handlePreviewSub2API() {
  try {
    const path = await Dialogs.OpenFile({
      Title: `导入 ${isCodex.value ? "Codex" : "Grok"} sub2api JSON`,
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
    const preview = await previewSub2APIImport(path, props.provider);
    const candidates = Array.isArray(preview?.accounts) ? preview.accounts : [];
    if (candidates.length === 0) {
      message(`文件中没有可导入的 ${isCodex.value ? "Codex" : "Grok"} OAuth 账号`);
      return;
    }
    sub2apiPath.value = path;
    sub2apiAccounts.value = candidates.map((account) => ({
      accountId: String(account?.accountId || ""),
      name: String(account?.name || ""),
      email: String(account?.email || ""),
      planLabel: String(account?.planLabel || ""),
      alreadyExists: Boolean(account?.alreadyExists),
    }));
    sub2apiSelected.value = sub2apiAccounts.value.map((account) => account.accountId);
    sub2apiSkippedCount.value = Number(preview?.skippedCount || 0);
    sub2apiOpen.value = true;
  } catch (error) {
    showActionError("读取 sub2api 文件失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

async function handleImportSub2API() {
  if (!sub2apiPath.value || sub2apiSelected.value.length === 0) {
    return;
  }
  try {
    busy.value = true;
    const result = await importSub2APIAccounts(
      sub2apiPath.value,
      props.provider,
      sub2apiSelected.value,
    );
    const importedCount = Array.isArray(result?.accounts) ? result.accounts.length : 0;
    await refreshAll();
    resetSub2APIImport();
    message(`已导入 ${importedCount} 个 ${isCodex.value ? "Codex" : "Grok"} 账号`);
  } catch (error) {
    showActionError("导入 sub2api 账号失败", toUserError(error));
  } finally {
    busy.value = false;
  }
}

async function handleClearCodex() {
  const confirmed = await showModal({
    title: "清除全部 Codex 认证副本",
    content: "只删除本应用保存的全部 Codex 认证副本，不会修改任何原始 auth.json。",
    confirmText: "清除",
    cancelText: "取消",
    showCancel: true,
  });
  if (!confirmed) {
    return;
  }
  try {
    busy.value = true;
    await clearCodexAuth();
    await refreshAll();
    message("已清除全部 Codex 认证副本");
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

async function handleRefreshAccountUsage(accountID) {
  try {
    busy.value = true;
    await refreshSubscriptionAccountUsage("codex", accountID);
    await refreshAll();
  } catch (error) {
    showActionError("刷新账号用量失败", toUserError(error));
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
      </div>
      <div class="flex shrink-0 gap-2">
        <Button
          v-if="isCodex"
          variant="primary"
          :disabled="busy"
          @click="handleImportCodex"
        >
          <span class="icon-[mdi--file-import-outline] text-[15px]" aria-hidden="true" />
          导入 auth.json
        </Button>
        <Button
          variant="default"
          :disabled="busy"
          @click="handlePreviewSub2API"
        >
          <span class="icon-[mdi--account-multiple-plus-outline] text-[15px]" aria-hidden="true" />
          导入 sub2api
        </Button>
        <Button
          v-if="!isCodex"
          variant="primary"
          :disabled="busy"
          @click="handleStartDevice('grok')"
        >
          <span class="icon-[mdi--plus] text-[15px]" aria-hidden="true" />
          添加授权
        </Button>
      </div>
    </div>

    <Card :padded="false" class="subscription-mode-card">
      <div class="subscription-mode-row">
        <span class="subscription-mode-label">接入方式</span>
        <div class="subscription-mode-select text-sm text-[var(--color-text-secondary)]">
          {{ isCodex ? "ChatGPT / Codex" : "Grok / xAI" }}
        </div>
        <Button :disabled="busy" @click="handleStartDevice(provider)">设备码授权</Button>
        <Button
          :disabled="busy || !activeAccount || activeAccount.state === 'auth_required'"
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
          {{ isCodex ? "可添加、激活、刷新或删除 Codex 授权账号" : "可添加、激活或删除 Grok 授权账号" }}
        </p>
      </div>

      <ul v-if="accounts.length" class="subscription-account-list">
        <li
          v-for="account in accounts"
          :key="account.accountId"
          class="flex items-center justify-between gap-2 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3"
        >
          <div class="min-w-0">
            <div class="truncate text-sm font-semibold text-[var(--color-text)]">
              {{ account.displayName || account.email || account.accountId }}
            </div>
            <div class="mt-1 text-xs text-[var(--color-text-secondary)]">
              {{ accountStateLabel(account) }}
              <span v-if="account.planLabel"> · {{ account.planLabel }}</span>
              <span v-if="isCodex && account.chatgptAccountId"> · {{ account.chatgptAccountId }}</span>
            </div>
            <div
              v-if="account.planLabel || account.resetAt || account.sessionResetAt || account.remainingPercent > 0"
              class="mt-1 text-xs text-[var(--color-text-secondary)]"
            >
              剩余 {{ Math.round(account.remainingPercent) }}%
              <span v-if="isCodex && account.sessionResetAt">
                · 会话剩余 {{ Math.round(account.sessionRemainingPercent) }}%
              </span>
            </div>
          </div>
          <div class="flex shrink-0 gap-2">
            <Button
              v-if="isCodex"
              variant="text"
              :disabled="busy || account.state === 'auth_required'"
              @click="handleRefreshAccountUsage(account.accountId)"
            >
              刷新用量
            </Button>
            <Button
              variant="text"
              :disabled="busy || account.active || account.state !== 'ready'"
              @click="handleActivate(account.accountId)"
            >
              激活
            </Button>
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
          {{ activeAccount ? stateLabel(activeAccount.state) : "未配置" }}
        </span>
        <Button
          v-if="isCodex && accountCount"
          variant="text"
          :disabled="busy"
          @click="handleClearCodex"
        >
          清除全部
        </Button>
      </div>
    </Card>

    <ContentModal
      :open="sub2apiOpen"
      :title="`选择要导入的 ${isCodex ? 'Codex' : 'Grok'} 账号`"
      size="lg"
      :close-disabled="busy"
      @close="closeSub2APIImport"
    >
      <div class="flex h-full min-h-0 flex-col bg-[var(--color-surface)]">
        <div class="flex items-center justify-between border-b border-[var(--color-border)] px-5 py-3">
          <p class="text-sm text-[var(--color-text-secondary)]">
            已按当前接入类型过滤，仅展示可导入的 {{ isCodex ? "Codex" : "Grok" }} OAuth 账号。
            <span v-if="sub2apiSkippedCount">已跳过 {{ sub2apiSkippedCount }} 项。</span>
          </p>
          <Button variant="text" :disabled="busy" @click="toggleAllSub2APIAccounts">
            {{ selectedSub2APICount === sub2apiAccounts.length ? "取消全选" : "全选" }}
          </Button>
        </div>
        <ul class="min-h-0 flex-1 space-y-2 overflow-y-auto p-5">
          <li
            v-for="account in sub2apiAccounts"
            :key="account.accountId"
            class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-soft)] p-3"
          >
            <label class="flex cursor-pointer items-start gap-3">
              <input
                v-model="sub2apiSelected"
                type="checkbox"
                :value="account.accountId"
                :disabled="busy"
                class="mt-0.5 size-4 accent-[var(--color-primary)]"
              />
              <span class="min-w-0 flex-1">
                <span class="block truncate text-sm font-semibold text-[var(--color-text)]">
                  {{ account.name || account.email || account.accountId }}
                </span>
                <span class="mt-1 block truncate text-xs text-[var(--color-text-secondary)]">
                  {{ account.email || account.accountId }}
                  <template v-if="account.planLabel"> · {{ account.planLabel }}</template>
                  <template v-if="account.alreadyExists"> · 已存在，将更新凭据</template>
                </span>
              </span>
            </label>
          </li>
        </ul>
        <div class="flex items-center justify-between border-t border-[var(--color-border)] px-5 py-4">
          <span class="text-xs text-[var(--color-text-secondary)]">已选择 {{ selectedSub2APICount }} 个账号</span>
          <div class="flex gap-2">
            <Button :disabled="busy" @click="closeSub2APIImport">取消</Button>
            <Button
              variant="primary"
              :disabled="busy || selectedSub2APICount === 0"
              @click="handleImportSub2API"
            >
              导入所选账号
            </Button>
          </div>
        </div>
      </div>
    </ContentModal>
  </div>
</template>