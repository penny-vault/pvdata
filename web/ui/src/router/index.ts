import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/',
    name: 'subscriptions',
    component: () => import('@/pages/SubscriptionsPage.vue'),
  },
  {
    path: '/subscriptions/new',
    name: 'new-subscription',
    component: () => import('@/pages/NewSubscriptionPage.vue'),
  },
  {
    path: '/subscriptions/:id',
    name: 'subscription-detail',
    component: () => import('@/pages/SubscriptionDetailPage.vue'),
  },
  {
    path: '/sql',
    name: 'sql-console',
    component: () => import('@/pages/SqlConsolePage.vue'),
  },
  {
    path: '/auth/callback',
    name: 'auth-callback',
    component: () => import('@/pages/SubscriptionsPage.vue'),
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})
