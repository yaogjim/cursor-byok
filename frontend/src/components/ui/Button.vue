<script setup>
defineProps({
  variant: {
    type: String,
    default: "default",
    validator: (v) => ["default", "primary", "text"].includes(v),
  },
});
</script>

<template>
  <button
    v-if="variant === 'text'"
    type="button"
    class="!whitespace-nowrap shrink-0 cursor-pointer text-[13px] font-semibold text-[var(--color-text-secondary)] transition-colors duration-150 hover:text-[var(--color-primary)] active:text-[var(--color-primary-hover)] disabled:cursor-not-allowed disabled:opacity-55"
  >
    <slot />
  </button>
  <button
    v-else
    type="button"
    class="!whitespace-nowrap relative center-row h-9 min-h-[32px] cursor-pointer gap-1.5 rounded-[10px] border px-3.5 text-[13px] font-semibold transition-all duration-150 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-55"
    :class="{
      'border-[var(--color-border)] bg-[var(--color-surface)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-strong)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]': variant === 'default',
      'border-[var(--color-primary)] bg-[var(--color-primary)] text-on-primary hover:border-[var(--color-primary-hover)] hover:bg-[var(--color-primary-hover)]': variant === 'primary',
    }"
  >
    <slot />
  </button>
</template>