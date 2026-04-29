<template>
  <Tag v-if="status" :value="status" :severity="severity">
    <template #default>
      <i v-if="status === 'running'" class="pi pi-spin pi-spinner" style="margin-right: 0.4rem; font-size: 11px" />
      {{ status }}
    </template>
  </Tag>
  <span v-else style="opacity: 0.5">--</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import Tag from 'primevue/tag'

const props = defineProps<{
  // RevoGrid passes `model` (the row), DataTable passes `status` directly.
  status?: string
  model?: any
  prop?: string
  value?: any
}>()

const status = computed<string>(() => props.status ?? props.model?.last_run_status ?? '')

const severity = computed(() => {
  switch (status.value) {
    case 'success': return 'success'
    case 'failed': return 'danger'
    case 'running': return 'warning'
    default: return 'secondary'
  }
})
</script>
