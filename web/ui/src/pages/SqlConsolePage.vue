<script setup lang="ts">
import { ref, onMounted, watch, shallowRef } from 'vue'
import { useRoute } from 'vue-router'
import { executeSQL, exportSQL } from '@/lib/api'
import { EditorView, basicSetup } from 'codemirror'
import { EditorState } from '@codemirror/state'
import { PostgreSQL } from '@codemirror/lang-sql'
import { oneDark } from '@codemirror/theme-one-dark'

const route = useRoute()

const editorContainer = ref<HTMLElement | null>(null)
const editorView = shallowRef<EditorView | null>(null)

const columns = ref<string[]>([])
const rows = ref<any[][]>([])
const rowCount = ref(0)
const executing = ref(false)
const error = ref('')
const showHistory = ref(false)
const history = ref<string[]>([])

const HISTORY_KEY = 'pvdata-sql-history'

function loadHistory() {
  try {
    const stored = localStorage.getItem(HISTORY_KEY)
    if (stored) {
      history.value = JSON.parse(stored)
    }
  } catch {
    history.value = []
  }
}

function saveHistory(query: string) {
  const trimmed = query.trim()
  if (!trimmed) return
  // Remove duplicate if exists
  history.value = history.value.filter((h) => h !== trimmed)
  history.value.unshift(trimmed)
  // Keep last 50
  if (history.value.length > 50) {
    history.value = history.value.slice(0, 50)
  }
  localStorage.setItem(HISTORY_KEY, JSON.stringify(history.value))
}

function clearHistory() {
  history.value = []
  localStorage.removeItem(HISTORY_KEY)
}

function getEditorContent(): string {
  if (!editorView.value) return ''
  return editorView.value.state.doc.toString()
}

function setEditorContent(text: string) {
  if (!editorView.value) return
  const transaction = editorView.value.state.update({
    changes: {
      from: 0,
      to: editorView.value.state.doc.length,
      insert: text,
    },
  })
  editorView.value.dispatch(transaction)
}

async function onExecute() {
  const query = getEditorContent().trim()
  if (!query) return

  executing.value = true
  error.value = ''
  columns.value = []
  rows.value = []
  rowCount.value = 0

  try {
    saveHistory(query)
    const result = await executeSQL(query)
    columns.value = result.columns || []
    rows.value = result.data || []
    rowCount.value = result.count ?? rows.value.length
  } catch (e: any) {
    error.value = e.message || 'Query execution failed'
  } finally {
    executing.value = false
  }
}

async function onExportCsv() {
  const query = getEditorContent().trim()
  if (!query) return
  try {
    await exportSQL(query)
  } catch (e: any) {
    error.value = e.message || 'Export failed'
  }
}

function loadFromHistory(query: string) {
  setEditorContent(query)
  showHistory.value = false
}

function formatCell(value: any): string {
  if (value === null || value === undefined) return 'NULL'
  if (typeof value === 'number') return value.toLocaleString()
  return String(value)
}

onMounted(() => {
  loadHistory()

  // Initialize CodeMirror
  const initialQuery = (route.query.q as string) || 'SELECT 1;'
  const startState = EditorState.create({
    doc: initialQuery,
    extensions: [
      basicSetup,
      PostgreSQL,
      oneDark,
      EditorView.theme({
        '&': {
          fontSize: '14px',
          minHeight: '180px',
          maxHeight: '400px',
          border: '1px solid var(--cds-border-subtle-01, #393939)',
        },
        '.cm-scroller': {
          overflow: 'auto',
          fontFamily: '"IBM Plex Mono", monospace',
        },
        '.cm-gutters': {
          backgroundColor: '#1e1e1e',
          borderRight: '1px solid #333',
        },
      }),
      EditorView.domEventHandlers({
        keydown(event: KeyboardEvent) {
          if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
            onExecute()
            return true
          }
          return false
        },
      }),
    ],
  })

  if (editorContainer.value) {
    editorView.value = new EditorView({
      state: startState,
      parent: editorContainer.value,
    })
  }
})
</script>

<template>
  <div class="sql-page">
    <div class="page-header">
      <h2>SQL Console</h2>
    </div>

    <!-- Editor -->
    <div class="editor-section">
      <div ref="editorContainer" class="editor-container"></div>

      <div class="editor-toolbar">
        <cv-button kind="primary" :disabled="executing" @click="onExecute">
          {{ executing ? 'Executing...' : 'Execute' }}
        </cv-button>
        <cv-button
          kind="ghost"
          :disabled="rows.length === 0"
          @click="onExportCsv"
        >
          Export CSV
        </cv-button>
        <div class="editor-toolbar__spacer"></div>
        <cv-button kind="ghost" @click="showHistory = !showHistory">
          {{ showHistory ? 'Hide History' : 'History' }}
        </cv-button>
      </div>

      <p class="editor-hint">Ctrl+Enter / Cmd+Enter to execute</p>
    </div>

    <!-- History panel -->
    <div v-if="showHistory" class="history-panel">
      <div class="history-panel__header">
        <h4>Query History</h4>
        <cv-button
          v-if="history.length > 0"
          kind="ghost"
          size="sm"
          @click="clearHistory"
        >
          Clear
        </cv-button>
      </div>
      <div v-if="history.length === 0" class="history-empty">
        No queries in history.
      </div>
      <div
        v-for="(q, i) in history"
        :key="i"
        class="history-item"
        @click="loadFromHistory(q)"
      >
        <code>{{ q.length > 120 ? q.substring(0, 120) + '...' : q }}</code>
      </div>
    </div>

    <!-- Error -->
    <div v-if="error" class="error-notice">
      <cv-inline-notification
        kind="error"
        :title="'Query Error'"
        :sub-title="error"
        @close="error = ''"
      />
    </div>

    <!-- Results -->
    <div v-if="columns.length > 0" class="results-section">
      <div class="results-header">
        <span class="results-count">
          {{ rowCount.toLocaleString() }} row{{ rowCount === 1 ? '' : 's' }}
        </span>
      </div>

      <div class="results-table-wrap">
        <table class="bx--data-table bx--data-table--compact">
          <thead>
            <tr>
              <th v-for="col in columns" :key="col">
                <span class="bx--table-header-label">{{ col }}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, ri) in rows" :key="ri">
              <td v-for="(cell, ci) in row" :key="ci">
                {{ formatCell(cell) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sql-page {
  padding: 2rem;
  max-width: 1400px;
  margin: 0 auto;
}

.page-header {
  margin-bottom: 1.5rem;
}

.page-header h2 {
  margin: 0;
  font-weight: 400;
  color: var(--cds-text-primary, #f4f4f4);
}

.editor-section {
  margin-bottom: 1.5rem;
}

.editor-container {
  margin-bottom: 0.75rem;
  border-radius: 2px;
  overflow: hidden;
}

.editor-toolbar {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.editor-toolbar__spacer {
  flex: 1;
}

.editor-hint {
  margin-top: 0.5rem;
  font-size: 0.75rem;
  color: var(--cds-text-placeholder, #6f6f6f);
}

.history-panel {
  background-color: var(--cds-layer-01, #262626);
  border: 1px solid var(--cds-border-subtle-01, #393939);
  border-radius: 2px;
  padding: 1rem;
  margin-bottom: 1.5rem;
  max-height: 300px;
  overflow-y: auto;
}

.history-panel__header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0.75rem;
}

.history-panel__header h4 {
  margin: 0;
  font-weight: 400;
  color: var(--cds-text-primary, #f4f4f4);
}

.history-empty {
  color: var(--cds-text-placeholder, #6f6f6f);
  font-size: 0.875rem;
}

.history-item {
  padding: 0.5rem;
  cursor: pointer;
  border-bottom: 1px solid var(--cds-border-subtle-01, #393939);
  transition: background-color 0.1s;
}

.history-item:last-child {
  border-bottom: none;
}

.history-item:hover {
  background-color: var(--cds-layer-hover-01, #353535);
}

.history-item code {
  font-family: 'IBM Plex Mono', monospace;
  font-size: 0.8125rem;
  color: var(--cds-text-secondary, #c6c6c6);
  white-space: pre-wrap;
  word-break: break-all;
}

.error-notice {
  margin-bottom: 1.5rem;
}

.results-section {
  margin-top: 1rem;
}

.results-header {
  margin-bottom: 0.75rem;
}

.results-count {
  font-size: 0.875rem;
  color: var(--cds-text-secondary, #c6c6c6);
}

.results-table-wrap {
  border: 1px solid var(--cds-border-subtle-01, #393939);
  border-radius: 2px;
  overflow: auto;
  max-height: 500px;
}

.results-table-wrap .bx--data-table {
  width: 100%;
}

.results-table-wrap .bx--data-table thead th {
  background-color: var(--cds-layer-accent-01, #333333);
  color: var(--cds-text-primary, #f4f4f4);
  position: sticky;
  top: 0;
  z-index: 1;
}

.results-table-wrap .bx--data-table tbody td {
  color: var(--cds-text-primary, #f4f4f4);
  white-space: nowrap;
  max-width: 300px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.results-table-wrap .bx--data-table tbody tr:hover td {
  background-color: var(--cds-layer-hover-01, #353535);
}
</style>
