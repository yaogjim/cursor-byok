<script setup>
import Select from "@/components/ui/Select.vue";
import { useLocale } from "@/i18n/runtime";

const props = defineProps({
  border: { type: Boolean, default: true },
  ariaLabel: { type: String, default: "界面语言" },
  buttonClass: { type: String, default: "" },
  menuClass: { type: String, default: "" },
  wrapperClass: { type: String, default: "w-[180px] max-w-full" },
  placeholder: { type: String, default: "选择语言" },
});

const { locale, localeOptions } = useLocale();
</script>

<template>
  <div :class="wrapperClass">
    <div class="flex items-center gap-2">
      <div class="min-w-0 flex-1">
        <Select
          :model-value="locale"
          :options="localeOptions"
          :border="border"
          :aria-label="ariaLabel"
          :button-class="buttonClass"
          :menu-class="menuClass"
          :placeholder="placeholder"
          disabled
        />
      </div>
      <span class="badge-soon shrink-0">计划中</span>
    </div>
  </div>
</template>
