import { CONSOLE_HISTORY_ANSI_BYTES, CONSOLE_HISTORY_LINES } from '@sc-panel/shared';

/**
 * 终端回滚缓冲（plants.md §2.2 / §5.3）。
 *
 * 双通道：
 * - `ansi`：原始字节流（含 ANSI 转义），供新连接时回放一屏颜色正确的终端画面；
 * - `lines`：清洗后的纯文本行，供日志/检索/事件匹配。
 *
 * 两者均为环形缓冲，超出容量即淘汰最旧内容，防止长会话内存膨胀。
 */
export class RollbackBuffer {
  private ansi = '';
  private readonly lines: string[] = [];

  constructor(
    private readonly maxLines: number = CONSOLE_HISTORY_LINES,
    private readonly maxAnsiBytes: number = CONSOLE_HISTORY_ANSI_BYTES,
  ) {}

  appendRaw(data: string): void {
    if (!data) return;
    this.ansi += data;
    if (this.ansi.length > this.maxAnsiBytes) {
      // 截尾保留最近内容。可能切断转义序列，但回放前会补一次重置，
      // 且紧接其后是完整新数据，xterm 会自行纠正。
      this.ansi = this.ansi.slice(this.ansi.length - this.maxAnsiBytes);
    }
  }

  appendLine(line: string): void {
    this.lines.push(line);
    if (this.lines.length > this.maxLines) {
      this.lines.splice(0, this.lines.length - this.maxLines);
    }
  }

  /** 历史纯文本行（由旧到新） */
  getLines(): string[] {
    return [...this.lines];
  }

  /** 历史 ANSI 快照（带一次 RESET 前缀，避免脏属性） */
  getAnsiSnapshot(): string {
    return this.ansi ? `\u001B[0m${this.ansi}` : '';
  }

  clear(): void {
    this.ansi = '';
    this.lines.length = 0;
  }

  get size(): { ansiBytes: number; lines: number } {
    return { ansiBytes: this.ansi.length, lines: this.lines.length };
  }
}
