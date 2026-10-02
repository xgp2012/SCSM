import { Controller, Get, NotFoundException, Param } from '@nestjs/common';
import type { ConsoleHistoryEvent } from '@sc-panel/shared';
import { ConsoleService } from './console.service';

@Controller('instances')
export class ConsoleController {
  constructor(private readonly console: ConsoleService) {}

  /** 终端回滚快照（供非 WS 回退或首屏） */
  @Get(':id/history')
  history(@Param('id') id: string): { ok: true; data: ConsoleHistoryEvent } {
    if (!this.console.getRuntime(id)) {
      throw new NotFoundException(`实例不存在: ${id}`);
    }
    const buffer = this.console.getBuffer(id);
    return {
      ok: true,
      data: {
        type: 'console.history',
        instanceId: id,
        lines: buffer.getLines(),
        ansi: buffer.getAnsiSnapshot(),
      },
    };
  }
}
