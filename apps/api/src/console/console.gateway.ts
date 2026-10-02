import { Logger } from '@nestjs/common';
import {
  WebSocketGateway,
  WebSocketServer,
  type OnGatewayConnection,
  type OnGatewayDisconnect,
} from '@nestjs/websockets';
import type { IncomingMessage } from 'node:http';
import type { Server, WebSocket } from 'ws';
import {
  AUTH_COOKIE_NAME,
  type ConsoleCommandMessage,
  type ConsoleWriteMessage,
  type TerminalResizeMessage,
  type WsClientEvent,
} from '@sc-panel/shared';
import { AuthService } from '../auth/auth.service';
import { AuditService } from '../audit/audit.service';
import { InstanceSupervisor } from '../instance/instance.supervisor';
import { ConsoleService } from './console.service';
import { MetricsService } from '../probe/metrics.service';
import type { PlayerListPayload, InstanceStatsPayload } from '../probe/metrics.service';
import type { SystemStats } from '@sc-panel/shared';

/** 输出合帧节流（plants.md §2.2：~16ms 批量合并，防 WS 洪泛） */
const FLUSH_INTERVAL_MS = 16;
/** 单实例单连接命令队列上限（防刷） */
const MAX_QUEUE = 200;
/** WS 命令限速窗口（M7 §9）：每连接窗口内最多 COMMAND_MAX 条命令/输入 */
const RATE_WINDOW_MS = 10_000;
const COMMAND_MAX = 40;

interface ClientState {
  instanceId: string;
  socket: WebSocket;
  username?: string;
  unsubscribe: () => void;
  queue: string[];
  timer?: NodeJS.Timeout;
  /** 限速窗口：窗口起点与窗口内计数 */
  windowStart: number;
  windowCount: number;
}

/**
 * 终端 WebSocket 网关（plants.md §5.3）。
 *
 * 握手经 HttpOnly Cookie 校验 JWT；每实例多观察者可写，写入命令队列化。
 * 服务端推送 console.raw/line/history/hello、instance.status。
 */
@WebSocketGateway({ path: '/ws/console' })
export class ConsoleGateway implements OnGatewayConnection, OnGatewayDisconnect {
  private readonly logger = new Logger('ConsoleGateway');
  private readonly clients = new Map<WebSocket, ClientState>();

  @WebSocketServer()
  server!: Server;

  constructor(
    private readonly auth: AuthService,
    private readonly supervisor: InstanceSupervisor,
    private readonly console: ConsoleService,
    private readonly metrics: MetricsService,
    private readonly audit: AuditService,
  ) {
    this.metrics.on('instance-stats', (e: InstanceStatsPayload) => {
      this.console.publish(e.instanceId, { type: 'instance.stats', ...e });
    });
    this.metrics.on('player-list', (e: PlayerListPayload) => {
      this.console.publish(e.instanceId, { type: 'player.list', ...e });
    });
    this.metrics.on('system-stats', (e: SystemStats) => {
      this.broadcastAll({ type: 'system.stats', ...e });
    });
  }

  /** 向全部已连接客户端广播（系统级指标） */
  private broadcastAll(event: unknown): void {
    const frame = JSON.stringify(event);
    for (const socket of this.clients.keys()) {
      try {
        socket.send(frame);
      } catch {
        // 单连接异常隔离
      }
    }
  }

  async handleConnection(socket: WebSocket, request: IncomingMessage): Promise<void> {
    const url = new URL(request.url ?? '/', 'http://localhost');
    const instanceId = url.searchParams.get('instanceId') ?? '';

    let token = this.parseCookie(request.headers.cookie, AUTH_COOKIE_NAME);
    const authHeader = request.headers.authorization;
    if (!token && authHeader?.startsWith('Bearer ')) token = authHeader.slice(7);

    let username: string | undefined;
    try {
      const user = await this.auth.verify(token);
      username = user.username;
    } catch {
      this.close(socket, 4401, '未登录');
      return;
    }

    if (!instanceId || !this.console.getRuntime(instanceId)) {
      this.close(socket, 4404, '实例不存在');
      return;
    }

    const state: ClientState = {
      instanceId,
      socket,
      username,
      unsubscribe: () => {},
      queue: [],
      windowStart: Date.now(),
      windowCount: 0,
    };

    state.unsubscribe = this.console.subscribe(instanceId, (event) => {
      socket.send(JSON.stringify(event));
    });
    this.clients.set(socket, state);

    const buffer = this.console.getBuffer(instanceId);
    socket.send(
      JSON.stringify({
        type: 'console.history',
        instanceId,
        lines: buffer.getLines(),
        ansi: buffer.getAnsiSnapshot(),
      }),
    );
    socket.send(
      JSON.stringify({
        type: 'console.hello',
        instanceId,
        runtime: this.console.getRuntime(instanceId),
      }),
    );

    socket.on('message', (raw: Buffer) => this.onMessage(state, raw));
    socket.on('error', () => this.onDisconnect(socket));
    this.logger.log(`终端已连接 instance=${instanceId}`);
  }

  handleDisconnect(socket: WebSocket): void {
    this.onDisconnect(socket);
  }

  private onDisconnect(socket: WebSocket): void {
    const state = this.clients.get(socket);
    if (!state) return;
    state.unsubscribe();
    if (state.timer) clearTimeout(state.timer);
    this.clients.delete(socket);
    this.logger.log(`终端已断开 instance=${state.instanceId}`);
  }

  private onMessage(state: ClientState, raw: Buffer): void {
    let msg: WsClientEvent;
    try {
      msg = JSON.parse(raw.toString('utf8')) as WsClientEvent;
    } catch {
      return;
    }

    switch (msg.type) {
      case 'console.write':
        if (!this.allow(state)) return;
        this.enqueue(state, (msg as ConsoleWriteMessage).data, false);
        break;
      case 'console.command': {
        if (!this.allow(state)) return;
        const command = (msg as ConsoleCommandMessage).command;
        this.enqueue(state, command, true);
        if (command) {
          void this.audit.log({
            action: 'console.command',
            actor: state.username,
            instanceId: state.instanceId,
            detail: `发送命令 /${command.replace(/^\//, '')}`,
            meta: { command },
          });
        }
        break;
      }
      case 'terminal.resize': {
        const m = msg as TerminalResizeMessage;
        this.supervisor.resize(state.instanceId, m.cols, m.rows);
        break;
      }
      case 'ping':
        try {
          state.socket.send(JSON.stringify({ type: 'pong' }));
        } catch {
          // ignore
        }
        break;
    }
  }

  /** WS 写入限速：固定窗口计数，超限丢弃并回错误帧（plants.md §9） */
  private allow(state: ClientState): boolean {
    const now = Date.now();
    if (now - state.windowStart >= RATE_WINDOW_MS) {
      state.windowStart = now;
      state.windowCount = 0;
    }
    state.windowCount += 1;
    if (state.windowCount > COMMAND_MAX) {
      try {
        state.socket.send(
          JSON.stringify({
            type: 'error',
            instanceId: state.instanceId,
            code: 'rate_limited',
            message: '输入过于频繁，请稍后再试',
          }),
        );
      } catch {
        // ignore
      }
      return false;
    }
    return true;
  }

  /**
   * 命令/输入写入：队列化 + 合帧节流（plants.md §2.2）。
   * 命令自动补 `/` 前缀并回车；原始输入原样写入（含 Ctrl+C 等）。
   */
  private enqueue(state: ClientState, payload: string, isCommand: boolean): void {
    if (!payload) return;
    const data = isCommand ? `${payload.startsWith('/') ? payload : `/${payload}`}\r` : payload;
    state.queue.push(data);
    if (state.queue.length > MAX_QUEUE) state.queue.splice(0, state.queue.length - MAX_QUEUE);
    if (!state.timer) {
      state.timer = setTimeout(() => this.flush(state), FLUSH_INTERVAL_MS);
      state.timer.unref?.();
    }
  }

  private flush(state: ClientState): void {
    state.timer = undefined;
    if (state.queue.length === 0) return;
    const data = state.queue.join('');
    state.queue.length = 0;
    this.supervisor.write(state.instanceId, data);
  }

  private parseCookie(header: string | undefined, name: string): string | undefined {
    if (!header) return undefined;
    for (const part of header.split(';')) {
      const [k, ...rest] = part.trim().split('=');
      if (k === name) return decodeURIComponent(rest.join('='));
    }
    return undefined;
  }

  private close(socket: WebSocket, code: number, reason: string): void {
    try {
      socket.close(code, reason);
    } catch {
      // ignore
    }
  }
}
