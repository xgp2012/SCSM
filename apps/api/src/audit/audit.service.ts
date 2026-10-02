import { Inject, Injectable, Logger } from '@nestjs/common';
import { appendFile, mkdir, readFile, readdir } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { join } from 'node:path';
import type { AuditInput, AuditQuery, AuditQueryResult, AuditRecord } from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { toAbsolute } from '../common/utils/paths';

/**
 * 审计日志（plants.md §9 / §10 M7）。
 *
 * - 存储：`<PANEL_DATA_DIR>/audit/YYYY-MM-DD.jsonl`，每行一个 `AuditRecord`；
 * - 按天轮转：写入时按记录时间落到当天文件，天然分片；
 * - 追加写 + 静默降级：审计写入失败仅记 warnings，不阻断业务主流程。
 */
@Injectable()
export class AuditService {
  private readonly logger = new Logger('Audit');
  private readonly root: string;
  private ready?: Promise<void>;

  constructor(@Inject(PANEL_ENV) env: PanelEnv) {
    this.root = join(toAbsolute(env.PANEL_DATA_DIR), 'audit');
  }

  /** 记录一条审计（非阻塞，失败不抛出） */
  async log(input: AuditInput): Promise<AuditRecord> {
    const record: AuditRecord = {
      id: randomUUID(),
      ts: new Date().toISOString(),
      action: input.action,
      outcome: input.outcome ?? 'ok',
      actor: input.actor,
      instanceId: input.instanceId,
      instanceName: input.instanceName,
      detail: input.detail,
      meta: input.meta,
      ip: input.ip,
    };
    try {
      await this.ensure();
      const file = join(this.root, `${this.dayOf(record.ts)}.jsonl`);
      await appendFile(file, `${JSON.stringify(record)}\n`, 'utf8');
    } catch (err) {
      this.logger.warn(`审计写入失败: ${String(err)}`);
    }
    return record;
  }

  /** 查询审计（倒序，最新在前） */
  async query(filter: AuditQuery = {}): Promise<AuditQueryResult> {
    await this.ensure();
    const days = await this.listDays();
    const inRange = days.filter((d) => {
      if (filter.from && d < filter.from) return false;
      if (filter.to && d > filter.to) return false;
      return true;
    });

    const records: AuditRecord[] = [];
    for (const day of inRange) {
      const lines = (await readFile(join(this.root, `${day}.jsonl`), 'utf8'))
        .split('\n')
        .filter(Boolean);
      for (const line of lines) {
        try {
          records.push(JSON.parse(line) as AuditRecord);
        } catch {
          // 跳过损坏行
        }
      }
    }

    const filtered = records.filter((r) => this.matches(r, filter));
    filtered.sort((a, b) => b.ts.localeCompare(a.ts));
    const limit = filter.limit && filter.limit > 0 ? Math.min(filter.limit, 2000) : 500;
    return {
      records: filtered.slice(0, limit),
      total: filtered.length,
      days: inRange,
    };
  }

  /** 可用的日期文件（升序） */
  async listDays(): Promise<string[]> {
    try {
      const entries = await readdir(this.root);
      return entries
        .filter((n) => /^\d{4}-\d{2}-\d{2}\.jsonl$/.test(n))
        .map((n) => n.replace(/\.jsonl$/, ''))
        .sort();
    } catch {
      return [];
    }
  }

  private matches(r: AuditRecord, f: AuditQuery): boolean {
    if (f.action && r.action !== f.action) return false;
    if (f.instanceId && r.instanceId !== f.instanceId) return false;
    if (f.outcome && r.outcome !== f.outcome) return false;
    if (f.q) {
      const needle = f.q.toLowerCase();
      const hay = `${r.detail ?? ''} ${r.actor ?? ''} ${r.instanceName ?? ''} ${JSON.stringify(r.meta ?? {})}`;
      if (!hay.toLowerCase().includes(needle)) return false;
    }
    return true;
  }

  private dayOf(iso: string): string {
    return iso.slice(0, 10);
  }

  private ensure(): Promise<void> {
    this.ready ??= mkdir(this.root, { recursive: true }).then(() => undefined);
    return this.ready;
  }
}
