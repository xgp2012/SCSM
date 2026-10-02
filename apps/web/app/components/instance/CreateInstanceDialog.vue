<template>
  <AppDialog
    :open="open"
    title="新建实例"
    size="lg"
    confirm-text="创建"
    cancel-text="取消"
    :loading="submitting"
    @update:open="(v: boolean) => emit('update:open', v)"
    @confirm="onConfirm"
    @cancel="emit('update:open', false)"
  >
    <div class="flex flex-col gap-4 text-sm">
      <label class="flex flex-col gap-1.5">
        <span class="text-xs text-zinc-400">实例名称</span>
        <input
          v-model="form.name"
          class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
          placeholder="我的服务器"
        >
      </label>

      <label class="flex flex-col gap-1.5">
        <span class="text-xs text-zinc-400">基础包来源</span>
        <select
          v-model="form.source"
          class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
        >
          <option value="template">模板（本地服务端本体）</option>
          <option value="upload">上传 zip 包（创建后上传）</option>
          <option value="import">导入已有目录</option>
        </select>
      </label>

      <p v-if="form.source === 'template'" class="rounded bg-zinc-900 p-2 text-xs text-zinc-500">
        模板目录：{{ templateInfo?.dir ?? '（未配置）' }}
        <span v-if="templateInfo && !templateInfo.hasServerBinary" class="text-red-400">
          — 缺少 Survivalcraft.dll，请配置 SERVER_TEMPLATE_DIR 或改用上传
        </span>
      </p>

      <label v-if="form.source === 'import'" class="flex flex-col gap-1.5">
        <span class="text-xs text-zinc-400">已有目录绝对路径</span>
        <input
          v-model="form.dir"
          class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
          placeholder="D:\\servers\\my-server"
        >
      </label>

      <div class="grid grid-cols-2 gap-4">
        <label class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">端口（留空自动分配）</span>
          <input
            v-model.number="form.serverPort"
            type="number"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
            placeholder="自动"
          >
        </label>
        <label class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">最大人数</span>
          <input
            v-model.number="form.maxPlayers"
            type="number"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 outline-none focus:border-emerald-500"
            placeholder="20"
          >
        </label>
      </div>

      <label class="flex items-center gap-2 text-xs text-zinc-300">
        <input v-model="form.autoRun" type="checkbox" class="accent-emerald-500" >
        无人值守开服（Autorun：启动后自动进入世界）
      </label>
      <label class="flex items-center gap-2 text-xs text-zinc-300">
        <input v-model="form.autoRestart" type="checkbox" class="accent-emerald-500" >
        崩溃后自动重启
      </label>

      <p v-if="error" class="text-xs text-red-400">{{ error }}</p>
    </div>
  </AppDialog>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue';
import AppDialog from '~/components/ui/AppDialog.vue';
import type { Instance, TemplateInfo } from '@sc-panel/shared';
import { api, ApiRequestError } from '~/composables/useApi';

const props = defineProps<{ open: boolean }>();
const emit = defineEmits<{ 'update:open': [boolean]; created: [Instance] }>();

const submitting = ref(false);
const error = ref('');
const templateInfo = ref<TemplateInfo | null>(null);

const form = reactive({
  name: '',
  source: 'template' as 'template' | 'upload' | 'import',
  dir: '',
  serverPort: undefined as number | undefined,
  maxPlayers: 20,
  autoRun: true,
  autoRestart: false,
});

onMounted(async () => {
  try {
    templateInfo.value = await api.get<TemplateInfo>('/instances/template');
  } catch {
    templateInfo.value = null;
  }
});

watch(
  () => props.open,
  (v) => {
    if (v) error.value = '';
  },
);

async function onConfirm(): Promise<void> {
  error.value = '';
  if (!form.name.trim()) {
    error.value = '请填写实例名称';
    return;
  }
  submitting.value = true;
  try {
    const payload: Record<string, unknown> = {
      name: form.name.trim(),
      source: form.source,
      maxPlayers: form.maxPlayers || 20,
      autoRun: form.autoRun,
      autoRestart: form.autoRestart,
    };
    if (form.serverPort) payload.serverPort = form.serverPort;
    if (form.source === 'import' && form.dir.trim()) payload.dir = form.dir.trim();

    const inst = await api.post<Instance>('/instances', payload);
    emit('created', inst);
    emit('update:open', false);
    form.name = '';
  } catch (err) {
    error.value = err instanceof ApiRequestError ? err.message : '创建失败';
  } finally {
    submitting.value = false;
  }
}
</script>
