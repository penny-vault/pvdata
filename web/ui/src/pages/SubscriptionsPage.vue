<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { getSubscriptions } from '@/lib/api'
import RevoGrid, { VGridVueTemplate } from '@revolist/vue3-datagrid'
import Button from 'primevue/button'
import InputText from 'primevue/inputtext'
import ProgressSpinner from 'primevue/progressspinner'
import Message from 'primevue/message'
import StatusCell from '@/components/StatusCell.vue'
import RunStatusCell from '@/components/RunStatusCell.vue'
import ProviderCell from '@/components/ProviderCell.vue'
import DatasetCell from '@/components/DatasetCell.vue'
import { displayTz, formatTimestamp } from '@/lib/timezone'

const POLL_INTERVAL_MS = 10_000

const router = useRouter()
const subscriptions = ref<any[]>([])
const loading = ref(true)
const error = ref('')
const searchQuery = ref('')

const gridColumns = [
  { prop: 'name', name: 'Name', sortable: true, size: 220 },
  { prop: 'provider', name: 'Provider', sortable: true, size: 130, cellTemplate: VGridVueTemplate(ProviderCell) },
  { prop: 'dataset', name: 'Dataset', sortable: true, size: 200, cellTemplate: VGridVueTemplate(DatasetCell) },
  { prop: 'status', name: 'Status', sortable: true, size: 110, cellTemplate: VGridVueTemplate(StatusCell) },
  { prop: 'last_run_status', name: 'Last Run', sortable: true, size: 120, cellTemplate: VGridVueTemplate(RunStatusCell) },
  { prop: 'total_records', name: 'Total Records', sortable: true, size: 180 },
  { prop: 'last_run', name: 'Last Import', sortable: true, size: 180 },
  { prop: 'records_last', name: 'Records Last', sortable: true, size: 150 },
  { prop: 'next_run', name: 'Next Import', sortable: true, size: 160 },
]

function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined) return '--'
  return n.toLocaleString()
}

const allRows = computed(() => {
  // Read displayTz so the computed re-runs on toggle.
  void displayTz.value
  return subscriptions.value.map(sub => ({
    _id: sub.id,
    name: sub.name || sub.id,
    provider: sub.provider,
    dataset: sub.dataset,
    status: sub.active ? 'Active' : 'Inactive',
    last_run_status: sub.last_run_status || '',
    total_records: formatNumber(sub.total_records),
    last_run: formatTimestamp(sub.last_run),
    records_last: formatNumber(sub.num_records_last_import),
    next_run: sub.next_run_human || '--',
  }))
})

const datasetFilter = ref('')

const uniqueDatasets = computed(() =>
  [...new Set(allRows.value.map(r => r.dataset))].sort()
)

const gridRows = computed(() => {
  let rows = allRows.value
  if (datasetFilter.value) {
    rows = rows.filter(r => r.dataset === datasetFilter.value)
  }
  const q = searchQuery.value.toLowerCase().trim()
  if (q) {
    rows = rows.filter(row =>
      Object.values(row).some(val =>
        String(val).toLowerCase().includes(q)
      )
    )
  }
  return [...rows].sort((a, b) => {
    if (a.status !== b.status) return a.status.localeCompare(b.status)
    return a.name.localeCompare(b.name)
  })
})

function toggleDatasetFilter(ds: string) {
  datasetFilter.value = datasetFilter.value === ds ? '' : ds
}

function onCellFocus(e: CustomEvent) {
  const detail = e.detail
  if (detail?.model?._id) {
    router.push(`/subscriptions/${detail.model._id}`)
  }
}

async function refresh() {
  try {
    subscriptions.value = await getSubscriptions()
    error.value = ''
  } catch (e: any) {
    error.value = e.message || 'Failed to load subscriptions'
  }
}

let pollTimer: ReturnType<typeof setInterval> | null = null

onMounted(async () => {
  await refresh()
  loading.value = false
  pollTimer = setInterval(refresh, POLL_INTERVAL_MS)
})

onUnmounted(() => {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
})
</script>

<template>
  <div>
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1rem">
      <h1>Subscriptions</h1>
      <Button label="New Subscription" icon="pi pi-plus" size="small" @click="router.push('/subscriptions/new')" />
    </div>

    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0">
      <ProgressSpinner />
    </div>

    <Message v-else-if="error" severity="error" :closable="true" @close="error = ''">{{ error }}</Message>

    <p v-else-if="subscriptions.length === 0">No subscriptions yet. Create one to get started.</p>

    <template v-else>
      <div style="display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.75rem; flex-wrap: wrap">
        <InputText
          v-model="searchQuery"
          placeholder="Search subscriptions..."
          style="width: 300px"
        />
        <Button
          v-for="ds in uniqueDatasets"
          :key="ds"
          :label="ds"
          size="small"
          :severity="datasetFilter === ds ? undefined : 'secondary'"
          :outlined="datasetFilter !== ds"
          @click="toggleDatasetFilter(ds)"
        />
      </div>

      <RevoGrid
        :columns="gridColumns"
        :source="gridRows"
        :filter="true"
        :resize="true"
        :rowSize="36"
        theme="darkCompact"
        style="height: 600px; cursor: pointer"
        @beforecellfocus="onCellFocus"
      />
    </template>
  </div>
</template>
