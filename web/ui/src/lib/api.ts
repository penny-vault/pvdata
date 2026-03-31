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
