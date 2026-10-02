<template>
  <div class="flex flex-col gap-6">
    <div>
      <h2 class="text-xl font-semibold tracking-tight">面板设置</h2>
      <p class="mt-1 text-sm text-zinc-400">备份策略、下载来源与管理界面偏好（运行期生效，无需重启）。</p>
    </div>

    <p v-if="loading" class="text-sm text-zinc-500">加载中…</p>

    <template v-else-if="settings">
      <section class="rounded-lg border border-zinc-800 bg-zinc-900/30 p-5">
        <h3 class="mb-1 text-sm font-medium text-zinc-200">备份策略</h3>
        <p class="mb-4 text-xs text-zinc-500">定时自动备份仅对已停止实例执行；保留数超出后按时间淘汰旧备份。</p>
        <div class="grid max-w-lg gap-3 sm:grid-cols-2">
          <label class="flex flex-col gap-1.5">
            <span class="text-xs text-zinc-400">定时备份间隔（秒，0 禁用）</span>
            <input
              v-model.number="form.backupIntervalSec"
              type="number"
              min="0"
              class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
            >
          </label>
          <label class="flex flex-col gap-1.5">
            <span class="text-xs text-zinc-400">每实例保留备份数（0 不限）</span>
            <input
              v-model.number="form.backupKeep"
              type="number"
              min="0"
              class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
            >
          </label>
        </div>
      </section>

      <section class="rounded-lg border border-zinc-800 bg-zinc-900/30 p-5">
        <h3 class="mb-1 text-sm font-medium text-zinc-200">下载与镜像</h3>
        <p class="mb-4 text-xs text-zinc-500">Gitee 不可达时可配置镜像 host；镜像变更会清空 releases 缓存。</p>
        <div class="grid max-w-lg gap-3 sm:grid-cols-2">
          <label class="flex flex-col gap-1.5 sm:col-span-2">
            <span class="text-xs text-zinc-400">Gitee 镜像 host（留空为直连）</span>
            <input
              v-model="form.giteeMirror"
              type="text"
              placeholder="e.g. mirror.example.com"
              class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
            >
          </label>
          <label class="flex flex-col gap-1.5">
            <span class="text-xs text-zinc-400">版本包大小上限（MB）</span>
            <input
              v-model.number="form.maxBytesMB"
              type="number"
              min="1"
              class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
            >
          </label>
          <label class="flex flex-col gap-1.5">
            <span class="text-xs text-zinc-400">releases 缓存（秒，0 不缓存）</span>
            <input
              v-model.number="form.cacheSec"
              type="number"
              min="0"
              class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
            >
          </label>
        </div>
      </section>

      <section class="rounded-lg border border-zinc-800 bg-zinc-900/30 p-5">
        <h3 class="mb-1 text-sm font-medium text-zinc-200">界面</h3>
        <p class="mb-4 text-xs text-zinc-500">面板目前为暗色控制台风格；此偏好被记录并可在后续版本扩展。</p>
        <label class="flex max-w-xs flex-col gap-1.5">
          <span class="text-xs text-zinc-400">主题</span>
          <select
            v-model="form.theme"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
          >
            <option value="dark">暗色</option>
            <option value="light">亮色（预留）</option>
            <option value="system">跟随系统（预留）</option>
          </select>
        </label>
      </section>

      <div class="flex items-center gap-3">
        <Button variant="primary" :loading="saving" @click="onSave">保存设置</Button>
        <span v-if="error" class="text-xs text-red-400">{{ error }}</span>
      </div>

      <section class="rounded-lg border border-zinc-800 bg-zinc-900/30 p-5">
        <h3 class="mb-1 text-sm font-medium text-zinc-200">修改管理员密码</h3>
        <p class="mb-4 text-xs text-zinc-500">需验证当前密码；修改后立即生效并持久化于面板数据目录。</p>
        <div class="grid max-w-lg gap-3">
          <input
            v-model="pw.current"
            type="password"
            autocomplete="current-password"
            placeholder="当前密码"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
          >
          <input
            v-model="pw.next"
            type="password"
            autocomplete="new-password"
            placeholder="新密码（至少 6 位）"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
          >
          <div class="flex items-center gap-3">
            <Button variant="outline" :loading="pwChanging" @click="onChangePassword">修改密码</Button>
            <span v-if="pwMsg" class="text-xs" :class="pwOk ? 'text-emerald-400' : 'text-red-400'">{{ pwMsg }}</span>
          </div>
        </div>
      </section>

      <section class="rounded-lg border border-zinc-800 bg-zinc-900/30 p-5">
        <h3 class="mb-3 text-sm font-medium text-zinc-200">运行时信息</h3>
        <dl class="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
          <div>
            <dt class="text-xs text-zinc-500">版本</dt>
            <dd class="text-zinc-200">{{ settings.runtime.version }}</dd>
          </div>
          <div>
            <dt class="text-xs text-zinc-500">Node</dt>
            <dd class="text-zinc-200">{{ settings.runtime.node }}</dd>
          </div>
          <div>
            <dt class="text-xs text-zinc-500">平台</dt>
            <dd class="text-zinc-200">{{ settings.runtime.platform }}</dd>
          </div>
          <div>
            <dt class="text-xs text-zinc-500">运行时长</dt>
            <dd class="text-zinc-200">{{ settings.runtime.uptimeSec }}s</dd>
          </div>
        </dl>
        <p class="mt-3 truncate text-xs text-zinc-500">数据目录：{{ settings.runtime.dataDir }}</p>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue';
import { Button } from 'fuxsto-design';
import { Dialog } from '~/components/ui/dialog';
import { usePanelSettings } from '~/composables/useSettings';

const { settings, loading, saving, error, load, save, changePassword } = usePanelSettings();

const form = reactive({
  backupIntervalSec: 0,
  backupKeep: 10,
  giteeMirror: '',
  maxBytesMB: 2048,
  cacheSec: 300,
  theme: 'dark' as 'dark' | 'light' | 'system',
});

watch(settings, (s) => {
  if (!s) return;
  form.backupIntervalSec = Math.round(s.backup.intervalMs / 1000);
  form.backupKeep = s.backup.keep;
  form.giteeMirror = s.download.giteeMirror;
  form.maxBytesMB = Math.round(s.download.maxBytes / 1024 / 1024);
  form.cacheSec = Math.round(s.download.cacheMs / 1000);
  form.theme = s.ui.theme;
});

void load();

async function onSave(): Promise<void> {
  const saved = await save({
    backup: { intervalMs: Math.max(0, form.backupIntervalSec) * 1000, keep: Math.max(0, form.backupKeep) },
    download: {
      giteeMirror: form.giteeMirror.trim(),
      maxBytes: Math.max(1, form.maxBytesMB) * 1024 * 1024,
      cacheMs: Math.max(0, form.cacheSec) * 1000,
    },
    ui: { theme: form.theme },
  });
  if (saved) Dialog.success({ title: '已保存', content: '面板设置已更新并即时生效。' });
}

const pw = reactive({ current: '', next: '' });
const pwChanging = ref(false);
const pwMsg = ref('');
const pwOk = ref(false);

async function onChangePassword(): Promise<void> {
  pwMsg.value = '';
  if (pw.next.length < 6) {
    pwOk.value = false;
    pwMsg.value = '新密码至少 6 位';
    return;
  }
  pwChanging.value = true;
  try {
    await changePassword({ currentPassword: pw.current, newPassword: pw.next });
    pwOk.value = true;
    pwMsg.value = '密码已更新';
    pw.current = '';
    pw.next = '';
  } catch (err) {
    pwOk.value = false;
    pwMsg.value = err instanceof Error ? err.message : '修改失败';
  } finally {
    pwChanging.value = false;
  }
}
</script>
