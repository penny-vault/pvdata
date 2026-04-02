import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getUserManager } from '@/lib/oidc'

export const useAuthStore = defineStore('auth', () => {
  const isAuthenticated = ref(false)
  const isLoading = ref(true)
  const userName = ref('')

  const mgr = getUserManager()

  /** True when OIDC is configured via env vars */
  const authEnabled = mgr !== null

  async function init() {
    if (!mgr) {
      // Auth not configured -- allow access without login
      isLoading.value = false
      isAuthenticated.value = true
      return
    }

    isLoading.value = true

    try {
      // Handle redirect callback
      if (window.location.pathname === '/auth/callback') {
        const user = await mgr.signinRedirectCallback()
        isAuthenticated.value = true
        userName.value = user.profile?.name || user.profile?.preferred_username || ''
        window.history.replaceState({}, '', '/')
      } else {
        // Check for existing session
        const user = await mgr.getUser()
        if (user && !user.expired) {
          isAuthenticated.value = true
          userName.value = user.profile?.name || user.profile?.preferred_username || ''
        }
      }
    } catch (err) {
      console.error('Auth initialization error:', err)
      isAuthenticated.value = false
    } finally {
      isLoading.value = false
    }
  }

  async function login() {
    if (!mgr) return
    await mgr.signinRedirect()
  }

  async function logout() {
    if (!mgr) return
    await mgr.signoutRedirect()
  }

  return { isAuthenticated, isLoading, userName, authEnabled, init, login, logout }
})
