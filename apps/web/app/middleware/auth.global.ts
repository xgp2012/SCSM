import { useAuthStore } from '~/stores/instances';

/** 全局路由守卫：未登录跳转登录页（SPA 模式） */
export default defineNuxtRouteMiddleware(async (to) => {
  if (to.path === '/login') return;

  const auth = useAuthStore();
  if (!auth.loaded) {
    await auth.fetchMe();
  }
  if (!auth.isAuthenticated) {
    return navigateTo(`/login?redirect=${encodeURIComponent(to.fullPath)}`);
  }
});
