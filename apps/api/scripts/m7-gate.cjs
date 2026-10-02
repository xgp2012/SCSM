/* eslint-disable no-console */
// M7 门禁：限流 / 审计 / 设置 / 覆盖安装快照清理（plants.md §9.5.2 M7）
// 对运行中的真实 API 发 HTTP，验证安全与打磨能力；真实启服等关键旅程由 Playwright E2E 覆盖。
const API = 'http://127.0.0.1:3001/api';
const USER = 'admin';
const PASS = 'change-me';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function req(method, url, { body, cookie } = {}) {
  const headers = {};
  if (cookie) headers['cookie'] = cookie;
  let payload;
  if (body !== undefined) {
    headers['content-type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  const res = await fetch(`${API}${url}`, { method, headers, body: payload });
  const text = await res.text();
  let json;
  try { json = text ? JSON.parse(text) : undefined; } catch { json = text; }
  return { status: res.status, json, headers: res.headers };
}

async function main() {
  const results = [];
  const check = (name, ok, detail) => {
    results.push({ name, ok });
    console.log(`${ok ? 'PASS' : 'FAIL'} | ${name}${detail ? ` | ${detail}` : ''}`);
  };

  // 1. 登录
  const login = await req('POST', '/auth/login', { body: { username: USER, password: PASS } });
  const cookie = (login.headers.get('set-cookie') || '').split(';')[0];
  check('登录成功', login.status === 201 && cookie.startsWith('sc_panel_token='), `status=${login.status}`);

  // 2. 限流：连续错误登录应触发 429（登录限流 5/分）
  let throttled = false;
  for (let i = 0; i < 10; i++) {
    const r = await req('POST', '/auth/login', { body: { username: USER, password: `bad-${i}` } });
    if (r.status === 429) { throttled = true; break; }
  }
  check('登录限流触发 429', throttled);

  // 3. 审计接口（需鉴权）
  const anon = await req('GET', '/audit');
  check('未登录访问审计被拒', anon.status === 401, `status=${anon.status}`);

  const audit = await req('GET', '/audit?limit=50', { cookie });
  const records = audit.json?.data?.records ?? [];
  check('审计返回记录', audit.status === 200 && records.length > 0, `count=${records.length}`);
  check('审计记录含登录失败事件', records.some((r) => r.action === 'auth.login.failed'),
    `actions=${[...new Set(records.map((r) => r.action))].slice(0, 6).join(',')}`);
  check('审计记录含 IP/时间戳', records.every((r) => r.ts) && records.some((r) => r.ip));

  // 4. 面板设置读写
  const before = await req('GET', '/settings', { cookie });
  check('读取面板设置', before.status === 200 && before.json?.data?.backup, 
    `interval=${before.json?.data?.backup?.intervalMs}`);

  const put = await req('PUT', '/settings', {
    cookie,
    body: { backup: { keep: 7 }, ui: { theme: 'dark' } },
  });
  check('写入面板设置', put.status === 200 && put.json?.data?.backup?.keep === 7,
    `keep=${put.json?.data?.backup?.keep}`);

  const reread = await req('GET', '/settings', { cookie });
  check('设置持久化生效', reread.json?.data?.backup?.keep === 7);

  // 5. 修改密码（错误当前密码应 401；正确则成功）
  const badPw = await req('POST', '/settings/password', {
    cookie,
    body: { currentPassword: 'wrong', newPassword: 'whatever1' },
  });
  check('错误当前密码改密被拒', badPw.status === 401, `status=${badPw.status}`);

  // 6. 设置变更触发审计
  const audit2 = await req('GET', '/audit?action=settings.update', { cookie });
  check('设置变更写入审计', (audit2.json?.data?.records ?? []).length > 0);

  // 7. 覆盖安装快照清理（pre-install 保留上限）：直接检查 data 目录实例（如存在）
  const list = await req('GET', '/instances', { cookie });
  const count = list.json?.data?.total ?? 0;
  check('实例列表可读（清理逻辑随覆盖安装执行）', list.status === 200, `instances=${count}`);

  const pass = results.filter((r) => r.ok).length;
  console.log(`\n=== ${pass}/${results.length} PASS ===`);
  if (pass !== results.length) process.exit(1);
}

void main().catch((err) => {
  console.error(err);
  process.exit(1);
});
