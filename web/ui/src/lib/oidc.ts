import { UserManager, WebStorageStateStore } from 'oidc-client-ts'
import { loadConfig } from '@/lib/config'

let userManager: UserManager | null = null
let userManagerInitialized = false

export async function getUserManager(): Promise<UserManager | null> {
  if (userManagerInitialized) {
    return userManager
  }

  const cfg = await loadConfig()

  if (!cfg.auth_issuer || !cfg.auth_client_id) {
    userManagerInitialized = true
    return null
  }

  userManager = new UserManager({
    authority: cfg.auth_issuer,
    client_id: cfg.auth_client_id,
    redirect_uri: `${window.location.origin}/auth/callback`,
    post_logout_redirect_uri: window.location.origin,
    response_type: 'code',
    scope: 'openid profile email',
    userStore: new WebStorageStateStore({ store: window.localStorage }),
    extraQueryParams: cfg.auth_audience ? { audience: cfg.auth_audience } : undefined,
  })
  userManagerInitialized = true

  return userManager
}

export async function getAccessToken(): Promise<string | null> {
  const mgr = await getUserManager()
  if (!mgr) return null

  const user = await mgr.getUser()
  if (!user || user.expired) return null

  return user.access_token
}
