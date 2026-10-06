<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { KeyRound, RefreshCw, ShieldCheck, UserPlus, Users } from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCard,
  UiEmpty,
  UiInput,
  UiKeyValue,
  UiLoading,
  UiSelect,
  UiTable,
  Message,
  type SelectOption,
  type TableColumn,
} from '@/components/ui'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { formatDateTime } from '@/utils/format'
import type { Role, User } from '@/api/types'

const auth = useAuthStore()

const users = ref<User[]>([])
const loading = ref(false)
const unavailable = ref(false)
const loadError = ref<string | null>(null)
const busy = ref(false)

const newUser = ref<{ username: string; password: string; role: Role }>({
  username: '',
  password: '',
  role: 'operator',
})

const passwordChange = ref({ current: '', next: '', confirm: '' })
const passwordError = ref<string | null>(null)

const roleOptions: SelectOption[] = [
  { label: '管理员 (admin)', value: 'admin' },
  { label: '运维 (operator)', value: 'operator' },
  { label: '只读 (viewer)', value: 'viewer' },
]

const roleLabel: Record<Role, string> = {
  admin: '管理员',
  operator: '运维',
  viewer: '只读',
}

const columns: TableColumn[] = [
  { key: 'username', title: '用户名', width: 180 },
  { key: 'role', title: '角色', width: 120 },
  { key: 'created_at', title: '创建时间', width: 180 },
  { key: 'last_login_at', title: '最近登录', width: 180 },
]

const isMultiUser = computed(() => users.value.length > 1)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  unavailable.value = false
  try {
    users.value = (await endpoints.listUsers()) ?? []
  } catch (err) {
    users.value = []
    if (isUnavailable(err)) unavailable.value = true
    else loadError.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function createUser(): Promise<void> {
  if (!newUser.value.username.trim()) {
    Message.warning('用户名不能为空')
    return
  }
  if (newUser.value.password.length < 8) {
    Message.warning('密码至少 8 位')
    return
  }
  busy.value = true
  try {
    await endpoints.createUser({
      username: newUser.value.username.trim(),
      password: newUser.value.password,
      role: newUser.value.role,
    })
    Message.success('用户已创建')
    newUser.value = { username: '', password: '', role: 'operator' }
    await load()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    busy.value = false
  }
}

async function changePassword(): Promise<void> {
  passwordError.value = null
  if (passwordChange.value.next.length < 8) {
    passwordError.value = '新密码至少 8 位'
    return
  }
  if (passwordChange.value.next !== passwordChange.value.confirm) {
    passwordError.value = '两次输入的新密码不一致'
    return
  }
  const me = auth.user
  if (!me) return
  busy.value = true
  try {
    await endpoints.updateUser(me.id, { password: passwordChange.value.next })
    Message.success('密码已更新')
    passwordChange.value = { current: '', next: '', confirm: '' }
  } catch (err) {
    passwordError.value = errorMessage(err)
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <UiAlert v-if="unavailable" type="warning" title="用户接口尚未实现">
      本版本后端对 <code>GET /api/v1/users</code> 返回 501。单用户模式下这是预期行为，账户页仍可修改密码（若接口可用）。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <UiCard padding="md" class="lg:col-span-2">
        <template #header>
          <div class="flex items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <Users class="size-4 text-zinc-400" />
              <span class="font-medium">当前账户</span>
            </div>
            <UiButton size="sm" variant="ghost" :icon="RefreshCw" :loading="loading" @click="load">
              刷新
            </UiButton>
          </div>
        </template>

        <div class="space-y-0">
          <UiKeyValue label="用户名" :value="auth.user?.username ?? '-'" />
          <UiKeyValue label="角色">
            <UiBadge variant="outline">
              {{ auth.user ? roleLabel[auth.user.role] : '-' }}
            </UiBadge>
          </UiKeyValue>
          <UiKeyValue label="用户 ID" :value="auth.user?.id ?? '-'" />
          <UiKeyValue
            label="最近登录"
            :value="formatDateTime(auth.user?.last_login_at ?? null)"
          />
        </div>

        <p class="mt-3 text-[11px] text-zinc-500">
          路由与组件已按多用户预留（RBAC 三档），单用户模式下用户列表可只显示自身。
        </p>
      </UiCard>

      <UiCard padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <KeyRound class="size-4 text-zinc-400" />
            <span class="font-medium">修改密码</span>
          </div>
        </template>
        <div class="space-y-3">
          <div>
            <label class="mb-1 block text-xs text-zinc-500">新密码</label>
            <UiInput v-model="passwordChange.next" type="password" placeholder="至少 8 位" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-zinc-500">确认新密码</label>
            <UiInput v-model="passwordChange.confirm" type="password" placeholder="再次输入" />
          </div>
          <UiAlert v-if="passwordError" type="error" :title="passwordError" />
          <UiButton class="w-full" :icon="ShieldCheck" :loading="busy" @click="changePassword">
            更新密码
          </UiButton>
        </div>
      </UiCard>
    </div>

    <UiCard padding="md">
      <template #header>
        <div class="flex items-center gap-2">
          <Users class="size-4 text-zinc-400" />
          <span class="font-medium">用户列表</span>
          <UiBadge v-if="isMultiUser" variant="outline">多用户</UiBadge>
          <UiBadge v-else variant="outline">单用户模式</UiBadge>
        </div>
      </template>

      <UiLoading :loading="loading">
        <UiEmpty
          v-if="users.length === 0 && !loading"
          size="sm"
          title="未获取到用户列表"
          description="单用户模式或接口返回 501。"
        />
        <UiTable v-else :columns="columns" :data="users" row-key="id" hover>
          <template #cell-role="{ row }">
            <UiBadge variant="outline">{{ roleLabel[(row as User).role] ?? (row as User).role }}</UiBadge>
          </template>
          <template #cell-created_at="{ row }">
            <span class="text-xs text-zinc-400">{{ formatDateTime((row as User).created_at) }}</span>
          </template>
          <template #cell-last_login_at="{ row }">
            <span class="text-xs text-zinc-400">
              {{ formatDateTime((row as User).last_login_at ?? null) }}
            </span>
          </template>
        </UiTable>
      </UiLoading>
    </UiCard>

    <UiCard v-if="auth.isAdmin" padding="md">
      <template #header>
        <div class="flex items-center gap-2">
          <UserPlus class="size-4 text-zinc-400" />
          <span class="font-medium">新增用户</span>
        </div>
      </template>
      <div class="grid grid-cols-1 gap-3 md:grid-cols-4">
        <UiInput v-model="newUser.username" placeholder="用户名" clearable />
        <UiInput v-model="newUser.password" type="password" placeholder="密码（至少 8 位）" />
        <UiSelect v-model="newUser.role" :options="roleOptions" />
        <UiButton :icon="UserPlus" :loading="busy" @click="createUser">创建</UiButton>
      </div>
    </UiCard>
  </div>
</template>
