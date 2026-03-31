import { UserManager, WebStorageStateStore } from 'oidc-client-ts'

const authDomain = import.meta.env.VITE_AUTH_DOMAIN || ''
const clientId = import.meta.env.VITE_AUTH_CLIENT_ID || ''

const settings = {
  authority: authDomain ? `https://${authDomain}` : '',
  client_id: clientId,
  redirect_uri: `${window.location.origin}/auth/callback`,
  post_logout_redirect_uri: window.location.origin,
  response_type: 'code',
  scope: 'openid profile email',
  userStore: new WebStorageStateStore({ store: window.localStorage }),
}

let userManager: UserManager | null = null

export function getUserManager(): UserManager | null {
  if (!authDomain || !clientId) {
    return null
  }
  if (!userManager) {
    userManager = new UserManager(settings)
  }
  return userManager
}

export async function getAccessToken(): Promise<string | null> {
  const mgr = getUserManager()
  if (!mgr) return null

  const user = await mgr.getUser()
  if (!user || user.expired) return null

  return user.access_token
}
