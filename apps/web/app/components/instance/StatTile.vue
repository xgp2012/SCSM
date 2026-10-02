<template>
  <div class="rounded-lg border border-zinc-800 bg-zinc-900/40 p-4">
    <p class="text-xs uppercase tracking-wide text-zinc-500">{{ label }}</p>
    <p class="mt-1 truncate text-lg font-medium text-zinc-100">{{ value }}</p>
  </div>
</template>

<script setup lang="ts">
defineProps<{ label: string; value: string }>();
</script>
