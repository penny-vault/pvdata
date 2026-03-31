<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getProviders, createSubscription } from '@/lib/api'

const router = useRouter()

const providers = ref<Record<string, any>>({})
const loading = ref(true)
const creating = ref(false)
const error = ref('')
const currentStep = ref(0)

// Form state
const selectedProvider = ref('')
const selectedDataset = ref('')
const schedule = ref('0 6 * * *')
const configEntries = ref<{ key: string; value: string }[]>([])
const healthCheckId = ref('')

const steps = ['Provider', 'Dataset', 'Schedule', 'Configuration', 'Health Check', 'Confirm']

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

const canNext = computed(() => {
  switch (currentStep.value) {
    case 0:
      return !!selectedProvider.value
    case 1:
      return !!selectedDataset.value
    case 2:
      return !!schedule.value.trim()
    case 3:
      return true
    case 4:
      return true
    case 5:
      return true
    default:
      return false
  }
})

function next() {
  if (currentStep.value < steps.length - 1) {
    currentStep.value++
    // Pre-populate config keys when entering config step
    if (currentStep.value === 3 && configEntries.value.length === 0) {
      const desc = selectedProviderData.value?.config_description || {}
      const keys = Object.keys(desc)
      if (keys.length > 0) {
        configEntries.value = keys.map((k) => ({ key: k, value: '' }))
      } else {
        configEntries.value = [{ key: '', value: '' }]
      }
    }
  }
}

function back() {
  if (currentStep.value > 0) {
    currentStep.value--
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
  <div class="new-sub-page">
    <div class="page-header">
      <button class="back-link" @click="router.push('/')">
        &larr; Subscriptions
      </button>
      <h2>New Subscription</h2>
    </div>

    <div v-if="loading" class="page-loading">
      <cv-loading active />
    </div>

    <template v-else>
      <div v-if="error" class="error-notice">
        <cv-inline-notification
          kind="error"
          :title="error"
          @close="error = ''"
        />
      </div>

      <!-- Progress indicator -->
      <div class="wizard-progress">
        <cv-progress :initial-step="currentStep" :steps="steps" />
      </div>

      <!-- Step content -->
      <div class="wizard-content">
        <!-- Step 0: Provider -->
        <div v-if="currentStep === 0" class="step-panel">
          <h3>Select a Provider</h3>
          <cv-radio-group
            legend-text="Available Providers"
            :hide-legend="true"
            vertical
            @change="(val: string) => selectedProvider = val"
          >
            <cv-radio-button
              v-for="p in providerList"
              :key="p.id"
              :value="p.id"
              :label="p.name"
              :checked="selectedProvider === p.id"
              name="provider"
            />
          </cv-radio-group>
          <p
            v-if="selectedProviderData?.description"
            class="step-description"
          >
            {{ selectedProviderData.description }}
          </p>
        </div>

        <!-- Step 1: Dataset -->
        <div v-if="currentStep === 1" class="step-panel">
          <h3>Select a Dataset</h3>
          <cv-radio-group
            legend-text="Available Datasets"
            :hide-legend="true"
            vertical
            @change="(val: string) => selectedDataset = val"
          >
            <cv-radio-button
              v-for="ds in datasetOptions"
              :key="ds.key"
              :value="ds.key"
              :label="ds.description ? `${ds.name} - ${ds.description}` : ds.name"
              :checked="selectedDataset === ds.key"
              name="dataset"
            />
          </cv-radio-group>
          <p v-if="datasetOptions.length === 0" class="step-description">
            No datasets available for this provider.
          </p>
        </div>

        <!-- Step 2: Schedule -->
        <div v-if="currentStep === 2" class="step-panel">
          <h3>Set Schedule</h3>
          <cv-text-input
            v-model="schedule"
            label="Cron Expression"
            helper-text="e.g. 0 6 * * 1-5 for weekdays at 6am, 0 0 * * 0 for weekly"
            placeholder="0 6 * * *"
          />
        </div>

        <!-- Step 3: Configuration -->
        <div v-if="currentStep === 3" class="step-panel">
          <h3>Configuration</h3>
          <p class="step-description">
            Set provider-specific configuration values.
          </p>
          <div
            v-for="(entry, i) in configEntries"
            :key="i"
            class="config-row"
          >
            <cv-text-input
              v-model="entry.key"
              label="Key"
              :placeholder="'config_key'"
            />
            <cv-text-input
              v-model="entry.value"
              label="Value"
              :placeholder="'config_value'"
            />
            <button
              class="bx--btn bx--btn--ghost bx--btn--sm config-remove"
              @click="removeConfigEntry(i)"
            >
              Remove
            </button>
          </div>
          <button
            class="bx--btn bx--btn--ghost bx--btn--sm"
            @click="addConfigEntry"
          >
            + Add config entry
          </button>
        </div>

        <!-- Step 4: Health Check -->
        <div v-if="currentStep === 4" class="step-panel">
          <h3>Health Check (optional)</h3>
          <cv-text-input
            v-model="healthCheckId"
            label="Health Check ID"
            helper-text="healthchecks.io check UUID (optional)"
            placeholder="UUID"
          />
        </div>

        <!-- Step 5: Confirm -->
        <div v-if="currentStep === 5" class="step-panel">
          <h3>Review &amp; Create</h3>
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
            <div v-if="configEntries.some(e => e.key.trim())" class="review-item review-item--full">
              <span class="review-label">Configuration</span>
              <div class="review-config">
                <div
                  v-for="entry in configEntries.filter(e => e.key.trim())"
                  :key="entry.key"
                  class="review-config-entry"
                >
                  <code>{{ entry.key }}</code>: {{ entry.value || '(empty)' }}
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Navigation buttons -->
      <div class="wizard-nav">
        <cv-button
          v-if="currentStep > 0"
          kind="secondary"
          @click="back"
        >
          Back
        </cv-button>
        <div class="wizard-nav__spacer"></div>
        <cv-button
          v-if="currentStep < steps.length - 1"
          kind="primary"
          :disabled="!canNext"
          @click="next"
        >
          Next
        </cv-button>
        <cv-button
          v-if="currentStep === steps.length - 1"
          kind="primary"
          :disabled="creating"
          @click="onCreate"
        >
          {{ creating ? 'Creating...' : 'Create Subscription' }}
        </cv-button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.new-sub-page {
  padding: 2rem;
  max-width: 800px;
  margin: 0 auto;
}

.page-header {
  margin-bottom: 2rem;
}

.page-header h2 {
  margin: 0;
  font-weight: 400;
  color: var(--cds-text-primary, #f4f4f4);
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

.page-loading {
  display: flex;
  justify-content: center;
  padding: 4rem;
}

.error-notice {
  margin-bottom: 1rem;
}

.wizard-progress {
  margin-bottom: 2rem;
}

.wizard-content {
  min-height: 300px;
  margin-bottom: 2rem;
}

.step-panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.step-panel h3 {
  font-weight: 400;
  margin: 0;
  color: var(--cds-text-primary, #f4f4f4);
}

.step-description {
  color: var(--cds-text-secondary, #c6c6c6);
  font-size: 0.875rem;
}

.config-row {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  gap: 0.75rem;
  align-items: flex-end;
  margin-bottom: 0.5rem;
}

.config-remove {
  margin-bottom: 0.25rem;
}

.review-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 1rem;
}

.review-item {
  background-color: var(--cds-layer-01, #262626);
  border: 1px solid var(--cds-border-subtle-01, #393939);
  border-radius: 2px;
  padding: 1rem;
}

.review-item--full {
  grid-column: 1 / -1;
}

.review-label {
  display: block;
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.32px;
  color: var(--cds-text-secondary, #c6c6c6);
  margin-bottom: 0.5rem;
}

.review-value {
  font-size: 1rem;
  color: var(--cds-text-primary, #f4f4f4);
}

.review-config {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

.review-config-entry {
  font-size: 0.875rem;
  color: var(--cds-text-primary, #f4f4f4);
}

.review-config-entry code {
  color: var(--cds-link-primary, #78a9ff);
  font-family: 'IBM Plex Mono', monospace;
}

.wizard-nav {
  display: flex;
  gap: 0.75rem;
  padding-top: 1rem;
  border-top: 1px solid var(--cds-border-subtle-01, #393939);
}

.wizard-nav__spacer {
  flex: 1;
}
</style>
