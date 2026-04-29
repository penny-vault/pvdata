<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import Menubar from 'primevue/menubar'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'
import { useAuthStore } from '@/stores/auth'
import { loadConfig } from '@/lib/config'

const auth = useAuthStore()
const router = useRouter()

const menuItems = [
  { label: 'Subscriptions', icon: 'pi pi-database', command: () => router.push('/') },
  { label: 'SQL Console', icon: 'pi pi-code', command: () => router.push('/sql') },
  { label: 'Publications', icon: 'pi pi-book', command: () => router.push('/publications') },
  { label: 'Data Quality', icon: 'pi pi-check-circle', command: () => router.push('/data-quality') },
]

const version = ref('')
const commit = ref('')
const buildDate = ref('')

const versionTitle = computed(() => {
  const parts: string[] = []
  if (commit.value) parts.push(`commit ${commit.value}`)
  if (buildDate.value) parts.push(`built ${buildDate.value}`)
  return parts.join(' · ')
})

onMounted(async () => {
  auth.init()
  const cfg = await loadConfig()
  version.value = cfg.version
  commit.value = cfg.commit
  buildDate.value = cfg.build_date
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

    <footer v-if="version || buildDate" class="app-footer">
      <div class="footer-inner">
        <span v-if="version" class="footer-version" :title="versionTitle">pvdata {{ version }}</span>
        <span v-if="buildDate" class="footer-build">built {{ buildDate }}</span>
      </div>
    </footer>
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

.app-footer {
  border-top: 1px solid rgba(255, 255, 255, 0.06);
  margin-top: 2rem;
}

.footer-inner {
  max-width: 1400px;
  margin: 0 auto;
  padding: 0.75rem 2rem;
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  font-size: 0.75rem;
  color: rgba(255, 255, 255, 0.4);
}

.footer-version {
  cursor: help;
}
</style>
