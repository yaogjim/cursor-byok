<script setup>
import { messageState, provideMessage, removeMessage } from "@/composables/useMessage";

provideMessage();
const tones = {
  success: { classes: "border-[var(--color-success-border)] bg-[var(--color-success-bg)]", icon: "icon-[mdi--check-circle-outline] text-[var(--color-success-text)]" },
  error: { classes: "border-[var(--color-error-border)] bg-[var(--color-error-bg)]", icon: "icon-[mdi--alert-circle-outline] text-[var(--color-error-text)]" },
  info: { classes: "border-[var(--color-info-border)] bg-[var(--color-info-bg)]", icon: "icon-[mdi--information-outline] text-[var(--color-info-text)]" },
};
</script>

<template>
  <Teleport to="body">
    <div class="pointer-events-none fixed inset-x-0 top-[calc(48px+env(safe-area-inset-top))] z-[11000] flex justify-center px-4">
      <Transition name="message-slide" mode="out-in">
        <div
          v-if="messageState.current"
          :key="messageState.current.id"
          :role="messageState.current.tone === 'error' ? 'alert' : 'status'"
          :aria-live="messageState.current.tone === 'error' ? 'assertive' : 'polite'"
          :class="tones[messageState.current.tone].classes"
          class="flex max-w-[min(520px,calc(100vw-32px))] items-center gap-2 rounded-[8px] border px-4 py-2.5 text-sm leading-5 text-[var(--color-text)] shadow-[var(--shadow-popover)]"
        >
          <span :class="tones[messageState.current.tone].icon" class="shrink-0 text-[18px]" aria-hidden="true" />
          <span class="min-w-0 break-words">{{ messageState.current.content }}</span>
          <button
            v-if="messageState.current.tone === 'error'"
            type="button"
            aria-label="关闭"
            class="pointer-events-auto flex size-6 shrink-0 cursor-pointer items-center justify-center rounded hover:bg-[var(--color-surface-hover)]"
            @click="removeMessage(messageState.current.id, { force: true })"
          >
            <span class="icon-[mdi--close] text-[18px]" aria-hidden="true" />
          </button>
        </div>
      </Transition>
    </div>
  </Teleport>
</template>

<style scoped>
.message-slide-enter-active,
.message-slide-leave-active {
  transition: transform 0.2s ease, opacity 0.2s ease;
}
.message-slide-enter-from {
  opacity: 0;
  transform: translateY(-8px);
}
.message-slide-enter-to,
.message-slide-leave-from {
  opacity: 1;
  transform: translateY(0);
}
.message-slide-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
