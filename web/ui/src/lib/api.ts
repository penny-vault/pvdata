import { getAccessToken } from './oidc'

const BASE = '/api/v1'

async function handleResponse<T>(res: Response): Promise<T> {
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
  return res.json()
}

async function authFetch(path: string, init?: RequestInit): Promise<Response> {
  const token = await getAccessToken()
  const headers = new Headers(init?.headers)

  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }

  if (!headers.has('Content-Type') && init?.body && typeof init.body === 'string') {
    headers.set('Content-Type', 'application/json')
  }

  return fetch(`${BASE}${path}`, { ...init, headers })
}

// ---------- Subscriptions ----------

export async function getSubscriptions() {
  const res = await authFetch('/subscriptions')
  return handleResponse<any[]>(res)
}

export async function getSubscription(id: string) {
  const res = await authFetch(`/subscriptions/${id}`)
  return handleResponse<any>(res)
}

export async function createSubscription(body: Record<string, unknown>) {
  const res = await authFetch('/subscriptions', {
    method: 'POST',
    body: JSON.stringify(body),
  })
  return handleResponse<any>(res)
}

export async function updateSubscription(id: string, body: Record<string, unknown>) {
  const res = await authFetch(`/subscriptions/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  })
  return handleResponse<any>(res)
}

export async function deleteSubscription(id: string) {
  const res = await authFetch(`/subscriptions/${id}`, { method: 'DELETE' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
}

export async function activateSubscription(id: string) {
  const res = await authFetch(`/subscriptions/${id}/activate`, { method: 'POST' })
  return handleResponse<any>(res)
}

export async function deactivateSubscription(id: string) {
  const res = await authFetch(`/subscriptions/${id}/deactivate`, { method: 'POST' })
  return handleResponse<any>(res)
}

// ---------- Providers ----------

export async function getProviders() {
  const res = await authFetch('/providers')
  return handleResponse<Record<string, any>>(res)
}

// ---------- Run History ----------

export async function getRunHistory(subscriptionId: string, limit = 50, offset = 0) {
  const res = await authFetch(`/subscriptions/${subscriptionId}/runs?limit=${limit}&offset=${offset}`)
  return handleResponse<any>(res)
}

export async function getSparkline(subscriptionId: string) {
  const res = await authFetch(`/subscriptions/${subscriptionId}/runs/sparkline`)
  return handleResponse<any[]>(res)
}

export interface RunLogPage {
  lines: string[]
  total: number
  // 1-indexed line number of lines[0] within the full log; 0 when empty.
  startLine: number
}

export async function getRunLog(
  subscriptionId: string,
  runId: string,
  opts: { before?: number; limit?: number } = {},
): Promise<RunLogPage> {
  const params = new URLSearchParams()
  if (opts.before && opts.before > 0) params.set('before', String(opts.before))
  if (opts.limit && opts.limit > 0) params.set('limit', String(opts.limit))
  const qs = params.toString()
  const res = await authFetch(`/subscriptions/${subscriptionId}/runs/${runId}/log${qs ? '?' + qs : ''}`)
  const body = await handleResponse<{ lines: string[]; total: number; start_line: number }>(res)
  return {
    lines: body.lines || [],
    total: body.total || 0,
    startLine: body.start_line || 0,
  }
}

export async function getRunStatus(subscriptionId: string): Promise<{ active: boolean }> {
  const res = await authFetch(`/subscriptions/${subscriptionId}/run/status`)
  return handleResponse<{ active: boolean }>(res)
}

// ---------- Data ----------

export async function getData(
  subscriptionId: string,
  dataType: string,
  params: Record<string, string> = {},
) {
  const qs = new URLSearchParams(params).toString()
  const res = await authFetch(
    `/subscriptions/${subscriptionId}/data/${dataType}${qs ? '?' + qs : ''}`,
  )
  return handleResponse<any>(res)
}

// ---------- SQL ----------

export async function executeSQL(sql: string) {
  const res = await authFetch('/sql', {
    method: 'POST',
    body: JSON.stringify({ query: sql }),
  })
  return handleResponse<any>(res)
}

export async function exportSQL(sql: string, format: 'csv' | 'parquet' = 'csv') {
  const res = await authFetch(`/sql/export?format=${format}`, {
    method: 'POST',
    body: JSON.stringify({ query: sql }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `export.${format}`
  a.click()
  URL.revokeObjectURL(url)
}

// ---------- Run On Demand ----------

export async function runSubscription(id: string, lookback?: string): Promise<void> {
  const qs = lookback ? `?lookback=${encodeURIComponent(lookback)}` : ''
  const res = await authFetch(`/subscriptions/${id}/run${qs}`, { method: 'POST' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
}

export async function cancelSubscriptionRun(id: string, force = false): Promise<void> {
  const qs = force ? '?force=true' : ''
  const res = await authFetch(`/subscriptions/${id}/run/cancel${qs}`, { method: 'POST' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
}

export async function downloadRunLog(subscriptionId: string, runId: string, filename?: string): Promise<void> {
  const res = await authFetch(`/subscriptions/${subscriptionId}/runs/${runId}/log/download`)
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
  if (res.status === 204) {
    throw new Error('No log captured for this run')
  }

  const disposition = res.headers.get('Content-Disposition') || ''
  const match = disposition.match(/filename="?([^";]+)"?/i)
  const fallback = filename || `run-${runId}.ndjson`
  const name = match ? match[1] : fallback

  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}

export async function subscribeRunEvents(id: string): Promise<EventSource> {
  const token = await getAccessToken()
  const qs = token ? `?token=${encodeURIComponent(token)}` : ''
  return new EventSource(`${BASE}/subscriptions/${id}/run/events${qs}`)
}

// ---------- Data Quality ----------

export async function getQualityIssues(params: Record<string, string> = {}) {
  const qs = new URLSearchParams(params).toString()
  const res = await authFetch(`/quality/issues${qs ? '?' + qs : ''}`)
  return handleResponse<{ issues: any[]; total: number; limit: number; offset: number }>(res)
}

export async function getQualitySummary() {
  const res = await authFetch('/quality/summary')
  return handleResponse<any[]>(res)
}

export async function runQualityCheck(): Promise<void> {
  const res = await authFetch('/quality/run', { method: 'POST' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
}

export async function subscribeQualityCheckEvents(): Promise<EventSource> {
  const token = await getAccessToken()
  const qs = token ? `?token=${encodeURIComponent(token)}` : ''
  return new EventSource(`${BASE}/quality/run/events${qs}`)
}

// ---------- Publications ----------

export async function getPublications() {
  const res = await authFetch('/publications')
  return handleResponse<any[]>(res)
}

export async function createPublication(body: { data_type_key: string }) {
  const res = await authFetch('/publications', {
    method: 'POST',
    body: JSON.stringify(body),
  })
  return handleResponse<any>(res)
}

export async function getPublication(id: string) {
  const res = await authFetch(`/publications/${id}`)
  return handleResponse<any>(res)
}

export async function updatePublication(id: string, body: { sources: any[] }) {
  const res = await authFetch(`/publications/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  })
  return handleResponse<any>(res)
}

export async function deletePublication(id: string) {
  const res = await authFetch(`/publications/${id}`, { method: 'DELETE' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
}

export async function getPublicationCandidates(id: string) {
  const res = await authFetch(`/publications/${id}/candidates`)
  return handleResponse<any[]>(res)
}

export async function getAvailablePublicationTypes() {
  const res = await authFetch('/publications/available-types')
  return handleResponse<any[]>(res)
}
