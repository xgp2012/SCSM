<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div
        v-if="open"
        class="fixed inset-0 z-50 flex items-center justify-center p-4"
        role="dialog"
        aria-modal="true"
        @click.self="onMaskClick"
      >
        <div class="absolute inset-0 bg-black/60" />
        <div
          class="relative z-10 w-full overflow-hidden rounded-xl border border-zinc-800 bg-zinc-900 text-zinc-100 shadow-2xl"
          :class="sizeClass"
        >
          <div class="flex items-start justify-between gap-4 border-b border-zinc-800 px-5 py-4">
            <div class="min-w-0">
              <h3 class="text-base font-semibold tracking-tight">{{ title }}</h3>
            </div>
            <button
              v-if="closable"
              type="button"
              aria-label="关闭"
              class="shrink-0 rounded p-1 text-zinc-500 transition hover:bg-zinc-800 hover:text-zinc-200"
              @click="close"
            >
              <XIcon class="h-4 w-4" />
            </button>
          </div>

          <div class="max-h-[70vh] overflow-y-auto px-5 py-4 text-sm text-zinc-300">
            <slot>{{ content }}</slot>
          </div>

          <div
            v-if="showCancel || showConfirm"
            class="flex items-center justify-end gap-2 border-t border-zinc-800 bg-zinc-900/60 px-5 py-3"
          >
            <button
              v-if="showCancel"
              type="button"
              class="rounded-md border border-zinc-700 px-3.5 py-1.5 text-xs font-medium text-zinc-300 transition hover:border-zinc-500 hover:text-zinc-100"
              @click="close"
            >
              {{ cancelText }}
            </button>
            <button
              v-if="showConfirm"
              type="button"
              :disabled="loading"
              class="rounded-md px-3.5 py-1.5 text-xs font-medium text-white transition disabled:opacity-50"
              :class="danger ? 'bg-red-600 hover:bg-red-500' : 'bg-emerald-600 hover:bg-emerald-500'"
              @click="onConfirm"
            >
              <span v-if="loading" class="mr-1 inline-block animate-pulse">…</span>{{ confirmText }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { X as XIcon } from 'lucide-vue-next';

const props = withDefaults(
  defineProps<{
    open?: boolean;
    title?: string;
    content?: string;
    size?: 'sm' | 'md' | 'lg';
    confirmText?: string;
    cancelText?: string;
    showConfirm?: boolean;
    showCancel?: boolean;
    closable?: boolean;
    loading?: boolean;
    danger?: boolean;
  }>(),
  {
    open: false,
    title: '',
    content: '',
    size: 'md',
    confirmText: '确认',
    cancelText: '取消',
    showConfirm: true,
    showCancel: true,
    closable: true,
    loading: false,
    danger: false,
  },
);

const emit = defineEmits<{
  'update:open': [boolean];
  confirm: [];
  cancel: [];
}>();

const sizeClass = computed(
  () => ({ sm: 'max-w-sm', md: 'max-w-lg', lg: 'max-w-2xl' })[props.size] ?? 'max-w-lg',
);

function close(): void {
  emit('update:open', false);
  emit('cancel');
}

function onConfirm(): void {
  emit('confirm');
}

function onMaskClick(): void {
  if (props.closable) close();
}
</script>
