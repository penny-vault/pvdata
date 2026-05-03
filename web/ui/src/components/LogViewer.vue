<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import InputText from 'primevue/inputtext'
import Button from 'primevue/button'
import Select from 'primevue/select'

interface LogEntry {
  raw: string
  time: string
  level: string
  message: string
  fields: Record<string, any>
}

const props = defineProps<{
  // Either pre-parsed entries or a multi-line text block of JSON.
  entries?: LogEntry[]
  text?: string
  // Show a heading + smaller layout when used inside a dialog.
  compact?: boolean
  // Total line count across the full log, when only a paged subset is in
  // `entries`. When greater than entries.length, the viewer shows a
  // "Load earlier" button driven by `onLoadEarlier`.
  total?: number
  // Called when the user clicks "Load earlier". Should resolve once the
  // earlier page has been merged into `entries`.
  onLoadEarlier?: () => Promise<void>
  loadingEarlier?: boolean
}>()

const search = ref('')
const sortKey = ref<'time' | 'level' | 'message'>('time')
const sortDir = ref<'asc' | 'desc'>('asc')
const levelFilter = ref<string>('')

const allEntries = computed<LogEntry[]>(() => {
  if (props.entries && props.entries.length > 0) return props.entries
  if (!props.text) return []
  return props.text
    .split('\n')
    .map(line => line.trim())
    .filter(line => line.length > 0)
    .map(parseLogLine)
})

const hasEarlier = computed(() =>
  typeof props.total === 'number'
  && props.total > allEntries.value.length
  && typeof props.onLoadEarlier === 'function',
)

const remainingEarlier = computed(() => Math.max(0, (props.total ?? 0) - allEntries.value.length))

const levels = computed(() => {
  const set = new Set<string>()
  for (const e of allEntries.value) if (e.level) set.add(e.level)
  return ['', ...Array.from(set).sort()]
})

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  let list = allEntries.value
  if (levelFilter.value) {
    list = list.filter(e => e.level === levelFilter.value)
  }
  if (q) {
    list = list.filter(e => e.raw.toLowerCase().includes(q))
  }
  const sorted = [...list].sort((a, b) => {
    const av = String(a[sortKey.value] ?? '')
    const bv = String(b[sortKey.value] ?? '')
    return sortDir.value === 'asc' ? av.localeCompare(bv) : bv.localeCompare(av)
  })
  return sorted
})

function setSort(key: 'time' | 'level' | 'message') {
  if (sortKey.value === key) {
    sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortKey.value = key
    sortDir.value = key === 'time' ? 'asc' : 'asc'
  }
}

function levelStyle(level: string): string {
  switch (level) {
    case 'error': case 'fatal': case 'panic':
      return 'color: var(--p-red-400); font-weight: 600'
    case 'warn':
      return 'color: var(--p-yellow-400); font-weight: 600'
    case 'info':
      return 'color: var(--p-green-400)'
    case 'debug':
      return 'color: var(--p-blue-400)'
    default:
      return 'opacity: 0.7'
  }
}

function formatFields(fields: Record<string, any>): string {
  const entries = Object.entries(fields).filter(([k]) => !['time', 'level', 'message'].includes(k))
  if (entries.length === 0) return ''
  return entries.map(([k, v]) => `${k}=${typeof v === 'string' ? v : JSON.stringify(v)}`).join(' ')
}

function parseLogLine(line: string): LogEntry {
  try {
    const obj = JSON.parse(line)
    return {
      raw: line,
      time: obj.time || obj.timestamp || '',
      level: (obj.level || '').toLowerCase(),
      message: obj.message || obj.msg || '',
      fields: obj,
    }
  } catch {
    return { raw: line, time: '', level: '', message: line, fields: {} }
  }
}

defineExpose({ parseLogLine })

const containerRef = ref<HTMLElement | null>(null)
watch(() => filtered.value.length, async () => {
  // Autoscroll on new entries when sorted ascending by time and not searching.
  if (sortKey.value === 'time' && sortDir.value === 'asc' && !search.value && containerRef.value) {
    await new Promise(r => requestAnimationFrame(r))
    containerRef.value.scrollTop = containerRef.value.scrollHeight
  }
})
</script>

<template>
  <div :style="{ display: 'flex', flexDirection: 'column', gap: '0.5rem', height: compact ? '60vh' : '400px' }">
    <div style="display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap">
      <InputText v-model="search" placeholder="Search logs..." size="small" style="flex: 1; min-width: 200px" />
      <Select v-model="levelFilter" :options="levels" placeholder="All levels" size="small" style="width: 140px">
        <template #option="{ option }">{{ option || 'All levels' }}</template>
        <template #value="{ value }">{{ value || 'All levels' }}</template>
      </Select>
      <span style="opacity: 0.7; font-size: 12px">
        {{ filtered.length.toLocaleString() }} / {{ allEntries.length.toLocaleString() }}<template v-if="typeof total === 'number' && total > allEntries.length"> of {{ total.toLocaleString() }}</template>
      </span>
      <Button
        v-if="hasEarlier"
        :label="`Load earlier (${remainingEarlier.toLocaleString()} more)`"
        icon="pi pi-arrow-up"
        size="small"
        text
        :loading="loadingEarlier"
        @click="onLoadEarlier && onLoadEarlier()"
      />
    </div>

    <div ref="containerRef" style="flex: 1; overflow: auto; border: 1px solid var(--p-content-border-color); border-radius: 4px; background: var(--p-surface-900)">
      <table style="width: 100%; border-collapse: collapse; font-family: monospace; font-size: 12px">
        <thead style="position: sticky; top: 0; background: var(--p-surface-800); user-select: none">
          <tr>
            <th style="text-align: left; padding: 0.4rem 0.6rem; cursor: pointer; width: 180px" @click="setSort('time')">
              time
              <i v-if="sortKey === 'time'" :class="sortDir === 'asc' ? 'pi pi-arrow-up' : 'pi pi-arrow-down'" style="font-size: 10px; margin-left: 0.25rem" />
            </th>
            <th style="text-align: left; padding: 0.4rem 0.6rem; cursor: pointer; width: 70px" @click="setSort('level')">
              level
              <i v-if="sortKey === 'level'" :class="sortDir === 'asc' ? 'pi pi-arrow-up' : 'pi pi-arrow-down'" style="font-size: 10px; margin-left: 0.25rem" />
            </th>
            <th style="text-align: left; padding: 0.4rem 0.6rem; cursor: pointer" @click="setSort('message')">
              message
              <i v-if="sortKey === 'message'" :class="sortDir === 'asc' ? 'pi pi-arrow-up' : 'pi pi-arrow-down'" style="font-size: 10px; margin-left: 0.25rem" />
            </th>
            <th style="text-align: left; padding: 0.4rem 0.6rem">fields</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(e, i) in filtered" :key="i" style="border-top: 1px solid var(--p-content-border-color)">
            <td style="padding: 0.3rem 0.6rem; opacity: 0.6; white-space: nowrap">{{ e.time }}</td>
            <td style="padding: 0.3rem 0.6rem; white-space: nowrap" :style="levelStyle(e.level)">{{ e.level }}</td>
            <td style="padding: 0.3rem 0.6rem">{{ e.message }}</td>
            <td style="padding: 0.3rem 0.6rem; opacity: 0.7; word-break: break-all">{{ formatFields(e.fields) }}</td>
          </tr>
          <tr v-if="filtered.length === 0">
            <td colspan="4" style="padding: 1rem; text-align: center; opacity: 0.6">No log lines.</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
