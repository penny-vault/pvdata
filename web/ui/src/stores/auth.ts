import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getUserManager } from '@/lib/oidc'

export const useAuthStore = defineStore('auth', () => {
  const isAuthenticated = ref(false)
  const isLoading = ref(true)
  const userName = ref('')
  const authEnabled = ref(false)

  async function init() {
    isLoading.value = true

    const mgr = await getUserManager()
    authEnabled.value = mgr !== null

    if (!mgr) {
      // Auth not configured -- allow access without login
      isLoading.value = false
      isAuthenticated.value = true
      return
    }

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
    const mgr = await getUserManager()
    if (!mgr) return
    await mgr.signinRedirect()
  }

  async function logout() {
    const mgr = await getUserManager()
    if (!mgr) return
    await mgr.signoutRedirect()
  }

  return { isAuthenticated, isLoading, userName, authEnabled, init, login, logout }
})
