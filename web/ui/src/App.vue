<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Menubar from 'primevue/menubar'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const router = useRouter()

const menuItems = [
  { label: 'Subscriptions', icon: 'pi pi-database', command: () => router.push('/') },
  { label: 'SQL Console', icon: 'pi pi-code', command: () => router.push('/sql') },
  { label: 'Data Quality', icon: 'pi pi-check-circle', command: () => router.push('/data-quality') },
]

onMounted(() => {
  auth.init()
})
</script>

<template>
  <div class="dark">
    <div class="header-bar">
      <div class="header-inner">
        <Menubar :model="menuItems">
          <template #start>
            <span class="app-logo" @click="router.push('/')">pv<strong>data</strong></span>
          </template>
          <template #end>
            <div v-if="auth.authEnabled && auth.isAuthenticated" style="display: flex; align-items: center; gap: 0.5rem">
              <span>{{ auth.userName }}</span>
              <Button label="Logout" severity="secondary" text size="small" @click="auth.logout()" />
            </div>
          </template>
        </Menubar>
      </div>
    </div>

    <main class="app-content">
      <div v-if="auth.isLoading" style="display: flex; justify-content: center; align-items: center; min-height: 50vh">
        <ProgressSpinner />
      </div>
      <div v-else-if="!auth.isAuthenticated && auth.authEnabled" style="display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 1rem; min-height: 50vh">
        <h2>Authentication Required</h2>
        <p>Please sign in to access pvdata.</p>
        <Button label="Sign In" @click="auth.login()" />
      </div>
      <router-view v-else />
    </main>
  </div>
</template>

<style scoped>
.header-bar {
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}

.header-inner {
  max-width: 1400px;
  margin: 0 auto;
  padding: 0 2rem;
}

:deep(.p-menubar) {
  background: transparent;
  border: none;
  border-radius: 0;
  padding-left: 0;
  padding-right: 0;
}

.app-logo {
  font-size: 1.25rem;
  cursor: pointer;
  margin-right: 1.5rem;
  letter-spacing: -0.02em;
}

.app-logo strong {
  color: var(--p-primary-color);
}

.app-content {
  max-width: 1400px;
  margin: 0 auto;
  padding: 2rem;
}
</style>
