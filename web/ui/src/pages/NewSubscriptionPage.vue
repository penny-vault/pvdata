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
import RadioButton from 'primevue/radiobutton'
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

// Form state
const selectedProvider = ref('')
const selectedDataset = ref('')
const schedule = ref('0 6 * * *')
const configEntries = ref<{ key: string; value: string }[]>([])
const healthCheckId = ref('')

const providerList = computed(() => {
  return Object.entries(providers.value).map(([key, val]) => ({
    id: key,
    name: val.name || key,
    description: val.description || '',
    datasets: val.datasets || [],
    config_description: val.config_description || {},
  }))
})

const selectedProviderData = computed(() => {
  return providerList.value.find((p) => p.id === selectedProvider.value)
})

const datasetOptions = computed(() => {
  if (!selectedProviderData.value) return []
  const datasets = selectedProviderData.value.datasets || {}
  return Object.entries(datasets).map(([key, val]: [string, any]) => ({
    key,
    name: val.name || key,
    description: val.description || '',
  }))
})

function populateConfigKeys() {
  if (configEntries.value.length === 0) {
    const desc = selectedProviderData.value?.config_description || {}
    const keys = Object.keys(desc)
    if (keys.length > 0) {
      configEntries.value = keys.map((k) => ({ key: k, value: '' }))
    } else {
      configEntries.value = [{ key: '', value: '' }]
    }
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
      if (entry.key.trim()) {
        config[entry.key.trim()] = entry.value
      }
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
  <div class="new-sub-page" style="max-width: 48rem; margin-left: auto; margin-right: auto">
    <div style="margin-bottom: 1.5rem">
      <Button label="Subscriptions" icon="pi pi-arrow-left" text size="small" @click="router.push('/')" style="margin-bottom: 0.5rem" />
      <h2>New Subscription</h2>
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
          <Step value="4">Configuration</Step>
          <Step value="5">Health Check</Step>
          <Step value="6">Confirm</Step>
        </StepList>
        <StepPanels>
          <!-- Step 1: Provider -->
          <StepPanel v-slot="{ activateCallback }" value="1">
            <div class="step-content">
              <h3 style="margin-bottom: 1rem">Select a Provider</h3>
              <div style="display: flex; flex-direction: column; gap: 0.75rem">
                <div
                  v-for="p in providerList"
                  :key="p.id"
                  style="display: flex; align-items: center; gap: 0.75rem"
                >
                  <RadioButton
                    v-model="selectedProvider"
                    :inputId="'provider-' + p.id"
                    :value="p.id"
                    name="provider"
                  />
                  <label :for="'provider-' + p.id" style="cursor: pointer">
                    {{ p.name }}
                  </label>
                </div>
              </div>
              <p v-if="selectedProviderData?.description" style="margin-top: 0.5rem">
                {{ selectedProviderData.description }}
              </p>
              <div style="display: flex; justify-content: flex-end; margin-top: 1.5rem">
                <Button
                  label="Next"
                  icon="pi pi-arrow-right"
                  iconPos="right"
                  :disabled="!selectedProvider"
                  @click="activateCallback('2')"
                />
              </div>
            </div>
          </StepPanel>

          <!-- Step 2: Dataset -->
          <StepPanel v-slot="{ activateCallback }" value="2">
            <div class="step-content">
              <h3 style="margin-bottom: 1rem">Select a Dataset</h3>
              <div style="display: flex; flex-direction: column; gap: 0.75rem">
                <div
                  v-for="ds in datasetOptions"
                  :key="ds.key"
                  style="display: flex; align-items: center; gap: 0.75rem"
                >
                  <RadioButton
                    v-model="selectedDataset"
                    :inputId="'dataset-' + ds.key"
                    :value="ds.key"
                    name="dataset"
                  />
                  <label :for="'dataset-' + ds.key" style="cursor: pointer">
                    {{ ds.description ? `${ds.name} - ${ds.description}` : ds.name }}
                  </label>
                </div>
              </div>
              <p v-if="datasetOptions.length === 0" style="margin-top: 0.5rem">
                No datasets available for this provider.
              </p>
              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('1')" />
                <Button
                  label="Next"
                  icon="pi pi-arrow-right"
                  iconPos="right"
                  :disabled="!selectedDataset"
                  @click="activateCallback('3')"
                />
              </div>
            </div>
          </StepPanel>

          <!-- Step 3: Schedule -->
          <StepPanel v-slot="{ activateCallback }" value="3">
            <div class="step-content">
              <h3 style="margin-bottom: 1rem">Set Schedule</h3>
              <div style="display: flex; flex-direction: column; gap: 0.5rem; max-width: 28rem">
                <label>Cron Expression</label>
                <InputText v-model="schedule" placeholder="0 6 * * *" />
                <small>
                  e.g. 0 6 * * 1-5 for weekdays at 6am, 0 0 * * 0 for weekly
                </small>
              </div>
              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('2')" />
                <Button
                  label="Next"
                  icon="pi pi-arrow-right"
                  iconPos="right"
                  :disabled="!schedule.trim()"
                  @click="populateConfigKeys(); activateCallback('4')"
                />
              </div>
            </div>
          </StepPanel>

          <!-- Step 4: Configuration -->
          <StepPanel v-slot="{ activateCallback }" value="4">
            <div class="step-content">
              <h3 style="margin-bottom: 0.5rem">Configuration</h3>
              <p style="margin-bottom: 1rem">
                Set provider-specific configuration values.
              </p>
              <div style="display: flex; flex-direction: column; gap: 0.75rem">
                <div
                  v-for="(entry, i) in configEntries"
                  :key="i"
                  style="display: grid; grid-template-columns: 1fr 1fr auto; gap: 0.75rem; align-items: flex-end"
                >
                  <div style="display: flex; flex-direction: column; gap: 0.25rem">
                    <label>Key</label>
                    <InputText v-model="entry.key" placeholder="config_key" />
                  </div>
                  <div style="display: flex; flex-direction: column; gap: 0.25rem">
                    <label>Value</label>
                    <InputText v-model="entry.value" placeholder="config_value" />
                  </div>
                  <Button
                    icon="pi pi-trash"
                    severity="danger"
                    text
                    size="small"
                    @click="removeConfigEntry(i)"
                  />
                </div>
                <div>
                  <Button
                    label="+ Add config entry"
                    text
                    size="small"
                    @click="addConfigEntry"
                  />
                </div>
              </div>
              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('3')" />
                <Button label="Next" icon="pi pi-arrow-right" iconPos="right" @click="activateCallback('5')" />
              </div>
            </div>
          </StepPanel>

          <!-- Step 5: Health Check -->
          <StepPanel v-slot="{ activateCallback }" value="5">
            <div class="step-content">
              <h3 style="margin-bottom: 1rem">Health Check (optional)</h3>
              <div style="display: flex; flex-direction: column; gap: 0.5rem; max-width: 28rem">
                <label>Health Check ID</label>
                <InputText v-model="healthCheckId" placeholder="UUID" />
                <small>healthchecks.io check UUID (optional)</small>
              </div>
              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('4')" />
                <Button label="Next" icon="pi pi-arrow-right" iconPos="right" @click="activateCallback('6')" />
              </div>
            </div>
          </StepPanel>

          <!-- Step 6: Confirm -->
          <StepPanel v-slot="{ activateCallback }" value="6">
            <div class="step-content">
              <h3 style="margin-bottom: 1rem">Review &amp; Create</h3>
              <div class="review-grid">
                <div class="review-item">
                  <span class="review-label">Provider</span>
                  <span class="review-value">{{ selectedProviderData?.name || selectedProvider }}</span>
                </div>
                <div class="review-item">
                  <span class="review-label">Dataset</span>
                  <span class="review-value">{{ selectedDataset }}</span>
                </div>
                <div class="review-item">
                  <span class="review-label">Schedule</span>
                  <span class="review-value">{{ schedule }}</span>
                </div>
                <div class="review-item">
                  <span class="review-label">Health Check</span>
                  <span class="review-value">{{ healthCheckId || 'None' }}</span>
                </div>
                <div
                  v-if="configEntries.some(e => e.key.trim())"
                  class="review-item col-span-2"
                >
                  <span class="review-label">Configuration</span>
                  <div style="display: flex; flex-direction: column; gap: 0.25rem; margin-top: 0.25rem">
                    <div
                      v-for="entry in configEntries.filter(e => e.key.trim())"
                      :key="entry.key"
                      style="font-size: 0.875rem"
                    >
                      <code>{{ entry.key }}</code>: {{ entry.value || '(empty)' }}
                    </div>
                  </div>
                </div>
              </div>
              <div style="display: flex; justify-content: space-between; margin-top: 1.5rem">
                <Button label="Back" severity="secondary" icon="pi pi-arrow-left" @click="activateCallback('5')" />
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

<style scoped>
.step-content {
  min-height: 250px;
  padding: 1.5rem 0;
}

.review-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 1rem;
}

.col-span-2 {
  grid-column: 1 / -1;
}
</style>
