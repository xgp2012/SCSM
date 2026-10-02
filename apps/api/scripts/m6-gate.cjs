/* eslint-disable no-console */
// M6 门禁：版本下载与安装（plants.md §9.5.2 M6）
// 对运行中的真实 API 验证：
//  1) 能从 Gitee 拉到 releases 列表（或明确走兜底路径并在界面提示）
//  2) 下载服务端包 → 解压到新实例目录 → 该实例可成功启动
//  3) 手动上传版本包兜底路径可用
//  4) 下载失败/中断时错误可读，且有重试/明确失败
//  附：覆盖既有实例（保留 Configs/Worlds）、下载进度、任务列表
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');

const API = 'http://127.0.0.1:3001/api';
const USER = 'admin';
const PASS = 'change-me';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function req(method, url, { body, cookie, form } = {}) {
  const headers = {};
  if (cookie) headers['cookie'] = cookie;
  let payload;
  if (form) {
    payload = form; // FormData -> fetch 自动设置 multipart 边界
  } else if (body !== undefined) {
    headers['content-type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  // 轮询期间对瞬时网络错误（ECONNRESET 等）做小退避重试
  let lastErr;
  for (let attempt = 0; attempt < 4; attempt++) {
    try {
      const res = await fetch(`${API}${url}`, { method, headers, body: payload });
      const text = await res.text();
      let json;
      try { json = text ? JSON.parse(text) : undefined; } catch { json = text; }
      return { status: res.status, json, setCookie: res.headers.get('set-cookie') };
    } catch (err) {
      lastErr = err;
      await sleep(500 * (attempt + 1));
    }
  }
  throw lastErr;
}

async function waitStatus(cookie, id, status, timeoutMs = 120_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const g = await req('GET', `/instances/${id}`, { cookie });
    const s = g.json?.data?.runtime?.status;
    if (s === status) return g.json.data;
    if (status === 'running' && s === 'crashed') return null;
    await sleep(1500);
  }
  return null;
}

async function waitTask(cookie, id, timeoutMs = 600_000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    const g = await req('GET', `/versions/tasks/${id}`, { cookie });
    last = g.json?.data;
    if (last && (last.phase === 'done' || last.phase === 'failed')) return last;
    await sleep(1000);
  }
  return last;
}

/** 构造一个最小可安装 zip（含 Survivalcraft.dll / Settings.xml / Configs） */
function makeVersionZip() {
  const AdmZip = require('adm-zip');
  const zip = new AdmZip();
  zip.addFile('Survivalcraft.dll', Buffer.from('MZ-M6-GATE-DLL'));
  zip.addFile('Content.scpak', Buffer.from('SCPAK'));
  zip.addFile(
    'Settings.xml',
    Buffer.from('<?xml version="1.0"?><Settings><Setting Name="ServerPort" Value="28887" /></Settings>'),
  );
  zip.addFile(
    'ServerSetting.json',
    Buffer.from('{"WorldPath":"app:/Worlds/World","WorldName":"Gate","WorldMaxPlayers":20,"Autorun":true}'),
  );
  return zip.toBuffer();
}

async function main() {
  const results = [];
  const check = (name, ok, detail) => {
    results.push({ name, ok, detail });
    console.log(`${ok ? 'PASS' : 'FAIL'} | ${name}${detail ? ` | ${detail}` : ''}`);
  };
  const hostPlatform = process.platform === 'win32' ? 'windows' : 'linux';

  // ── 登录 ──────────────────────────────────────────────
  const login = await req('POST', '/auth/login', { body: { username: USER, password: PASS } });
  const cookie = (login.setCookie || '').split(';')[0];
  check('登录获取 Cookie', login.status === 201 && cookie.startsWith('sc_panel_token='), `status=${login.status}`);

  // ── 门禁 1：拉取 releases 列表 ────────────────────────
  // 强制刷新，避免读到旧构建写入的资产排序缓存
  await req('POST', '/versions/releases/refresh', { cookie }).catch(() => {});
  const rel = await req('GET', '/versions/releases', { cookie });
  const data = rel.json?.data;
  const releases = data?.releases ?? [];
  const hasServerAsset = releases.some((r) => (r.assets || []).some((a) => a.isServerPackage));
  check(
    '从 Gitee 拉到 releases 列表（含服务端包）',
    rel.status === 200 && releases.length > 0 && hasServerAsset && data.fallback === false,
    `releases=${releases.length} host=${data?.hostPlatform} fallback=${data?.fallback} notice=${data?.notice ?? '-'}`,
  );

  // 缓存路径
  const relCached = await req('GET', '/versions/releases', { cookie });
  check(
    'releases 支持缓存（cached=true）',
    relCached.status === 200 && relCached.json?.data?.cached === true,
    `cached=${relCached.json?.data?.cached}`,
  );

  // 选一个 release+asset 用于真实下载（优先含托管式 Survivalcraft.dll 的包）
  // 实测 E2E：x26.06.19 含 [服务端]SCNET...z1.zip（内含 Survivalcraft.dll）；
  // x26.07.01.01 含 服务端X26.07.01.01.zip（内含 net10.0/Survivalcraft.dll）。
  const isManaged = (n) => /^服务端/.test(n) || /\[服务端\].*scnet/i.test(n);
  const isVariant = (n) => /pocketsurvival|psterminal/i.test(n);
  let target = null;
  for (const r of releases) {
    const servers = (r.assets || []).filter((a) => a.isServerPackage);
    const a =
      servers.find((x) => isManaged(x.name)) ||
      servers.find((x) => !isVariant(x.name)) ||
      servers[0];
    if (a) { target = { tag: r.tag, asset: a.name }; break; }
  }
  check('挑选可下载的服务端包', !!target, target ? `${target.tag} / ${target.asset}` : 'none');
  if (!target) throw new Error('无可用服务端包，无法继续 M6 下载门禁');

  // ── 门禁 2：下载 → 解压到新实例 → 可启动 ───────────────
  const dl = await req('POST', '/versions/download', {
    cookie,
    body: { tag: target.tag, asset: target.asset, mode: 'new', name: `m6-gate-${Date.now()}` },
  });
  const taskId = dl.json?.data?.id;
  check('创建下载任务并返回 taskId', dl.status === 201 && !!taskId, `taskId=${taskId} phase=${dl.json?.data?.phase}`);

  const task = await waitTask(cookie, taskId);
  check(
    '下载+解压+安装完成',
    task?.phase === 'done' && !!task?.instanceId,
    `phase=${task?.phase} err=${task?.error ?? '-'} instance=${task?.instanceName} bytes=${task?.receivedBytes}`,
  );
  const instId = task?.instanceId;
  if (!instId) throw new Error('下载安装未产出实例，无法继续');

  // 校验实例目录含服务端本体
  const inst = await req('GET', `/instances/${instId}`, { cookie });
  const dir = inst.json?.data?.dir;
  const hasDll = dir ? fs.existsSync(path.join(dir, 'Survivalcraft.dll')) : false;
  check('新实例目录含 Survivalcraft.dll', hasDll, `dir=${dir}`);

  // 真实启动（下载包来自 Gitee，能进入 running 即证明解压安装有效）
  await req('POST', `/instances/${instId}/start`, { cookie });
  const running = await waitStatus(cookie, instId, 'running');
  check(
    '下载安装的新实例可成功启动',
    !!running,
    running ? `status=running port=${running.runtime.serverPort} pid=${running.runtime.pid}` : '未进入 running',
  );
  await req('POST', `/instances/${instId}/stop`, { cookie });
  await waitStatus(cookie, instId, 'stopped', 30_000);

  // ── 门禁 4：下载失败错误可读 ───────────────────────────
  const bad = await req('POST', '/versions/download', {
    cookie,
    body: { tag: target.tag, asset: '不存在的资源.zip', mode: 'new' },
  });
  const badId = bad.json?.data?.id;
  const badTask = await waitTask(cookie, badId, 60_000);
  check(
    '下载失败/中断时错误可读',
    badTask?.phase === 'failed' && typeof badTask?.error === 'string' && badTask.error.length > 0,
    `phase=${badTask?.phase} error=${badTask?.error}`,
  );

  // ── 门禁 3：手动上传版本包兜底 ─────────────────────────
  const zipBuf = makeVersionZip();
  const form = new FormData();
  form.append('file', new Blob([zipBuf], { type: 'application/zip' }), 'manual-vX.zip');
  form.append('mode', 'new');
  form.append('name', `m6-upload-${Date.now()}`);
  const up = await req('POST', '/versions/upload', { cookie, form });
  const upInstId = up.json?.data?.instanceId;
  check(
    '手动上传版本包兜底（新建实例）',
    up.status === 201 && up.json?.data?.task?.phase === 'done' && !!upInstId,
    `instanceId=${upInstId} name=${up.json?.data?.instanceName}`,
  );
  const upInst = await req('GET', `/instances/${upInstId}`, { cookie });
  check(
    '上传包实例目录含 Survivalcraft.dll',
    upInst.json?.data?.dir ? fs.existsSync(path.join(upInst.json.data.dir, 'Survivalcraft.dll')) : false,
    `dir=${upInst.json?.data?.dir}`,
  );

  // ── 附：覆盖既有实例（保留配置/存档）───────────────────
  // 用模板实例做覆盖目标，先写入一个自定义配置，覆盖后应保留
  const seeded = await req('POST', '/instances', {
    cookie,
    body: { name: `m6-overwrite-${Date.now()}`, source: 'template', serverPort: 28970, autoRun: true },
  });
  const owId = seeded.json?.data?.id;
  const owDir = seeded.json?.data?.dir;
  const sentinel = path.join(owDir, 'Configs', '__m6_sentinel.json');
  fs.writeFileSync(sentinel, '{"keep":true}');

  const owForm = new FormData();
  owForm.append('file', new Blob([zipBuf], { type: 'application/zip' }), 'overwrite.zip');
  owForm.append('mode', 'overwrite');
  owForm.append('instanceId', owId);
  owForm.append('preserve', 'true');
  const ow = await req('POST', '/versions/upload', { cookie, form: owForm });
  const sentinelKept = fs.existsSync(sentinel) && fs.readFileSync(sentinel, 'utf8').includes('keep');
  check(
    '覆盖既有实例且保留 Configs（写保护目录）',
    ow.status === 201 && ow.json?.data?.task?.phase === 'done' && sentinelKept,
    `phase=${ow.json?.data?.task?.phase} sentinelKept=${sentinelKept}`,
  );

  // ── 附：任务列表 ──────────────────────────────────────
  const tasks = await req('GET', '/versions/tasks', { cookie });
  check(
    '下载任务列表可见',
    tasks.status === 200 && Array.isArray(tasks.json?.data) && tasks.json.data.length >= 3,
    `count=${tasks.json?.data?.length}`,
  );

  // ── 清理 ──────────────────────────────────────────────
  for (const id of [instId, upInstId, owId]) {
    await req('POST', `/instances/${id}/stop`, { cookie }).catch(() => {});
    await sleep(500);
    await req('DELETE', `/instances/${id}?deleteFiles=true`, { cookie }).catch(() => {});
  }

  const failed = results.filter((r) => !r.ok);
  console.log(`\n=== ${results.length - failed.length}/${results.length} PASS ===`);
  if (failed.length) {
    console.log('FAILED:', failed.map((f) => f.name).join(', '));
    process.exit(1);
  }
  process.exit(0);
}

main().catch((err) => { console.error(err); process.exit(1); });
