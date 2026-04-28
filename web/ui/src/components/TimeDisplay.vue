<script setup lang="ts">
import { computed } from 'vue'
import { formatTimestamp, toggleTz, displayTz, type FormatOpts } from '@/lib/timezone'

const props = withDefaults(defineProps<{
  value: string | Date | null | undefined
  date?: boolean
  time?: boolean
  showTz?: boolean
  clickable?: boolean
}>(), {
  date: true,
  time: true,
  showTz: true,
  clickable: true,
})

const formatted = computed(() => {
  // Read displayTz so the computed re-runs on toggle.
  void displayTz.value
  const opts: FormatOpts = { date: props.date, time: props.time, showTz: props.showTz }
  return formatTimestamp(props.value, opts)
})

function onClick(e: MouseEvent) {
  if (!props.clickable) return
  e.stopPropagation()
  toggleTz()
}
</script>

<template>
  <span
    class="time-display"
    :class="{ clickable }"
    :title="clickable ? 'Click to switch timezone' : undefined"
    @click="onClick"
  >{{ formatted }}</span>
</template>

<style scoped>
.time-display.clickable {
  cursor: pointer;
  border-bottom: 1px dotted currentColor;
  opacity: 0.95;
}
.time-display.clickable:hover {
  opacity: 1;
}
</style>
