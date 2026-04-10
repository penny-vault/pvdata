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
    path: '/data-quality',
    name: 'data-quality',
    component: () => import('@/pages/DataQualityPage.vue'),
  },
  {
    path: '/publications',
    name: 'publications',
    component: () => import('@/pages/PublicationsPage.vue'),
  },
  {
    path: '/publications/new',
    name: 'new-publication',
    component: () => import('@/pages/NewPublicationPage.vue'),
  },
  {
    path: '/publications/:id',
    name: 'publication-detail',
    component: () => import('@/pages/PublicationDetailPage.vue'),
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
