<script setup>
defineProps({
  padded: {
    type: Boolean,
    default: true,
  },
});
</script>
<template>
  <div class="ui-card" :class="{ 'ui-card--flush': !padded }">
    <slot />
  </div>
</template>