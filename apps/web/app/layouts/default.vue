<template>
  <div class="min-h-screen bg-zinc-950 text-zinc-100">
    <header class="border-b border-zinc-800 bg-zinc-900/40 backdrop-blur">
      <div class="mx-auto flex max-w-6xl items-center gap-6 px-6 py-3.5">
        <NuxtLink to="/" class="flex items-center gap-2.5">
          <span class="inline-block h-2.5 w-2.5 rounded-full bg-emerald-400" />
          <span class="text-lg font-semibold tracking-tight">SC-Panel</span>
        </NuxtLink>

        <nav class="flex items-center gap-4 text-sm text-zinc-400">
          <NuxtLink to="/" class="transition hover:text-zinc-100">实例</NuxtLink>
          <NuxtLink to="/versions" class="transition hover:text-zinc-100">版本</NuxtLink>
          <NuxtLink to="/audit" class="transition hover:text-zinc-100">审计</NuxtLink>
          <NuxtLink to="/settings" class="transition hover:text-zinc-100">设置</NuxtLink>
        </nav>

        <div class="ml-auto flex items-center gap-3 text-xs text-zinc-400">
          <span>{{ auth.user?.username }}</span>
          <button
            class="rounded border border-zinc-700 px-2.5 py-1 transition hover:border-zinc-500 hover:text-zinc-200"
            @click="onLogout"
          >
            退出
          </button>
        </div>
      </div>
    </header>

    <main class="mx-auto max-w-6xl px-6 py-8">
      <slot />
    </main>
  </div>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router';
import { useAuthStore } from '~/stores/instances';

const auth = useAuthStore();
const router = useRouter();

async function onLogout(): Promise<void> {
  await auth.logout();
  await router.push('/login');
}
</script>
