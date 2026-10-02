<template>
  <div class="flex flex-col gap-6">
    <!-- 世界存档 -->
    <section class="flex flex-col gap-3">
      <div class="flex items-center justify-between">
        <h3 class="text-sm font-medium text-zinc-200">世界存档</h3>
        <Button variant="ghost" size="sm" :loading="loading" @click="load">刷新</Button>
      </div>
      <p v-if="worlds.length === 0" class="text-xs text-zinc-500">
        未发现 Worlds/ 下的存档目录。
      </p>
      <table v-else class="w-full text-left text-xs">
        <thead class="text-zinc-500">
          <tr>
            <th class="py-1">世界名</th>
            <th>路径</th>
            <th>大小</th>
            <th>文件数</th>
            <th>最近修改</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="w in worlds" :key="w.name" class="border-t border-zinc-800">
            <td class="py-1 text-zinc-200">
              {{ w.name }}
              <span v-if="w.isCurrent" class="ml-1 rounded bg-emerald-900/50 px-1.5 py-0.5 text-emerald-300">当前</span>
              <span v-if="!w.hasProject" class="ml-1 rounded bg-amber-900/50 px-1.5 py-0.5 text-amber-300">无 Project.json</span>
            </td>
            <td class="text-zinc-400">{{ w.relPath }}</td>
            <td class="text-zinc-300">{{ formatSize(w.size) }}</td>
            <td class="text-zinc-300">{{ w.files }}</td>
            <td class="text-zinc-400">{{ w.lastModified ? new Date(w.lastModified).toLocaleString() : '—' }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- 创建备份 -->
    <section class="flex flex-col gap-3 rounded border border-zinc-800 bg-zinc-900/30 p-3">
      <h3 class="text-sm font-medium text-zinc-200">创建备份</h3>
      <div v-if="isRunning" class="rounded border border-amber-600/40 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
        实例运行中不可创建备份（避免存档快照不一致），请先停止实例。
      </div>
      <div class="flex flex-wrap items-end gap-3">
        <label class="flex flex-col gap-1">
          <span class="text-xs text-zinc-400">世界</span>
          <select v-model="newWorld" class="input w-52" :disabled="isRunning">
            <option v-for="w in worlds" :key="w.name" :value="w.name">
              {{ w.name }}{{ w.isCurrent ? '（当前）' : '' }}
            </option>
          </select>
        </label>
        <label class="flex items-center gap-2 pb-1 text-xs text-zinc-300">
          <input v-model="includeConfig" type="checkbox" class="accent-emerald-500" :disabled="isRunning" >
          包含配置（Settings/ServerSetting/Configs/Plugins）
        </label>
        <Button variant="primary" size="sm" :loading="backingUp" :disabled="isRunning || worlds.length === 0" @click="onCreate">
          立即备份
        </Button>
      </div>
    </section>

    <!-- 备份列表 -->
    <section class="flex flex-col gap-3">
      <h3 class="text-sm font-medium text-zinc-200">备份列表（{{ backups.length }}）</h3>
      <p v-if="backups.length === 0" class="text-xs text-zinc-500">暂无备份。</p>
      <table v-else class="w-full text-left text-xs">
        <thead class="text-zinc-500">
          <tr>
            <th class="py-1">时间</th>
            <th>类型</th>
            <th>世界</th>
            <th>内容</th>
            <th>大小</th>
            <th class="w-44">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="b in backups" :key="b.id" class="border-t border-zinc-800">
            <td class="py-1 text-zinc-300">{{ new Date(b.createdAt).toLocaleString() }}</td>
            <td>
              <span class="rounded px-1.5 py-0.5" :class="typeClass(b.type)">{{ typeLabel(b.type) }}</span>
            </td>
            <td class="text-zinc-300">{{ b.world }}</td>
            <td class="text-zinc-400">{{ b.includes.config ? '世界+配置' : '仅世界' }}</td>
            <td class="text-zinc-300">{{ formatSize(b.size) }}</td>
            <td>
              <div class="flex gap-2">
                <button class="text-emerald-400 hover:underline" @click="onRestore(b)">恢复</button>
                <a class="text-sky-400 hover:underline" :href="downloadUrl(b.id)">下载</a>
                <button class="text-red-400 hover:underline" @click="onRemove(b)">删除</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <p class="text-xs text-zinc-500">
        恢复会先自动生成「恢复前快照」（类型 恢复前快照）再覆盖当前世界与配置；实例运行中恢复将先自动停止实例。
      </p>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { Button } from 'fuxsto-design';
import { Dialog } from '~/components/ui/dialog';
import type { BackupMeta, BackupType, WorldInfo } from '@sc-panel/shared';
import { api, ApiRequestError } from '~/composables/useApi';

const props = defineProps<{ instanceId: string; running?: boolean }>();

const worlds = ref<WorldInfo[]>([]);
const backups = ref<BackupMeta[]>([]);
const loading = ref(false);
const backingUp = ref(false);
const newWorld = ref('');
const includeConfig = ref(true);

const isRunning = computed(() => props.running === true);

function typeLabel(t: BackupType): string {
  return ({ manual: '手动', auto: '定时', 'pre-restore': '恢复前快照' } as const)[t];
}
function typeClass(t: BackupType): string {
  return ({
    manual: 'bg-zinc-700/60 text-zinc-200',
    auto: 'bg-sky-900/50 text-sky-300',
    'pre-restore': 'bg-amber-900/50 text-amber-300',
  } as const)[t];
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
}

function downloadUrl(id: string): string {
  return `/api/instances/${props.instanceId}/backups/${id}/download`;
}

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [w, b] = await Promise.all([
      api.get<WorldInfo[]>(`/instances/${props.instanceId}/worlds`),
      api.get<BackupMeta[]>(`/instances/${props.instanceId}/backups`),
    ]);
    worlds.value = w;
    backups.value = b;
    if (!newWorld.value || !w.some((x) => x.name === newWorld.value)) {
      newWorld.value = w.find((x) => x.isCurrent)?.name ?? w[0]?.name ?? '';
    }
  } catch (err) {
    Dialog.error({ title: '读取存档/备份失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
  } finally {
    loading.value = false;
  }
}

async function onCreate(): Promise<void> {
  if (!newWorld.value) return;
  backingUp.value = true;
  try {
    await api.post<BackupMeta>(`/instances/${props.instanceId}/backups`, {
      world: newWorld.value,
      includeConfig: includeConfig.value,
    });
    Dialog.success({ title: '备份完成', content: '归档已写入实例 backups/ 目录。' });
    await load();
  } catch (err) {
    Dialog.error({ title: '备份失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
  } finally {
    backingUp.value = false;
  }
}

function onRestore(b: BackupMeta): void {
  Dialog.confirm({
    title: '恢复备份',
    content: `确定用「${new Date(b.createdAt).toLocaleString()}」的备份覆盖当前世界「${b.world}」？将先生成恢复前快照。`,
    confirmText: '恢复',
    onConfirm: async () => {
      try {
        await api.post(`/instances/${props.instanceId}/backups/${b.id}/restore`, { snapshot: true });
        Dialog.success({ title: '已恢复', content: '存档与配置已还原，可重新启动实例。' });
        await load();
      } catch (err) {
        Dialog.error({ title: '恢复失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
      }
    },
  });
}

function onRemove(b: BackupMeta): void {
  Dialog.confirm({
    title: '删除备份',
    content: `确定删除该备份（${new Date(b.createdAt).toLocaleString()}）？`,
    confirmText: '删除',
    danger: true,
    onConfirm: async () => {
      try {
        await api.del(`/instances/${props.instanceId}/backups/${b.id}`);
        await load();
      } catch (err) {
        Dialog.error({ title: '删除失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
      }
    },
  });
}

onMounted(load);
</script>

<style scoped>
.input {
  border-radius: 0.375rem;
  border: 1px solid rgb(63 63 70);
  background: rgb(9 9 11);
  padding: 0.375rem 0.625rem;
  font-size: 0.8125rem;
  color: rgb(228 228 231);
  outline: none;
}
.input:focus {
  border-color: rgb(16 185 129);
}
</style>
