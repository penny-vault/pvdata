<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getProviders, createSubscription } from '@/lib/api'
import Stepper from 'primevue/stepper'
import StepList from 'primevue/steplist'
import StepPanels from 'primevue/steppanels'
import StepPanel from 'primevue/steppanel'
import StepItem from 'primevue/stepitem'
import Step from 'primevue/step'
import InputText from 'primevue/inputtext'
import Button from 'primevue/button'
import Card from 'primevue/card'
import Message from 'primevue/message'
import ProgressSpinner from 'primevue/progressspinner'

const router = useRouter()

const providers = ref<Record<string, any>>({})
const loading = ref(true)
const creating = ref(false)
const error = ref('')
const activeStep = ref('1')

const selectedProvider = ref('')
const selectedDataset = ref('')
const schedule = ref('0 6 * * *')
const configEntries = ref<{ key: string; value: string }[]>([])
const healthCheckId = ref('')

const providerList = computed(() =>
  Object.entries(providers.value).map(([key, val]) => ({
    id: key,
    name: val.name || key,
    description: val.description || '',
    datasets: val.datasets || {},
    config_description: val.config_description || {},
  }))
)

const selectedProviderData = computed(() =>
  providerList.value.find(p => p.id === selectedProvider.value)
)

const datasetOptions = computed(() => {
  if (!selectedProviderData.value) return []
  return Object.entries(selectedProviderData.value.datasets).map(([key, val]: [string, any]) => ({
    key,
    name: val.name || key,
    description: val.description || '',
    dataTypes: val.data_types || [],
  }))
})

function populateConfigKeys() {
  if (configEntries.value.length === 0) {
    const desc = selectedProviderData.value?.config_description || {}
    const keys = Object.keys(desc)
    configEntries.value = keys.length > 0
      ? keys.map(k => ({ key: k, value: '' }))
      : [{ key: '', value: '' }]
  }
}

function addConfigEntry() {
  configEntries.value.push({ key: '', value: '' })
}

function removeConfigEntry(index: number) {
  configEntries.value.splice(index, 1)
}

async function onCreate() {
  creating.value = true
  error.value = ''
  try {
    const config: Record<string, string> = {}
    for (const entry of configEntries.value) {
      if (entry.key.trim()) config[entry.key.trim()] = entry.value
    }
    const result = await createSubscription({
      provider: selectedProvider.value,
      dataset: selectedDataset.value,
      schedule: schedule.value,
      config,
      health_check_id: healthCheckId.value || undefined,
    })
    router.push(`/subscriptions/${result.id}`)
  } catch (e: any) {
    error.value = e.message || 'Failed to create subscription'
  } finally {
    creating.value = false
  }
}

onMounted(async () => {
  try {
    providers.value = await getProviders()
  } catch (e: any) {
    error.value = e.message || 'Failed to load providers'
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div style="max-width: 700px; margin: 0 auto">
    <div style="margin-bottom: 1.5rem">
      <Button label="Subscriptions" icon="pi pi-arrow-left" text size="small" @click="router.push('/')" style="margin-bottom: 0.5rem" />
      <h1>New Subscription</h1>
    </div>

    <div v-if="loading" style="display: flex; justify-content: center; padding: 4rem 0">
      <ProgressSpinner />
    </div>

    <template v-else>
      <Message v-if="error" severity="error" :closable="true" style="margin-bottom: 1rem" @close="error = ''">
        {{ error }}
      </Message>

      <Stepper v-model:value="activeStep" linear>
        <StepList>
          <Step value="1">Provider</Step>
          <Step value="2">Dataset</Step>
          <Step value="3">Schedule</Step>
          <Step value="4">Config</Step>
          <Step value="5">Review</Step>
        </StepList>
        <StepPanels>
          <!-- Step 1: Provider -->
          <StepPanel v-slot="{ activateCallback }" value="1">
            <div style="padding: 1.5rem 0; min-height: 300px">
              <h3 style="margin-bottom: 1rem">Select a provider</h3>
              <div style="display: flex; flex-direction: column; gap: 0.5rem">
                <div
                  v-for="p in providerList"
                  :key="p.id"
                  :style="{
                    padding: '0.75rem 1rem',
                    borderRadius: '6px',
                    cursor: 'pointer',
                    border: selectedProvider === p.id ? '1px solid var(--p-primary-color)' : '1px solid transparent',
                    background: selectedProvider === p.id ? 'rgba(var(--p-primary-500), 0.08)' : 'var(--p-content-background)',
                  }"
                  @click="selectedProvider = p.id; selectedDataset = ''"
                >
                  <div style="font-weight: 600">{{ p.name }}</div>
                  <div v-if="p.description" style="font-size: 12px; opacity: 0.6; margin-top: 2px">{{ p.description }}</div>
                </div>
              </div>
              <div style="display: flex; justify-content: flex-end; margin-top: 1.5rem">
                <Button label="Next" icon="pi pi-arrow-right" iconPos="right" :disabled="!selectedProvider" @click="activateCallback('2')" />
              </div>
            </div>
          </StepPanel>

          <!-- Step 2: Dataset -->
          <StepPanel v-slot="{ activateCallback }" value="2">
            <div style="padding: 1.5rem 0; min-height: 300px">
              <h3 style="margin-bottom: 1rem">Select a dataset</h3>
              <div style="display: flex; flex-direction: column; gap: 0.5rem">
                <div
                  v-for="ds in datasetOptions"
                  :key="ds.key"
                  :style="{
                    padding: '0.75rem 1rem',
                    borderRadius: '6px',
                    cursor: 'pointer',
                    border: selectedDataset === ds.key ? '1px solid var(--p-primary-color)' : '1px solid transparent',
                    background: selectedDataset === ds.key ? 'rgba(var(--p-primary-500), 0.08)' : 'var(--p-content-background)',
                  }"
                  @click="selectedDataset = ds.key"
                >
                  <div style="font-weight: 600">{{ ds.name }}</div>
                  <div v-if="ds.description" style="font-size: 12px; opacity: 0.6; margin-top: 2px">{{ ds.description }}</div>
                  <div v-if="ds.dataTypes.length" style="font-size: 11px; opacity: 0.4; margin-top: 4px">
                    Data types: {{ ds.dataTypes.join(', ') }}
                  </div>
                </div>
              </div>
              <p v-if="datasetOptions.length === 0" style="opacity: 0.5">No datasets available for this provider.</p>
              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('1')" />
                <Button label="Next" icon="pi pi-arrow-right" iconPos="right" :disabled="!selectedDataset" @click="activateCallback('3')" />
              </div>
            </div>
          </StepPanel>

          <!-- Step 3: Schedule + Config -->
          <StepPanel v-slot="{ activateCallback }" value="3">
            <div style="padding: 1.5rem 0; min-height: 300px">
              <h3 style="margin-bottom: 1rem">Schedule &amp; Configuration</h3>

              <div style="margin-bottom: 1.5rem">
                <label style="display: block; margin-bottom: 0.25rem; font-weight: 600">Cron Schedule</label>
                <InputText v-model="schedule" placeholder="0 6 * * *" style="width: 100%; max-width: 300px" />
                <div style="font-size: 12px; opacity: 0.5; margin-top: 0.25rem">e.g. 0 6 * * 1-5 for weekdays at 6am</div>
              </div>

              <div style="margin-bottom: 1.5rem">
                <label style="display: block; margin-bottom: 0.25rem; font-weight: 600">Health Check ID <span style="font-weight: 400; opacity: 0.5">(optional)</span></label>
                <InputText v-model="healthCheckId" placeholder="healthchecks.io UUID" style="width: 100%; max-width: 400px" />
              </div>

              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('2')" />
                <Button label="Next" icon="pi pi-arrow-right" iconPos="right" :disabled="!schedule.trim()" @click="populateConfigKeys(); activateCallback('4')" />
              </div>
            </div>
          </StepPanel>

          <!-- Step 4: Provider Config -->
          <StepPanel v-slot="{ activateCallback }" value="4">
            <div style="padding: 1.5rem 0; min-height: 300px">
              <h3 style="margin-bottom: 0.5rem">Provider Configuration</h3>
              <p style="opacity: 0.5; margin-bottom: 1rem">Key-value pairs specific to {{ selectedProviderData?.name || selectedProvider }}.</p>

              <div style="display: flex; flex-direction: column; gap: 0.75rem">
                <div
                  v-for="(entry, i) in configEntries"
                  :key="i"
                  style="display: flex; gap: 0.5rem; align-items: flex-end"
                >
                  <div style="flex: 1">
                    <label style="display: block; font-size: 12px; margin-bottom: 0.25rem">Key</label>
                    <InputText v-model="entry.key" placeholder="config_key" style="width: 100%" />
                  </div>
                  <div style="flex: 1">
                    <label style="display: block; font-size: 12px; margin-bottom: 0.25rem">Value</label>
                    <InputText v-model="entry.value" placeholder="config_value" style="width: 100%" />
                  </div>
                  <Button icon="pi pi-trash" severity="danger" text size="small" @click="removeConfigEntry(i)" />
                </div>
                <div>
                  <Button label="Add entry" icon="pi pi-plus" text size="small" @click="addConfigEntry" />
                </div>
              </div>

              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('3')" />
                <Button label="Next" icon="pi pi-arrow-right" iconPos="right" @click="activateCallback('5')" />
              </div>
            </div>
          </StepPanel>

          <!-- Step 5: Review -->
          <StepPanel v-slot="{ activateCallback }" value="5">
            <div style="padding: 1.5rem 0; min-height: 300px">
              <h3 style="margin-bottom: 1rem">Review &amp; Create</h3>

              <Card style="margin-bottom: 1rem">
                <template #content>
                  <div style="display: grid; grid-template-columns: 120px 1fr; gap: 0.5rem 1rem; font-size: 14px">
                    <span style="opacity: 0.5">Provider</span>
                    <span style="font-weight: 600">{{ selectedProviderData?.name || selectedProvider }}</span>
                    <span style="opacity: 0.5">Dataset</span>
                    <span style="font-weight: 600">{{ selectedDataset }}</span>
                    <span style="opacity: 0.5">Schedule</span>
                    <span><code>{{ schedule }}</code></span>
                    <template v-if="healthCheckId">
                      <span style="opacity: 0.5">Health Check</span>
                      <span>{{ healthCheckId }}</span>
                    </template>
                    <template v-for="entry in configEntries.filter(e => e.key.trim())" :key="entry.key">
                      <span style="opacity: 0.5">{{ entry.key }}</span>
                      <span>{{ entry.value || '(empty)' }}</span>
                    </template>
                  </div>
                </template>
              </Card>

              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('4')" />
                <Button
                  :label="creating ? 'Creating...' : 'Create Subscription'"
                  icon="pi pi-check"
                  :loading="creating"
                  @click="onCreate"
                />
              </div>
            </div>
          </StepPanel>
        </StepPanels>
      </Stepper>
    </template>
  </div>
</template>
