<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'
import Message from 'primevue/message'
import { getPublications } from '@/lib/api'

const router = useRouter()
const publications = ref<any[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  try {
    publications.value = await getPublications()
  } catch (e: any) {
    error.value = e.message || 'Failed to load publications'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1.5rem">
      <h2>Publications</h2>
      <Button label="New" icon="pi pi-plus" @click="router.push('/publications/new')" />
    </div>

    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0">
      <ProgressSpinner />
    </div>

    <Message v-else-if="error" severity="error" :closable="true" @close="error = ''">{{ error }}</Message>

    <DataTable v-else :value="publications" @row-click="(e: any) => router.push(`/publications/${e.data.id}`)">
      <Column field="view_name" header="View Name" style="cursor: pointer" />
      <Column field="data_type_key" header="Data Type" />
      <Column field="source_count" header="Sources" />
    </DataTable>
  </div>
</template>
