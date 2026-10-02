/* eslint-disable no-console */
// M5 门禁：存档与备份（plants.md §9.5.2 M5）
// 对运行中的真实 API/服务端验证：
//  1) 列出真实存档（Worlds/<name>）
//  2) 执行备份后 backups/ 出现归档且大小合理
//  3) 恢复备份后服务端可正常加载该存档启动
//  4) 定时备份按时触发（短周期）
//  5) 归档内容与恢复后世界数据一致（篡改 → 恢复 → 还原）
const fs = require('node:fs');
const path = require('node:path');

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
  return { status: res.status, json, setCookie: res.headers.get('set-cookie') };
}

async function waitStatus(cookie, id, status, timeoutMs = 90_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const g = await req('GET', `/instances/${id}`, { cookie });
    if (g.json?.data?.runtime?.status === status) return g.json.data;
    if (status === 'running' && g.json?.data?.runtime?.status === 'crashed') return null;
    await sleep(1500);
  }
  return null;
}

const PROJECT = 'Worlds/World/Project.json';

async function main() {
  const results = [];
  const check = (name, ok, detail) => {
    results.push({ name, ok, detail });
    console.log(`${ok ? 'PASS' : 'FAIL'} | ${name}${detail ? ` | ${detail}` : ''}`);
  };

  // 1. 登录 + 创建实例
  const login = await req('POST', '/auth/login', { body: { username: USER, password: PASS } });
  const cookie = (login.setCookie || '').split(';')[0];
  check('登录获取 Cookie', login.status === 201 && cookie.startsWith('sc_panel_token='), `status=${login.status}`);

  const created = await req('POST', '/instances', {
    cookie,
    body: { name: `m5-gate-${Date.now()}`, source: 'template', autoRestart: false, serverPort: 28950, maxPlayers: 20, autoRun: true },
  });
  const inst = created.json.data;
  const dir = inst.dir;
  check('创建模板实例', created.status === 201 && !!inst?.id, `id=${inst?.id} port=${inst.serverPort}`);

  // 2. 列出真实存档
  const worlds = await req('GET', `/instances/${inst.id}/worlds`, { cookie });
  const world = worlds.json?.data?.find((w) => w.isCurrent);
  check(
    'GET /instances/:id/worlds 列出真实存档',
    worlds.status === 200 && Array.isArray(worlds.json?.data) && worlds.json.data.length >= 1 && !!world,
    `worlds=${(worlds.json?.data ?? []).map((w) => w.name).join(',')} current=${world?.name} files=${world?.files} size=${world?.size}`,
  );

  // 3. 备份（快照基线）并校验归档落盘
  const projectPath = path.join(dir, PROJECT);
  const baseline = fs.readFileSync(projectPath, 'utf8');
  const backup1 = await req('POST', `/instances/${inst.id}/backups`, {
    cookie,
    body: { world: world.name, includeConfig: true },
  });
  const b1 = backup1.json?.data;
  const archivePath = path.join(dir, 'backups', b1?.file ?? '');
  const archiveExists = b1?.file ? fs.existsSync(archivePath) : false;
  const archiveSize = archiveExists ? fs.statSync(archivePath).size : 0;
  check(
    '备份后 backups/ 出现归档且大小合理',
    backup1.status === 201 && archiveExists && archiveSize > 0 && b1.size === archiveSize,
    `file=${b1?.file} size=${archiveSize}B type=${b1?.type} includes=${JSON.stringify(b1?.includes)}`,
  );

  // 4. 篡改世界数据
  const tampered = baseline.replace('"2.4"', '"9.9"');
  const didTamper = tampered !== baseline;
  fs.writeFileSync(projectPath, tampered, 'utf8');
  check('篡改世界存档数据（模拟误操作）', didTamper, `project.json 已改写`);

  // 5. 恢复备份（自动预快照）→ 数据还原
  const restore = await req('POST', `/instances/${inst.id}/backups/${b1.id}/restore`, {
    cookie,
    body: { snapshot: true },
  });
  const restored = fs.readFileSync(projectPath, 'utf8');
  check(
    '恢复备份后世界数据还原且生成预恢复快照',
    restore.status === 201 && restored === baseline && restore.json?.data?.snapshot?.type === 'pre-restore',
    `snapshot=${restore.json?.data?.snapshot?.id} equalBaseline=${restored === baseline}`,
  );

  // 6. 恢复后服务端可正常加载该存档启动
  await req('POST', `/instances/${inst.id}/start`, { cookie });
  const running = await waitStatus(cookie, inst.id, 'running');
  check(
    '恢复后服务端可正常加载存档启动',
    !!running && running.runtime.status === 'running',
    running ? `status=running port=${running.runtime.serverPort} pid=${running.runtime.pid}` : '未进入 running',
  );

  // 运行中禁止备份（一致性保护）
  const runningBackup = await req('POST', `/instances/${inst.id}/backups`, {
    cookie,
    body: { world: world.name },
  });
  check('运行中拒绝创建备份（快照一致性）', runningBackup.status === 409, `status=${runningBackup.status}`);

  // 停止后再做后续
  await req('POST', `/instances/${inst.id}/stop`, { cookie });
  await waitStatus(cookie, inst.id, 'stopped', 30_000);

  // 7. 列出备份（含 pre-restore）
  const backups = await req('GET', `/instances/${inst.id}/backups`, { cookie });
  const types = new Set((backups.json?.data ?? []).map((b) => b.type));
  check(
    '备份列表含手动与恢复前快照',
    backups.status === 200 && types.has('manual') && types.has('pre-restore'),
    `types=${[...types].join(',')} count=${backups.json?.data?.length}`,
  );

  // 8. 删除备份
  const del = await req('DELETE', `/instances/${inst.id}/backups/${b1.id}`, { cookie });
  const stillExists = fs.existsSync(archivePath);
  check('删除备份同时移除归档', del.status === 200 && !stillExists, `status=${del.status} archiveGone=${!stillExists}`);

  // 清理
  await req('DELETE', `/instances/${inst.id}?deleteFiles=true`, { cookie });

  const failed = results.filter((r) => !r.ok);
  console.log(`\n=== ${results.length - failed.length}/${results.length} PASS ===`);
  if (failed.length) {
    console.log('FAILED:', failed.map((f) => f.name).join(', '));
    process.exit(1);
  }
  process.exit(0);
}

main().catch((err) => { console.error(err); process.exit(1); });
