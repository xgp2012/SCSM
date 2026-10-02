/**
 * 将后端 API 基址暴露到 `window.__SC_API_BASE__`，供终端 WebSocket 直连使用。
 *
 * 背景（M7）：生产 SPA 常与后端分端口部署，而 Nitro `routeRules.proxy` 不处理
 * WebSocket 升级；终端因此直连后端 origin（Cookie 按 host 作用域，跨端口仍携带）。
 */
export default defineNuxtPlugin(() => {
  const config = useRuntimeConfig();
  if (import.meta.client && config.public.apiBase) {
    (window as unknown as { __SC_API_BASE__?: string }).__SC_API_BASE__ = config.public
      .apiBase as string;
  }
});
