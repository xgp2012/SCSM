<template>
  <div class="flex flex-col gap-6">
    <div class="flex items-center justify-between">
      <div>
        <h2 class="text-xl font-semibold tracking-tight">实例概览</h2>
        <p class="mt-1 text-sm text-zinc-400">
          共 {{ store.instances.length }} 个实例 · 运行中 {{ store.runningCount }}
        </p>
      </div>
      <button
        class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-emerald-500"
        @click="showCreate = true"
      >
        新建实例
      </button>
    </div>

    <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <StatTile label="主机 CPU" :value="cpuText" />
      <StatTile label="主机内存" :value="memText" />
      <StatTile label="数据盘" :value="diskText" />
      <StatTile label="运行实例" :value="`${store.runningCount} / ${store.instances.length}`" />
    </div>

    <p v-if="store.loading" class="text-sm text-zinc-500">加载中…</p>

    <div
      v-else-if="store.instances.length === 0"
      class="rounded-lg border border-dashed border-zinc-800 p-10 text-center text-sm text-zinc-500"
    >
      暂无实例。点击「新建实例」从模板或上传包创建。
    </div>

    <div v-else class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <InstanceCard
        v-for="inst in store.instances"
        :key="inst.id"
        :instance="inst"
        @action="(a) => onAction(inst.id, a)"
      />
    </div>

    <CreateInstanceDialog v-model:open="showCreate" @created="onCreated" />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import type { InstanceAction } from '@sc-panel/shared';
import { useInstancesStore } from '~/stores/instances';
import InstanceCard from '~/components/instance/InstanceCard.vue';
import CreateInstanceDialog from '~/components/instance/CreateInstanceDialog.vue';
import StatTile from '~/components/instance/StatTile.vue';
import { useSystemStats } from '~/composables/useSystemStats';

const store = useInstancesStore();
const showCreate = ref(false);
const { stats: sysStats, start: startSystem } = useSystemStats(3000);

const cpuText = computed(() => (sysStats.value ? `${sysStats.value.cpu}%` : '—'));
const memText = computed(() =>
  sysStats.value
    ? `${sysStats.value.memPercent}% · ${(sysStats.value.memUsedMB / 1024).toFixed(1)}/${(sysStats.value.memTotalMB / 1024).toFixed(1)}G`
    : '—',
);
const diskText = computed(() =>
  sysStats.value?.diskPercent !== undefined ? `${sysStats.value.diskPercent}%` : '—',
);

onMounted(() => {
  void store.fetchAll();
  store.startPolling(3000);
  startSystem();
});

onBeforeUnmount(() => {
  store.stopPolling();
});

async function onAction(id: string, action: InstanceAction): Promise<void> {
  await store.action(id, action);
}

function onCreated(): void {
  void store.fetchAll();
}
</script>
