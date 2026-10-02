<template>
  <div class="border-b border-zinc-800 pb-6">
    <h2 class="mb-4 text-lg font-medium">SurvivalcraftNet 开服面板</h2>
    <p class="mb-6 max-w-2xl text-sm leading-relaxed text-zinc-400">
      全栈 Node.js 面板：Nuxt 4 SPA 前端 + NestJS 后端 + node-pty 伪终端；
      单管理员认证、单节点多实例托管、WebSocket 实时控制台、存档/配置/备份管理与版本下载。
    </p>

    <ul class="mb-6 grid gap-2 text-sm text-zinc-300 sm:grid-cols-2">
      <li>· 前端 Nuxt 4（<code class="text-emerald-400">ssr: false</code>）</li>
      <li>· 后端 NestJS（:3001）</li>
      <li>· PTY 伪终端（node-pty）</li>
      <li>· 共享类型：@sc-panel/shared</li>
    </ul>

    <!-- fuxsto-design 组件真实渲染（M0 门禁） -->
    <div class="flex flex-wrap items-center gap-3">
      <Button variant="primary" @click="onPrimary">启动实例</Button>
      <Button variant="outline" @click="onOpenDialog">打开对话框</Button>
      <Button variant="ghost" danger @click="onDanger">停止</Button>
    </div>
    <p v-if="clicked" data-test="click-count" class="mt-3 text-xs text-zinc-500">
      已点击主按钮 {{ clicked }} 次
    </p>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import { Button } from 'fuxsto-design';
import { Dialog } from '~/components/ui/dialog';

const clicked = ref(0);

function onPrimary(): void {
  clicked.value += 1;
}

function onDanger(): void {
  Dialog.error({
    title: '停止实例',
    content: 'M0 脚手架演示：此处将用于确认停止实例操作。',
  });
}

function onOpenDialog(): void {
  Dialog({
    title: 'SC-Panel Dialog',
    content: 'M0 门禁：Dialog 组件真实渲染。',
    confirmText: '知道了',
    showCancel: false,
  });
}
</script>
