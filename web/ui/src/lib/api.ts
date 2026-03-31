import { getAccessToken } from './oidc'

const BASE = '/api/v1'

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
  return res.json()
}

export async function getSubscription(id: string) {
  const res = await authFetch(`/subscriptions/${id}`)
  return res.json()
}

export async function createSubscription(body: Record<string, unknown>) {
  const res = await authFetch('/subscriptions', {
    method: 'POST',
    body: JSON.stringify(body),
  })
  return res.json()
}

export async function updateSubscription(id: string, body: Record<string, unknown>) {
  const res = await authFetch(`/subscriptions/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  })
  return res.json()
}

export async function deleteSubscription(id: string) {
  return authFetch(`/subscriptions/${id}`, { method: 'DELETE' })
}

export async function activateSubscription(id: string) {
  const res = await authFetch(`/subscriptions/${id}/activate`, { method: 'POST' })
  return res.json()
}

export async function deactivateSubscription(id: string) {
  const res = await authFetch(`/subscriptions/${id}/deactivate`, { method: 'POST' })
  return res.json()
}

// ---------- Providers ----------

export async function getProviders() {
  const res = await authFetch('/providers')
  return res.json()
}

// ---------- Run History ----------

export async function getRunHistory(subscriptionId: string) {
  const res = await authFetch(`/subscriptions/${subscriptionId}/runs`)
  return res.json()
}

export async function getSparkline(subscriptionId: string) {
  const res = await authFetch(`/subscriptions/${subscriptionId}/runs/sparkline`)
  return res.json()
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
  return res.json()
}

// ---------- SQL ----------

export async function executeSQL(sql: string) {
  const res = await authFetch('/sql', {
    method: 'POST',
    body: JSON.stringify({ query: sql }),
  })
  return res.json()
}

export async function exportSQL(sql: string, format: 'csv' | 'parquet' = 'csv') {
  const res = await authFetch(`/sql/export?format=${format}`, {
    method: 'POST',
    body: JSON.stringify({ query: sql }),
  })
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `export.${format}`
  a.click()
  URL.revokeObjectURL(url)
}
