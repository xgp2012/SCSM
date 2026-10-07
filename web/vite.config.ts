import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// scnetm panel — frontend build (T7)
// Built artifacts land in web/dist and are embedded by internal/webui/embed.go.

// Where `pnpm dev` proxies API and WebSocket calls. It must match the panel's
// `listen` address, whose default is 0.0.0.0:7000.
//
// This used to be hard-coded to 8080, which is both the panel's *former*
// default and — on the reference host — a port occupied by an unrelated
// application that answers with HTML. A stale proxy target therefore looks
// like a working setup while every API call returns a web page.
//
// Override when the panel runs elsewhere:
//   SCNETM_API=http://127.0.0.1:9000 pnpm dev
const DEV_API_TARGET = process.env.SCNETM_API ?? 'http://127.0.0.1:7000'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      output: {
        manualChunks: {
          xterm: ['@xterm/xterm', '@xterm/addon-fit', '@xterm/addon-web-links'],
          charts: ['uplot'],
        },
      },
    },
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': {
        target: DEV_API_TARGET,
        changeOrigin: false,
      },
      '/ws': {
        target: DEV_API_TARGET,
        ws: true,
        changeOrigin: false,
      },
    },
  },
})
