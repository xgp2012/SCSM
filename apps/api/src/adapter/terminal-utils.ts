/**
 * 终端辅助工具：ANSI 清洗、行缓冲拆分。
 *
 * 终端字节流（raw）原样转发给 xterm.js；同时派生纯文本行（line），
 * 用于日志落盘与事件匹配，避免 ANSI 控制串污染（plants.md §2.2）。
 */

// OSC（ESC ] ... BEL | ST）必须先于 CSI 匹配，否则 CSI 分支会先吞掉 "ESC ]"
const OSC_RE = /\u001B\][^\u0007\u001B]*(?:\u0007|\u001B\\)/g;
// CSI / 其余 ESC 序列
const CSI_RE = /\u001B[[\]()#;?]*(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-PR-TZcf-nqry=><]/g;

export function stripAnsi(input: string): string {
  return input.replace(OSC_RE, '').replace(CSI_RE, '');
}

/**
 * 增量行拆分器：把 chunk 流按 \n 拆行，保留跨 chunk 的半行。
 * 遇到 \r（回车覆盖行）时取最后一段，贴近终端最终显示效果。
 */
export class LineSplitter {
  private buffer = '';

  push(chunk: string): string[] {
    this.buffer += chunk;
    const out: string[] = [];
    let idx: number;
    while ((idx = this.buffer.indexOf('\n')) >= 0) {
      const rawLine = this.buffer.slice(0, idx);
      this.buffer = this.buffer.slice(idx + 1);
      const line = this.normalize(rawLine);
      if (line.length > 0) out.push(line);
    }
    return out;
  }

  flush(): string[] {
    if (this.buffer.length === 0) return [];
    const line = this.normalize(this.buffer);
    this.buffer = '';
    return line.length > 0 ? [line] : [];
  }

  private normalize(rawLine: string): string {
    const cleaned = stripAnsi(rawLine);
    // \r 覆盖：取最后一段非空内容
    const parts = cleaned.split('\r');
    const last = parts[parts.length - 1] ?? '';
    return last.replace(/\u0000/g, '').trim();
  }
}
