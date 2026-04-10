<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Select from 'primevue/select'
import InputText from 'primevue/inputtext'
import Tag from 'primevue/tag'
import { getQualityIssues, getQualitySummary } from '@/lib/api'

// ----- Filter state -----
const severityOptions = [
  { label: 'All Severities', value: '' },
  { label: 'Critical', value: 'critical' },
  { label: 'Error', value: 'error' },
  { label: 'Warning', value: 'warning' },
  { label: 'Info', value: 'info' },
]

const selectedSeverity = ref('')
const filterDataType = ref('')
const filterTicker = ref('')

// ----- Summary state -----
const summary = ref<any[]>([])
const summaryLoading = ref(false)

const criticalCount = computed(() =>
  summary.value.filter((r) => r.severity === 'critical').reduce((s, r) => s + r.count, 0),
)
const errorCount = computed(() =>
  summary.value.filter((r) => r.severity === 'error').reduce((s, r) => s + r.count, 0),
)
const warningCount = computed(() =>
  summary.value.filter((r) => r.severity === 'warning').reduce((s, r) => s + r.count, 0),
)
const infoCount = computed(() =>
  summary.value.filter((r) => r.severity === 'info').reduce((s, r) => s + r.count, 0),
)

// ----- Issues table state -----
const issues = ref<any[]>([])
const totalRecords = ref(0)
const tableLoading = ref(false)
const rows = ref(50)
const first = ref(0)

function tagSeverity(sev: string): string {
  switch (sev) {
    case 'critical':
    case 'error':
      return 'danger'
    case 'warning':
      return 'warn'
    case 'info':
      return 'info'
    default:
      return 'secondary'
  }
}

function formatDate(val: string | null): string {
  if (!val) return ''
  return new Date(val).toLocaleDateString()
}

function formatTimestamp(val: string | null): string {
  if (!val) return ''
  return new Date(val).toLocaleString()
}

async function loadSummary() {
  summaryLoading.value = true
  try {
    summary.value = await getQualitySummary()
  } catch (e: any) {
    console.error('Failed to load quality summary:', e.message)
  } finally {
    summaryLoading.value = false
  }
}

async function loadIssues(offset = 0) {
  tableLoading.value = true
  const params: Record<string, string> = {
    limit: String(rows.value),
    offset: String(offset),
  }
  if (selectedSeverity.value) params.severity = selectedSeverity.value
  if (filterDataType.value.trim()) params.data_type = filterDataType.value.trim()
  if (filterTicker.value.trim()) params.ticker = filterTicker.value.trim()
  try {
    const result = await getQualityIssues(params)
    issues.value = result.issues
    totalRecords.value = result.total
    first.value = offset
  } catch (e: any) {
    console.error('Failed to load quality issues:', e.message)
  } finally {
    tableLoading.value = false
  }
}

function onPage(event: any) {
  loadIssues(event.first)
}

function onFilterChange() {
  loadIssues(0)
}

watch([selectedSeverity, filterDataType, filterTicker], () => {
  onFilterChange()
})

onMounted(() => {
  loadSummary()
  loadIssues(0)
})
</script>

<template>
  <div>
    <h2 style="margin-bottom: 1.5rem">Data Quality</h2>

    <!-- Summary cards -->
    <div class="summary-cards" style="display: flex; gap: 1rem; margin-bottom: 1.5rem; flex-wrap: wrap">
      <div class="summary-card critical">
        <div class="card-label">Critical</div>
        <div class="card-count">{{ criticalCount.toLocaleString() }}</div>
      </div>
      <div class="summary-card error">
        <div class="card-label">Error</div>
        <div class="card-count">{{ errorCount.toLocaleString() }}</div>
      </div>
      <div class="summary-card warning">
        <div class="card-label">Warning</div>
        <div class="card-count">{{ warningCount.toLocaleString() }}</div>
      </div>
      <div class="summary-card info">
        <div class="card-label">Info</div>
        <div class="card-count">{{ infoCount.toLocaleString() }}</div>
      </div>
    </div>

    <!-- Filters -->
    <div style="display: flex; gap: 1rem; margin-bottom: 1rem; flex-wrap: wrap; align-items: flex-end">
      <div style="display: flex; flex-direction: column; gap: 0.25rem">
        <label style="font-size: 0.875rem">Severity</label>
        <Select
          v-model="selectedSeverity"
          :options="severityOptions"
          option-label="label"
          option-value="value"
          style="width: 180px"
        />
      </div>
      <div style="display: flex; flex-direction: column; gap: 0.25rem">
        <label style="font-size: 0.875rem">Data Type</label>
        <InputText v-model="filterDataType" placeholder="e.g. eod" style="width: 160px" />
      </div>
      <div style="display: flex; flex-direction: column; gap: 0.25rem">
        <label style="font-size: 0.875rem">Ticker</label>
        <InputText v-model="filterTicker" placeholder="e.g. AAPL" style="width: 140px" />
      </div>
    </div>

    <!-- Issues table -->
    <DataTable
      :value="issues"
      :loading="tableLoading"
      :rows="rows"
      :total-records="totalRecords"
      lazy
      paginator
      @page="onPage"
      :first="first"
      scroll-height="600px"
      scrollable
    >
      <Column field="detected_at" header="Detected At" style="min-width: 160px">
        <template #body="{ data }">{{ formatTimestamp(data.detected_at) }}</template>
      </Column>
      <Column field="severity" header="Severity" style="min-width: 100px">
        <template #body="{ data }">
          <Tag :value="data.severity" :severity="tagSeverity(data.severity)" />
        </template>
      </Column>
      <Column field="check_name" header="Check" style="min-width: 180px" />
      <Column field="ticker" header="Ticker" style="min-width: 90px" />
      <Column field="data_type" header="Data Type" style="min-width: 100px" />
      <Column field="message" header="Message" style="min-width: 260px" />
      <Column field="event_date" header="Event Date" style="min-width: 120px">
        <template #body="{ data }">{{ formatDate(data.event_date) }}</template>
      </Column>
      <Column field="field" header="Field" style="min-width: 100px" />
    </DataTable>
  </div>
</template>

<style scoped>
.summary-card {
  flex: 1;
  min-width: 140px;
  border-radius: 8px;
  padding: 1rem 1.5rem;
  background: var(--p-surface-800, rgba(255, 255, 255, 0.05));
  border: 1px solid var(--p-content-border-color);
}

.card-label {
  font-size: 0.8rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  opacity: 0.7;
  margin-bottom: 0.25rem;
}

.card-count {
  font-size: 2rem;
  font-weight: 600;
}

.summary-card.critical .card-count {
  color: var(--p-red-400, #f87171);
}

.summary-card.error .card-count {
  color: var(--p-orange-400, #fb923c);
}

.summary-card.warning .card-count {
  color: var(--p-yellow-400, #facc15);
}

.summary-card.info .card-count {
  color: var(--p-blue-400, #60a5fa);
}
</style>
