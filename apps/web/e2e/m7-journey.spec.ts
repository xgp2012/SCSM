import { test, expect, type Page, type APIRequestContext } from '@playwright/test';

/**
 * M7 端到端（plants.md §9.5.1 / §9.5.2 M7）——真实全链路：
 * 登录 → 创建实例 → 真实启服 → 终端 /help 回执 → 停止 → 备份 → 审计可见。
 *
 * 使用真实服务端（模板目录）与真实 node-pty 进程；不做任何 mock。
 */

const USER = 'admin';
const PASS = 'change-me';

test.describe.configure({ mode: 'serial' });

async function loginViaUi(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input[type="text"]', USER);
  await page.fill('input[type="password"]', PASS);
  await page.click('button[type="submit"]');
  await expect(page).toHaveURL(/\/$/);
}

test('关键旅程：登录 → 建实例 → 启服 → 终端 /help → 备份', async ({ page }) => {
  const name = `m7-e2e-${Date.now()}`;

  await loginViaUi(page);

  // 1) 新建实例（template 为默认来源）
  await page.getByRole('button', { name: '新建实例' }).click();
  await page.getByPlaceholder('我的服务器').fill(name);
  await page.getByRole('button', { name: '创建' }).click();

  // 2) 进入实例详情
  const link = page.getByRole('link', { name });
  await expect(link).toBeVisible({ timeout: 30_000 });
  await link.click();
  await expect(page).toHaveURL(/\/instances\//);
  const instanceId = page.url().split('/instances/')[1];

  // 3) 真实启动
  await page.getByRole('button', { name: '启动' }).click();
  await expect(page.getByText('运行中').first()).toBeVisible({ timeout: 120_000 });

  // 4) 终端：等待真实启动日志并发送 /help
  const mirror = page.getByTestId('console-mirror');
  await expect(mirror).toContainText(/开启服务器成功|Entered screen|服务器/, { timeout: 120_000 });

  const cmdInput = page.getByPlaceholder(/输入命令后回车/);
  await cmdInput.fill('help');
  await page.getByRole('button', { name: '发送' }).click();
  await expect(mirror).toContainText(/可用命令|\/help|命令帮助|第 \d+\/\d+ 页/, { timeout: 60_000 });

  // 5) 停止（优雅）
  await page.getByRole('button', { name: '停止' }).click();
  await expect(page.getByText('已停止').first()).toBeVisible({ timeout: 60_000 });

  // 6) 备份（存档与备份标签）
  await page.getByRole('button', { name: '存档与备份' }).click();
  await page.getByRole('button', { name: '立即备份' }).click();
  // 成功后有提示，且备份列表出现一条记录（含“手动”类型）
  await expect(page.getByText(/归档已写入|备份完成/).first()).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText('手动').first()).toBeVisible({ timeout: 30_000 });

  // 7) 审计可见（新页面）
  await page.goto('/audit');
  await expect(page.getByRole('heading', { name: '审计日志' })).toBeVisible();
  await page.getByPlaceholder('instanceId').fill(instanceId);
  await page.getByRole('button', { name: '查询' }).click();
  await expect(page.locator('table td').filter({ hasText: /instance\.(create|start|stop)|backup\.create/ }).first()).toBeVisible({
    timeout: 30_000,
  });
});

test('限流：连续错误登录触发 429', async ({ request }: { request: APIRequestContext }) => {
  let sawTooMany = false;
  for (let i = 0; i < 8; i++) {
    const res = await request.post('http://127.0.0.1:3001/api/auth/login', {
      data: { username: 'admin', password: `wrong-${i}` },
    });
    if (res.status() === 429) {
      sawTooMany = true;
      break;
    }
  }
  expect(sawTooMany).toBe(true);
});
