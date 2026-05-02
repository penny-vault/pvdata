<template>
  <span v-if="status" class="chip" :class="chipClass">
    <i v-if="status === 'running'" class="pi pi-spin pi-spinner" style="margin-right: 0.4rem; font-size: 11px" />
    {{ status }}
  </span>
  <span v-else style="opacity: 0.5">--</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  // RevoGrid passes `model` (the row), DataTable passes `status` directly.
  status?: string
  model?: any
  prop?: string
  value?: any
}>()

const status = computed<string>(() => props.status ?? props.model?.last_run_status ?? '')

const chipClass = computed(() => {
  switch (status.value) {
    case 'success': return 'chip-success'
    case 'failed': return 'chip-failed'
    case 'running': return 'chip-running'
    case 'cancelled': return 'chip-cancelled'
    default: return 'chip-secondary'
  }
})
</script>

<style scoped>
.chip {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  line-height: 1.4;
}
.chip-success {
  background: #166534;
  color: #4ade80;
}
.chip-failed {
  background: #7f1d1d;
  color: #fca5a5;
}
.chip-running {
  background: #78350f;
  color: #fcd34d;
}
.chip-cancelled {
  background: #4b5563;
  color: #e5e7eb;
}
.chip-secondary {
  background: #374151;
  color: #9ca3af;
}
</style>
