export interface PublicConfig {
  auth_issuer: string
  auth_client_id: string
  auth_audience: string
  version: string
  commit: string
  build_date: string
}

const EMPTY: PublicConfig = {
  auth_issuer: '',
  auth_client_id: '',
  auth_audience: '',
  version: '',
  commit: '',
  build_date: '',
}

let configPromise: Promise<PublicConfig> | null = null

export function loadConfig(): Promise<PublicConfig> {
  if (!configPromise) {
    configPromise = fetch('/config.json')
      .then((r) => (r.ok ? r.json() : EMPTY))
      .then((cfg) => ({ ...EMPTY, ...cfg }))
      .catch(() => ({ ...EMPTY }))
  }

  return configPromise
}
