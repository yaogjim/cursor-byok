<script setup>
import { autoUpdate, computePosition, flip, offset, shift } from "@floating-ui/dom";
import copyTextToClipboard from "copy-text-to-clipboard";
import { computed, nextTick, onBeforeUnmount, ref, useSlots, watchPostEffect } from "vue";

const props = defineProps({
  content: { type: String, default: "" },
  copyable: { type: Boolean, default: false },
  copyText: { type: String, default: "" },
});

const slots = useSlots();
const HIDE_DELAY_MS = 300;
const COPY_RESET_DELAY_MS = 1500;
const triggerRef = ref(null);
const tooltipRef = ref(null);
const isOpen = ref(false);
const tooltipStyle = ref({});
const copied = ref(false);
let hideTimer = null;
let copyResetTimer = null;

const copyValue = computed(() => String(props.copyText || props.content || "").trim());
const hasContent = computed(() => !!props.content || !!slots.default);
const showCopyButton = computed(() => props.copyable && !!copyValue.value);

function showTooltip() {
  if (!hasContent.value) {
    return;
  }
  clearHideTimer();
  isOpen.value = true;
  nextTick(() => {
    updatePosition();
  });
}

function hideTooltip() {
  isOpen.value = false;
}

function clearHideTimer() {
  if (hideTimer) {
    window.clearTimeout(hideTimer);
    hideTimer = null;
  }
}

function clearCopyResetTimer() {
  if (copyResetTimer) {
    window.clearTimeout(copyResetTimer);
    copyResetTimer = null;
  }
}

function scheduleHideTooltip() {
  clearHideTimer();
  hideTimer = window.setTimeout(() => {
    hideTooltip();
    hideTimer = null;
  }, HIDE_DELAY_MS);
}

function updatePosition() {
  if (!triggerRef.value || !tooltipRef.value) {
    return;
  }

  computePosition(triggerRef.value, tooltipRef.value, {
    placement: "top",
    middleware: [
      offset(10),
      flip({ padding: 12 }),
      shift({ padding: 12 }),
    ],
  }).then(({ x, y }) => {
    tooltipStyle.value = {
      left: `${x}px`,
      top: `${y}px`,
    };
  });
}

function handleCopy() {
  if (!copyValue.value) {
    return;
  }
  copyTextToClipboard(copyValue.value);
  copied.value = true;
  clearCopyResetTimer();
  copyResetTimer = window.setTimeout(() => {
    copied.value = false;
    copyResetTimer = null;
  }, COPY_RESET_DELAY_MS);
}

watchPostEffect((cleanup) => {
  if (!isOpen.value || !triggerRef.value || !tooltipRef.value) {
    return;
  }

  const stop = autoUpdate(triggerRef.value, tooltipRef.value, updatePosition);
  cleanup(() => {
    stop();
  });
});

onBeforeUnmount(() => {
  clearHideTimer();
  clearCopyResetTimer();
  hideTooltip();
});
</script>

<template>
  <span class="inline-flex">
    <button
      ref="triggerRef"
      type="button"
      class="center-row h-[16px] w-[16px] cursor-help rounded-full text-[var(--color-text-muted)] transition-colors duration-150 hover:text-[var(--color-text)]"
      @mouseenter="showTooltip"
      @mouseleave="scheduleHideTooltip"
      @focus="showTooltip"
      @blur="scheduleHideTooltip"
    >
      <span class="icon-[mdi--information-outline] text-[14px]"></span>
    </button>

    <Teleport to="body">
      <div
        v-if="isOpen"
        ref="tooltipRef"
        class="fixed z-[10000] flex max-h-[320px] max-w-[420px] flex-col overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-left text-[12px] leading-relaxed text-[var(--color-text)] shadow-[0_12px_32px_rgba(0,0,0,0.45)]"
        :style="tooltipStyle"
        @mouseenter="showTooltip"
        @mouseleave="scheduleHideTooltip"
      >
        <div v-if="showCopyButton" class="mb-2 flex shrink-0 justify-end">
          <button
            type="button"
            class="center-row gap-1 rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-2 py-1 text-[11px] text-[var(--color-text)] transition-colors duration-150 hover:border-[var(--color-border-strong)] hover:bg-[var(--color-surface-hover)]"
            @click="handleCopy"
          >
            <span :class="copied ? 'icon-[mdi--check]' : 'icon-[mdi--content-copy]'" class="text-[13px]"></span>
            <span>{{ copied ? "已复制" : "拷贝" }}</span>
          </button>
        </div>
        <div class="min-h-0 overflow-auto break-words">
          <slot>
            <div class="whitespace-pre-wrap">{{ content }}</div>
          </slot>
        </div>
      </div>
    </Teleport>
  </span>
</template>
