import { Logger } from '@nestjs/common';
import * as pty from 'node-pty';
import type {
  AdapterEventListener,
  AdapterStartOptions,
  IServerAdapter,
} from './server-adapter.interface';
import { LineSplitter } from './terminal-utils';

/**
 * 基于 node-pty 的伪终端适配器（plants.md §1.1 / §2.2，硬性要求）。
 *
 * - 真实 TTY：ANSI、增强控制台热键、行缓冲、Ctrl+C 语义；
 * - raw 通道：原样字节流，供 xterm.js；line 通道：清洗后的纯文本行。
 * - Windows 走 ConPTY（node-pty 内置），Linux 走 pty。
 */
export class PtyAdapter implements IServerAdapter {
  private readonly logger = new Logger('PtyAdapter');
  private term?: pty.IPty;
  private readonly listeners = new Set<AdapterEventListener>();
  private readonly splitter = new LineSplitter();
  private lastPid: number | undefined;

  on(listener: AdapterEventListener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private emit(event: Parameters<AdapterEventListener>[0]): void {
    for (const l of this.listeners) l(event);
  }

  async start(opts: AdapterStartOptions): Promise<{ pid: number }> {
    if (this.term) {
      throw new Error('适配器已在运行');
    }
    this.term = pty.spawn(opts.command, opts.args, {
      name: 'xterm-256color',
      cols: opts.cols ?? 120,
      rows: opts.rows ?? 30,
      cwd: opts.cwd,
      env: { ...process.env, TERM: 'xterm-256color', ...opts.env } as Record<string, string>,
    });
    this.lastPid = this.term.pid;

    this.term.onData((data) => {
      this.emit({ type: 'raw', data });
      for (const line of this.splitter.push(data)) {
        this.emit({ type: 'line', line, ts: Date.now() });
      }
    });

    this.term.onExit(({ exitCode, signal }) => {
      for (const line of this.splitter.flush()) {
        this.emit({ type: 'line', line, ts: Date.now() });
      }
      this.logger.log(`PTY 退出 pid=${this.lastPid} exitCode=${exitCode} signal=${signal ?? 0}`);
      this.term = undefined;
      this.emit({ type: 'exit', info: { exitCode, signal } });
    });

    this.logger.log(`PTY 已启动 pid=${this.term.pid} cmd=${opts.command}`);
    return { pid: this.term.pid };
  }

  /**
   * 优雅停止：优先写 `/stop` 命令，等待退出；超时后 SIGTERM → SIGKILL。
   * 注意：node-pty 在 Windows 无信号语义，此处退回 kill()。
   */
  async stop(opts?: { gracefulMs?: number }): Promise<void> {
    if (!this.term) return;
    const gracefulMs = opts?.gracefulMs ?? 15000;
    this.sendCommand('/stop');

    await new Promise<void>((resolve) => {
      const start = Date.now();
      const timer = setInterval(() => {
        if (!this.term) {
          clearInterval(timer);
          resolve();
          return;
        }
        if (Date.now() - start >= gracefulMs) {
          clearInterval(timer);
          this.logger.warn('优雅停止超时，强制终止 PTY');
          this.kill();
          resolve();
        }
      }, 200);
    });
  }

  kill(): void {
    if (!this.term) return;
    try {
      this.term.kill();
    } catch (err) {
      this.logger.warn(`kill PTY 失败: ${String(err)}`);
    }
  }

  sendCommand(cmd: string): void {
    const normalized = cmd.startsWith('/') ? cmd : `/${cmd}`;
    this.term?.write(`${normalized}\r`);
  }

  write(data: string): void {
    this.term?.write(data);
  }

  resize(cols: number, rows: number): void {
    try {
      this.term?.resize(Math.max(1, cols), Math.max(1, rows));
    } catch (err) {
      this.logger.warn(`resize 失败: ${String(err)}`);
    }
  }

  isAlive(): boolean {
    return this.term !== undefined;
  }

  getPid(): number | undefined {
    return this.lastPid;
  }
}
