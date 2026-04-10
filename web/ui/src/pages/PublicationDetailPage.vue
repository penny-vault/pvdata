<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Button from 'primevue/button'
import Dialog from 'primevue/dialog'
import InputText from 'primevue/inputtext'
import ProgressSpinner from 'primevue/progressspinner'
import Message from 'primevue/message'
import {
  getPublication,
  updatePublication,
  deletePublication,
  getPublicationCandidates,
} from '@/lib/api'

const route = useRoute()
const router = useRouter()
const id = computed(() => route.params.id as string)

const publication = ref<any>(null)
const loading = ref(true)
const error = ref('')
const saving = ref(false)

// Add source dialog
const showAddSource = ref(false)
const candidates = ref<any[]>([])
const loadingCandidates = ref(false)

// Edit dates dialog
const showEditDates = ref(false)
const editingSourceIdx = ref(-1)
const editFrom = ref('')
const editUntil = ref('')
const editDateError = ref('')

// Remove source dialog
const showRemoveSource = ref(false)
const removeSourceIdx = ref(-1)

// Delete view dialog
const showDeleteView = ref(false)

const isLastSource = computed(() =>
  publication.value && publication.value.sources.length === 1 && removeSourceIdx.value >= 0
)

async function load() {
  loading.value = true
  try {
    publication.value = await getPublication(id.value)
  } catch (e: any) {
    error.value = e.message || 'Failed to load publication'
  } finally {
    loading.value = false
  }
}

// --- Add source ---

async function openAddSource() {
  showAddSource.value = true
  loadingCandidates.value = true
  try {
    candidates.value = await getPublicationCandidates(id.value)
  } catch (e: any) {
    error.value = e.message || 'Failed to load candidates'
    showAddSource.value = false
  } finally {
    loadingCandidates.value = false
  }
}

async function addSource(candidate: any) {
  saving.value = true
  const newSource = {
    table_name: candidate.table_name,
    subscription_id: candidate.subscription_id,
  }
  const updatedSources = [
    ...publication.value.sources.map((s: any) => ({
      table_name: s.table_name,
      subscription_id: s.subscription_id,
      from_date: s.from_date ? new Date(s.from_date) : undefined,
      until_date: s.until_date ? new Date(s.until_date) : undefined,
    })),
    newSource,
  ]
  try {
    publication.value = await updatePublication(id.value, { sources: updatedSources })
    showAddSource.value = false
  } catch (e: any) {
    error.value = e.message || 'Failed to add source'
  } finally {
    saving.value = false
  }
}

// --- Edit dates ---

function openEditDates(idx: number) {
  editingSourceIdx.value = idx
  const source = publication.value.sources[idx]
  editFrom.value = source.from_date || ''
  editUntil.value = source.until_date || ''
  editDateError.value = ''
  showEditDates.value = true
}

function isValidDate(str: string): boolean {
  if (str === '') return true
  return /^\d{4}-\d{2}-\d{2}$/.test(str) && !isNaN(new Date(str).getTime())
}

async function saveEditDates() {
  if (!isValidDate(editFrom.value) || !isValidDate(editUntil.value)) {
    editDateError.value = 'Dates must be in YYYY-MM-DD format or empty'
    return
  }

  saving.value = true
  editDateError.value = ''

  const updatedSources = publication.value.sources.map((s: any, i: number) => {
    const source: any = {
      table_name: s.table_name,
      subscription_id: s.subscription_id,
    }

    if (i === editingSourceIdx.value) {
      if (editFrom.value) source.from_date = new Date(editFrom.value)
      if (editUntil.value) source.until_date = new Date(editUntil.value)
    } else {
      if (s.from_date) source.from_date = new Date(s.from_date)
      if (s.until_date) source.until_date = new Date(s.until_date)
    }

    return source
  })

  try {
    publication.value = await updatePublication(id.value, { sources: updatedSources })
    showEditDates.value = false
  } catch (e: any) {
    error.value = e.message || 'Failed to update dates'
  } finally {
    saving.value = false
  }
}

// --- Remove source ---

function openRemoveSource(idx: number) {
  removeSourceIdx.value = idx
  showRemoveSource.value = true
}

async function confirmRemoveSource() {
  saving.value = true

  if (publication.value.sources.length === 1) {
    try {
      await deletePublication(id.value)
      router.push('/publications')
    } catch (e: any) {
      error.value = e.message || 'Failed to delete publication'
      saving.value = false
    }

    return
  }

  const updatedSources = publication.value.sources
    .filter((_: any, i: number) => i !== removeSourceIdx.value)
    .map((s: any) => {
      const source: any = {
        table_name: s.table_name,
        subscription_id: s.subscription_id,
      }

      if (s.from_date) source.from_date = new Date(s.from_date)
      if (s.until_date) source.until_date = new Date(s.until_date)

      return source
    })

  try {
    publication.value = await updatePublication(id.value, { sources: updatedSources })
    showRemoveSource.value = false
  } catch (e: any) {
    error.value = e.message || 'Failed to remove source'
  } finally {
    saving.value = false
  }
}

// --- Delete view ---

async function confirmDeleteView() {
  saving.value = true
  try {
    await deletePublication(id.value)
    router.push('/publications')
  } catch (e: any) {
    error.value = e.message || 'Failed to delete publication'
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0">
      <ProgressSpinner />
    </div>

    <div v-else-if="error && !publication">
      <Message severity="error" :closable="true" @close="error = ''">{{ error }}</Message>
    </div>

    <template v-else-if="publication">
      <Button label="Publications" icon="pi pi-arrow-left" text size="small" @click="router.push('/publications')" style="margin-bottom: 0.5rem" />
      <h2 style="margin-bottom: 0.25rem">{{ publication.view_name }}</h2>
      <p style="margin-bottom: 1.5rem; opacity: 0.7">Data type: {{ publication.data_type_key }}</p>

      <Message v-if="error" severity="error" :closable="true" style="margin-bottom: 1rem" @close="error = ''">{{ error }}</Message>

      <Message v-for="(overlap, i) in (publication.overlaps || [])" :key="i" severity="warn" style="margin-bottom: 0.5rem">
        {{ overlap }}
      </Message>

      <!-- Sources table -->
      <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; margin-top: 1rem">
        <h3>Sources</h3>
        <Button label="Add Source" icon="pi pi-plus" size="small" @click="openAddSource" />
      </div>

      <DataTable :value="publication.sources" :loading="saving">
        <Column field="table_name" header="Source Table" />
        <Column field="subscription_name" header="Subscription" />
        <Column header="Provider / Dataset">
          <template #body="{ data }">{{ data.provider }} / {{ data.dataset }}</template>
        </Column>
        <Column field="from_date" header="From">
          <template #body="{ data }">{{ data.from_date || '' }}</template>
        </Column>
        <Column field="until_date" header="Until">
          <template #body="{ data }">{{ data.until_date || '' }}</template>
        </Column>
        <Column header="Actions" style="width: 120px">
          <template #body="{ index }">
            <Button icon="pi pi-pencil" text size="small" @click="openEditDates(index)" />
            <Button icon="pi pi-trash" text size="small" severity="danger" @click="openRemoveSource(index)" />
          </template>
        </Column>
      </DataTable>

      <div v-if="publication.sources.length === 0" style="text-align: center; padding: 2rem; opacity: 0.6">
        No sources yet. Click "Add Source" to get started.
      </div>

      <!-- Add source dialog -->
      <Dialog v-model:visible="showAddSource" header="Add Source" :modal="true" :style="{ width: '50rem' }">
        <div v-if="loadingCandidates" style="display: flex; justify-content: center; padding: 2rem 0">
          <ProgressSpinner />
        </div>
        <div v-else-if="candidates.length === 0">
          <Message severity="info">No eligible subscription tables found for this data type.</Message>
        </div>
        <DataTable v-else :value="candidates" @row-click="(e: any) => addSource(e.data)" style="cursor: pointer">
          <Column field="table_name" header="Table Name" />
          <Column field="subscription_name" header="Subscription" />
          <Column header="Provider / Dataset">
            <template #body="{ data }">{{ data.provider }} / {{ data.dataset }}</template>
          </Column>
        </DataTable>
      </Dialog>

      <!-- Edit dates dialog -->
      <Dialog v-model:visible="showEditDates" header="Edit Date Bounds" :modal="true" :style="{ width: '25rem' }">
        <div v-if="editingSourceIdx >= 0" style="margin-bottom: 1rem; opacity: 0.7">
          {{ publication.sources[editingSourceIdx]?.table_name }}
        </div>
        <div style="display: flex; flex-direction: column; gap: 1rem">
          <div>
            <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem">From date (YYYY-MM-DD or empty)</label>
            <InputText v-model="editFrom" placeholder="YYYY-MM-DD" style="width: 100%" />
          </div>
          <div>
            <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem">Until date (YYYY-MM-DD or empty)</label>
            <InputText v-model="editUntil" placeholder="YYYY-MM-DD" style="width: 100%" />
          </div>
        </div>
        <Message v-if="editDateError" severity="error" style="margin-top: 0.5rem">{{ editDateError }}</Message>
        <template #footer>
          <Button label="Cancel" severity="secondary" @click="showEditDates = false" />
          <Button label="Save" :loading="saving" @click="saveEditDates" />
        </template>
      </Dialog>

      <!-- Remove source dialog -->
      <Dialog v-model:visible="showRemoveSource" header="Remove Source" :modal="true" :style="{ width: '30rem' }">
        <p v-if="removeSourceIdx >= 0">
          Remove <strong>{{ publication.sources[removeSourceIdx]?.table_name }}</strong> from this view?
        </p>
        <Message v-if="isLastSource" severity="warn" style="margin-top: 0.5rem">
          This is the last source -- the entire published view will be deleted.
        </Message>
        <template #footer>
          <Button label="Cancel" severity="secondary" @click="showRemoveSource = false" />
          <Button label="Remove" severity="danger" :loading="saving" @click="confirmRemoveSource" />
        </template>
      </Dialog>

      <!-- Delete view -->
      <Dialog v-model:visible="showDeleteView" header="Delete Published View" :modal="true" :style="{ width: '30rem' }">
        <p>Permanently delete the <strong>{{ publication.view_name }}</strong> published view and drop the database view? This cannot be undone.</p>
        <template #footer>
          <Button label="Cancel" severity="secondary" @click="showDeleteView = false" />
          <Button label="Delete" severity="danger" :loading="saving" @click="confirmDeleteView" />
        </template>
      </Dialog>

      <div style="margin-top: 2rem; display: flex; gap: 0.5rem">
        <Button label="Open in SQL Console" icon="pi pi-code" text @click="router.push({ path: '/sql', query: { q: `SELECT *\nFROM ${publication.view_name}\nLIMIT 100;` } })" />
        <Button label="Delete View" icon="pi pi-trash" severity="danger" outlined @click="showDeleteView = true" />
      </div>
    </template>
  </div>
</template>
