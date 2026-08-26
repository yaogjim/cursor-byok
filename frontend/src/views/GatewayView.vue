<script setup>
import GatewayCard from "@/components/GatewayCard.vue";
import { configSectionDirty, configSectionStatusText } from "@/state/appState";
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto scroll-shadow-bottom p-4 pt-0 text-[var(--color-text)]">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-medium">网关集成</h2>
        <div class="text-sm text-[var(--color-text-secondary)]">
          为 Codex、OpenCode、Cherry Studio 等提供本机 HTTP 入口
        </div>
      </div>
      <span
        class="text-xs"
        :class="configSectionDirty.gateway ? 'text-amber-700 dark:text-amber-300' : 'text-[var(--color-text-muted)]'"
      >
        {{ configSectionStatusText("gateway") }}
      </span>
    </div>
    <GatewayCard />
  </div>
</template>