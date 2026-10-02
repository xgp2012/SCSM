import { createReadStream, type Stats } from 'node:fs';
import { stat } from 'node:fs/promises';
import { join } from 'node:path';

/**
 * 增量 tail 一个文本日志文件（服务端 `Bugs/Game.log`）。
 *
 * 服务端不同 `LogMode` 下 stdout 与文件日志的内容可能不一致，
 * 面板需**同时**消费 PTY stdout 与 Game.log（plants.md §0.2 / §5.9）。
 * 这里按字节偏移增量读取，跨行缓冲，处理文件被重建/截断的情况。
 */
export class FileTailer {
  private readonly path: string;
  private offset = 0;
  private buffer = '';
  private timer?: NodeJS.Timeout;
  private running = false;

  constructor(dir: string, private readonly onLine: (line: string) => void) {
    this.path = join(dir, 'Bugs', 'Game.log');
  }

  start(intervalMs = 500): void {
    if (this.running) return;
    this.running = true;
    // 从当前文件末尾开始（只关心新产生的内容）
    void this.initOffset().then(() => {
      this.timer = setInterval(() => void this.poll(), intervalMs);
      this.timer.unref?.();
    });
  }

  stop(): void {
    this.running = false;
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
  }

  private async initOffset(): Promise<void> {
    try {
      const s = await stat(this.path);
      this.offset = s.size;
    } catch {
      this.offset = 0;
    }
  }

  private async poll(): Promise<void> {
    if (!this.running) return;
    let s: Stats;
    try {
      s = await stat(this.path);
    } catch {
      return; // 文件尚未生成
    }
    if (s.size < this.offset) {
      // 文件被截断/重建
      this.offset = 0;
      this.buffer = '';
    }
    if (s.size === this.offset) return;

    await new Promise<void>((resolve) => {
      const stream = createReadStream(this.path, { start: this.offset, end: s.size - 1, encoding: 'utf8' });
      stream.on('data', (chunk) => {
        this.buffer += chunk;
        let idx: number;
        while ((idx = this.buffer.indexOf('\n')) >= 0) {
          const line = this.buffer.slice(0, idx).replace(/\r$/, '');
          this.buffer = this.buffer.slice(idx + 1);
          if (line.trim().length > 0) this.onLine(line.trim());
        }
      });
      stream.on('end', () => {
        this.offset = s.size;
        resolve();
      });
      stream.on('error', () => resolve());
    });
  }
}
