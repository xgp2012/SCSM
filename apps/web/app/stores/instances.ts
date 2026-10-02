import { defineStore } from 'pinia';
import type { AuthUser, Instance, OverviewResponse } from '@sc-panel/shared';
import { api, ApiRequestError } from '~/composables/useApi';

export const useAuthStore = defineStore('auth', {
  state: () => ({
    user: null as AuthUser | null,
    loaded: false,
  }),
  getters: {
    isAuthenticated: (s) => s.user !== null,
  },
  actions: {
    async login(username: string, password: string) {
      const data = await api.post<{ user: AuthUser }>('/auth/login', { username, password });
      this.user = data.user;
      this.loaded = true;
    },
    async fetchMe() {
      try {
        const data = await api.get<{ user: AuthUser }>('/auth/me');
        this.user = data.user;
      } catch (err) {
        if (err instanceof ApiRequestError && err.statusCode === 401) {
          this.user = null;
        } else {
          throw err;
        }
      } finally {
        this.loaded = true;
      }
    },
    async logout() {
      try {
        await api.post('/auth/logout');
      } finally {
        this.user = null;
      }
    },
  },
});

export const useInstancesStore = defineStore('instances', {
  state: () => ({
    instances: [] as Instance[],
    loading: false,
    selectedId: null as string | null,
    _pollTimer: null as ReturnType<typeof setInterval> | null,
  }),
  getters: {
    runningCount: (s) => s.instances.filter((i) => i.runtime.status === 'running').length,
  },
  actions: {
    async fetchAll() {
      this.loading = true;
      try {
        const data = await api.get<OverviewResponse>('/instances');
        this.instances = data.instances;
      } finally {
        this.loading = false;
      }
    },
    async refreshOne(id: string) {
      const inst = await api.get<Instance>(`/instances/${id}`);
      const idx = this.instances.findIndex((i) => i.id === id);
      if (idx >= 0) this.instances[idx] = inst;
      return inst;
    },
    async action(id: string, action: 'start' | 'stop' | 'restart' | 'kill') {
      const inst = await api.post<Instance>(`/instances/${id}/${action}`);
      const idx = this.instances.findIndex((i) => i.id === id);
      if (idx >= 0) this.instances[idx] = inst;
      return inst;
    },
    async create(input: Record<string, unknown>) {
      const inst = await api.post<Instance>('/instances', input);
      this.instances.push(inst);
      return inst;
    },
    async update(id: string, input: Record<string, unknown>) {
      const inst = await api.patch<Instance>(`/instances/${id}`, input);
      const idx = this.instances.findIndex((i) => i.id === id);
      if (idx >= 0) this.instances[idx] = inst;
      return inst;
    },
    async remove(id: string) {
      await api.del(`/instances/${id}`);
      this.instances = this.instances.filter((i) => i.id !== id);
    },
    /** 概览轻量轮询：M3 起指标/探测经 REST 汇总到 /instances（WS 在详情页实时推送） */
    startPolling(intervalMs = 3000) {
      this.stopPolling();
      this._pollTimer = setInterval(() => {
        void this.fetchAll();
      }, intervalMs);
    },
    stopPolling() {
      if (this._pollTimer) clearInterval(this._pollTimer);
      this._pollTimer = null;
    },
  },
});
