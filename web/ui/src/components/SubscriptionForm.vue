<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { updateSubscription } from '@/lib/api'
import InputText from 'primevue/inputtext'
import Button from 'primevue/button'
import Message from 'primevue/message'

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
  <div class="subscription-form" style="display: flex; flex-direction: column; gap: 1.5rem; max-width: 36rem">
    <Message v-if="error" severity="error" :closable="true" @close="error = ''">
      {{ error }}
    </Message>

    <div style="display: flex; flex-direction: column; gap: 0.5rem">
      <label>Schedule (cron expression)</label>
      <InputText v-model="schedule" placeholder="0 6 * * *" />
      <small>e.g. 0 6 * * 1-5 for weekdays at 6am</small>
    </div>

    <div style="display: flex; flex-direction: column; gap: 0.75rem">
      <label>Configuration</label>
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
    </div>

    <div style="display: flex; flex-direction: column; gap: 0.5rem">
      <label>Health Check ID</label>
      <InputText v-model="healthCheckId" placeholder="UUID" />
      <small>Optional healthchecks.io check ID</small>
    </div>

    <div style="display: flex; gap: 0.75rem; padding-top: 1rem">
      <Button label="Save" :loading="saving" @click="onSave" />
      <Button label="Cancel" severity="secondary" @click="$emit('cancel')" />
    </div>
  </div>
</template>
