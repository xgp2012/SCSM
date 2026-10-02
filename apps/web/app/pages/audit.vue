<template>
  <div class="flex flex-col gap-6">
    <div class="flex flex-wrap items-end justify-between gap-4">
      <div>
        <h2 class="text-xl font-semibold tracking-tight">审计日志</h2>
        <p class="mt-1 text-sm text-zinc-400">
          登录、启停、命令、配置、备份、下载等操作的审计记录（JSONL 按天分文件）。
        </p>
      </div>
      <Button variant="outline" size="sm" :loading="loading" @click="reload">刷新</Button>
    </div>

    <div class="flex flex-wrap items-end gap-3 rounded-lg border border-zinc-800 bg-zinc-900/30 p-4">
      <label class="flex flex-col gap-1">
        <span class="text-xs text-zinc-500">动作</span>
        <select v-model="filters.action" class="rounded-md border border-zinc-700 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-emerald-500">
          <option value="">全部</option>
          <option v-for="a in ACTION_OPTIONS" :key="a" :value="a">{{ a }}</option>
        </select>
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-xs text-zinc-500">结果</span>
        <select v-model="filters.outcome" class="rounded-md border border-zinc-700 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-emerald-500">
          <option value="">全部</option>
          <option value="ok">成功</option>
          <option value="failed">失败</option>
        </select>
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-xs text-zinc-500">实例 ID</span>
        <input v-model="filters.instanceId" type="text" placeholder="instanceId" class="w-48 rounded-md border border-zinc-700 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-emerald-500">
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-xs text-zinc-500">关键词</span>
        <input v-model="filters.q" type="text" placeholder="命令 / 实例名 / 备注" class="w-52 rounded-md border border-zinc-700 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-emerald-500">
      </label>
      <Button variant="primary" size="sm" @click="reload">查询</Button>
      <span v-if="result" class="ml-auto text-xs text-zinc-500">共 {{ result.total }} 条 · {{ result.days.length }} 个文件</span>
    </div>

    <p v-if="loading" class="text-sm text-zinc-500">加载中…</p>
    <p v-else-if="error" class="text-sm text-red-400">{{ error }}</p>
    <div
      v-else-if="records.length === 0"
      class="rounded-lg border border-dashed border-zinc-800 p-10 text-center text-sm text-zinc-500"
    >
      暂无审计记录。
    </div>

    <div v-else class="overflow-x-auto rounded-lg border border-zinc-800">
      <table class="w-full text-left text-sm">
        <thead class="bg-zinc-900/60 text-xs uppercase text-zinc-500">
          <tr>
            <th class="whitespace-nowrap px-4 py-2.5 font-medium">时间</th>
            <th class="whitespace-nowrap px-4 py-2.5 font-medium">动作</th>
            <th class="whitespace-nowrap px-4 py-2.5 font-medium">结果</th>
            <th class="whitespace-nowrap px-4 py-2.5 font-medium">操作者</th>
            <th class="whitespace-nowrap px-4 py-2.5 font-medium">实例</th>
            <th class="px-4 py-2.5 font-medium">详情</th>
            <th class="whitespace-nowrap px-4 py-2.5 font-medium">IP</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-zinc-800/70">
          <tr v-for="r in records" :key="r.id" class="text-zinc-300">
            <td class="whitespace-nowrap px-4 py-2 font-mono text-xs text-zinc-400">{{ fmtTime(r.ts) }}</td>
            <td class="whitespace-nowrap px-4 py-2 font-mono text-xs">{{ r.action }}</td>
            <td class="whitespace-nowrap px-4 py-2">
              <span
                class="rounded px-1.5 py-0.5 text-[11px]"
                :class="r.outcome === 'ok' ? 'bg-emerald-500/15 text-emerald-400' : 'bg-red-500/15 text-red-400'"
              >{{ r.outcome === 'ok' ? '成功' : '失败' }}</span>
            </td>
            <td class="whitespace-nowrap px-4 py-2">{{ r.actor ?? '—' }}</td>
            <td class="whitespace-nowrap px-4 py-2">{{ r.instanceName ?? r.instanceId?.slice(0, 8) ?? '—' }}</td>
            <td class="px-4 py-2 text-zinc-400">{{ r.detail ?? '—' }}</td>
            <td class="whitespace-nowrap px-4 py-2 font-mono text-xs text-zinc-500">{{ r.ip ?? '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive } from 'vue';
import { Button } from 'fuxsto-design';
import type { AuditAction } from '@sc-panel/shared';
import { useAuditLog } from '~/composables/useSettings';

const { result, records, loading, error, query } = useAuditLog();

const ACTION_OPTIONS: AuditAction[] = [
  'auth.login',
  'auth.login.failed',
  'auth.logout',
  'auth.password.change',
  'instance.create',
  'instance.start',
  'instance.stop',
  'instance.restart',
  'instance.kill',
  'instance.remove',
  'instance.config.update',
  'console.command',
  'backup.create',
  'backup.restore',
  'backup.remove',
  'backup.download',
  'version.download',
  'version.install',
  'version.upload',
  'settings.update',
];

const filters = reactive({ action: '', outcome: '', instanceId: '', q: '' });

function reload(): void {
  void query({
    action: filters.action || undefined,
    outcome: (filters.outcome || undefined) as 'ok' | 'failed' | undefined,
    instanceId: filters.instanceId || undefined,
    q: filters.q || undefined,
    limit: 500,
  });
}

function fmtTime(iso: string): string {
  return new Date(iso).toLocaleString();
}

reload();
</script>
