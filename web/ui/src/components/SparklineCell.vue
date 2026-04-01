<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  data: { date: string; num_observations: number }[]
}>()

const points = computed(() => {
  if (!props.data || props.data.length === 0) return ''
  const sorted = [...props.data].sort(
    (a, b) => new Date(a.date).getTime() - new Date(b.date).getTime()
  )
  const values = sorted.map((d) => d.num_observations)
  const max = Math.max(...values)
  const min = Math.min(...values)
  const range = max - min || 1
  const width = 120
  const height = 24
  const padding = 2

  return sorted
    .map((_, i) => {
      const x = (i / Math.max(sorted.length - 1, 1)) * (width - padding * 2) + padding
      const y = height - padding - ((values[i] - min) / range) * (height - padding * 2)
      return `${x},${y}`
    })
    .join(' ')
})

const hasData = computed(() => props.data && props.data.length > 0)
</script>

<template>
  <span v-if="!hasData" class="sparkline-empty">--</span>
  <svg v-else class="sparkline-svg" width="120" height="24" viewBox="0 0 120 24">
    <polyline
      :points="points"
      fill="none"
      stroke="var(--p-primary-color)"
      stroke-width="1.5"
      stroke-linejoin="round"
      stroke-linecap="round"
    />
  </svg>
</template>

<style scoped>
.sparkline-empty {
  opacity: 0.4;
}
.sparkline-svg {
  display: block;
}
</style>
