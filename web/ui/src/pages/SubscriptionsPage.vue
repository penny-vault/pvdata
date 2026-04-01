<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getSubscriptions, getSparkline } from '@/lib/api'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Tag from 'primevue/tag'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'
import Message from 'primevue/message'
import SparklineCell from '@/components/SparklineCell.vue'

const router = useRouter()
const subscriptions = ref<any[]>([])
const sparklines = ref<Record<string, any[]>>({})
const loading = ref(true)
const error = ref('')

function formatDate(dateStr: string | null): string {
  if (!dateStr || dateStr.startsWith('0001')) return '--'
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return '--'
  return d.toLocaleDateString(undefined, {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  })
}

function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined) return '--'
  return n.toLocaleString()
}

function onRowClick(event: any) {
  if (event.data?.id) router.push(`/subscriptions/${event.data.id}`)
}

async function loadSparkline(id: string) {
  try { sparklines.value[id] = await getSparkline(id) } catch { /* non-critical */ }
}

onMounted(async () => {
  try {
    subscriptions.value = await getSubscriptions()
  } catch (e: any) {
    error.value = e.message || 'Failed to load subscriptions'
  } finally {
    loading.value = false
  }
  for (const sub of subscriptions.value) loadSparkline(sub.id)
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

    <DataTable v-else :value="subscriptions" :rowHover="true" size="small" @row-click="onRowClick" style="cursor: pointer">
      <Column field="name" header="Name">
        <template #body="{ data }">{{ data.name || data.id }}</template>
      </Column>
      <Column field="provider" header="Provider" />
      <Column field="dataset" header="Dataset" />
      <Column field="active" header="Status">
        <template #body="{ data }">
          <Tag :value="data.active ? 'Active' : 'Inactive'" :severity="data.active ? 'success' : 'secondary'" />
        </template>
      </Column>
      <Column header="Sparkline" :sortable="false" style="width: 140px">
        <template #body="{ data }"><SparklineCell :data="sparklines[data.id] || []" /></template>
      </Column>
      <Column header="Last Import">
        <template #body="{ data }">{{ formatDate(data.last_run) }}</template>
      </Column>
      <Column header="Records">
        <template #body="{ data }">{{ formatNumber(data.num_records_last_import) }}</template>
      </Column>
      <Column header="Next Import">
        <template #body="{ data }">{{ data.next_run_human || '--' }}</template>
      </Column>
    </DataTable>
  </div>
</template>
