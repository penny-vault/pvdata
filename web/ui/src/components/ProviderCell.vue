<template>
  <span class="chip" :style="{ background: color.bg, color: color.fg }">
    {{ props.model?.provider }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ model?: any; prop?: string; value?: any }>()

const palette: Record<string, { bg: string; fg: string }> = {
  tiingo:   { bg: '#1d4ed8', fg: '#93c5fd' },
  sharadar: { bg: '#7e22ce', fg: '#d8b4fe' },
  fred:     { bg: '#a16207', fg: '#fde68a' },
  ishares:  { bg: '#0e7490', fg: '#67e8f9' },
  nasdaq:   { bg: '#15803d', fg: '#86efac' },
  zacks:    { bg: '#b91c1c', fg: '#fca5a5' },
  massive:  { bg: '#be185d', fg: '#f9a8d4' },
  polygon:  { bg: '#be185d', fg: '#f9a8d4' },
  legacy:   { bg: '#4338ca', fg: '#c7d2fe' },
}

const defaultColor = { bg: '#374151', fg: '#d1d5db' }

const color = computed(() => palette[props.model?.provider] || defaultColor)
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
</style>
