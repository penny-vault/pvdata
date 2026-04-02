<template>
  <span class="chip" :style="{ background: color.bg, color: color.fg }">
    {{ props.model?.dataset }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ model?: any; prop?: string; value?: any }>()

const palette: Record<string, { bg: string; fg: string }> = {
  eod:                { bg: '#1e40af', fg: '#93c5fd' },
  assets:             { bg: '#065f46', fg: '#6ee7b7' },
  'market-holidays':  { bg: '#7e22ce', fg: '#d8b4fe' },
  fundamentals:       { bg: '#b45309', fg: '#fde68a' },
  metrics:            { bg: '#0e7490', fg: '#67e8f9' },
  ratings:            { bg: '#be123c', fg: '#fda4af' },
  consensus:          { bg: '#4338ca', fg: '#c7d2fe' },
  estimates:          { bg: '#a21caf', fg: '#f0abfc' },
  quotes:             { bg: '#15803d', fg: '#86efac' },
}

const defaultColor = { bg: '#374151', fg: '#d1d5db' }

const color = computed(() => {
  const ds = props.model?.dataset?.toLowerCase()
  for (const [key, val] of Object.entries(palette)) {
    if (ds?.includes(key)) return val
  }
  return defaultColor
})
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
