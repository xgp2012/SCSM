import { onBeforeUnmount, ref } from 'vue';
import type { SystemStats } from '@sc-panel/shared';
import { api } from './useApi';

/**
 * 面板主机系统指标轮询（plants.md §5.7）。
 * 概览页无需绑定具体实例，故用 REST 轮询；实例详情页则经 WS `system.stats` 实时推送。
 */
export function useSystemStats(intervalMs = 3000) {
  const stats = ref<SystemStats | null>(null);
  const error = ref<string | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;

  async function fetchOnce(): Promise<void> {
    try {
      stats.value = await api.get<SystemStats>('/system/stats');
      error.value = null;
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err);
    }
  }

  function start(): void {
    void fetchOnce();
    timer = setInterval(() => void fetchOnce(), intervalMs);
  }

  function stop(): void {
    if (timer) clearInterval(timer);
    timer = null;
  }

  onBeforeUnmount(stop);

  return { stats, error, fetchOnce, start, stop };
}
