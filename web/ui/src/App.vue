<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  CvHeader,
  CvHeaderName,
  CvHeaderNav,
  CvHeaderMenuItem,
  CvContent,
  CvSkipToContent,
  CvLoading,
  CvButton,
} from '@carbon/vue'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const router = useRouter()

onMounted(() => {
  auth.init()
})

function navigateTo(path: string) {
  router.push(path)
}
</script>

<template>
  <div>
    <cv-header aria-label="pvdata">
      <cv-skip-to-content href="#main-content" />
      <cv-header-name prefix="pv" to="/" @click.prevent="navigateTo('/')">
        data
      </cv-header-name>
      <cv-header-nav v-if="auth.isAuthenticated" aria-label="main navigation">
        <cv-header-menu-item @click="navigateTo('/')">
          Subscriptions
        </cv-header-menu-item>
        <cv-header-menu-item @click="navigateTo('/sql')">
          SQL Console
        </cv-header-menu-item>
      </cv-header-nav>
      <template v-if="auth.authEnabled && auth.isAuthenticated" #header-global>
        <span class="auth-user">{{ auth.userName }}</span>
        <cv-button kind="ghost" size="sm" @click="auth.logout()">Logout</cv-button>
      </template>
    </cv-header>

    <cv-content id="main-content">
      <div v-if="auth.isLoading" class="loading-container">
        <cv-loading active />
      </div>
      <div v-else-if="!auth.isAuthenticated && auth.authEnabled" class="login-container">
        <h2>Authentication Required</h2>
        <p>Please sign in to access pvdata.</p>
        <cv-button @click="auth.login()">Sign In</cv-button>
      </div>
      <router-view v-else />
    </cv-content>
  </div>
</template>

<style scoped>
.loading-container {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 50vh;
}

.login-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 50vh;
  gap: 1rem;
}

.auth-user {
  display: flex;
  align-items: center;
  padding: 0 1rem;
  color: var(--cds-text-secondary, #c6c6c6);
  font-size: 0.875rem;
}
</style>
