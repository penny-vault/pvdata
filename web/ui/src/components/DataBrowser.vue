<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { getData } from '@/lib/api'
import RevoGrid from '@revolist/vue3-datagrid'
import InputText from 'primevue/inputtext'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'

const props = defineProps<{ subscriptionId: string; datatype: string; tableName?: string }>()

const columns = ref<string[]>([])
const rows = ref<any[]>([])
const total = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const searchQuery = ref('')
const sortColumn = ref('')
const sortOrder = ref<'asc' | 'desc'>('asc')
const offset = ref(0)
const limit = 50

async function fetchData(append = false) {
  if (append) loadingMore.value = true; else { loading.value = true; offset.value = 0 }
  try {
    const params: Record<string, string> = { limit: String(limit), offset: String(offset.value) }
    if (searchQuery.value) params.q = searchQuery.value
    if (sortColumn.value) { params.sort = sortColumn.value; params.order = sortOrder.value }
    const result = await getData(props.subscriptionId, props.datatype, params)
    if (result.columns) columns.value = result.columns
    rows.value = append ? [...rows.value, ...(result.data || [])] : (result.data || [])
    total.value = result.total || rows.value.length
  } catch (e) { console.error('Failed to fetch data:', e) }
  finally { loading.value = false; loadingMore.value = false }
}

function formatCell(value: any): string {
  if (value === null || value === undefined) return '--'
  if (typeof value === 'number') return value.toLocaleString()
  return String(value)
}

const gridColumns = computed(() =>
  columns.value.map(col => ({ prop: col, name: col, sortable: true, autoSize: true }))
)

const gridRows = computed(() =>
  rows.value.map(row => {
    const obj: Record<string, any> = {}
    columns.value.forEach((col, i) => {
      const val = Array.isArray(row) ? row[i] : row[col]
      obj[col] = formatCell(val)
    })
    return obj
  })
)

onMounted(() => fetchData(false))
watch(() => [props.subscriptionId, props.datatype], () => fetchData(false))
</script>

<template>
  <div>
    <div style="display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; margin-bottom: 1rem">
      <div style="display: flex; align-items: center; gap: 0.5rem">
        <InputText v-model="searchQuery" placeholder="Search..." @keyup.enter="fetchData(false)" />
        <Button label="Search" icon="pi pi-search" size="small" @click="fetchData(false)" />
      </div>
      <span>{{ rows.length.toLocaleString() }} of {{ total.toLocaleString() }} rows</span>
    </div>

    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0"><ProgressSpinner /></div>

    <div v-else>
      <RevoGrid
        :columns="gridColumns"
        :source="gridRows"
        :resize="true"
        theme="darkCompact"
        style="height: 500px"
      />
      <div v-if="rows.length < total && !loadingMore" style="display: flex; justify-content: center; padding: 0.5rem 0">
        <Button label="Load more" text size="small" @click="offset = rows.length; fetchData(true)" />
      </div>
    </div>
  </div>
</template>
