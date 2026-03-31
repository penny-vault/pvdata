<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { updateSubscription } from '@/lib/api'

interface Subscription {
  id: string
  schedule: string
  config: Record<string, string>
  health_check_id: string
  [key: string]: any
}

const props = defineProps<{
  subscription: Subscription
}>()

const emit = defineEmits<{
  saved: [sub: any]
  cancel: []
}>()

const schedule = ref('')
const healthCheckId = ref('')
const configEntries = ref<{ key: string; value: string }[]>([])
const saving = ref(false)
const error = ref('')

onMounted(() => {
  schedule.value = props.subscription.schedule || ''
  healthCheckId.value = props.subscription.health_check_id || ''
  const cfg = props.subscription.config || {}
  configEntries.value = Object.entries(cfg).map(([key, value]) => ({
    key,
    value: String(value),
  }))
  if (configEntries.value.length === 0) {
    configEntries.value.push({ key: '', value: '' })
  }
})

function addConfigEntry() {
  configEntries.value.push({ key: '', value: '' })
}

function removeConfigEntry(index: number) {
  configEntries.value.splice(index, 1)
}

async function onSave() {
  saving.value = true
  error.value = ''
  try {
    const config: Record<string, string> = {}
    for (const entry of configEntries.value) {
      if (entry.key.trim()) {
        config[entry.key.trim()] = entry.value
      }
    }
    const result = await updateSubscription(props.subscription.id, {
      schedule: schedule.value,
      config,
      health_check_id: healthCheckId.value,
    })
    emit('saved', result)
  } catch (e: any) {
    error.value = e.message || 'Failed to save subscription'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="subscription-form">
    <div v-if="error" class="form-error">
      <cv-inline-notification
        kind="error"
        :title="error"
        @close="error = ''"
      />
    </div>

    <div class="form-field">
      <cv-text-input
        v-model="schedule"
        label="Schedule (cron expression)"
        helper-text="e.g. 0 6 * * 1-5 for weekdays at 6am"
        placeholder="0 6 * * *"
      />
    </div>

    <div class="form-section">
      <h4 class="form-section__title">Configuration</h4>
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

    <div class="form-field">
      <cv-text-input
        v-model="healthCheckId"
        label="Health Check ID"
        helper-text="Optional healthchecks.io check ID"
        placeholder="UUID"
      />
    </div>

    <div class="form-actions">
      <cv-button kind="primary" :disabled="saving" @click="onSave">
        {{ saving ? 'Saving...' : 'Save' }}
      </cv-button>
      <cv-button kind="secondary" @click="$emit('cancel')">
        Cancel
      </cv-button>
    </div>
  </div>
</template>

<style scoped>
.subscription-form {
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
  max-width: 640px;
}

.form-error {
  margin-bottom: 0.5rem;
}

.form-field {
  display: flex;
  flex-direction: column;
}

.form-section__title {
  color: var(--cds-text-secondary, #c6c6c6);
  font-size: 0.875rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.32px;
  margin-bottom: 0.75rem;
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

.form-actions {
  display: flex;
  gap: 0.75rem;
  padding-top: 1rem;
}
</style>
