<script setup lang="ts">
import { ref, computed, onMounted, shallowRef } from 'vue'
import { useRoute } from 'vue-router'
import { executeSQL, exportSQL, getPublications } from '@/lib/api'
import { EditorView, basicSetup } from 'codemirror'
import { EditorState } from '@codemirror/state'
import { PostgreSQL } from '@codemirror/lang-sql'
import { oneDark } from '@codemirror/theme-one-dark'
import RevoGrid from '@revolist/vue3-datagrid'
import Button from 'primevue/button'
import Toolbar from 'primevue/toolbar'
import Message from 'primevue/message'
import Card from 'primevue/card'

const route = useRoute()
const editorContainer = ref<HTMLElement | null>(null)
const editorView = shallowRef<EditorView | null>(null)
const columns = ref<string[]>([])
const rows = ref<any[][]>([])
const rowCount = ref(0)
const executing = ref(false)
const error = ref('')
const showHistory = ref(false)
const showTemplates = ref(false)
const publications = ref<any[]>([])
const history = ref<string[]>([])
const HISTORY_KEY = 'pvdata-sql-history'

function loadHistory() {
  try { const s = localStorage.getItem(HISTORY_KEY); if (s) history.value = JSON.parse(s) } catch { history.value = [] }
}
function saveHistory(query: string) {
  const t = query.trim(); if (!t) return
  history.value = [t, ...history.value.filter(h => h !== t)].slice(0, 50)
  localStorage.setItem(HISTORY_KEY, JSON.stringify(history.value))
}
function clearHistory() { history.value = []; localStorage.removeItem(HISTORY_KEY) }
async function loadPublications() {
  try { publications.value = await getPublications() } catch { publications.value = [] }
}
function templateQuery(viewName: string): string {
  return `SELECT *\nFROM ${viewName}\nLIMIT 100;`
}
function getEditorContent(): string { return editorView.value?.state.doc.toString() || '' }
function setEditorContent(text: string) {
  if (!editorView.value) return
  editorView.value.dispatch({ changes: { from: 0, to: editorView.value.state.doc.length, insert: text } })
}

async function onExecute() {
  const query = getEditorContent().trim(); if (!query) return
  executing.value = true; error.value = ''; columns.value = []; rows.value = []; rowCount.value = 0
  try {
    saveHistory(query)
    const result = await executeSQL(query)
    columns.value = result.columns || []; rows.value = result.data || []; rowCount.value = result.count ?? rows.value.length
  } catch (e: any) { error.value = e.message || 'Query execution failed' }
  finally { executing.value = false }
}

async function onExport(format: 'csv' | 'parquet') {
  const query = getEditorContent().trim(); if (!query) return
  try { await exportSQL(query, format) } catch (e: any) { error.value = e.message || 'Export failed' }
}

function formatCell(value: any): string {
  if (value === null || value === undefined) return 'NULL'
  if (typeof value === 'number') return value.toLocaleString()
  if (typeof value === 'object') {
    try { return JSON.stringify(value) } catch { return '--' }
  }
  return String(value)
}

const gridColumns = computed(() =>
  columns.value.map(col => ({ prop: col, name: col, sortable: true, size: 150 }))
)

const gridRows = computed(() =>
  rows.value.map(row => {
    const obj: Record<string, any> = {}
    columns.value.forEach((col, i) => {
      const val = Array.isArray(row) ? row[i] : row[col]
      obj[col] = formatCell(val)
    })
    return obj
  })
)

onMounted(() => {
  loadHistory()
  const initialQuery = (route.query.q as string) || "SELECT ticker, event_date, open, high, low, close, volume\nFROM eod\nWHERE event_date >= now() - interval '7 days'\nORDER BY event_date DESC, ticker\nLIMIT 100;"
  const startState = EditorState.create({
    doc: initialQuery,
    extensions: [
      basicSetup, PostgreSQL, oneDark,
      EditorView.theme({ '&': { fontSize: '14px', minHeight: '180px', maxHeight: '400px', borderRadius: '6px' }, '.cm-scroller': { overflow: 'auto', fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, monospace' } }),
      EditorView.domEventHandlers({ keydown(event: KeyboardEvent) { if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') { onExecute(); return true }; return false } }),
    ],
  })
  if (editorContainer.value) editorView.value = new EditorView({ state: startState, parent: editorContainer.value })
  loadPublications()
})
</script>

<template>
  <div>
    <h2 style="margin-bottom: 1rem">SQL Console</h2>

    <div ref="editorContainer" style="border-radius: 6px; overflow: hidden; margin-bottom: 0.75rem"></div>

    <Toolbar style="margin-bottom: 1rem">
      <template #start>
        <div style="display: flex; align-items: center; gap: 0.5rem">
          <Button label="Execute" icon="pi pi-play" :loading="executing" @click="onExecute" />
          <Button label="Export CSV" icon="pi pi-download" text :disabled="rows.length === 0" @click="onExport('csv')" />
          <Button label="Export Parquet" icon="pi pi-download" text :disabled="rows.length === 0" @click="onExport('parquet')" />
        </div>
      </template>
      <template #end>
        <div style="display: flex; gap: 0.5rem">
          <Button :label="showTemplates ? 'Hide Templates' : 'Templates'" :icon="showTemplates ? 'pi pi-times' : 'pi pi-book'" text @click="showTemplates = !showTemplates; if (showTemplates) showHistory = false" />
          <Button :label="showHistory ? 'Hide History' : 'History'" :icon="showHistory ? 'pi pi-times' : 'pi pi-history'" text @click="showHistory = !showHistory; if (showHistory) showTemplates = false" />
        </div>
      </template>
    </Toolbar>

    <Card v-if="showTemplates" style="margin-bottom: 1rem">
      <template #title>Published Views</template>
      <template #content>
        <p v-if="publications.length === 0">No published views found.</p>
        <div v-for="pub in publications" :key="pub.id" style="padding: 0.5rem; cursor: pointer; border-bottom: 1px solid var(--p-content-border-color); display: flex; justify-content: space-between; align-items: center" @click="setEditorContent(templateQuery(pub.view_name)); showTemplates = false">
          <div>
            <strong>{{ pub.view_name }}</strong>
            <span style="opacity: 0.6; margin-left: 0.5rem">{{ pub.data_type_key }} &middot; {{ pub.source_count }} source{{ pub.source_count === 1 ? '' : 's' }}</span>
          </div>
          <code style="font-size: 0.8rem; opacity: 0.5">SELECT * FROM {{ pub.view_name }} ...</code>
        </div>
      </template>
    </Card>

    <Card v-if="showHistory" style="margin-bottom: 1rem">
      <template #title>
        <div style="display: flex; justify-content: space-between; align-items: center">
          <span>Query History</span>
          <Button v-if="history.length > 0" label="Clear" text size="small" severity="danger" @click="clearHistory" />
        </div>
      </template>
      <template #content>
        <p v-if="history.length === 0">No queries in history.</p>
        <div v-for="(q, i) in history" :key="i" style="padding: 0.5rem; cursor: pointer; border-bottom: 1px solid var(--p-content-border-color)" @click="setEditorContent(q); showHistory = false">
          <code>{{ q.length > 120 ? q.substring(0, 120) + '...' : q }}</code>
        </div>
      </template>
    </Card>

    <Message v-if="error" severity="error" :closable="true" style="margin-bottom: 1rem" @close="error = ''">{{ error }}</Message>

    <div v-if="columns.length > 0">
      <p style="margin-bottom: 0.5rem">{{ rowCount.toLocaleString() }} row{{ rowCount === 1 ? '' : 's' }}</p>
      <RevoGrid
        :columns="gridColumns"
        :source="gridRows"
        :resize="true"
        :autoSizeColumn="true"
        stretch="all"
        theme="darkCompact"
        style="height: 500px"
      />
    </div>
  </div>
</template>
