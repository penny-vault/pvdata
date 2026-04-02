<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getSubscription, getRunHistory,
  activateSubscription, deactivateSubscription, deleteSubscription,
} from '@/lib/api'
import RevoGrid from '@revolist/vue3-datagrid'
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
import Message from 'primevue/message'
import RunHistoryChart from '@/components/RunHistoryChart.vue'
import DataBrowser from '@/components/DataBrowser.vue'
import SubscriptionForm from '@/components/SubscriptionForm.vue'

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

function formatDuration(start: string, end: string | null): string {
  if (!end) return '--'
  const ms = new Date(end).getTime() - new Date(start).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60000).toFixed(1)}m`
}

const runHistoryColumns = [
  { prop: 'date', name: 'Date', sortable: true, size: 200 },
  { prop: 'status', name: 'Status', sortable: true, size: 120 },
  { prop: 'records', name: 'Records', sortable: true, size: 120 },
  { prop: 'duration', name: 'Duration', sortable: true, size: 120 },
]

const runHistoryRows = computed(() =>
  runs.value.map(run => ({
    date: formatDate(run.start_time),
    status: run.status || 'unknown',
    records: formatNumber(run.num_observations),
    duration: formatDuration(run.start_time, run.end_time),
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

onMounted(() => { loadSubscription(); loadRuns() })
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
        <div style="display: flex; gap: 0.5rem; flex-wrap: wrap">
          <Button v-if="subscription.provider !== 'legacy'" :label="subscription.active ? 'Deactivate' : 'Activate'" :icon="subscription.active ? 'pi pi-pause' : 'pi pi-play'" text @click="toggleActive" />
          <Button :label="editing ? 'Cancel Edit' : 'Edit'" icon="pi pi-pencil" severity="secondary" @click="editing = !editing" />
          <Button label="Delete" icon="pi pi-trash" severity="danger" @click="showDeleteConfirm = true" />
        </div>
      </div>

      <Message v-if="error" severity="error" :closable="true" style="margin-bottom: 1rem" @close="error = ''">{{ error }}</Message>

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

      <div v-if="!editing" style="display: grid; grid-template-columns: repeat(4, 1fr); gap: 1rem; margin-bottom: 1rem">
        <Card><template #content><small>TOTAL RECORDS</small><h3>{{ formatNumber(subscription.total_records) }}</h3></template></Card>
        <Card><template #content><small>LAST IMPORT</small><h3>{{ formatDate(subscription.last_run) }}</h3></template></Card>
        <Card><template #content><small>RECORDS LAST IMPORT</small><h3>{{ formatNumber(subscription.num_records_last_import) }}</h3></template></Card>
        <Card><template #content><small>NEXT RUN</small><h3>{{ subscription.next_run_human || '--' }}</h3></template></Card>
      </div>

      <div v-if="!editing" style="margin-bottom: 1rem">
        <Tabs v-model:value="activeTab">
          <TabList>
            <Tab value="0">Run History</Tab>
            <Tab v-for="(dt, idx) in (subscription.data_types || [])" :key="dt" :value="String(idx + 1)">{{ dt }}</Tab>
          </TabList>
          <TabPanels>
            <TabPanel value="0">
              <RunHistoryChart :runs="runs" />
              <RevoGrid
                :columns="runHistoryColumns"
                :source="runHistoryRows"
                theme="darkCompact"
                style="height: 400px; margin-top: 1rem"
              />
              <p v-if="runs.length === 0 && !loadingRuns">No runs recorded yet.</p>
              <div v-if="runs.length < runsTotal && !loadingRuns" style="display: flex; justify-content: center; padding: 0.5rem 0">
                <Button label="Load more" text @click="runsOffset = runs.length; loadRuns(true)" />
              </div>
            </TabPanel>
            <TabPanel v-for="(dt, idx) in (subscription.data_types || [])" :key="dt" :value="String(idx + 1)">
              <DataBrowser :subscription-id="id" :datatype="dt" />
            </TabPanel>
          </TabPanels>
        </Tabs>
      </div>

      <Button v-if="!editing" label="Open in SQL Console" icon="pi pi-code" text @click="openSqlConsole" />
    </template>
  </div>
</template>
