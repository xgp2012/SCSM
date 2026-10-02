/* eslint-disable no-console */
// M2 门禁：真实启服 + 真实 WS 终端交互
const { spawn } = require('node:child_process');
const path = require('node:path');
const WebSocket = require('ws');

const API = 'http://127.0.0.1:3001/api';
const WS_BASE = 'ws://127.0.0.1:3001/ws/console';
const USER = 'admin';
const PASS = 'change-me';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function req(method, url, { body, cookie, isForm } = {}) {
  const headers = {};
  if (cookie) headers['cookie'] = cookie;
  let payload;
  if (body !== undefined) {
    if (isForm) {
      payload = body;
    } else {
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

function openWs(instanceId, cookie) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${WS_BASE}?instanceId=${instanceId}`, {
      headers: { cookie },
    });
    ws.frames = [];
    ws.on('message', (d) => ws.frames.push(JSON.parse(d.toString())));
    ws.on('open', () => resolve(ws));
    ws.on('error', reject);
    ws.on('unexpected-response', (_r, res) => reject(new Error(`WS HTTP ${res.statusCode}`)));
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

  // 2. WS 未鉴权被拒
  const badWs = new WebSocket(`${WS_BASE}?instanceId=none`);
  const badCode = await new Promise((resolve) => {
    badWs.on('close', (code) => resolve(code));
    badWs.on('error', () => resolve('error'));
    setTimeout(() => resolve('timeout'), 3000);
  });
  check('未登录访问 WS 被拒', badCode === 4401, `closeCode=${badCode}`);

  // 3. 创建实例
  const created = await req('POST', '/instances', {
    cookie,
    body: { name: `m2-gate-${Date.now()}`, source: 'template', autoRestart: false },
  });
  const inst = created.json.data;
  check('创建模板实例', created.status === 201 && !!inst?.id, `id=${inst?.id} port=${inst?.serverPort}`);

  // 4. 启动实例
  await req('POST', `/instances/${inst.id}/start`, { cookie });

  // 5. 连接 WS 并等待实例 running + 收到启动日志
  const ws = await openWs(inst.id, cookie);
  check('WS 鉴权连接成功', ws.readyState === WebSocket.OPEN);

  // 等待 history 帧到达
  const histDeadline0 = Date.now() + 5000;
  while (Date.now() < histDeadline0 && !ws.frames.some((f) => f.type === 'console.history')) {
    await sleep(200);
  }
  const gotHistory = ws.frames.some((f) => f.type === 'console.history');
  check('收到 console.history 回放帧', gotHistory);

  // 等待真实启动日志（raw/line）
  let running = false;
  let sawStartLine = false;
  let sawRaw = false;
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    sawRaw = sawRaw || ws.frames.some((f) => f.type === 'console.raw' && f.data.length > 0);
    sawStartLine = sawStartLine || ws.frames.some(
      (f) => f.type === 'console.line' && /开启服务器成功|Entered screen/.test(f.line),
    );
    running = running || ws.frames.some(
      (f) => (f.type === 'instance.status' && f.status === 'running') ||
        (f.type === 'console.hello' && f.runtime?.status === 'running'),
    );
    if (sawStartLine && running && sawRaw) break;
    await sleep(1000);
  }
  check('WS 收到真实服务端启动输出(raw)', sawRaw);
  check('WS 收到启动特征行(line)', sawStartLine);
  check('WS 收到 instance.status=running', running);

  // 诊断：打印最近若干 console.line
  const lines = ws.frames.filter((f) => f.type === 'console.line').map((f) => f.line);
  console.log('--- 最近 console.line (最多 20) ---');
  for (const l of lines.slice(-20)) console.log('  |', l);
  console.log('--- 启动相关行 ---');
  for (const l of lines.filter((x) => /开启|Entered|screen|服务器/.test(x)).slice(-10)) console.log('  *', l);

  // 6. 发送 /help 命令，校验真实回执
  const beforeIdx = ws.frames.length;
  ws.send(JSON.stringify({ type: 'console.command', command: 'help' }));
  let helpEcho = false;
  let helpText = '';
  const helpDeadline = Date.now() + 15_000;
  while (Date.now() < helpDeadline) {
    const newFrames = ws.frames.slice(beforeIdx);
    const textFrames = newFrames.filter(
      (f) => (f.type === 'console.raw' || f.type === 'console.line') &&
        (f.data || f.line || '').length > 0,
    );
    helpEcho = textFrames.length > 0;
    helpText = textFrames.map((f) => f.data || f.line).join('\n');
    // 命令帮助通常包含 "help"/"命令"/"帮助" 等字样
    if (helpEcho && /help|命令|帮助|用法|用法/i.test(helpText)) break;
    await sleep(500);
  }
  check('/help 命令产生终端回显', helpEcho, `回显帧 ${ws.frames.length - beforeIdx} 条`);
  check('/help 回执为命令帮助文本', /help|命令|帮助|用法/i.test(helpText), `回执片段: ${helpText.replace(/\u001b\[[0-9;]*m/g, '').slice(0, 120).trim()}`);

  // 6b. 多观察者一致性：第二个连接应收到历史与后续实时输出
  const ws2 = await openWs(inst.id, cookie);
  await sleep(800);
  const ws2History = ws2.frames.find((f) => f.type === 'console.history');
  ws.send(JSON.stringify({ type: 'console.command', command: 'time' }));
  let ws2Live = 0;
  const liveDeadline = Date.now() + 10_000;
  while (Date.now() < liveDeadline) {
    ws2Live = ws2.frames.filter((f) => f.type === 'console.raw' || f.type === 'console.line').length;
    if (ws2Live > 0) break;
    await sleep(500);
  }
  const hasHistory = !!ws2History && Array.isArray(ws2History.lines) && ws2History.lines.length > 0;
  check('第二个观察者收到历史回放', hasHistory, `history lines=${ws2History?.lines?.length ?? 0}`);
  check('第二个观察者收到实时输出', ws2Live > 0, `ws2 实时帧=${ws2Live}`);
  ws2.close();

  // 7. resize 不报错
  ws.send(JSON.stringify({ type: 'terminal.resize', cols: 100, rows: 40 }));
  await sleep(1000);
  check('terminal.resize 发送无异常', ws.readyState === WebSocket.OPEN);

  // 8. history REST 端点
  const hist = await req('GET', `/instances/${inst.id}/history`, { cookie });
  const histOk = hist.status === 200 && Array.isArray(hist.json.data.lines) && hist.json.data.lines.length > 0;
  check('GET :id/history 返回非空历史', histOk, `lines=${hist.json?.data?.lines?.length}`);

  // 9. 停止实例
  await req('POST', `/instances/${inst.id}/stop`, { cookie });
  await sleep(3000);
  const stopped = ws.frames.some((f) => f.type === 'instance.status' && (f.status === 'stopped' || f.status === 'stopping'));
  check('停止触发状态推送', stopped);

  ws.close();

  // 10. 清理
  await req('DELETE', `/instances/${inst.id}?deleteFiles=true`, { cookie });

  const failed = results.filter((r) => !r.ok);
  console.log(`\n=== ${results.length - failed.length}/${results.length} PASS ===`);
  if (failed.length) {
    console.log('FAILED:', failed.map((f) => f.name).join(', '));
    process.exit(1);
  }
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
