/* eslint-disable no-console */
// M4 门禁：配置管理（plants.md §9.5.2 M4）
// 对运行中的真实 API/服务端验证：
//  1) 面板改 ServerPort/WorldMaxPlayers/Autorun 并落盘，重启后服务端读取新值（探测核实）
//  2) 新增/删除权限、封禁、改密码、调限制并保存，重读确认落盘且未知字段未破坏
//  3) 非法输入被拒绝并有清晰错误
const fs = require('node:fs');
const path = require('node:path');
const dgram = require('node:dgram');
const zlib = require('node:zlib');

const API = 'http://127.0.0.1:3001/api';
const USER = 'admin';
const PASS = 'change-me';
const PROBE_NET_PROPERTY = 0x09;
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

function probeRaw(port, host = '127.0.0.1', timeoutMs = 2500) {
  return new Promise((resolve) => {
    const sock = dgram.createSocket({ type: 'udp4', reuseAddr: true });
    let done = false;
    const finish = (v) => { if (done) return; done = true; try { sock.close(); } catch {} resolve(v); };
    sock.bind(() => {
      const timer = setTimeout(() => finish({ online: false, error: 'timeout' }), timeoutMs);
      sock.on('message', (msg) => {
        clearTimeout(timer);
        try {
          const inner = zlib.inflateRawSync(msg.subarray(1));
          let o = 0; o++; o++;
          o++; // requestInfo
          const vlen = inner[o++];
          const version = inner.subarray(o, o + vlen).toString('utf8'); o += vlen;
          const playerCount = inner.readUInt16LE(o); o += 2;
          const maxCount = inner.readUInt16LE(o); o += 2;
          const gameMode = inner[o++];
          const needLogin = inner[o++] === 1;
          const needPassword = inner[o++] === 1;
          finish({ online: true, version, playerCount, maxCount, gameMode, needLogin, needPassword });
        } catch (e) {
          finish({ online: false, error: 'decode: ' + e.message });
        }
      });
      sock.on('error', (e) => { clearTimeout(timer); finish({ online: false, error: e.message }); });
      sock.send(Buffer.concat([Buffer.from([PROBE_NET_PROPERTY]), zlib.deflateRawSync(Buffer.from([0x88, 0x00, 0x01]))]), port, host, (err) => {
        if (err) { clearTimeout(timer); finish({ online: false, error: err.message }); }
      });
    });
  });
}

async function waitRunning(cookie, id, timeoutMs = 90_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const g = await req('GET', `/instances/${id}`, { cookie });
    if (g.json?.data?.runtime?.status === 'running') return g.json.data;
    await sleep(1500);
  }
  return null;
}

async function waitProbe(port, matcher, tries = 8) {
  for (let i = 0; i < tries; i++) {
    const p = await probeRaw(port);
    if (p.online && (!matcher || matcher(p))) return p;
    await sleep(1200);
  }
  return await probeRaw(port);
}

async function main() {
  const results = [];
  const check = (name, ok, detail) => {
    results.push({ name, ok, detail });
    console.log(`${ok ? 'PASS' : 'FAIL'} | ${name}${detail ? ` | ${detail}` : ''}`);
  };

  // 1. 登录
  const login = await req('POST', '/auth/login', { body: { username: USER, password: PASS } });
  const setCookie = (login.setCookie || '').split(';')[0];
  check('登录获取 Cookie', login.status === 201 && setCookie.startsWith('sc_panel_token='), `status=${login.status}`);

  // 2. 创建实例
  const created = await req('POST', '/instances', {
    cookie: setCookie,
    body: { name: `m4-gate-${Date.now()}`, source: 'template', autoRestart: false, serverPort: 28940, maxPlayers: 20, autoRun: true },
  });
  const inst = created.json.data;
  const dir = inst.dir;
  check('创建模板实例', created.status === 201 && !!inst?.id, `id=${inst?.id} port=${inst.serverPort} dir=${dir}`);

  // 3. 读取配置快照：确认分区齐全、密码脱敏
  const bundle = await req('GET', `/instances/${inst.id}/config`, { cookie: setCookie });
  check(
    'GET /instances/:id/config 返回全部分区',
    bundle.status === 200 && bundle.json?.data?.sections &&
      typeof bundle.json.data.sections.level === 'object' &&
      typeof bundle.json.data.sections.ban === 'object' &&
      bundle.json.data.meta.length === 6,
    `sections=${Object.keys(bundle.json?.data?.sections ?? {}).join(',')}`,
  );

  // 4. 写权限 + 封禁 + 限制 + 密码（文件落盘 + 未知字段保留）
  const limitBefore = JSON.parse(fs.readFileSync(path.join(dir, 'Configs', 'LimitConfig.json'), 'utf8'));
  const banBefore = JSON.parse(fs.readFileSync(path.join(dir, 'Configs', 'BanConfig.json'), 'utf8'));

  await req('PUT', `/instances/${inst.id}/config/level`, { cookie: setCookie, body: { Alice: 100, Bob: 50 } });
  await req('PUT', `/instances/${inst.id}/config/ban`, { cookie: setCookie, body: { ...banBefore, BanUserList: ['Eve'], BanIpList: ['10.0.0.9'] } });
  await req('PUT', `/instances/${inst.id}/config/limit`, { cookie: setCookie, body: { WaterLength: 9, MagmaLength: 6, LimitBlockBreak: false, LimitExplode: true } });
  await req('PUT', `/instances/${inst.id}/config/password`, { cookie: setCookie, body: { IsUse: true, DefaultPassword: 'pw-123', PlayerPassword: { Alice: 'alice-pw' } } });

  const levelFile = JSON.parse(fs.readFileSync(path.join(dir, 'Configs', 'LevelConfig.json'), 'utf8'));
  check('权限条目已落盘', levelFile.Alice === 100 && levelFile.Bob === 50, JSON.stringify(levelFile));

  const banFile = JSON.parse(fs.readFileSync(path.join(dir, 'Configs', 'BanConfig.json'), 'utf8'));
  check(
    '封禁列表已落盘且未知字段未破坏',
    banFile.BanUserList.includes('Eve') && banFile.BanIpList.includes('10.0.0.9') && banFile.Unknown === banBefore.Unknown,
    JSON.stringify(banFile),
  );

  const limitFile = JSON.parse(fs.readFileSync(path.join(dir, 'Configs', 'LimitConfig.json'), 'utf8'));
  check(
    '限制配置已落盘且未知字段未破坏',
    limitFile.WaterLength === 9 && limitFile.LimitBlockBreak === false && limitFile.CustomField === limitBefore.CustomField,
    JSON.stringify(limitFile),
  );

  const pwFile = JSON.parse(fs.readFileSync(path.join(dir, 'Plugins', 'Password.json'), 'utf8'));
  check(
    '玩家密码已落盘',
    pwFile.IsUse === true && pwFile.DefaultPassword === 'pw-123' && pwFile.PlayerPassword.Alice === 'alice-pw',
    JSON.stringify(pwFile),
  );

  // 删除一条权限（置 0 / 移除）
  await req('PUT', `/instances/${inst.id}/config/level`, { cookie: setCookie, body: { Alice: 0 } });
  const levelFile2 = JSON.parse(fs.readFileSync(path.join(dir, 'Configs', 'LevelConfig.json'), 'utf8'));
  check('权限删除生效', levelFile2.Alice === 0 && levelFile2.Bob === 50, JSON.stringify(levelFile2));

  // 5. 非法输入被拒绝
  const bad1 = await req('PUT', `/instances/${inst.id}/config/level`, { cookie: setCookie, body: { X: -1 } });
  const bad2 = await req('PUT', `/instances/${inst.id}/config/ban`, { cookie: setCookie, body: { BanUserList: 'oops' } });
  const bad3 = await req('PUT', `/instances/${inst.id}/config/nope`, { cookie: setCookie, body: { a: 1 } });
  check(
    '非法输入被拒绝且有清晰错误',
    bad1.status === 400 && bad2.status === 400 && bad3.status === 400 &&
      typeof bad1.json?.message === 'string' && bad1.json.message.length > 0,
    `level=${bad1.status} ban=${bad2.status} section=${bad3.status} msg=${bad1.json?.message}`,
  );

  // 6. 改世界参数（ServerSetting）+ 端口，重启后服务端读取新值
  const newPort = 28941;
  const newMax = 33;
  const patch = await req('PATCH', `/instances/${inst.id}`, {
    cookie: setCookie,
    body: { serverPort: newPort, maxPlayers: newMax, autoRun: true, gameMode: 2 },
  });
  check('PATCH 实例设置成功', patch.status === 200, `status=${patch.status}`);

  const xml = fs.readFileSync(path.join(dir, 'Settings.xml'), 'utf8');
  const ss = JSON.parse(fs.readFileSync(path.join(dir, 'ServerSetting.json'), 'utf8'));
  check(
    '配置写入 Settings.xml / ServerSetting.json',
    xml.includes(`Name="ServerPort" Value="${newPort}"`) && ss.WorldMaxPlayers === newMax && ss.Autorun === true && ss.GameMode === 2,
    `xmlPort=${/ServerPort" Value="(\d+)"/.exec(xml)?.[1]} WorldMaxPlayers=${ss.WorldMaxPlayers}`,
  );

  // 7. 启动实例并核实运行时端口 + 探测（服务端经新端口响应 = 读取了新配置）
  await req('POST', `/instances/${inst.id}/start`, { cookie: setCookie });
  const running = await waitRunning(setCookie, inst.id);
  check('实例进入 running（新端口）', running && running.runtime.serverPort === newPort, `runtimePort=${running?.runtime?.serverPort}`);

  const probe = await waitProbe(newPort, (p) => p.online && p.version);
  check(
    '重启后服务端在新端口响应（读取新配置）',
    probe.online && probe.version === 'x26.06.19',
    probe.online ? `port=${newPort} maxCount=${probe.maxCount} version=${probe.version}（maxCount 来自世界数据，见报告说明）` : 'no reply',
  );
  // WorldMaxPlayers 写入 ServerSetting.json 已在上一步文件断言；实际生效上限存于世界数据
  // （Project.json/MessagePack 的 WorldSettings.MaxOnlinePlayerCount），ServerSetting 仅在建图时播种。

  // 8. 运行中禁止改非热更分区
  const runningWrite = await req('PUT', `/instances/${inst.id}/config/level`, { cookie: setCookie, body: { Zzz: 1 } });
  check('运行中拒绝写非热更分区', runningWrite.status === 409, `status=${runningWrite.status} msg=${runningWrite.json?.message}`);

  // 清理
  await req('POST', `/instances/${inst.id}/stop`, { cookie: setCookie });
  await sleep(3000);
  await req('DELETE', `/instances/${inst.id}?deleteFiles=true`, { cookie: setCookie });

  const failed = results.filter((r) => !r.ok);
  console.log(`\n=== ${results.length - failed.length}/${results.length} PASS ===`);
  if (failed.length) {
    console.log('FAILED:', failed.map((f) => f.name).join(', '));
    process.exit(1);
  }
  process.exit(0);
}

main().catch((err) => { console.error(err); process.exit(1); });
