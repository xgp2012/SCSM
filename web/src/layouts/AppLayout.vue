<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import {
  Activity,
  Archive,
  CalendarClock,
  LayoutDashboard,
  LogOut,
  ScrollText,
  ServerCog,
  UserRound,
} from 'lucide-vue-next'
import { UiBadge, UiButton, Message } from '@/components/ui'
import { useAuthStore } from '@/stores/auth'
import { useInstancesStore } from '@/stores/instances'
import { cn } from '@/components/ui/cn'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const instances = useInstancesStore()

interface NavItem {
  name: string
  label: string
  icon: unknown
}

const navItems: NavItem[] = [
  { name: 'overview', label: '总览', icon: LayoutDashboard },
  { name: 'backups', label: '备份中心', icon: Archive },
  { name: 'jobs', label: '任务计划', icon: CalendarClock },
  { name: 'account', label: '账户', icon: UserRound },
  { name: 'audit', label: '审计日志', icon: ScrollText },
  { name: 'settings', label: '系统设置', icon: ServerCog },
]

const activeName = computed(() => {
  if (route.name === 'instance-detail') return 'overview'
  return typeof route.name === 'string' ? route.name : ''
})

const eventStateLabel = computed(() => {
  switch (instances.eventState) {
    case 'open':
      return '实时连接正常'
    case 'connecting':
      return '正在连接…'
    case 'reconnecting':
      return '连接中断，重连中'
    default:
      return '实时连接未建立'
  }
})

async function handleLogout(): Promise<void> {
  await auth.logout()
  instances.reset()
  Message.success('已登出')
  await router.push({ name: 'login' })
}

onMounted(() => {
  instances.connectEvents()
  void instances.fetchAll()
})
</script>

<template>
  <div class="flex min-h-screen bg-background text-foreground">
    <aside
      class="hidden w-60 shrink-0 flex-col border-r border-zinc-800 bg-zinc-950/60 p-4 md:flex"
    >
      <div class="mb-6 flex items-center gap-2 px-2">
        <Activity class="size-5 text-zinc-300" />
        <div class="leading-tight">
          <div class="text-sm font-semibold tracking-wide">SCNETM</div>
          <div class="text-[11px] text-zinc-500">生存战争2 开服面板</div>
        </div>
      </div>

      <nav class="flex flex-1 flex-col gap-1">
        <RouterLink
          v-for="item in navItems"
          :key="item.name"
          :to="{ name: item.name }"
          :class="
            cn(
              'flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors',
              activeName === item.name
                ? 'bg-zinc-800 text-zinc-50'
                : 'text-zinc-400 hover:bg-zinc-900 hover:text-zinc-100',
            )
          "
        >
          <component :is="item.icon" class="size-4" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="mt-4 space-y-2 border-t border-zinc-800 pt-4">
        <div class="flex items-center gap-2 px-2 text-[11px] text-zinc-500">
          <span
            :class="
              cn(
                'size-1.5 rounded-full',
                instances.eventState === 'open' ? 'bg-emerald-500' : 'bg-zinc-600',
              )
            "
          />
          <span>{{ eventStateLabel }}</span>
        </div>
        <div class="flex items-center justify-between px-2">
          <div class="min-w-0">
            <div class="truncate text-sm text-zinc-200">{{ auth.user?.username ?? '未登录' }}</div>
            <div class="text-[11px] text-zinc-500">{{ auth.user?.role ?? '-' }}</div>
          </div>
          <UiButton variant="ghost" size="sm" :icon="LogOut" @click="handleLogout">登出</UiButton>
        </div>
      </div>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <header
        class="flex items-center justify-between gap-4 border-b border-zinc-800 px-4 py-3 md:px-6"
      >
        <div class="flex min-w-0 items-center gap-3">
          <RouterLink :to="{ name: 'overview' }" class="text-sm font-medium md:hidden">
            SCNETM
          </RouterLink>
          <h1 class="truncate text-base font-semibold">
            {{ typeof route.meta.title === 'string' ? route.meta.title : '面板' }}
          </h1>
        </div>
        <div class="flex items-center gap-2">
          <UiBadge variant="outline">{{ instances.running.length }} 个运行中</UiBadge>
          <UiBadge variant="outline">{{ instances.totalPlayers }} 名在线</UiBadge>
        </div>
      </header>

      <main class="min-w-0 flex-1 p-4 md:p-6">
        <RouterView />
      </main>
    </div>
  </div>
</template>
