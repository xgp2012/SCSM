<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Plus, RefreshCw, Server } from 'lucide-vue-next'
import {
  UiAlert,
  UiButton,
  UiCard,
  UiEmpty,
  UiInput,
  UiInputNumber,
  UiLoading,
  UiSwitch,
  Message,
} from '@/components/ui'
import InstanceCard from '@/components/instance/InstanceCard.vue'
import { useInstancesStore } from '@/stores/instances'
import { useAuthStore } from '@/stores/auth'
import { errorMessage } from '@/api/client'

const instances = useInstancesStore()
const auth = useAuthStore()

const dialogOpen = ref(false)
const creating = ref(false)
const formError = ref<string | null>(null)
const form = ref({
  name: '',
  port: 17631,
  world_name: 'World',
  max_players: 8,
  auto_start: true,
})

const canOperate = computed(() => auth.canOperate)
const cards = computed(() => instances.instances)

function openDialog(): void {
  formError.value = null
  form.value = { name: '', port: 17631, world_name: 'World', max_players: 8, auto_start: true }
  dialogOpen.value = true
}

async function submitCreate(): Promise<void> {
  formError.value = null
  const name = form.value.name.trim()
  if (!name) {
    formError.value = '实例名不能为空'
    return
  }
  if (!/^[A-Za-z0-9_-]{1,32}$/.test(name)) {
    formError.value = '实例名只允许字母、数字、下划线与短横线（1–32 字符）'
    return
  }
  if (form.value.port < 1 || form.value.port > 65535) {
    formError.value = '端口需在 1–65535 之间'
    return
  }
  if (form.value.max_players < 1 || form.value.max_players > 256) {
    formError.value = '最大玩家数需在 1–256 之间'
    return
  }
  creating.value = true
  try {
    await instances.create({
      name,
      port: form.value.port,
      world_name: form.value.world_name.trim() || 'World',
      max_players: form.value.max_players,
      auto_start: form.value.auto_start,
    })
    Message.success('实例已创建')
    dialogOpen.value = false
  } catch (err) {
    formError.value = errorMessage(err)
  } finally {
    creating.value = false
  }
}

async function refresh(): Promise<void> {
  await instances.fetchAll()
  if (!instances.error && !instances.unavailable) Message.success('已刷新')
}

onMounted(() => {
  void instances.fetchAll()
})
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <Server class="size-5 text-zinc-400" />
        <div>
          <h2 class="text-lg font-semibold">实例总览</h2>
          <p class="text-xs text-zinc-500">
            共 {{ cards.length }} 个实例 · {{ instances.running.length }} 个运行中 ·
            {{ instances.totalPlayers }} 名玩家在线
          </p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <UiButton size="sm" variant="ghost" :icon="RefreshCw" @click="refresh">刷新</UiButton>
        <UiButton size="sm" :icon="Plus" :disabled="!canOperate" @click="openDialog">新建实例</UiButton>
      </div>
    </div>

    <UiAlert v-if="instances.unavailable" type="warning" title="实例接口尚未实现">
      本版本后端对 <code>GET /api/v1/instances</code> 返回 501，总览暂无法列出实例。面板不会因此崩溃。
    </UiAlert>
    <UiAlert v-else-if="instances.error" type="error" :title="instances.error" />

    <UiLoading :loading="instances.loading">
      <UiEmpty
        v-if="cards.length === 0 && !instances.loading"
        variant="dashed"
        title="还没有实例"
        :description="
          instances.unavailable
            ? '后端未实现实例接口。'
            : '点击「新建实例」按模板创建第一个生存战争2 服务端实例。'
        "
      >
        <template #extra>
          <UiButton size="sm" :icon="Plus" :disabled="!canOperate" @click="openDialog">
            新建实例
          </UiButton>
        </template>
      </UiEmpty>

      <div v-else class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
        <InstanceCard v-for="item in cards" :key="item.id" :instance="item" />
      </div>
    </UiLoading>

    <!-- New-instance flow (danger-free, so a plain panel dialog is enough). -->
    <div
      v-if="dialogOpen"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      @click.self="dialogOpen = false"
    >
      <UiCard padding="md" class="w-full max-w-lg">
        <template #header>
          <div class="flex items-center gap-2">
            <Plus class="size-4 text-zinc-400" />
            <span class="font-medium">新建实例</span>
          </div>
        </template>

        <div class="space-y-3">
          <div>
            <label class="mb-1 block text-xs text-zinc-500">实例名</label>
            <UiInput v-model="form.name" placeholder="survival-01" clearable />
          </div>
          <div>
            <label class="mb-1 block text-xs text-zinc-500">监听端口</label>
            <UiInputNumber v-model="form.port" :min="1" :max="65535" controls />
          </div>
          <div>
            <label class="mb-1 block text-xs text-zinc-500">初始世界名</label>
            <UiInput v-model="form.world_name" placeholder="World" clearable />
          </div>
          <div>
            <label class="mb-1 block text-xs text-zinc-500">最大玩家数</label>
            <UiInputNumber v-model="form.max_players" :min="1" :max="256" controls />
          </div>
          <div class="flex items-center gap-2">
            <UiSwitch v-model="form.auto_start" />
            <span class="text-xs text-zinc-500">随面板自启</span>
          </div>

          <UiAlert v-if="formError" type="error" :title="formError" />

          <div class="flex items-center justify-end gap-2 pt-1">
            <UiButton variant="ghost" @click="dialogOpen = false">取消</UiButton>
            <UiButton :loading="creating" :icon="Plus" @click="submitCreate">创建</UiButton>
          </div>
        </div>
      </UiCard>
    </div>

    <UiAlert v-if="!canOperate" type="info" title="只读账号">
      当前角色为 {{ auth.user?.role }}，创建 / 启停按钮已禁用。
    </UiAlert>
  </div>
</template>
