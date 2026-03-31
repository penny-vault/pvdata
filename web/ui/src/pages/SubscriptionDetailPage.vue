<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getSubscription,
  getRunHistory,
  activateSubscription,
  deactivateSubscription,
  deleteSubscription,
} from '@/lib/api'
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

function formatDate(dateStr: string | null): string {
  if (!dateStr) return '--'
  const d = new Date(dateStr)
  return d.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
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

async function loadSubscription() {
  try {
    subscription.value = await getSubscription(id.value)
  } catch (e: any) {
    error.value = e.message || 'Failed to load subscription'
  } finally {
    loading.value = false
  }
}

async function loadRuns(append = false) {
  loadingRuns.value = true
  try {
    const result = await getRunHistory(id.value)
    if (Array.isArray(result)) {
      if (append) {
        runs.value = [...runs.value, ...result]
      } else {
        runs.value = result
      }
      runsTotal.value = result.length
    } else {
      if (append) {
        runs.value = [...runs.value, ...(result.data || [])]
      } else {
        runs.value = result.data || []
      }
      runsTotal.value = result.total || runs.value.length
    }
  } catch {
    // Non-critical
  } finally {
    loadingRuns.value = false
  }
}

async function loadMoreRuns() {
  runsOffset.value = runs.value.length
  await loadRuns(true)
}

async function toggleActive() {
  if (!subscription.value) return
  try {
    if (subscription.value.enabled) {
      await deactivateSubscription(id.value)
    } else {
      await activateSubscription(id.value)
    }
    subscription.value.enabled = !subscription.value.enabled
  } catch (e: any) {
    error.value = e.message || 'Failed to toggle subscription'
  }
}

async function onDelete() {
  try {
    await deleteSubscription(id.value)
    router.push('/')
  } catch (e: any) {
    error.value = e.message || 'Failed to delete subscription'
  }
}

function onSaved(updated: any) {
  subscription.value = { ...subscription.value, ...updated }
  editing.value = false
}

function openSqlConsole() {
  const sub = subscription.value
  const table = sub?.data_types?.[0] || 'unknown'
  const query = `SELECT * FROM ${table} LIMIT 100`
  router.push({ path: '/sql', query: { q: query } })
}

onMounted(() => {
  loadSubscription()
  loadRuns()
})
</script>

<template>
  <div class="detail-page">
    <div v-if="loading" class="page-loading">
      <cv-loading active />
    </div>

    <div v-else-if="error && !subscription">
      <cv-inline-notification
        kind="error"
        :title="error"
        @close="error = ''"
      />
    </div>

    <template v-else-if="subscription">
      <!-- Header -->
      <div class="detail-header">
        <div class="detail-header__info">
          <button
            class="back-link"
            @click="router.push('/')"
          >
            &larr; Subscriptions
          </button>
          <h2>{{ subscription.name || subscription.id }}</h2>
          <div class="detail-header__tags">
            <cv-tag :label="subscription.provider" kind="blue" />
            <cv-tag :label="subscription.dataset" kind="purple" />
            <cv-tag
              :label="subscription.enabled ? 'Active' : 'Inactive'"
              :kind="subscription.enabled ? 'green' : 'gray'"
            />
          </div>
        </div>
        <div class="detail-header__actions">
          <cv-button kind="ghost" @click="toggleActive">
            {{ subscription.enabled ? 'Deactivate' : 'Activate' }}
          </cv-button>
          <cv-button kind="secondary" @click="editing = !editing">
            {{ editing ? 'Cancel Edit' : 'Edit' }}
          </cv-button>
          <cv-button kind="danger" @click="showDeleteConfirm = true">
            Delete
          </cv-button>
        </div>
      </div>

      <div v-if="error" style="margin-bottom: 1rem">
        <cv-inline-notification
          kind="error"
          :title="error"
          @close="error = ''"
        />
      </div>

      <!-- Delete confirmation -->
      <cv-modal
        :visible="showDeleteConfirm"
        kind="danger"
        @modal-hidden="showDeleteConfirm = false"
        @primary-click="onDelete"
      >
        <template #title>Delete Subscription</template>
        <template #content>
          <p>
            Are you sure you want to delete
            <strong>{{ subscription.name || subscription.id }}</strong>?
            This action cannot be undone.
          </p>
        </template>
      </cv-modal>

      <!-- Edit form -->
      <div v-if="editing" class="detail-section">
        <SubscriptionForm
          :subscription="subscription"
          @saved="onSaved"
          @cancel="editing = false"
        />
      </div>

      <!-- Stats row -->
      <div v-if="!editing" class="stats-grid">
        <div class="stat-card">
          <span class="stat-card__label">Total Records</span>
          <span class="stat-card__value">
            {{ formatNumber(subscription.total_records) }}
          </span>
        </div>
        <div class="stat-card">
          <span class="stat-card__label">Last Import</span>
          <span class="stat-card__value">
            {{ formatDate(subscription.last_run_time) }}
          </span>
        </div>
        <div class="stat-card">
          <span class="stat-card__label">Records Last Import</span>
          <span class="stat-card__value">
            {{ formatNumber(subscription.num_records_last_import) }}
          </span>
        </div>
        <div class="stat-card">
          <span class="stat-card__label">Next Run</span>
          <span class="stat-card__value">
            {{ subscription.next_run_human || '--' }}
          </span>
        </div>
      </div>

      <!-- Tabs -->
      <div v-if="!editing" class="detail-tabs">
        <cv-tabs>
          <cv-tab label="Run History">
            <div class="tab-content">
              <RunHistoryChart :runs="runs" />

              <div class="runs-table-wrap">
                <table class="bx--data-table bx--data-table--compact">
                  <thead>
                    <tr>
                      <th><span class="bx--table-header-label">Date</span></th>
                      <th><span class="bx--table-header-label">Status</span></th>
                      <th><span class="bx--table-header-label">Records</span></th>
                      <th><span class="bx--table-header-label">Duration</span></th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="run in runs" :key="run.id || run.start_time">
                      <td>{{ formatDate(run.start_time) }}</td>
                      <td>
                        <cv-tag
                          :label="run.status || 'unknown'"
                          :kind="run.status === 'success' ? 'green' : run.status === 'failed' ? 'red' : 'gray'"
                        />
                      </td>
                      <td>{{ formatNumber(run.num_observations) }}</td>
                      <td>{{ formatDuration(run.start_time, run.end_time) }}</td>
                    </tr>
                  </tbody>
                </table>

                <div v-if="runs.length === 0 && !loadingRuns" class="runs-empty">
                  No runs recorded yet.
                </div>

                <div v-if="loadingRuns" class="runs-loading">
                  Loading...
                </div>

                <div
                  v-if="runs.length < runsTotal && !loadingRuns"
                  class="runs-load-more"
                >
                  <cv-button kind="ghost" @click="loadMoreRuns">
                    Load more
                  </cv-button>
                </div>
              </div>
            </div>
          </cv-tab>

          <cv-tab
            v-for="dt in (subscription.data_types || [])"
            :key="dt"
            :label="dt"
          >
            <div class="tab-content">
              <DataBrowser
                :subscription-id="id"
                :datatype="dt"
              />
            </div>
          </cv-tab>
        </cv-tabs>
      </div>

      <!-- SQL link -->
      <div v-if="!editing" class="detail-footer">
        <cv-button kind="ghost" @click="openSqlConsole">
          Open in SQL Console
        </cv-button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.detail-page {
  padding: 2rem;
  max-width: 1400px;
  margin: 0 auto;
}

.page-loading {
  display: flex;
  justify-content: center;
  padding: 4rem;
}

.back-link {
  background: none;
  border: none;
  color: var(--cds-link-primary, #78a9ff);
  cursor: pointer;
  padding: 0;
  font-size: 0.875rem;
  margin-bottom: 0.5rem;
  display: inline-block;
}

.back-link:hover {
  text-decoration: underline;
}

.detail-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 2rem;
  gap: 1rem;
  flex-wrap: wrap;
}

.detail-header__info h2 {
  margin: 0 0 0.75rem 0;
  font-weight: 400;
  color: var(--cds-text-primary, #f4f4f4);
}

.detail-header__tags {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.detail-header__actions {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 1rem;
  margin-bottom: 2rem;
}

@media (max-width: 768px) {
  .stats-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}

.stat-card {
  background-color: var(--cds-layer-01, #262626);
  border: 1px solid var(--cds-border-subtle-01, #393939);
  border-radius: 2px;
  padding: 1.25rem;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.stat-card__label {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.32px;
  color: var(--cds-text-secondary, #c6c6c6);
}

.stat-card__value {
  font-size: 1.5rem;
  font-weight: 300;
  color: var(--cds-text-primary, #f4f4f4);
}

.detail-tabs {
  margin-bottom: 2rem;
}

.tab-content {
  padding: 1.5rem 0;
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
}

.runs-table-wrap {
  border: 1px solid var(--cds-border-subtle-01, #393939);
  border-radius: 2px;
  overflow-x: auto;
}

.runs-table-wrap .bx--data-table {
  width: 100%;
}

.runs-table-wrap .bx--data-table thead th {
  background-color: var(--cds-layer-accent-01, #333333);
  color: var(--cds-text-primary, #f4f4f4);
}

.runs-table-wrap .bx--data-table tbody td {
  color: var(--cds-text-primary, #f4f4f4);
}

.runs-empty,
.runs-loading {
  text-align: center;
  padding: 2rem;
  color: var(--cds-text-secondary, #c6c6c6);
}

.runs-load-more {
  display: flex;
  justify-content: center;
  padding: 0.75rem;
}

.detail-section {
  margin-bottom: 2rem;
  padding: 1.5rem;
  background-color: var(--cds-layer-01, #262626);
  border: 1px solid var(--cds-border-subtle-01, #393939);
  border-radius: 2px;
}

.detail-footer {
  padding-top: 1rem;
  border-top: 1px solid var(--cds-border-subtle-01, #393939);
}
</style>
