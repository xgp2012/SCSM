<template>
  <div class="flex flex-col gap-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h2 class="text-xl font-semibold tracking-tight">版本下载与安装</h2>
        <p class="mt-1 text-sm text-zinc-400">
          来源 {{ list?.source ?? 'gitee' }} · 主机平台 {{ list?.hostPlatform ?? '—' }}
          <span v-if="list?.cached" class="ml-1 text-zinc-500">（缓存）</span>
        </p>
      </div>
      <div class="flex items-center gap-2">
        <button
          class="rounded-md border border-zinc-700 px-3 py-1.5 text-sm transition hover:border-zinc-500"
          :disabled="loading"
          @click="refresh(true)"
        >
          刷新
        </button>
        <button
          class="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white transition hover:bg-emerald-500"
          @click="showUpload = true"
        >
          手动上传版本包
        </button>
      </div>
    </div>

    <p
      v-if="list?.notice"
      class="rounded-md border border-amber-800/60 bg-amber-950/40 px-3 py-2 text-xs text-amber-300"
    >
      {{ list.notice }}
    </p>

    <p v-if="loading" class="text-sm text-zinc-500">加载中…</p>

    <div
      v-else-if="!list || list.releases.length === 0"
      class="rounded-lg border border-dashed border-zinc-800 p-10 text-center text-sm text-zinc-500"
    >
      暂未获取到版本。可点击「刷新」重试，或使用「手动上传版本包」兜底。
    </div>

    <div v-else class="flex flex-col gap-4">
      <div
        v-for="rel in list.releases"
        :key="rel.tag"
        class="rounded-lg border border-zinc-800 bg-zinc-900/40 p-4"
      >
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div>
            <div class="flex items-center gap-2">
              <span class="font-medium">{{ rel.name }}</span>
              <span class="rounded bg-zinc-800 px-1.5 py-0.5 text-[11px] text-zinc-400">
                {{ rel.tag }}
              </span>
              <span
                v-if="rel.prerelease"
                class="rounded bg-amber-900/60 px-1.5 py-0.5 text-[11px] text-amber-300"
              >
                预发布
              </span>
            </div>
            <p v-if="rel.publishedAt" class="mt-0.5 text-xs text-zinc-500">
              {{ new Date(rel.publishedAt).toLocaleString() }}
            </p>
          </div>
        </div>

        <div class="mt-3 flex flex-col gap-2">
          <div
            v-for="asset in serverAssets(rel)"
            :key="asset.name"
            class="flex flex-wrap items-center gap-3 rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2 text-xs"
          >
            <span class="min-w-0 flex-1 truncate text-zinc-300">{{ asset.name }}</span>
            <span class="rounded bg-zinc-800 px-1.5 py-0.5 text-zinc-400">
              {{ asset.platform }}
            </span>
            <span v-if="asset.arch" class="rounded bg-zinc-800 px-1.5 py-0.5 text-zinc-400">
              {{ asset.arch }}
            </span>
            <span v-if="asset.size" class="text-zinc-500">{{ formatBytes(asset.size) }}</span>
            <button
              v-if="asset.name === rel.recommendedAsset"
              class="rounded bg-emerald-700/40 px-1.5 py-0.5 text-emerald-300"
              title="匹配面板主机平台"
            >
              推荐
            </button>
            <button
              class="rounded-md border border-zinc-700 px-2.5 py-1 transition hover:border-emerald-500 hover:text-emerald-300"
              @click="openInstall(rel, asset)"
            >
              安装
            </button>
          </div>
          <p v-if="serverAssets(rel).length === 0" class="text-xs text-zinc-500">
            该版本未发现服务端包（可能仅提供客户端）。
          </p>
        </div>
      </div>
    </div>

    <!-- 安装对话框 -->
    <AppDialog
      :open="installOpen"
      title="安装版本包"
      confirm-text="开始安装"
      :loading="submitting"
      @update:open="(v: boolean) => (installOpen = v)"
      @confirm="confirmInstall"
      @cancel="installOpen = false"
    >
      <div v-if="selected" class="flex flex-col gap-4 text-sm">
        <p class="rounded bg-zinc-900 p-2 text-xs text-zinc-400">
          {{ selected.release.tag }} / {{ selected.asset.name }}
        </p>

        <label class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">安装方式</span>
          <select
            v-model="mode"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
          >
            <option value="new">新建实例</option>
            <option value="overwrite">覆盖既有实例（保留配置与存档）</option>
          </select>
        </label>

        <label v-if="mode === 'new'" class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">实例名称（留空用版本号）</span>
          <input
            v-model="name"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
            placeholder="我的服务器"
          >
        </label>

        <label v-else class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">目标实例</span>
          <select
            v-model="instanceId"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
          >
            <option disabled value="">请选择要覆盖的实例</option>
            <option v-for="inst in store.instances" :key="inst.id" :value="inst.id">
              {{ inst.name }} (:{{ inst.serverPort }} · {{ inst.version }})
            </option>
          </select>
          <span class="text-[11px] text-zinc-500">运行中的实例需先停止。</span>
        </label>

        <label v-if="mode === 'overwrite'" class="flex items-center gap-2 text-xs text-zinc-300">
          <input v-model="preserve" type="checkbox" class="accent-emerald-500" >
          保留既有 Configs / Worlds / Plugins / 备份
        </label>

        <p v-if="error" class="text-xs text-red-400">{{ error }}</p>
      </div>
    </AppDialog>

    <!-- 上传对话框 -->
    <AppDialog
      :open="showUpload"
      title="手动上传版本包"
      confirm-text="上传并安装"
      :loading="uploading"
      @update:open="(v: boolean) => (showUpload = v)"
      @confirm="submitUpload"
      @cancel="showUpload = false"
    >
      <div class="flex flex-col gap-4 text-sm">
        <input
          type="file"
          accept=".zip,.tar.gz,.tgz"
          class="text-xs text-zinc-400 file:mr-3 file:rounded file:border-0 file:bg-zinc-800 file:px-3 file:py-1.5 file:text-zinc-200"
          @change="onFileChange"
        >

        <label class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">安装方式</span>
          <select
            v-model="uploadMode"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
          >
            <option value="new">新建实例</option>
            <option value="overwrite">覆盖既有实例</option>
          </select>
        </label>

        <label v-if="uploadMode === 'new'" class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">实例名称</span>
          <input
            v-model="uploadName"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
            placeholder="上传包实例"
          >
        </label>

        <label v-else class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">目标实例</span>
          <select
            v-model="uploadInstanceId"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
          >
            <option disabled value="">请选择要覆盖的实例</option>
            <option v-for="inst in store.instances" :key="inst.id" :value="inst.id">
              {{ inst.name }} (:{{ inst.serverPort }})
            </option>
          </select>
        </label>

        <p v-if="uploadError" class="text-xs text-red-400">{{ uploadError }}</p>
      </div>
    </AppDialog>

    <!-- 任务进度 -->
    <section v-if="tasks.length" class="rounded-lg border border-zinc-800 bg-zinc-900/40 p-4">
      <h3 class="mb-3 text-sm font-medium text-zinc-300">下载任务</h3>
      <div class="flex flex-col gap-3">
        <div v-for="t in tasks" :key="t.id" class="text-xs">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="truncate text-zinc-300">
              {{ t.tag }} / {{ t.asset }}
              <span class="ml-2 rounded bg-zinc-800 px-1.5 py-0.5 text-zinc-400">
                {{ t.mode === 'new' ? '新建' : '覆盖' }}
              </span>
            </span>
            <span :class="phaseClass(t)">
              {{ phaseText(t) }}
            </span>
          </div>
          <div class="mt-1.5 h-1.5 w-full overflow-hidden rounded bg-zinc-800">
            <div
              class="h-full bg-emerald-500 transition-all"
              :style="{ width: `${barWidth(t)}%` }"
            />
          </div>
          <p v-if="t.error" class="mt-1 text-red-400">{{ t.error }}</p>
          <p v-else class="mt-1 text-zinc-500">
            <span v-if="t.percent !== undefined">{{ t.percent }}% · </span>
            {{ formatBytes(t.receivedBytes) }}
            <span v-if="t.totalBytes"> / {{ formatBytes(t.totalBytes) }}</span>
            <span v-if="t.speedBps"> · {{ formatBytes(t.speedBps) }}/s</span>
            <span v-if="t.instanceName" class="text-emerald-400"> · 已安装到「{{ t.instanceName }}」</span>
          </p>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue';
import AppDialog from '~/components/ui/AppDialog.vue';
import type {
  CreateDownloadInput,
  DownloadTask,
  InstallMode,
  ReleaseAsset,
  ReleaseInfo,
  ReleaseListResponse,
} from '@sc-panel/shared';
import { api, ApiRequestError } from '~/composables/useApi';
import { useInstancesStore } from '~/stores/instances';

const store = useInstancesStore();
const list = ref<ReleaseListResponse | null>(null);
const loading = ref(false);
const tasks = ref<DownloadTask[]>([]);
let pollTimer: ReturnType<typeof setInterval> | null = null;

const installOpen = ref(false);
const submitting = ref(false);
const error = ref('');
const mode = ref<InstallMode>('new');
const name = ref('');
const instanceId = ref('');
const preserve = ref(true);
const selected = ref<{ release: ReleaseInfo; asset: ReleaseAsset } | null>(null);

const showUpload = ref(false);
const uploading = ref(false);
const uploadError = ref('');
const uploadMode = ref<InstallMode>('new');
const uploadName = ref('');
const uploadInstanceId = ref('');
const uploadFile = ref<File | null>(null);

onMounted(() => {
  void refresh(false);
  void store.fetchAll();
  void loadTasks();
  pollTimer = setInterval(() => void loadTasks(), 1000);
});

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer);
});

function serverAssets(rel: ReleaseInfo): ReleaseAsset[] {
  return rel.assets.filter((a) => a.isServerPackage);
}

async function refresh(force: boolean): Promise<void> {
  loading.value = true;
  try {
    list.value = force
      ? await api.post<ReleaseListResponse>('/versions/releases/refresh')
      : await api.get<ReleaseListResponse>('/versions/releases');
  } catch (err) {
    list.value = null;
    error.value = err instanceof ApiRequestError ? err.message : '获取版本列表失败';
  } finally {
    loading.value = false;
  }
}

function openInstall(rel: ReleaseInfo, asset: ReleaseAsset): void {
  selected.value = { release: rel, asset };
  mode.value = 'new';
  name.value = '';
  instanceId.value = '';
  preserve.value = true;
  error.value = '';
  installOpen.value = true;
}

async function confirmInstall(): Promise<void> {
  if (!selected.value) return;
  error.value = '';
  if (mode.value === 'overwrite' && !instanceId.value) {
    error.value = '请选择要覆盖的实例';
    return;
  }
  submitting.value = true;
  try {
    const payload: CreateDownloadInput = {
      tag: selected.value.release.tag,
      asset: selected.value.asset.name,
      mode: mode.value,
      name: name.value.trim() || undefined,
      instanceId: instanceId.value || undefined,
      preserve: preserve.value,
    };
    const task = await api.post<DownloadTask>('/versions/download', payload);
    tasks.value = [task, ...tasks.value.filter((t) => t.id !== task.id)];
    installOpen.value = false;
  } catch (err) {
    error.value = err instanceof ApiRequestError ? err.message : '下载失败';
  } finally {
    submitting.value = false;
  }
}

async function loadTasks(): Promise<void> {
  try {
    const all = await api.get<DownloadTask[]>('/versions/tasks');
    tasks.value = all.slice(0, 10);
  } catch {
    // 轮询失败静默
  }
}

function onFileChange(e: Event): void {
  const input = e.target as HTMLInputElement;
  uploadFile.value = input.files?.[0] ?? null;
}

async function submitUpload(): Promise<void> {
  uploadError.value = '';
  if (!uploadFile.value) {
    uploadError.value = '请选择版本包文件';
    return;
  }
  if (uploadMode.value === 'overwrite' && !uploadInstanceId.value) {
    uploadError.value = '请选择要覆盖的实例';
    return;
  }
  uploading.value = true;
  try {
    const form = new FormData();
    form.append('file', uploadFile.value);
    form.append('mode', uploadMode.value);
    if (uploadName.value.trim()) form.append('name', uploadName.value.trim());
    if (uploadInstanceId.value) form.append('instanceId', uploadInstanceId.value);
    form.append('preserve', 'true');
    form.append('filename', uploadFile.value.name);
    await api.upload('/versions/upload', form);
    showUpload.value = false;
    uploadFile.value = null;
    await loadTasks();
    await store.fetchAll();
  } catch (err) {
    uploadError.value = err instanceof ApiRequestError ? err.message : '上传安装失败';
  } finally {
    uploading.value = false;
  }
}

function phaseText(t: DownloadTask): string {
  const map: Record<DownloadTask['phase'], string> = {
    queued: '排队中',
    downloading: '下载中',
    extracting: '解压中',
    installing: '安装中',
    done: '完成',
    failed: '失败',
  };
  return map[t.phase];
}

function phaseClass(t: DownloadTask): string {
  if (t.phase === 'done') return 'text-emerald-400';
  if (t.phase === 'failed') return 'text-red-400';
  return 'text-amber-300';
}

function barWidth(t: DownloadTask): number {
  if (t.phase === 'done') return 100;
  if (t.phase === 'extracting' || t.phase === 'installing') return 100;
  return t.percent ?? 0;
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** i).toFixed(i === 0 ? 0 : 2)} ${units[i]}`;
}
</script>
