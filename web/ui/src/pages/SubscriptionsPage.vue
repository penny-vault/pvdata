<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getSubscriptions, getSparkline } from '@/lib/api'
import SparklineCell from '@/components/SparklineCell.vue'

const router = useRouter()
const subscriptions = ref<any[]>([])
const sparklines = ref<Record<string, any[]>>({})
const loading = ref(true)
const error = ref('')

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

function goToDetail(id: string) {
  router.push(`/subscriptions/${id}`)
}

function goToNew() {
  router.push('/subscriptions/new')
}

async function loadSparkline(id: string) {
  try {
    const data = await getSparkline(id)
    sparklines.value[id] = data
  } catch {
    // Sparkline load failure is non-critical
  }
}

onMounted(async () => {
  try {
    subscriptions.value = await getSubscriptions()
  } catch (e: any) {
    error.value = e.message || 'Failed to load subscriptions'
  } finally {
    loading.value = false
  }

  // Load sparklines in background
  for (const sub of subscriptions.value) {
    loadSparkline(sub.id)
  }
})
</script>

<template>
  <div class="subscriptions-page">
    <div class="page-header">
      <h2>Subscriptions</h2>
      <cv-button kind="primary" @click="goToNew">
        New Subscription
      </cv-button>
    </div>

    <div v-if="loading" class="page-loading">
      <cv-loading active />
    </div>

    <div v-else-if="error">
      <cv-inline-notification
        kind="error"
        :title="error"
        @close="error = ''"
      />
    </div>

    <div v-else-if="subscriptions.length === 0" class="page-empty">
      <p>No subscriptions yet. Create one to get started.</p>
    </div>

    <div v-else class="table-container">
      <table class="bx--data-table">
        <thead>
          <tr>
            <th><span class="bx--table-header-label">Name</span></th>
            <th><span class="bx--table-header-label">Provider</span></th>
            <th><span class="bx--table-header-label">Dataset</span></th>
            <th><span class="bx--table-header-label">Status</span></th>
            <th><span class="bx--table-header-label">Sparkline</span></th>
            <th><span class="bx--table-header-label">Last Import</span></th>
            <th><span class="bx--table-header-label">Records</span></th>
            <th><span class="bx--table-header-label">Next Import</span></th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="sub in subscriptions"
            :key="sub.id"
            class="clickable-row"
            @click="goToDetail(sub.id)"
          >
            <td>{{ sub.name || sub.id }}</td>
            <td>{{ sub.provider }}</td>
            <td>{{ sub.dataset }}</td>
            <td>
              <cv-tag
                :label="sub.active ? 'Active' : 'Inactive'"
                :kind="sub.active ? 'green' : 'gray'"
              />
            </td>
            <td>
              <SparklineCell :data="sparklines[sub.id] || []" />
            </td>
            <td>{{ formatDate(sub.last_run) }}</td>
            <td>{{ formatNumber(sub.num_records_last_import) }}</td>
            <td>{{ sub.next_run_human || '--' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.subscriptions-page {
  padding: 2rem;
  max-width: 1400px;
  margin: 0 auto;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 2rem;
}

.page-header h2 {
  margin: 0;
  font-weight: 400;
  color: var(--cds-text-primary, #f4f4f4);
}

.page-loading {
  display: flex;
  justify-content: center;
  padding: 4rem;
}

.page-empty {
  text-align: center;
  padding: 4rem;
  color: var(--cds-text-secondary, #c6c6c6);
}

.table-container {
  border: 1px solid var(--cds-border-subtle-01, #393939);
  border-radius: 2px;
  overflow-x: auto;
}

.bx--data-table {
  width: 100%;
}

.bx--data-table thead th {
  background-color: var(--cds-layer-accent-01, #333333);
  color: var(--cds-text-primary, #f4f4f4);
}

.bx--data-table tbody td {
  color: var(--cds-text-primary, #f4f4f4);
}

.clickable-row {
  cursor: pointer;
}

.clickable-row:hover td {
  background-color: var(--cds-layer-hover-01, #353535);
}
</style>
