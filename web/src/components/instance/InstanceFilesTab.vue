<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  ChevronRight,
  CornerUpLeft,
  Download,
  FileArchive,
  FileText,
  FolderPlus,
  HardDrive,
  Pencil,
  RefreshCw,
  Trash2,
  TriangleAlert,
  Upload,
} from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiBreadcrumb,
  UiBreadcrumbItem,
  UiButton,
  UiCard,
  UiEmpty,
  UiInput,
  UiList,
  UiListItem,
  UiLoading,
  UiPopconfirm,
  Message,
  type UploadFile,
} from '@/components/ui'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { formatBytes, formatDateTime, suffixOf } from '@/utils/format'
import type { FileEntry } from '@/api/types'

/* Template-callable wrappers (avoid ref-unwrapping surprises in inline handlers). */
const doUnzip = (entry: FileEntry): Promise<unknown> => endpoints.unzipFile(props.instanceId, entry.path)
const doDelete = (entry: FileEntry): Promise<unknown> => endpoints.deleteFile(props.instanceId, entry.path)

const props = defineProps<{ instanceId: string }>()

const cwd = ref('')
const entries = ref<FileEntry[]>([])
const root = ref('')
const loading = ref(false)
const unavailable = ref(false)
const loadError = ref<string | null>(null)
const newFolder = ref('')
const renameTarget = ref<FileEntry | null>(null)
const renameValue = ref('')
const uploadFiles = ref<UploadFile[]>([])

const trail = computed(() => {
  const parts = cwd.value.split('/').filter(Boolean)
  const items: Array<{ label: string; path: string }> = [{ label: '实例根目录', path: '' }]
  let acc = ''
  for (const part of parts) {
    acc = acc ? `${acc}/${part}` : part
    items.push({ label: part, path: acc })
  }
  return items
})

const sorted = computed(() =>
  [...entries.value].sort((a, b) => {
    if (a.kind === 'dir' && b.kind !== 'dir') return -1
    if (a.kind !== 'dir' && b.kind === 'dir') return 1
    return a.name.localeCompare(b.name)
  }),
)

const uploadWarning = computed(
  () =>
    '上传仅允许写入该实例目录内（后端会做 filepath.Clean + EvalSymlinks 校验并拒绝符号链接逃逸）。' +
    '允许类型：.zip / .dll / .scpak / .json / .xml / .txt。',
)

function iconFor(entry: FileEntry): unknown {
  if (entry.kind === 'dir') return HardDrive
  const ext = suffixOf(entry.name)
  if (ext === 'zip' || ext === 'scpak') return FileArchive
  return FileText
}

async function load(path = cwd.value): Promise<void> {
  loading.value = true
  loadError.value = null
  unavailable.value = false
  try {
    const listing = await endpoints.listFiles(props.instanceId, path)
    entries.value = listing.entries ?? []
    cwd.value = listing.path ?? path
    root.value = listing.root ?? ''
  } catch (err) {
    entries.value = []
    if (isUnavailable(err)) {
      unavailable.value = true
    } else {
      loadError.value = errorMessage(err)
    }
  } finally {
    loading.value = false
  }
}

function open(entry: FileEntry): void {
  if (entry.kind === 'dir') {
    void load(entry.path)
  }
}

async function guard(action: () => Promise<unknown>, success: string): Promise<void> {
  try {
    await action()
    Message.success(success)
    await load()
  } catch (err) {
    Message.error(errorMessage(err))
  }
}

async function makeFolder(): Promise<void> {
  const name = newFolder.value.trim()
  if (!name) return
  const path = cwd.value ? `${cwd.value}/${name}` : name
  await guard(() => endpoints.mkdir(props.instanceId, path), '目录已创建')
  newFolder.value = ''
}

async function submitRename(): Promise<void> {
  const target = renameTarget.value
  const next = renameValue.value.trim()
  if (!target || !next) return
  const parent = target.path.includes('/') ? target.path.slice(0, target.path.lastIndexOf('/')) : ''
  const to = parent ? `${parent}/${next}` : next
  await guard(() => endpoints.renameFile(props.instanceId, target.path, to), '已重命名')
  renameTarget.value = null
  renameValue.value = ''
}

async function upload(): Promise<void> {
  const file = uploadFiles.value[0]?.raw
  if (!file) {
    Message.warning('请先选择文件')
    return
  }
  const form = new FormData()
  form.append('file', file, file.name)
  form.append('path', cwd.value)
  await guard(async () => endpoints.uploadFile(props.instanceId, form), '上传完成')
  uploadFiles.value = []
}

function download(entry: FileEntry): void {
  const anchor = document.createElement('a')
  anchor.href = endpoints.fileDownloadUrl(props.instanceId, entry.path)
  anchor.download = entry.name
  document.body.appendChild(anchor)
  anchor.click()
  document.body.removeChild(anchor)
}

onMounted(() => load(''))
</script>

<template>
  <div class="space-y-4">
    <UiAlert type="warning" :title="'上传范围受限'" show-icon>
      <div class="flex gap-2">
        <TriangleAlert class="mt-0.5 size-4 shrink-0" />
        <span>{{ uploadWarning }}</span>
      </div>
    </UiAlert>

    <UiAlert v-if="unavailable" type="warning" title="文件接口尚未实现">
      本版本后端对 <code>GET /api/v1/instances/:id/files</code> 返回 501。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <UiCard padding="md">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <HardDrive class="size-4 text-zinc-400" />
            <span class="font-medium">文件浏览</span>
            <UiBadge v-if="root" variant="outline" class="font-mono text-[11px]">
              边界：{{ root }}
            </UiBadge>
          </div>
          <div class="flex items-center gap-2">
            <UiButton
              size="sm"
              variant="ghost"
              :icon="CornerUpLeft"
              :disabled="!cwd"
              @click="load(cwd.includes('/') ? cwd.slice(0, cwd.lastIndexOf('/')) : '')"
            >
              上级
            </UiButton>
            <UiButton size="sm" variant="ghost" :icon="RefreshCw" :loading="loading" @click="load()">
              刷新
            </UiButton>
          </div>
        </div>
      </template>

      <UiBreadcrumb class="mb-3">
        <UiBreadcrumbItem
          v-for="(crumb, index) in trail"
          :key="crumb.path"
          :clickable="index !== trail.length - 1"
          @click="index !== trail.length - 1 && load(crumb.path)"
        >
          {{ crumb.label }}
        </UiBreadcrumbItem>
      </UiBreadcrumb>

      <UiLoading :loading="loading">
        <UiEmpty
          v-if="sorted.length === 0 && !loading"
          size="sm"
          title="目录为空"
          description="当前目录下没有文件，或接口返回 501。"
        />
        <UiList v-else bordered>
          <UiListItem
            v-for="entry in sorted"
            :key="entry.path"
            :title="entry.name"
            :description="`${entry.kind === 'dir' ? '目录' : formatBytes(entry.size_bytes)} · ${formatDateTime(entry.modified_at)}`"
            :icon="iconFor(entry)"
            @click="open(entry)"
          >
            <template #suffix>
              <div class="flex items-center gap-1">
                <UiButton
                  v-if="entry.kind === 'dir'"
                  size="sm"
                  variant="ghost"
                  :icon="ChevronRight"
                  @click.stop="open(entry)"
                />
                <UiButton
                  v-else
                  size="sm"
                  variant="ghost"
                  :icon="Download"
                  @click.stop="download(entry)"
                />
                <UiButton
                  v-if="suffixOf(entry.name) === 'zip'"
                  size="sm"
                  variant="ghost"
                  :icon="FileArchive"
                  @click.stop="guard(() => doUnzip(entry), '解压完成')"
                />
                <UiButton
                  size="sm"
                  variant="ghost"
                  :icon="Pencil"
                  @click.stop="
                    () => {
                      renameTarget = entry
                      renameValue = entry.name
                    }
                  "
                />
                <UiPopconfirm
                  title="删除该条目？"
                  danger
                  confirm-text="删除"
                  @confirm="guard(() => doDelete(entry), '已删除')"
                >
                  <UiButton size="sm" variant="ghost" danger :icon="Trash2" @click.stop />
                </UiPopconfirm>
              </div>
            </template>
          </UiListItem>
        </UiList>
      </UiLoading>
    </UiCard>

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <UiCard padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <FolderPlus class="size-4 text-zinc-400" />
            <span class="font-medium">新建目录</span>
          </div>
        </template>
        <div class="flex items-center gap-2">
          <UiInput v-model="newFolder" placeholder="新目录名" clearable class="flex-1" />
          <UiButton size="sm" :icon="FolderPlus" @click="makeFolder">创建</UiButton>
        </div>
      </UiCard>

      <UiCard padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <Upload class="size-4 text-zinc-400" />
            <span class="font-medium">上传到 {{ cwd || '实例根目录' }}</span>
          </div>
        </template>
        <div class="space-y-2">
          <UiUpload
            v-model="uploadFiles"
            accept=".zip,.dll,.scpak,.json,.xml,.txt"
            :max-count="1"
            :auto-upload="false"
            drag
            tip="单次一个文件；后端会校验大小上限与扩展名白名单"
          />
          <UiButton size="sm" :icon="Upload" @click="upload">上传</UiButton>
        </div>
      </UiCard>
    </div>

    <UiCard v-if="renameTarget" padding="md">
      <template #header>
        <span class="font-medium">重命名 {{ renameTarget.path }}</span>
      </template>
      <div class="flex items-center gap-2">
        <UiInput v-model="renameValue" class="flex-1" clearable />
        <UiButton size="sm" @click="submitRename">确定</UiButton>
        <UiButton size="sm" variant="ghost" @click="renameTarget = null">取消</UiButton>
      </div>
    </UiCard>
  </div>
</template>
