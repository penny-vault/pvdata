<template>
  <span class="chip" :class="props.model?.status === 'Active' ? 'chip-active' : 'chip-inactive'">
    {{ props.model?.status }}
  </span>
</template>

<script setup lang="ts">
const props = defineProps<{ model?: any; prop?: string; value?: any }>()
</script>

<style>
.chip {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  line-height: 1.4;
}
.chip-active {
  background: #166534;
  color: #4ade80;
}
.chip-inactive {
  background: #374151;
  color: #9ca3af;
}
</style>
