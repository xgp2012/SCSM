import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { getToken, notifyUnauthorized, setUnauthorizedHandler } from '@/api/authState'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/pages/LoginPage.vue'),
    meta: { public: true, title: '登录' },
  },
  {
    path: '/setup',
    name: 'setup',
    component: () => import('@/pages/SetupPage.vue'),
    meta: { public: true, title: '初始化' },
  },
  {
    path: '/',
    component: () => import('@/layouts/AppLayout.vue'),
    children: [
      {
        path: '',
        name: 'overview',
        component: () => import('@/pages/OverviewPage.vue'),
        meta: { title: '总览' },
      },
      {
        path: 'instances/:id',
        name: 'instance-detail',
        component: () => import('@/pages/InstanceDetailPage.vue'),
        props: true,
        meta: { title: '实例详情' },
      },
      {
        path: 'backups',
        name: 'backups',
        component: () => import('@/pages/BackupsPage.vue'),
        meta: { title: '备份中心' },
      },
      {
        path: 'jobs',
        name: 'jobs',
        component: () => import('@/pages/JobsPage.vue'),
        meta: { title: '任务计划' },
      },
      {
        path: 'account',
        name: 'account',
        component: () => import('@/pages/AccountPage.vue'),
        meta: { title: '账户' },
      },
      {
        path: 'audit',
        name: 'audit',
        component: () => import('@/pages/AuditPage.vue'),
        meta: { title: '审计日志' },
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('@/pages/SettingsPage.vue'),
        meta: { title: '系统设置' },
      },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/pages/NotFoundPage.vue'),
    meta: { public: true, title: '页面不存在' },
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

/** Navigation guard: everything except /login and /setup requires a session. */
router.beforeEach((to) => {
  const authenticated = Boolean(getToken())
  const isPublic = to.meta.public === true

  if (!authenticated && !isPublic) {
    return { name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
  }
  if (authenticated && (to.name === 'login' || to.name === 'setup')) {
    return { name: 'overview' }
  }
  return true
})

router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? to.meta.title : ''
  document.title = title ? `${title} · SCNETM` : 'SCNETM · 生存战争2 开服面板'
})

// A 401 from any API call funnels here: drop the session and go to /login.
setUnauthorizedHandler(() => {
  if (router.currentRoute.value.name === 'login') return
  void router.replace({ name: 'login', query: { redirect: router.currentRoute.value.fullPath } })
})

export { notifyUnauthorized }
