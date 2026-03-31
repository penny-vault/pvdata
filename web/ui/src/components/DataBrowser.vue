<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { getData } from '@/lib/api'

const props = defineProps<{
  subscriptionId: string
  datatype: string
  tableName?: string
}>()

const columns = ref<string[]>([])
const rows = ref<any[][]>([])
const total = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const searchQuery = ref('')
const sortColumn = ref('')
const sortOrder = ref<'asc' | 'desc'>('asc')
const offset = ref(0)
const limit = 50
const scrollContainer = ref<HTMLElement | null>(null)

async function fetchData(append = false) {
  if (append) {
    loadingMore.value = true
  } else {
    loading.value = true
    offset.value = 0
  }

  try {
    const params: Record<string, string> = {
      limit: String(limit),
      offset: String(offset.value),
    }
    if (searchQuery.value) params.q = searchQuery.value
    if (sortColumn.value) {
      params.sort = sortColumn.value
      params.order = sortOrder.value
    }

    const result = await getData(props.subscriptionId, props.datatype, params)

    if (result.columns) {
      columns.value = result.columns
    }
    if (append) {
      rows.value = [...rows.value, ...(result.data || [])]
    } else {
      rows.value = result.data || []
    }
    total.value = result.total || rows.value.length
  } catch (e) {
    console.error('Failed to fetch data:', e)
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

function onSearch() {
  fetchData(false)
}

function toggleSort(col: string) {
  if (sortColumn.value === col) {
    sortOrder.value = sortOrder.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortColumn.value = col
    sortOrder.value = 'asc'
  }
  fetchData(false)
}

function onScroll() {
  if (!scrollContainer.value || loadingMore.value) return
  const el = scrollContainer.value
  const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 100
  if (nearBottom && rows.value.length < total.value) {
    offset.value = rows.value.length
    fetchData(true)
  }
}

function formatCell(value: any): string {
  if (value === null || value === undefined) return '--'
  if (typeof value === 'number') return value.toLocaleString()
  return String(value)
}

onMounted(() => {
  fetchData(false)
})

watch(
  () => [props.subscriptionId, props.datatype],
  () => fetchData(false)
)
</script>

<template>
  <div class="data-browser">
    <div class="data-browser__toolbar">
      <div class="data-browser__search">
        <input
          v-model="searchQuery"
          type="text"
          class="bx--text-input"
          placeholder="Search..."
          @keyup.enter="onSearch"
        />
        <button class="bx--btn bx--btn--primary bx--btn--sm" @click="onSearch">
          Search
        </button>
      </div>
      <span class="data-browser__count">
        {{ rows.length.toLocaleString() }} of {{ total.toLocaleString() }} rows
      </span>
    </div>

    <div v-if="loading" class="data-browser__loading">
      <cv-loading active />
    </div>

    <div
      v-else
      ref="scrollContainer"
      class="data-browser__table-wrap"
      @scroll="onScroll"
    >
      <table class="bx--data-table bx--data-table--compact">
        <thead>
          <tr>
            <th
              v-for="col in columns"
              :key="col"
              class="data-browser__th"
              @click="toggleSort(col)"
            >
              <span class="bx--table-header-label">
                {{ col }}
                <span v-if="sortColumn === col" class="sort-indicator">
                  {{ sortOrder === 'asc' ? '\u25B2' : '\u25BC' }}
                </span>
              </span>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, ri) in rows" :key="ri">
            <td v-for="(cell, ci) in row" :key="ci">
              {{ formatCell(cell) }}
            </td>
          </tr>
        </tbody>
      </table>

      <div v-if="loadingMore" class="data-browser__loading-more">
        Loading more...
      </div>
    </div>
  </div>
</template>

<style scoped>
.data-browser {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.data-browser__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  flex-wrap: wrap;
}

.data-browser__search {
  display: flex;
  gap: 0.5rem;
  align-items: center;
}

.data-browser__search .bx--text-input {
  background-color: var(--cds-field-01, #262626);
  color: var(--cds-text-primary, #f4f4f4);
  border: none;
  border-bottom: 1px solid var(--cds-border-strong-01, #6f6f6f);
  padding: 0.5rem 1rem;
  min-width: 280px;
}

.data-browser__count {
  color: var(--cds-text-secondary, #c6c6c6);
  font-size: 0.875rem;
  white-space: nowrap;
}

.data-browser__loading {
  display: flex;
  justify-content: center;
  padding: 3rem;
}

.data-browser__table-wrap {
  max-height: 500px;
  overflow: auto;
  border: 1px solid var(--cds-border-subtle-01, #393939);
  border-radius: 2px;
}

.data-browser__th {
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}

.sort-indicator {
  margin-left: 0.25rem;
  font-size: 0.625rem;
}

.data-browser__loading-more {
  text-align: center;
  padding: 1rem;
  color: var(--cds-text-secondary, #c6c6c6);
  font-size: 0.875rem;
}

.bx--data-table {
  width: 100%;
}

.bx--data-table thead th {
  background-color: var(--cds-layer-accent-01, #333333);
  color: var(--cds-text-primary, #f4f4f4);
  position: sticky;
  top: 0;
  z-index: 1;
}

.bx--data-table tbody tr:hover td {
  background-color: var(--cds-layer-hover-01, #353535);
}

.bx--data-table td {
  color: var(--cds-text-primary, #f4f4f4);
  white-space: nowrap;
  max-width: 300px;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
