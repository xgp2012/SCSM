import { defineConfig, devices } from '@playwright/test';

/**
 * M7 E2E 配置（plants.md §9.5.1 / §9.5.2 M7）。
 *
 * 关键路径用**真实服务端进程**跑通：登录 → 创建实例 → 启动（真实 dotnet）→
 * 终端 `/help` 回执 → 备份。前后端由 Playwright 自动拉起（复用已构建产物）。
 *
 * 前置：`pnpm build`（shared → api → web）；`.env` 指向真实服务端模板目录。
 */
export default defineConfig({
  testDir: './e2e',
  timeout: 180_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'e2e-report' }]],
  use: {
    baseURL: 'http://127.0.0.1:3000',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: [
    {
      command: 'node dist/main.js',
      cwd: '../api',
      url: 'http://127.0.0.1:3001/api/health',
      reuseExistingServer: true,
      timeout: 120_000,
      env: {
        PANEL_HOST: '127.0.0.1',
        PANEL_PORT: '3001',
        PANEL_USER: 'admin',
        PANEL_PASSWORD: 'change-me',
        JWT_SECRET: 'e2e-secret',
        NODE_ENV: 'production',
        LOG_PRETTY: 'false',
      },
    },
    {
      command: 'npx nuxt preview --port 3000',
      cwd: '.',
      url: 'http://127.0.0.1:3000',
      reuseExistingServer: true,
      timeout: 120_000,
      env: {
        NUXT_PUBLIC_API_BASE: 'http://127.0.0.1:3001',
      },
    },
  ],
});
