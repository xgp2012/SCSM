<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Activity, LockKeyhole, LogIn, User } from 'lucide-vue-next'
import { UiAlert, UiButton, UiCard, UiInput, Message } from '@/components/ui'
import { useAuthStore } from '@/stores/auth'
import { endpoints } from '@/api/endpoints'
import { errorMessage } from '@/api/client'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const username = ref('admin')
const password = ref('')
const error = ref<string | null>(null)
const submitting = ref(false)
const checkingSetup = ref(true)

async function submit(): Promise<void> {
  error.value = null
  if (!username.value.trim() || !password.value) {
    error.value = '请输入用户名与密码'
    return
  }
  submitting.value = true
  try {
    await auth.login(username.value.trim(), password.value)
    Message.success('登录成功')
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    await router.replace(redirect)
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    submitting.value = false
  }
}

onMounted(async () => {
  // First run: no admin password exists yet → force the /setup flow.
  try {
    const status = await endpoints.setupStatus()
    if (status && status.initialized === false) {
      await router.replace({ name: 'setup' })
      return
    }
  } catch {
    // A panel that does not expose /setup/status is assumed to be initialized.
  } finally {
    checkingSetup.value = false
  }
})
</script>

<template>
  <div class="flex min-h-screen items-center justify-center bg-background p-4">
    <UiCard padding="lg" class="w-full max-w-sm">
      <template #header>
        <div class="flex items-center gap-2">
          <Activity class="size-5 text-zinc-300" />
          <div>
            <div class="text-sm font-semibold">SCNETM</div>
            <div class="text-[11px] text-zinc-500">生存战争2 联机版开服面板</div>
          </div>
        </div>
      </template>

      <form class="space-y-3" @submit.prevent="submit">
        <div>
          <label class="mb-1 block text-xs text-zinc-500">用户名</label>
          <div class="relative">
            <User class="pointer-events-none absolute left-2.5 top-2.5 size-4 text-zinc-500" />
            <UiInput v-model="username" class="pl-8" placeholder="admin" autocomplete="username" />
          </div>
        </div>

        <div>
          <label class="mb-1 block text-xs text-zinc-500">密码</label>
          <div class="relative">
            <LockKeyhole class="pointer-events-none absolute left-2.5 top-2.5 size-4 text-zinc-500" />
            <UiInput
              v-model="password"
              class="pl-8"
              type="password"
              placeholder="••••••••"
              autocomplete="current-password"
            />
          </div>
        </div>

        <UiAlert v-if="error" type="error" :title="error" />

        <UiButton
          class="w-full"
          :icon="LogIn"
          :loading="submitting || checkingSetup"
          type="submit"
          @click="submit"
        >
          登录
        </UiButton>

        <p class="text-center text-[11px] text-zinc-600">
          首次启动请使用初始化页面设置管理员密码。
        </p>
      </form>
    </UiCard>
  </div>
</template>
