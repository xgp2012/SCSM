<template>
  <div class="flex min-h-screen items-center justify-center bg-zinc-950 px-4 text-zinc-100">
    <div class="w-full max-w-sm rounded-xl border border-zinc-800 bg-zinc-900/50 p-8 shadow-xl">
      <div class="mb-6 flex items-center gap-2.5">
        <span class="inline-block h-2.5 w-2.5 rounded-full bg-emerald-400" />
        <h1 class="text-xl font-semibold tracking-tight">SC-Panel</h1>
        <span class="rounded bg-zinc-800 px-2 py-0.5 text-[11px] text-zinc-400">登录</span>
      </div>

      <form class="flex flex-col gap-4" @submit.prevent="onSubmit">
        <label class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">用户名</span>
          <input
            v-model="username"
            type="text"
            autocomplete="username"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
          >
        </label>
        <label class="flex flex-col gap-1.5">
          <span class="text-xs text-zinc-400">密码</span>
          <input
            v-model="password"
            type="password"
            autocomplete="current-password"
            class="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-500"
          >
        </label>

        <p v-if="error" class="text-xs text-red-400">{{ error }}</p>

        <button
          type="submit"
          :disabled="loading"
          class="mt-1 rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-emerald-500 disabled:opacity-50"
        >
          {{ loading ? '登录中…' : '登录' }}
        </button>
      </form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { useAuthStore } from '~/stores/instances';
import { ApiRequestError } from '~/composables/useApi';

definePageMeta({ layout: 'blank' });

const username = ref('admin');
const password = ref('');
const loading = ref(false);
const error = ref('');
const auth = useAuthStore();
const route = useRoute();
const router = useRouter();

async function onSubmit(): Promise<void> {
  error.value = '';
  loading.value = true;
  try {
    await auth.login(username.value, password.value);
    const redirect = (route.query.redirect as string | undefined) ?? '/';
    await router.push(redirect);
  } catch (err) {
    error.value = err instanceof ApiRequestError ? err.message : '登录失败';
  } finally {
    loading.value = false;
  }
}
</script>
