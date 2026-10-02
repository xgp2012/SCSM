import { ref, computed } from 'vue';
import type {
  AuditQuery,
  AuditQueryResult,
  ChangePasswordInput,
  PanelSettings,
  UpdatePanelSettingsInput,
} from '@sc-panel/shared';
import { api } from './useApi';

/** 面板设置读写（plants.md §6 `/settings`） */
export function usePanelSettings() {
  const settings = ref<PanelSettings | null>(null);
  const loading = ref(false);
  const saving = ref(false);
  const error = ref('');

  async function load(): Promise<void> {
    loading.value = true;
    error.value = '';
    try {
      settings.value = await api.get<PanelSettings>('/settings');
    } catch (err) {
      error.value = err instanceof Error ? err.message : '加载失败';
    } finally {
      loading.value = false;
    }
  }

  async function save(input: UpdatePanelSettingsInput): Promise<PanelSettings | null> {
    saving.value = true;
    error.value = '';
    try {
      settings.value = await api.put<PanelSettings>('/settings', input);
      return settings.value;
    } catch (err) {
      error.value = err instanceof Error ? err.message : '保存失败';
      return null;
    } finally {
      saving.value = false;
    }
  }

  async function changePassword(input: ChangePasswordInput): Promise<void> {
    await api.post('/settings/password', input);
  }

  return { settings, loading, saving, error, load, save, changePassword };
}

/** 审计日志查询（plants.md §9 / §10 M7） */
export function useAuditLog() {
  const result = ref<AuditQueryResult | null>(null);
  const loading = ref(false);
  const error = ref('');
  const records = computed(() => result.value?.records ?? []);

  async function query(filter: AuditQuery = {}): Promise<void> {
    loading.value = true;
    error.value = '';
    try {
      const params = new URLSearchParams();
      for (const [k, v] of Object.entries(filter)) {
        if (v !== undefined && v !== '' && v !== null) params.set(k, String(v));
      }
      const qs = params.toString();
      result.value = await api.get<AuditQueryResult>(`/audit${qs ? `?${qs}` : ''}`);
    } catch (err) {
      error.value = err instanceof Error ? err.message : '加载失败';
    } finally {
      loading.value = false;
    }
  }

  return { result, records, loading, error, query };
}
