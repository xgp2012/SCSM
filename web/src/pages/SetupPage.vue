<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { KeyRound, ShieldCheck } from 'lucide-vue-next'
import { UiAlert, UiButton, UiCard, UiInput, Message } from '@/components/ui'
import { useAuthStore } from '@/stores/auth'
import { errorMessage } from '@/api/client'
import { endpoints } from '@/api/endpoints'

const auth = useAuthStore()
const router = useRouter()

const username = ref('admin')
const password = ref('')
const confirm = ref('')
const error = ref<string | null>(null)
const submitting = ref(false)
/**
 * Server policy (auth.MinPasswordLength), fetched rather than hardcoded. It is
 * 6 today because the seeded default admin password is 6 characters; a client
 * that assumed 8 would reject a password the server accepts.
 */
const minLength = ref(6)

onMounted(async () => {
  try {
    const status = await endpoints.setupStatus()
    // The seeded admin's real name, so the form cannot silently rename it.
    if (status?.username) username.value = status.username
    if (status?.min_password_length) minLength.value = status.min_password_length
  } catch {
    // Keep the defaults; the server re-validates on submit regardless.
  }
})

const strength = computed(() => {
  const value = password.value
  let score = 0
  if (value.length >= 8) score += 1
  if (value.length >= 12) score += 1
  if (/[A-Z]/.test(value) && /[a-z]/.test(value)) score += 1
  if (/\d/.test(value)) score += 1
  if (/[^\w\s]/.test(value)) score += 1
  return score
})

const strengthLabel = computed(() => {
  if (password.value.length === 0) return '未输入'
  if (strength.value <= 2) return '弱'
  if (strength.value === 3) return '中'
  return '强'
})

const strengthTone = computed(() => {
  if (password.value.length === 0) return 'text-zinc-500'
  if (strength.value <= 2) return 'text-red-400'
  if (strength.value === 3) return 'text-amber-400'
  return 'text-emerald-400'
})

function validate(): string | null {
  if (!username.value.trim()) return '用户名不能为空'
  if (password.value.length < minLength.value) {
    return `密码至少 ${minLength.value} 位`
  }
  if (password.value !== confirm.value) return '两次输入的密码不一致'
  return null
}

async function submit(): Promise<void> {
  error.value = validate()
  if (error.value) return
  submitting.value = true
  try {
    await auth.setup(password.value, username.value.trim())
    Message.success('管理员密码已设置')
    await router.replace('/')
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="flex min-h-screen items-center justify-center bg-background p-4">
    <UiCard padding="lg" class="w-full max-w-md">
      <template #header>
        <div class="flex items-center gap-2">
          <ShieldCheck class="size-5 text-emerald-400" />
          <div>
            <div class="text-sm font-semibold">初始化面板</div>
            <div class="text-[11px] text-zinc-500">
              首次启动必须设置管理员密码，否则面板不应对外监听
            </div>
          </div>
        </div>
      </template>

      <form class="space-y-3" @submit.prevent="submit">
        <div>
          <label class="mb-1 block text-xs text-zinc-500">管理员用户名</label>
          <UiInput v-model="username" placeholder="admin" clearable />
        </div>

        <div>
          <label class="mb-1 block text-xs text-zinc-500">管理员密码</label>
          <div class="relative">
            <KeyRound class="pointer-events-none absolute left-2.5 top-2.5 size-4 text-zinc-500" />
            <UiInput v-model="password" class="pl-8" type="password" :placeholder="`至少 ${minLength} 位`" />
          </div>
          <div class="mt-1 flex items-center justify-between text-[11px]">
            <span class="text-zinc-600">强度</span>
            <span :class="strengthTone">{{ strengthLabel }}</span>
          </div>
        </div>

        <div>
          <label class="mb-1 block text-xs text-zinc-500">确认密码</label>
          <UiInput v-model="confirm" type="password" placeholder="再次输入" />
        </div>

        <UiAlert v-if="error" type="error" :title="error" />

        <UiButton class="w-full" :loading="submitting" type="submit" @click="submit">
          设置密码并进入面板
        </UiButton>
      </form>
    </UiCard>
  </div>
</template>
