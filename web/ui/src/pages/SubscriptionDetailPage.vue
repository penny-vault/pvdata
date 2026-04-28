<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getSubscription, getRunHistory,
  activateSubscription, deactivateSubscription, deleteSubscription,
  runSubscription, subscribeRunEvents,
  getRunStatus, getRunLog,
} from '@/lib/api'
import RevoGrid from '@revolist/vue3-datagrid'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Tag from 'primevue/tag'
import Button from 'primevue/button'
import Card from 'primevue/card'
import Tabs from 'primevue/tabs'
import TabList from 'primevue/tablist'
import Tab from 'primevue/tab'
import TabPanels from 'primevue/tabpanels'
import TabPanel from 'primevue/tabpanel'
import Dialog from 'primevue/dialog'
import InputText from 'primevue/inputtext'
import ProgressSpinner from 'primevue/progressspinner'
import ProgressBar from 'primevue/progressbar'
import Message from 'primevue/message'
import Menu from 'primevue/menu'
import RunHistoryChart from '@/components/RunHistoryChart.vue'
import DataBrowser from '@/components/DataBrowser.vue'
import SubscriptionForm from '@/components/SubscriptionForm.vue'
import LogViewer from '@/components/LogViewer.vue'
import TimeDisplay from '@/components/TimeDisplay.vue'

const route = useRoute()
const router = useRouter()
const id = computed(() => route.params.id as string)

const subscription = ref<any>(null)
const runs = ref<any[]>([])
const runsTotal = ref(0)
const loading = ref(true)
const error = ref('')
const editing = ref(false)
const showDeleteConfirm = ref(false)
const loadingRuns = ref(false)
const runsOffset = ref(0)
const runsLimit = 20
const activeTab = ref('0')
const deleteConfirmText = ref('')

const runStatus = ref<'idle' | 'running' | 'completed' | 'failed'>('idle')
const runRecordCount = ref(0)
const runRecords = ref<{ type: string; summary: string }[]>([])
const maxRunRecords = 200
const runLookback = ref('14d')
const runLogRef = ref<HTMLElement | null>(null)
const runPanelTab = ref<'records' | 'logs'>('records')

interface LogEntry {
  raw: string
  time: string
  level: string
  message: string
  fields: Record<string, any>
}

const liveLogs = ref<LogEntry[]>([])
const maxLiveLogs = 5000

function parseLogLine(line: string): LogEntry {
  try {
    const obj = JSON.parse(line)
    return {
      raw: line,
      time: obj.time || obj.timestamp || '',
      level: (obj.level || '').toLowerCase(),
      message: obj.message || obj.msg || '',
      fields: obj,
    }
  } catch {
    return { raw: line, time: '', level: '', message: line, fields: {} }
  }
}

const showRunLogDialog = ref(false)
const selectedRunLog = ref('')
const selectedRunMeta = ref<{ start: string | null; status: string } | null>(null)
const loadingRunLog = ref(false)
const runMenuRef = ref()
const showCustomLookback = ref(false)
const customLookbackInput = ref('')
const runMenuItems = [
  { label: 'Last 1 day', command: () => { runLookback.value = '1d'; triggerRun() } },
  { label: 'Last 7 days', command: () => { runLookback.value = '7d'; triggerRun() } },
  { label: 'Last 14 days', command: () => { runLookback.value = '14d'; triggerRun() } },
  { label: 'Last 30 days', command: () => { runLookback.value = '30d'; triggerRun() } },
  { label: 'Last 90 days', command: () => { runLookback.value = '90d'; triggerRun() } },
  { label: 'Last 365 days', command: () => { runLookback.value = '365d'; triggerRun() } },
  { separator: true },
  { label: 'Custom...', icon: 'pi pi-pencil', command: () => { customLookbackInput.value = runLookback.value; showCustomLookback.value = true } },
]
const actionsMenuRef = ref()
const actionsMenuItems = computed(() => {
  const items: any[] = []
  if (subscription.value?.provider !== 'legacy') {
    items.push({
      label: subscription.value?.active ? 'Deactivate' : 'Activate',
      icon: subscription.value?.active ? 'pi pi-pause' : 'pi pi-play',
      command: toggleActive,
    })
  }
  items.push({
    label: editing.value ? 'Cancel Edit' : 'Edit',
    icon: 'pi pi-pencil',
    command: () => { editing.value = !editing.value },
  })
  items.push({
    separator: true,
  })
  items.push({
    label: 'Delete',
    icon: 'pi pi-trash',
    command: () => { showDeleteConfirm.value = true },
    class: 'p-menuitem-danger',
  })
  return items
})
let eventSource: EventSource | null = null
const reconnectMaxAttempts = 5
const reconnectDelayMs = 2000
let reconnectAttempts = 0

function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined) return '--'
  return n.toLocaleString()
}

function formatDuration(start: string, end: string | null, status?: string): string {
  if (!end) return '--'
  // Running rows store end_time = start_time as a placeholder until
  // FinalizeRun overwrites it; rendering "0ms" alongside the running
  // spinner is misleading, so show a sentinel instead.
  if (status === 'running' || end === start) return '--'
  const ms = new Date(end).getTime() - new Date(start).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60000).toFixed(1)}m`
}

const runHistoryRows = computed(() =>
  runs.value.map(run => ({
    id: run.id,
    raw: run,
    start_time: run.start_time,
    status: run.status || 'unknown',
    records: formatNumber(run.num_observations),
    duration: formatDuration(run.start_time, run.end_time, run.status),
  }))
)

async function loadSubscription() {
  try { subscription.value = await getSubscription(id.value) }
  catch (e: any) { error.value = e.message || 'Failed to load subscription' }
  finally { loading.value = false }
}

async function loadRuns(append = false) {
  loadingRuns.value = true
  try {
    const result = await getRunHistory(id.value, runsLimit, runsOffset.value)
    runs.value = append ? [...runs.value, ...(result.data || [])] : (result.data || [])
    runsTotal.value = result.total || runs.value.length
  } catch { /* non-critical */ }
  finally { loadingRuns.value = false }
}

async function toggleActive() {
  if (!subscription.value) return
  try {
    if (subscription.value.active) await deactivateSubscription(id.value)
    else await activateSubscription(id.value)
    subscription.value.active = !subscription.value.active
  } catch (e: any) { error.value = e.message || 'Failed to toggle subscription' }
}

async function onDelete() {
  try { await deleteSubscription(id.value); router.push('/') }
  catch (e: any) { error.value = e.message || 'Failed to delete subscription' }
}

function onSaved(updated: any) {
  subscription.value = { ...subscription.value, ...updated }
  editing.value = false
}

function openSqlConsole() {
  const sub = subscription.value
  const table = sub?.data_tables_map?.[sub?.data_types?.[0]] || 'unknown'
  router.push({ path: '/sql', query: { q: `SELECT * FROM ${table} LIMIT 100` } })
}

async function attachEventSource() {
  eventSource = await subscribeRunEvents(id.value)

  eventSource.addEventListener('started', () => {
    runStatus.value = 'running'
  })

  eventSource.addEventListener('record', (e: MessageEvent) => {
    const data = JSON.parse(e.data)
    runRecordCount.value = data.count
    runRecords.value.push({ type: data.type, summary: data.summary })
    if (runRecords.value.length > maxRunRecords) {
      runRecords.value = runRecords.value.slice(-maxRunRecords)
    }
  })

  eventSource.addEventListener('log', (e: MessageEvent) => {
    const entry = parseLogLine(e.data)
    liveLogs.value.push(entry)
    if (liveLogs.value.length > maxLiveLogs) {
      liveLogs.value = liveLogs.value.slice(-maxLiveLogs)
    }
  })

  eventSource.addEventListener('completed', (e: MessageEvent) => {
    const data = JSON.parse(e.data)
    runRecordCount.value = data.count
    runStatus.value = 'completed'
    reconnectAttempts = 0
    eventSource?.close()
    eventSource = null
    loadSubscription()
    loadRuns()
  })

  eventSource.addEventListener('failed', (e: MessageEvent) => {
    const data = JSON.parse(e.data)
    runRecordCount.value = data.count
    runStatus.value = 'failed'
    reconnectAttempts = 0
    error.value = data.error || 'Run failed'
    eventSource?.close()
    eventSource = null
    loadSubscription()
    loadRuns()
  })

  eventSource.onerror = async () => {
    eventSource?.close()
    eventSource = null

    if (runStatus.value !== 'running') {
      reconnectAttempts = 0
      return
    }

    if (reconnectAttempts >= reconnectMaxAttempts) {
      // Don't pretend the run failed — we don't actually know.
      // Clear the live banner; Run History below shows the truth.
      reconnectAttempts = 0
      runStatus.value = 'idle'
      error.value = ''
      await loadRuns()
      await loadSubscription()
      return
    }

    try {
      const status = await getRunStatus(id.value)
      if (status.active) {
        reconnectAttempts++
        // Refresh the run history so the running row's live count is
        // visible, then re-attach to the SSE stream.
        await loadRuns()
        // Hold off briefly so a flapping connection doesn't tight-loop.
        await new Promise(resolve => setTimeout(resolve, reconnectDelayMs))
        await attachEventSource()
        return
      }

      // Registry says no active run — it either finished cleanly
      // (we just missed the 'completed' event) or is genuinely gone.
      // Either way, the Run History row records the actual outcome;
      // surface that instead of fabricating a "Failed" state.
      reconnectAttempts = 0
      runStatus.value = 'idle'
      error.value = ''
      await loadRuns()
      await loadSubscription()
    } catch {
      // Network or auth failure on the status probe; we genuinely
      // don't know what happened. Mark idle and let the user refresh.
      reconnectAttempts = 0
      runStatus.value = 'idle'
      error.value = ''
    }
  }
}

async function triggerRun() {
  try {
    runStatus.value = 'running'
    reconnectAttempts = 0
    runRecordCount.value = 0
    runRecords.value = []
    liveLogs.value = []
    error.value = ''

    await runSubscription(id.value, runLookback.value)

    await attachEventSource()
  } catch (e: any) {
    runStatus.value = 'failed'
    error.value = e.message || 'Failed to start run'
  }
}

async function checkAndAttach() {
  try {
    const status = await getRunStatus(id.value)
    if (status.active) {
      runStatus.value = 'running'
      runRecordCount.value = 0
      runRecords.value = []
      liveLogs.value = []
      await attachEventSource()
    }
  } catch {
    // status endpoint missing or no run; ignore
  }
}

async function viewRunLog(runID: string, meta: { start: string | null; status: string }) {
  selectedRunMeta.value = meta
  selectedRunLog.value = ''
  loadingRunLog.value = true
  showRunLogDialog.value = true
  try {
    selectedRunLog.value = await getRunLog(id.value, runID)
  } catch (e: any) {
    error.value = e.message || 'Failed to load run log'
    showRunLogDialog.value = false
  } finally {
    loadingRunLog.value = false
  }
}

function dismissRunPanel() {
  runStatus.value = 'idle'
  runRecords.value = []
  runRecordCount.value = 0
}

watch(runRecords, () => {
  nextTick(() => {
    if (runLogRef.value) {
      runLogRef.value.scrollTop = runLogRef.value.scrollHeight
    }
  })
}, { deep: true })

onMounted(() => { loadSubscription(); loadRuns(); checkAndAttach() })

onUnmounted(() => {
  eventSource?.close()
})
</script>

<template>
  <div>
    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0"><ProgressSpinner /></div>

    <div v-else-if="error && !subscription">
      <Message severity="error" :closable="true" @close="error = ''">{{ error }}</Message>
    </div>

    <template v-else-if="subscription">
      <div style="display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 1rem; gap: 1rem; flex-wrap: wrap">
        <div>
          <Button label="Subscriptions" icon="pi pi-arrow-left" text size="small" @click="router.push('/')" style="margin-bottom: 0.5rem" />
          <h2 style="margin-bottom: 0.5rem">{{ subscription.name || subscription.id }}</h2>
          <div style="display: flex; gap: 0.5rem; flex-wrap: wrap">
            <Tag :value="subscription.provider" severity="info" />
            <Tag :value="subscription.dataset" severity="warn" />
            <Tag :value="subscription.active ? 'Active' : 'Inactive'" :severity="subscription.active ? 'success' : 'secondary'" />
          </div>
        </div>
        <div style="display: flex; gap: 0.5rem; align-items: center">
          <div v-if="subscription.provider !== 'legacy'" style="display: flex; align-items: center">
            <Button :label="'Run ' + runLookback" icon="pi pi-bolt" :disabled="runStatus === 'running'" @click="triggerRun" style="border-top-right-radius: 0; border-bottom-right-radius: 0" />
            <Button icon="pi pi-chevron-down" :disabled="runStatus === 'running'" @click="(e: any) => runMenuRef.toggle(e)" style="border-top-left-radius: 0; border-bottom-left-radius: 0; border-left: 1px solid rgba(255,255,255,0.2)" />
            <Menu ref="runMenuRef" :model="runMenuItems" :popup="true" />
          </div>
          <Button icon="pi pi-ellipsis-v" text severity="secondary" @click="(e: any) => actionsMenuRef.toggle(e)" />
          <Menu ref="actionsMenuRef" :model="actionsMenuItems" :popup="true" />
        </div>
      </div>

      <Message v-if="error" severity="error" :closable="true" style="margin-bottom: 1rem" @close="error = ''">{{ error }}</Message>

      <Dialog v-model:visible="showCustomLookback" header="Custom Lookback" :modal="true" :style="{ width: '20rem' }">
        <div style="margin-bottom: 0.5rem">Enter lookback period (e.g. 500d):</div>
        <InputText v-model="customLookbackInput" placeholder="14d" style="width: 100%" @keyup.enter="runLookback = customLookbackInput; showCustomLookback = false; triggerRun()" />
        <template #footer>
          <Button label="Cancel" severity="secondary" @click="showCustomLookback = false" />
          <Button label="Run" icon="pi pi-bolt" :disabled="!customLookbackInput.trim()" @click="runLookback = customLookbackInput; showCustomLookback = false; triggerRun()" />
        </template>
      </Dialog>

      <Dialog v-model:visible="showDeleteConfirm" header="Delete Subscription" :modal="true" :style="{ width: '30rem' }">
        <p>This will permanently delete <strong>{{ subscription.name || subscription.id }}</strong> and all its data tables. This cannot be undone.</p>
        <p style="margin-top: 0.75rem">Type <strong>{{ subscription.name }}</strong> to confirm:</p>
        <InputText v-model="deleteConfirmText" :placeholder="subscription.name" style="width: 100%; margin-top: 0.5rem" />
        <template #footer>
          <Button label="Cancel" severity="secondary" @click="showDeleteConfirm = false; deleteConfirmText = ''" />
          <Button label="Delete" severity="danger" :disabled="deleteConfirmText !== subscription.name" @click="onDelete" />
        </template>
      </Dialog>

      <div v-if="editing" style="margin-bottom: 1rem">
        <Card>
          <template #content>
            <SubscriptionForm :subscription="subscription" @saved="onSaved" @cancel="editing = false" />
          </template>
        </Card>
      </div>

      <div v-if="runStatus !== 'idle'" style="margin-bottom: 1rem; border: 1px solid var(--p-content-border-color); border-radius: 8px; overflow: hidden">
        <div :style="{
          padding: '0.75rem 1rem',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          background: runStatus === 'completed' ? 'var(--p-green-900)' : runStatus === 'failed' ? 'var(--p-red-900)' : 'var(--p-surface-800)',
        }">
          <div style="display: flex; align-items: center; gap: 0.5rem">
            <i v-if="runStatus === 'running'" class="pi pi-spin pi-spinner" />
            <i v-else-if="runStatus === 'completed'" class="pi pi-check-circle" />
            <i v-else class="pi pi-times-circle" />
            <span style="font-weight: 600">
              {{ runStatus === 'running' ? 'Running...' : runStatus === 'completed' ? 'Completed' : 'Failed' }}
            </span>
          </div>
          <div style="display: flex; align-items: center; gap: 1rem">
            <span>Records: {{ runRecordCount.toLocaleString() }}</span>
            <Button v-if="runStatus !== 'running'" icon="pi pi-times" text size="small" @click="dismissRunPanel" />
          </div>
        </div>
        <div v-if="runStatus === 'running'" style="height: 2px">
          <ProgressBar mode="indeterminate" style="height: 2px" />
        </div>
        <div style="display: flex; gap: 0.25rem; padding: 0.4rem 0.6rem 0; background: var(--p-surface-900)">
          <Button :text="runPanelTab !== 'records'" :outlined="runPanelTab === 'records'" size="small" label="Records" @click="runPanelTab = 'records'" />
          <Button :text="runPanelTab !== 'logs'" :outlined="runPanelTab === 'logs'" size="small" :label="`Logs${liveLogs.length ? ' (' + liveLogs.length + ')' : ''}`" @click="runPanelTab = 'logs'" />
        </div>
        <div v-show="runPanelTab === 'records'" ref="runLogRef" style="max-height: 300px; overflow-y: auto; font-family: monospace; font-size: 12px; padding: 0.5rem; background: var(--p-surface-900)">
          <div v-for="(rec, i) in runRecords" :key="i" style="padding: 1px 0; white-space: nowrap">
            <span style="opacity: 0.5; margin-right: 0.5rem">{{ rec.type }}</span>
            <span>{{ rec.summary }}</span>
          </div>
        </div>
        <div v-show="runPanelTab === 'logs'" style="padding: 0.5rem; background: var(--p-surface-900)">
          <LogViewer :entries="liveLogs" />
        </div>
      </div>

      <Dialog v-model:visible="showRunLogDialog" :modal="true" :style="{ width: '80vw', maxWidth: '1200px' }">
        <template #header>
          <div>
            <div style="font-weight: 600">Run Log</div>
            <div v-if="selectedRunMeta" style="font-size: 12px; opacity: 0.7">
              <TimeDisplay :value="selectedRunMeta.start" /> &middot; {{ selectedRunMeta.status }}
            </div>
          </div>
        </template>
        <div v-if="loadingRunLog" style="display: flex; justify-content: center; padding: 2rem"><ProgressSpinner /></div>
        <div v-else-if="!selectedRunLog" style="padding: 2rem; text-align: center; opacity: 0.7">
          No log was captured for this run, or the 30-day retention has cleared it.
        </div>
        <LogViewer v-else :text="selectedRunLog" :compact="true" />
      </Dialog>

      <div v-if="!editing" style="display: grid; grid-template-columns: repeat(4, 1fr); gap: 1rem; margin-bottom: 1rem">
        <Card><template #content><small>TOTAL RECORDS</small><h3>{{ formatNumber(subscription.total_records) }}</h3></template></Card>
        <Card><template #content><small>LAST IMPORT</small><h3><TimeDisplay :value="subscription.last_run" /></h3></template></Card>
        <Card><template #content><small>RECORDS LAST IMPORT</small><h3>{{ formatNumber(subscription.num_records_last_import) }}</h3></template></Card>
        <Card><template #content><small>NEXT RUN</small><h3>{{ subscription.next_run_human || '--' }}</h3></template></Card>
      </div>

      <div v-if="!editing" style="margin-bottom: 1rem">
        <Tabs v-model:value="activeTab">
          <TabList>
            <Tab value="0">Run History</Tab>
            <Tab v-for="(dt, idx) in (subscription.data_types || [])" :key="dt" :value="String(Number(idx) + 1)">{{ dt }}</Tab>
          </TabList>
          <TabPanels>
            <TabPanel value="0">
              <RunHistoryChart :runs="runs" />
              <DataTable
                :value="runHistoryRows"
                :sort-field="'start_time'"
                :sort-order="-1"
                size="small"
                style="margin-top: 1rem"
              >
                <Column field="start_time" header="Date" sortable>
                  <template #body="{ data }">
                    <TimeDisplay :value="data.start_time" />
                  </template>
                </Column>
                <Column field="status" header="Status" sortable>
                  <template #body="{ data }">
                    <Tag :value="data.status"
                         :severity="data.status === 'success' ? 'success' : data.status === 'failed' ? 'danger' : data.status === 'running' ? 'warning' : 'secondary'">
                      <template #default>
                        <i v-if="data.status === 'running'" class="pi pi-spin pi-spinner" style="margin-right: 0.4rem; font-size: 11px" />
                        {{ data.status }}
                      </template>
                    </Tag>
                  </template>
                </Column>
                <Column field="records" header="Records" sortable />
                <Column field="duration" header="Duration" sortable />
                <Column header="" style="width: 6rem">
                  <template #body="{ data }">
                    <Button label="Log" icon="pi pi-file" size="small" text @click="viewRunLog(data.id, { start: data.start_time, status: data.status })" />
                  </template>
                </Column>
              </DataTable>
              <p v-if="runs.length === 0 && !loadingRuns">No runs recorded yet.</p>
              <div v-if="runs.length < runsTotal && !loadingRuns" style="display: flex; justify-content: center; padding: 0.5rem 0">
                <Button label="Load more" text @click="runsOffset = runs.length; loadRuns(true)" />
              </div>
            </TabPanel>
            <TabPanel v-for="(dt, idx) in (subscription.data_types || [])" :key="dt" :value="String(Number(idx) + 1)">
              <DataBrowser :subscription-id="id" :datatype="dt" />
            </TabPanel>
          </TabPanels>
        </Tabs>
      </div>

      <Button v-if="!editing" label="Open in SQL Console" icon="pi pi-code" text @click="openSqlConsole" />
    </template>
  </div>
</template>
