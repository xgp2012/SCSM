import tailwindcss from '@tailwindcss/vite';

// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  compatibilityDate: '2025-07-15',

  // SPA 模式（plants.md §2 / §6）
  ssr: false,

  devtools: { enabled: false },

  modules: ['@pinia/nuxt', '@nuxt/eslint'],

  css: ['~/assets/css/main.css'],

  // Tailwind CSS v4（fuxsto-design 依赖）
  vite: {
    // 类型上 @tailwindcss/vite 与 Nuxt 内置 vite 存在重复实例，运行时无碍，此处断言规避。
    plugins: [tailwindcss() as never],
  },

  typescript: {
    strict: true,
    typeCheck: false,
  },

  runtimeConfig: {
    public: {
      apiBase: process.env.NUXT_PUBLIC_API_BASE ?? 'http://127.0.0.1:3001',
    },
  },

  // 开发/生产期把 /api 与 /ws 代理到 NestJS 后端（Nitro routeRules.proxy）
  nitro: {
    routeRules: {
      '/api/**': {
        proxy: `${process.env.NUXT_PUBLIC_API_BASE ?? 'http://127.0.0.1:3001'}/api/**`,
      },
      '/ws/**': {
        proxy: `${process.env.NUXT_PUBLIC_API_BASE ?? 'http://127.0.0.1:3001'}/ws/**`,
      },
    },
  },

  app: {
    head: {
      title: 'SC-Panel',
      htmlAttrs: { lang: 'zh-CN' },
      meta: [{ name: 'viewport', content: 'width=device-width, initial-scale=1' }],
    },
  },
});
