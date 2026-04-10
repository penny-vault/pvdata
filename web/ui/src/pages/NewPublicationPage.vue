<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'
import Message from 'primevue/message'
import { getAvailablePublicationTypes, createPublication } from '@/lib/api'

const router = useRouter()
const types = ref<any[]>([])
const loading = ref(true)
const creating = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  try {
    types.value = await getAvailablePublicationTypes()
  } catch (e: any) {
    error.value = e.message || 'Failed to load available types'
  } finally {
    loading.value = false
  }
}

async function selectType(dataTypeKey: string) {
  creating.value = true
  error.value = ''
  try {
    const pv = await createPublication({ data_type_key: dataTypeKey })
    router.push(`/publications/${pv.id}`)
  } catch (e: any) {
    error.value = e.message || 'Failed to create publication'
    creating.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <Button label="Publications" icon="pi pi-arrow-left" text size="small" @click="router.push('/publications')" style="margin-bottom: 0.5rem" />
    <h2 style="margin-bottom: 1.5rem">New Publication</h2>

    <p style="margin-bottom: 1rem">Select a data type to create a published view for:</p>

    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0">
      <ProgressSpinner />
    </div>

    <Message v-else-if="error" severity="error" :closable="true" @close="error = ''">{{ error }}</Message>

    <div v-else-if="types.length === 0">
      <Message severity="info">All data types already have published views.</Message>
    </div>

    <DataTable
      v-else
      :value="types"
      :loading="creating"
      @row-click="(e: any) => selectType(e.data.data_type_key)"
      style="cursor: pointer"
    >
      <Column field="data_type_key" header="Data Type" />
      <Column field="view_name" header="View Name" />
    </DataTable>
  </div>
</template>
