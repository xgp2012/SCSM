/* eslint-disable no-console */
// M3 门禁：UDP ServerInfo 探测 + 系统指标（plants.md §9.5.2 M3）
// 对运行中的真实服务端发单播探测，并校验 REST/WS 指标推送。
const dgram = require('node:dgram');
const zlib = require('node:zlib');
const WebSocket = require('ws');

const API = 'http://127.0.0.1:3001/api';
const WS_BASE = 'ws://127.0.0.1:3001/ws/console';
const USER = 'admin';
const PASS = 'change-me';

const PROBE_NET_PROPERTY = 0x09;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function req(method, url, { body, cookie, isForm } = {}) {
  const headers = {};
  if (cookie) headers['cookie'] = cookie;
  let payload;
  if (body !== undefined) {
    if (isForm) payload = body;
    else {
      headers['content-type'] = 'application/json';
      payload = JSON.stringify(body);
    }
  }
  const res = await fetch(`${API}${url}`, { method, headers, body: payload });
  const text = await res.text();
  let json;
  try { json = text ? JSON.parse(text) : undefined; } catch { json = text; }
  return { status: res.status, json, headers: res.headers };
}

// 独立实现探测（不依赖后端代码），验证协议可被第三方独立复现
function buildProbeRequest() {
  const inner = Buffer.from([0x88, 0x00, 0x01]);
  return Buffer.concat([Buffer.from([PROBE_NET_PROPERTY]), zlib.deflateRawSync(inner)]);
}
function probeRaw(port, host = '127.0.0.1', timeoutMs = 2500) {
  return new Promise((resolve) => {
    const sock = dgram.createSocket({ type: 'udp4', reuseAddr: true });
    let done = false;
    const finish = (v) => { if (done) return; done = true; try { sock.close(); } catch {} resolve(v); };
    const started = Date.now();
    sock.bind(() => {
      const timer = setTimeout(() => finish({ online: false, error: 'timeout' }), timeoutMs);
      sock.on('message', (msg) => {
        clearTimeout(timer);
        try {
          const inner = zlib.inflateRawSync(msg.subarray(1));
          let o = 0;
          o++; // marker 0x88
          o++; // id
          const req_ = inner[o++];
          const vlen = inner[o++];
          const version = inner.subarray(o, o + vlen).toString('utf8'); o += vlen;
          const playerCount = inner.readUInt16LE(o); o += 2;
          const maxCount = inner.readUInt16LE(o); o += 2;
          const gameModeRaw = inner[o++];
          const needLogin = inner[o++] === 1;
          const needPassword = inner[o++] === 1;
          const timeOfDay = inner.readFloatLE(o); o += 4;
          finish({ online: true, version, playerCount, maxCount, gameModeRaw, needLogin, needPassword, timeOfDay, pingMs: Date.now() - started });
        } catch (e) {
          finish({ online: false, error: 'decode: ' + e.message });
        }
      });
      sock.on('error', (e) => { clearTimeout(timer); finish({ online: false, error: e.message }); });
      sock.send(buildProbeRequest(), port, host, (err) => { if (err) { clearTimeout(timer); finish({ online: false, error: err.message }); } });
    });
  });
}

function openWs(instanceId, cookie) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${WS_BASE}?instanceId=${instanceId}`, { headers: { cookie } });
    ws.frames = [];
    ws.on('message', (d) => { try { ws.frames.push(JSON.parse(d.toString())); } catch {} });
    ws.on('open', () => resolve(ws));
    ws.on('error', reject);
  });
}

async function main() {
  const results = [];
  const check = (name, ok, detail) => {
    results.push({ name, ok, detail });
    console.log(`${ok ? 'PASS' : 'FAIL'} | ${name}${detail ? ` | ${detail}` : ''}`);
  };

  // 1. 登录
  const login = await req('POST', '/auth/login', { body: { username: USER, password: PASS } });
  const cookie = (login.headers.get('set-cookie') || '').split(';')[0];
  check('登录获取 Cookie', login.status === 201 && cookie.startsWith('sc_panel_token='), `status=${login.status}`);

  // 2. 系统指标快照（含真实采样）
  const sys1 = await req('GET', '/system/stats', { cookie });
  const s1 = sys1.json?.data;
  check('GET /system/stats 返回真实主机指标', sys1.status === 200 && s1 && typeof s1.cpu === 'number' && s1.memTotalMB > 0 && s1.cpuCount > 0, JSON.stringify(s1 && { cpu: s1.cpu, memPercent: s1.memPercent, memTotalMB: s1.memTotalMB, diskPercent: s1.diskPercent, cpuCount: s1.cpuCount }));

  // 3. 创建并启动实例
  const created = await req('POST', '/instances', {
    cookie,
    body: { name: `m3-gate-${Date.now()}`, source: 'template', autoRestart: false },
  });
  const inst = created.json.data;
  check('创建模板实例', created.status === 201 && !!inst?.id, `id=${inst?.id} port=${inst?.serverPort}`);
  await req('POST', `/instances/${inst.id}/start`, { cookie });

  // 4. 等待 running
  let running = false;
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    const g = await req('GET', `/instances/${inst.id}`, { cookie });
    if (g.json?.data?.runtime?.status === 'running') { running = true; break; }
    await sleep(1500);
  }
  check('实例进入 running', running, `serverPort=${inst.serverPort}`);
  if (!running) { console.log('实例未运行，终止'); process.exit(1); }

  // 5. 独立 UDP 探测（第三方脚本，直接向 ServerPort 单播）
  let probe = null;
  for (let i = 0; i < 6; i++) {
    probe = await probeRaw(inst.serverPort);
    if (probe.online) break;
    await sleep(1200);
  }
  check(
    '独立 UDP 探测返回 version/人数/上限/模式/密码',
    probe?.online && !!probe.version && typeof probe.playerCount === 'number' && typeof probe.maxCount === 'number' && probe.maxCount > 0 && typeof probe.needPassword === 'boolean',
    probe ? `version=${probe.version} players=${probe.playerCount}/${probe.maxCount} gameMode=${probe.gameModeRaw} needPass=${probe.needPassword} ping=${probe.pingMs}ms` : 'no reply',
  );
  check('探测 maxCount 与实例配置一致', probe?.online && probe.maxCount === inst.maxPlayers, `probe=${probe?.maxCount} cfg=${inst.maxPlayers}`);

  // 6. REST 即时探测端点
  const pRest = await req('GET', `/instances/${inst.id}/probe`, { cookie });
  check('GET /instances/:id/probe 与独立探测一致', pRest.status === 200 && pRest.json?.data?.online === true && pRest.json?.data?.version === probe.version, JSON.stringify(pRest.json?.data && { online: pRest.json.data.online, version: pRest.json.data.version, playerCount: pRest.json.data.playerCount }));

  // 7. WS 订阅：应收到 player.list 与 instance.stats
  const ws = await openWs(inst.id, cookie);
  const wsDeadline = Date.now() + 12_000;
  let gotPlayerList = false;
  let gotStats = false;
  while (Date.now() < wsDeadline && !(gotPlayerList && gotStats)) {
    gotPlayerList = gotPlayerList || ws.frames.some((f) => f.type === 'player.list' && f.probeOnline === true);
    gotStats = gotStats || ws.frames.some((f) => f.type === 'instance.stats' && typeof f.cpu === 'number');
    await sleep(500);
  }
  const plFrame = ws.frames.find((f) => f.type === 'player.list' && f.probeOnline === true);
  const stFrame = ws.frames.find((f) => f.type === 'instance.stats');
  check('WS 收到 player.list（探测在线）', gotPlayerList, plFrame ? `online=${plFrame.online}/${plFrame.maxOnline} mode=${plFrame.gameMode} version=${plFrame.version}` : 'none');
  check('WS 收到 instance.stats（进程指标）', gotStats, stFrame ? `cpu=${stFrame.cpu}% mem=${stFrame.memMB}MB` : 'none');
  const gotSystem = ws.frames.some((f) => f.type === 'system.stats');
  ws.close();

  // 8. 实例运行时聚合了探测与指标
  const after = await req('GET', `/instances/${inst.id}`, { cookie });
  const rt = after.json?.data?.runtime;
  check('runtime 聚合 player/stats 字段', rt?.probeOnline === true && typeof rt?.online === 'number' && typeof rt?.memMB === 'number', JSON.stringify(rt && { online: rt.online, maxOnline: rt.maxOnline, gameMode: rt.gameMode, cpu: rt.cpu, memMB: rt.memMB, probeOnline: rt.probeOnline }));

  // 9. 系统指标随采样变化（间隔取两次）
  await sleep(3500);
  const sys2 = await req('GET', '/system/stats', { cookie });
  const s2 = sys2.json?.data;
  check('系统指标可持续采样', sys2.status === 200 && s2 && s2.ts !== s1.ts, `ts1=${s1?.ts} ts2=${s2?.ts} cpu2=${s2?.cpu} mem2=${s2?.memPercent}`);

  // 10. 停止并清理
  await req('POST', `/instances/${inst.id}/stop`, { cookie });
  await sleep(3000);
  const stopped = await req('GET', `/instances/${inst.id}`, { cookie });
  check('停止后清理运行时指标', stopped.json?.data?.runtime?.probeOnline === false && stopped.json?.data?.runtime?.memMB === undefined, JSON.stringify(stopped.json?.data?.runtime && { status: stopped.json.data.runtime.status, probeOnline: stopped.json.data.runtime.probeOnline, memMB: stopped.json.data.runtime.memMB }));
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
