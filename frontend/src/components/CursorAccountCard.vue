<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  disconnectCursorAccount,
  getCursorAccountStatus,
  startCursorAccountLogin,
} from "@/services/clientApi";
import { toUserError } from "@/state/appState";
import { Browser } from "@wailsio/runtime";
import { computed, onMounted, onUnmounted, ref } from "vue";

const CURSOR_ACCOUNT_CONTRIBUTOR_URL = "https://github.com/aike0210";
const message = useMessage();

defineProps({
  flush: {
    type: Boolean,
    default: false,
  },
});

const cursorAccountStatus = ref({
  state: "signed_out",
  authId: "",
  email: "",
  error: "",
});
const cursorAccountBusy = ref(false);
let cursorAccountTimer = null;

function maskCursorAccountIdentifier(value) {
  const identifier = String(value || "").trim();
  if (!identifier) return "";

  const atIndex = identifier.indexOf("@");
  if (atIndex > 0 && atIndex < identifier.length - 1) {
    const localPart = identifier.slice(0, atIndex);
    const domain = identifier.slice(atIndex + 1);
    const maskedLocalPart = localPart.length <= 2
      ? `${localPart[0]}***`
      : `${localPart[0]}***${localPart.at(-1)}`;
    return `${maskedLocalPart}@${domain}`;
  }

  if (identifier.length <= 8) return "****";
  return `${identifier.slice(0, 4)}****${identifier.slice(-4)}`;
}

const cursorAccountSignedIn = computed(
  () => cursorAccountStatus.value.state === "signed_in",
);
const cursorAccountWaiting = computed(
  () => cursorAccountStatus.value.state === "waiting",
);
const cursorAccountDisplayIdentifier = computed(() => {
  if (!cursorAccountSignedIn.value) return "";
  return maskCursorAccountIdentifier(
    cursorAccountStatus.value.email || cursorAccountStatus.value.authId,
  );
});
const cursorAccountCountText = computed(() => {
  if (cursorAccountSignedIn.value) {
    return "1 个账号；独立用于插件、Skills 和 MCP，后台调用策略不在此配置";
  }
  return "尚未登录；登录后用于插件、Skills 和 MCP，不会改变 Cursor 客户端当前账号";
});
const cursorAccountStateText = computed(() => {
  if (cursorAccountSignedIn.value) return "已经登录";
  if (cursorAccountWaiting.value) return "等待浏览器登录";
  return "未连接";
});
const cursorAccountTitle = computed(() =>
  cursorAccountDisplayIdentifier.value || "未连接",
);

function showActionError(title, error) {
  const detail = String(error || "服务错误").trim() || "服务错误";
  message(`${title}：${detail}`);
}

async function handleOpenContributor() {
  try {
    await Browser.OpenURL(CURSOR_ACCOUNT_CONTRIBUTOR_URL);
  } catch (error) {
    showActionError("打开贡献者主页失败", toUserError(error));
  }
}

async function refreshCursorAccountStatus() {
  cursorAccountStatus.value = await getCursorAccountStatus();
}

async function handleCursorAccountLogin() {
  cursorAccountBusy.value = true;
  try {
    cursorAccountStatus.value = await startCursorAccountLogin();
  } catch (error) {
    showActionError("登录失败", toUserError(error));
    await refreshCursorAccountStatus().catch(() => {});
  } finally {
    cursorAccountBusy.value = false;
  }
}

async function handleCursorAccountDisconnect() {
  const confirmed = await showModal({
    title: "退出登录",
    content: "只会退出 cursor-byok 中的 Cursor 账号，不会退出 Cursor 客户端。是否继续？",
    confirmText: "退出登录",
    cancelText: "取消",
    showCancel: true,
  });
  if (!confirmed) return;

  cursorAccountBusy.value = true;
  try {
    cursorAccountStatus.value = await disconnectCursorAccount();
  } catch (error) {
    showActionError("退出登录失败", toUserError(error));
  } finally {
    cursorAccountBusy.value = false;
  }
}

onMounted(async () => {
  await refreshCursorAccountStatus().catch(() => {});
  cursorAccountTimer = window.setInterval(() => {
    if (cursorAccountWaiting.value) {
      void refreshCursorAccountStatus().catch(() => {});
    }
  }, 1500);
});

onUnmounted(() => {
  if (cursorAccountTimer) {
    window.clearInterval(cursorAccountTimer);
    cursorAccountTimer = null;
  }
});
</script>

<template>
  <component :is="flush ? 'div' : Card">
    <div class="setting-row">
      <div class="setting-l">
        <div class="setting-t">
          授权账号
          <Tooltip>
            <div class="flex min-w-[220px] flex-col gap-2">
              <div>感谢 @aike0210 对 Cursor 控制面账号功能的贡献。</div>
              <button
                type="button"
                class="flex items-center gap-2 text-left text-[var(--color-primary)] transition-opacity duration-150 hover:opacity-80"
                @click="handleOpenContributor"
              >
                <span class="icon-[mdi--github] text-[14px]"></span>
                <span>github.com/aike0210</span>
                <span class="icon-[mdi--open-in-new] text-[12px]"></span>
              </button>
            </div>
          </Tooltip>
        </div>
        <div class="setting-s">{{ cursorAccountCountText }}</div>
      </div>
    </div>
    <div class="setting-row">
      <div class="row-inline">
        <span class="access-client-icon is-cursor" style="width:32px;height:32px;flex-basis:32px" aria-hidden="true">
          <span class="icon-[mdi--account-circle-outline] text-[18px]" />
        </span>
        <div class="min-w-0">
          <div class="setting-t truncate">{{ cursorAccountTitle }}</div>
          <div class="setting-s">Cursor 控制面账号 · 用于插件、Skills 和 MCP</div>
          <div v-if="cursorAccountWaiting" class="setting-s text-[var(--color-warning-text)]">
            请在浏览器完成登录，完成后返回 Cursor 重新打开插件市场
          </div>
          <div v-if="cursorAccountStatus.error" class="setting-s break-all text-[var(--color-error-text)]">
            {{ cursorAccountStatus.error }}
          </div>
        </div>
      </div>
      <div class="row-inline">
        <span
          class="status-pill"
          :class="cursorAccountSignedIn ? 'is-ok' : (cursorAccountWaiting ? 'is-warn' : 'is-off')"
        >
          {{ cursorAccountStateText }}
        </span>
        <Button
          v-if="cursorAccountSignedIn"
          class="btn-sm"
          :disabled="cursorAccountBusy"
          @click="handleCursorAccountDisconnect"
        >
          退出登录
        </Button>
        <Button
          v-else
          class="btn-sm"
          variant="primary"
          :disabled="cursorAccountBusy || cursorAccountWaiting"
          @click="handleCursorAccountLogin"
        >
          {{ cursorAccountWaiting ? "等待登录..." : "登录 Cursor" }}
        </Button>
      </div>
    </div>
  </component>
</template>
