<script setup>
import { computed, ref } from "vue";
import { useDialogFocus } from "@/composables/useDialogFocus";

const props = defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: "" },
  size: {
    type: String,
    default: "md",
    validator: (value) => ["md", "lg", "xl"].includes(value),
  },
  closeOnBackdrop: { type: Boolean, default: true },
  closeOnEscape: { type: Boolean, default: true },
  closeDisabled: { type: Boolean, default: false },
  headerless: { type: Boolean, default: false },
});

const emit = defineEmits(["close"]);
const panelRef = ref(null);
const closeButtonRef = ref(null);

const panelClass = computed(() => ({
  md: "max-w-[420px]",
  lg: "max-w-[680px]",
  xl: "h-full max-h-[760px] max-w-[880px]",
}[props.size]));

function requestClose() {
  if (!props.closeDisabled) {
    emit("close");
  }
}

function handleBackdropClick() {
  if (props.closeOnBackdrop) {
    requestClose();
  }
}

useDialogFocus({
  open: () => props.open,
  panelRef,
  initialFocus: () => panelRef.value?.querySelector(
    "input:not([disabled]), textarea:not([disabled]), select:not([disabled])",
  ) || closeButtonRef.value,
  onEscape: () => { if (props.closeOnEscape) requestClose(); },
});
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition-opacity duration-150 ease-out"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition-opacity duration-100 ease-in"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
      <div
        v-show="open"
        class="fixed  inset-0 z-[100] flex items-center justify-center bg-black/60 px-7 pb-[38px] pt-[calc(48px+env(safe-area-inset-top))]"
        @click.self="handleBackdropClick"
      >
        <section
          ref="panelRef"
          tabindex="-1"
          role="dialog"
          aria-modal="true"
          :aria-label="title || undefined"
          class="flex w-full max-h-[660px] min-h-0 flex-col overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[0_24px_64px_rgba(0,0,0,0.65)]"
          :class="panelClass"
          @click.stop
        >
          <header v-if="!headerless" class="flex h-12 shrink-0 items-center justify-between border-b border-[var(--color-border)] px-4">
            <h2 class="min-w-0 truncate text-base font-medium text-[var(--color-text)]">{{ title }}</h2>
            <button
              ref="closeButtonRef"
              type="button"
              class="center-row w-[28px] h-[28px] justify-center size-8 shrink-0 rounded-[6px] text-[var(--color-text-secondary)] outline-none transition-colors hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)] focus-visible:ring-2 focus-visible:ring-[#10AD5D]/40 disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="closeDisabled"
              aria-label="关闭"
              @click="requestClose"
            >
              <span class="icon-[mdi--close] text-[19px]"></span>
            </button>
          </header>
          <div class="min-h-0 flex-1 overflow-hidden">
            <slot />
          </div>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>
