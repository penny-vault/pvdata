<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { getData } from '@/lib/api'
import RevoGrid from '@revolist/vue3-datagrid'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'

const props = defineProps<{ subscriptionId: string; datatype: string }>()

const columns = ref<string[]>([])
const searchColumns = ref<string[]>([])
const rows = ref<any[]>([])
const total = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const searchQuery = ref('')
const searchField = ref('')
const offset = ref(0)
const limit = 50
const sortField = ref('')
const sortOrder = ref<'asc' | 'desc'>('desc')

async function fetchData(append = false) {
  if (append) loadingMore.value = true; else { loading.value = true; offset.value = 0 }
  try {
    const params: Record<string, string> = { limit: String(limit), offset: String(offset.value) }
    if (searchQuery.value && searchField.value) {
      params.q = searchQuery.value
      params.search_col = searchField.value
    }
    if (sortField.value) {
      params.sort = sortField.value
      params.order = sortOrder.value
    }
    const result = await getData(props.subscriptionId, props.datatype, params)
    if (result.columns) columns.value = result.columns
    if (result.search_columns) {
      searchColumns.value = result.search_columns
      if (!searchField.value && result.search_columns.length > 0) {
        searchField.value = result.search_columns[0]
      }
    }
    if (result.sort && !sortField.value) sortField.value = result.sort
    if (result.order && !sortOrder.value) sortOrder.value = result.order
    rows.value = append ? [...rows.value, ...(result.data || [])] : (result.data || [])
    total.value = result.total || rows.value.length
  } catch (e) { console.error('Failed to fetch data:', e) }
  finally { loading.value = false; loadingMore.value = false }
}

function changeSort(col: string) {
  if (sortField.value === col) {
    sortOrder.value = sortOrder.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortField.value = col
    sortOrder.value = 'asc'
  }
  fetchData(false)
}

function formatCell(value: any): string {
  if (value === null || value === undefined) return '--'
  if (typeof value === 'number') return value.toLocaleString()
  if (typeof value === 'object') {
    // Handle PostgreSQL time/date objects that come as {hours, minutes, ...} or similar
    try { return JSON.stringify(value) } catch { return '--' }
  }
  return String(value)
}

function imageCellTemplate(createElement: any, props: any) {
  const url = props.model?.[props.prop]
  if (!url || typeof url !== 'string' || !url.startsWith('http')) {
    return ''
  }
  return createElement('img', {
    src: url,
    alt: props.prop,
    style: 'max-height: 28px; max-width: 80px; vertical-align: middle',
    loading: 'lazy',
    referrerpolicy: 'no-referrer',
  })
}

const gridColumns = computed(() =>
  columns.value.map(col => {
    // Size columns based on content type heuristics
    let size = 120
    if (col === 'composite_figi' || col === 'share_class_figi') size = 150
    else if (col.includes('date') || col.includes('time')) size = 180
    else if (col === 'ticker' || col === 'series') size = 100
    else if (col === 'name' || col === 'description') size = 250
    else if (col === 'icon_url' || col === 'logo_url') size = 100
    else if (col.length > 12) size = col.length * 10

    const indicator = sortField.value === col ? (sortOrder.value === 'asc' ? ' ↑' : ' ↓') : ''

    const column: Record<string, any> = { prop: col, name: col + indicator, size }
    if (col === 'icon_url' || col === 'logo_url') {
      column.cellTemplate = imageCellTemplate
    }
    return column as any
  })
)

const gridRows = computed(() =>
  rows.value.map(row => {
    const obj: Record<string, any> = {}
    columns.value.forEach((col, i) => {
      const val = Array.isArray(row) ? row[i] : row[col]
      if (col === 'icon_url' || col === 'logo_url') {
        obj[col] = val ?? ''
      } else {
        obj[col] = formatCell(val)
      }
    })
    return obj
  })
)

function doSearch() {
  fetchData(false)
}

function clearSearch() {
  searchQuery.value = ''
  fetchData(false)
}

onMounted(() => fetchData(false))
watch(() => [props.subscriptionId, props.datatype], () => { searchQuery.value = ''; fetchData(false) })
</script>

<template>
  <div>
    <div style="display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; flex-wrap: wrap; margin-bottom: 1rem">
      <div style="display: flex; align-items: center; gap: 0.5rem">
        <Select
          v-if="searchColumns.length > 0"
          v-model="searchField"
          :options="searchColumns"
          placeholder="Column"
          style="width: 160px"
        />
        <InputText
          v-model="searchQuery"
          :placeholder="searchField ? `Filter by ${searchField}...` : 'Search...'"
          @keyup.enter="doSearch"
          style="width: 200px"
        />
        <Button icon="pi pi-search" size="small" @click="doSearch" />
        <Button v-if="searchQuery" icon="pi pi-times" severity="secondary" text size="small" @click="clearSearch" />
      </div>
      <div style="display: flex; align-items: center; gap: 0.5rem">
        <Select
          v-if="columns.length > 0"
          v-model="sortField"
          :options="columns"
          placeholder="Sort by"
          size="small"
          style="width: 180px"
          @update:model-value="fetchData(false)"
        />
        <Button
          v-if="sortField"
          :icon="sortOrder === 'asc' ? 'pi pi-sort-amount-up' : 'pi pi-sort-amount-down'"
          size="small"
          severity="secondary"
          text
          @click="sortOrder = sortOrder === 'asc' ? 'desc' : 'asc'; fetchData(false)"
        />
        <span>{{ rows.length.toLocaleString() }} of {{ total.toLocaleString() }} rows</span>
      </div>
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
