<script setup>
import { computed, ref, useAttrs, watch } from "vue";

defineOptions({
  inheritAttrs: false,
});

const props = defineProps({
  modelValue: { type: String, default: "" },
  type: { type: String, default: "text" },
  placeholder: { type: String, default: "" },
  disabled: { type: Boolean, default: false },
  allowVisibilityToggle: { type: Boolean, default: false },
});

const emit = defineEmits(["update:modelValue"]);
const attrs = useAttrs();
const isPasswordVisible = ref(false);

const canToggleVisibility = computed(() => props.type === "password" && props.allowVisibilityToggle);
const inputType = computed(() => {
  if (!canToggleVisibility.value) {
    return props.type;
  }
  return isPasswordVisible.value ? "text" : "password";
});

watch(
  () => [props.type, props.allowVisibilityToggle],
  ([type, allowVisibilityToggle]) => {
    if (type !== "password" || !allowVisibilityToggle) {
      isPasswordVisible.value = false;
    }
  },
  { immediate: true },
);

function handleInput(event) {
  emit("update:modelValue", event?.target?.value ?? "");
}

function toggleVisibility() {
  if (!canToggleVisibility.value || props.disabled) {
    return;
  }
  isPasswordVisible.value = !isPasswordVisible.value;
}
</script>

<template>
  <div class="relative w-full">
    <input
      v-bind="attrs"
      :value="modelValue"
      :type="inputType"
      :placeholder="placeholder"
      :disabled="disabled"
      class="h-9 w-full rounded-[6px] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 text-sm text-[var(--color-text)] outline-none transition-colors placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-primary)] disabled:cursor-not-allowed disabled:opacity-60"
      :class="canToggleVisibility ? 'pr-10' : ''"
      @input="handleInput"
    />

    <button
      v-if="canToggleVisibility"
      type="button"
      class="absolute inset-y-0 right-0 center-row px-3 text-[var(--color-text-muted)] transition-colors hover:text-[var(--color-text)] focus:text-[var(--color-text)] focus:outline-none disabled:cursor-not-allowed disabled:opacity-50"
      :aria-label="isPasswordVisible ? '隐藏访问密钥' : '显示访问密钥'"
      :aria-pressed="isPasswordVisible"
      :disabled="disabled"
      @click="toggleVisibility"
    >
      <span
        :class="[
          isPasswordVisible ? 'icon-[mdi--eye-off-outline]' : 'icon-[mdi--eye-outline]',
          'text-[18px]',
        ]"
      ></span>
    </button>
  </div>
</template>
