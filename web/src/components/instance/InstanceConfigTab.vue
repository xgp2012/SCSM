<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { FileCode2, Info, Save, ShieldAlert, Wand2 } from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCard,
  UiForm,
  UiFormItem,
  UiInput,
  UiInputNumber,
  UiSegmented,
  UiSelect,
  UiSwitch,
  UiTextarea,
  UiEmpty,
  UiLoading,
  Message,
} from '@/components/ui'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { GAME_MODE_OPTIONS, type ConfigBundle, type ServerSetting } from '@/api/types'

const props = defineProps<{ instanceId: string }>()

interface SettingForm {
  WorldName: string
  Seed: number
  MaxPlayers: number
  GameMode: number
  PVP: boolean
  Seasons: boolean
  DaySpeed: number
  RecoverySpeed: number
  AutoRun: boolean
}

const defaultForm = (): SettingForm => ({
  WorldName: 'World',
  Seed: 0,
  MaxPlayers: 8,
  GameMode: 1,
  PVP: false,
  Seasons: true,
  DaySpeed: 1,
  RecoverySpeed: 1,
  AutoRun: true,
})

const form = ref<SettingForm>(defaultForm())
const bundle = ref<ConfigBundle | null>(null)
const loading = ref(false)
const saving = ref(false)
const unavailable = ref(false)
const loadError = ref<string | null>(null)
const dirty = ref(false)
const needsRestart = ref(false)
const validationIssues = ref<string[]>([])

const mode = ref<'form' | 'advanced'>('form')
const modeOptions = [
  { label: '表单', value: 'form' },
  { label: '高级 (XML/JSON)', value: 'advanced' },
]

const gameModeOptions = GAME_MODE_OPTIONS.map((option) => ({
  label: option.label,
  value: option.value,
}))

/** D5: only Harmless(1) has been measured on the real server. */
const gameModeInferred = computed(() => {
  const found = GAME_MODE_OPTIONS.find((option) => option.value === form.value.GameMode)
  return found ? found.inferred : true
})

/* ---------------- advanced editor ---------------- */
const rawFiles = ref<Record<string, string>>({})
const rawSelected = ref<string>('')
const rawDirty = ref<Record<string, boolean>>({})

const rawFileOptions = computed(() =>
  Object.keys(rawFiles.value).map((path) => ({ label: path, value: path })),
)

const rawContent = computed({
  get: () => rawFiles.value[rawSelected.value] ?? '',
  set: (value: string) => {
    rawFiles.value = { ...rawFiles.value, [rawSelected.value]: value }
    rawDirty.value = { ...rawDirty.value, [rawSelected.value]: true }
    dirty.value = true
  },
})

function applyBundle(next: ConfigBundle): void {
  bundle.value = next
  const setting = next.server_setting ?? {}
  const base = defaultForm()
  form.value = {
    WorldName: typeof setting.WorldName === 'string' ? setting.WorldName : base.WorldName,
    Seed: typeof setting.Seed === 'number' ? setting.Seed : base.Seed,
    MaxPlayers: typeof setting.MaxPlayers === 'number' ? setting.MaxPlayers : base.MaxPlayers,
    GameMode: typeof setting.GameMode === 'number' ? setting.GameMode : base.GameMode,
    PVP: typeof setting.PVP === 'boolean' ? setting.PVP : base.PVP,
    Seasons: typeof setting.Seasons === 'boolean' ? setting.Seasons : base.Seasons,
    DaySpeed: typeof setting.DaySpeed === 'number' ? setting.DaySpeed : base.DaySpeed,
    RecoverySpeed:
      typeof setting.RecoverySpeed === 'number' ? setting.RecoverySpeed : base.RecoverySpeed,
    AutoRun: typeof setting.AutoRun === 'boolean' ? setting.AutoRun : base.AutoRun,
  }
  const files: Record<string, string> = {}
  if (typeof next.settings_xml === 'string') files['Settings.xml'] = next.settings_xml
  for (const [name, content] of Object.entries(next.configs ?? {})) {
    files[name.startsWith('Configs/') ? name : `Configs/${name}`] = content
  }
  rawFiles.value = files
  rawSelected.value = Object.keys(files)[0] ?? ''
  needsRestart.value = next.restart_required === true
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  unavailable.value = false
  try {
    applyBundle(await endpoints.getConfig(props.instanceId))
  } catch (err) {
    if (isUnavailable(err)) {
      unavailable.value = true
    } else {
      loadError.value = errorMessage(err)
    }
  } finally {
    loading.value = false
  }
}

function validate(): boolean {
  const issues: string[] = []
  if (!form.value.WorldName.trim()) issues.push('世界名（WorldName）不能为空')
  if (form.value.MaxPlayers < 1 || form.value.MaxPlayers > 256) issues.push('最大玩家数需在 1–256 之间')
  if (form.value.Seed < 0) issues.push('种子（Seed）不能为负数')
  if (form.value.DaySpeed <= 0) issues.push('昼夜速度（DaySpeed）必须大于 0')
  if (form.value.RecoverySpeed <= 0) issues.push('恢复速度（RecoverySpeed）必须大于 0')
  validationIssues.value = issues
  return issues.length === 0
}

function payload(): {
  server_setting: Partial<ServerSetting>
  raw_files: Record<string, string>
} {
  return {
    server_setting: {
      WorldName: form.value.WorldName.trim(),
      Seed: form.value.Seed,
      MaxPlayers: form.value.MaxPlayers,
      GameMode: form.value.GameMode,
      PVP: form.value.PVP,
      Seasons: form.value.Seasons,
      DaySpeed: form.value.DaySpeed,
      RecoverySpeed: form.value.RecoverySpeed,
      AutoRun: form.value.AutoRun,
    },
    raw_files: rawFiles.value,
  }
}

async function checkOnServer(): Promise<void> {
  if (!validate()) {
    Message.warning('表单校验未通过，请先修正')
    return
  }
  try {
    const result = await endpoints.validateConfig(props.instanceId, payload())
    if (result.valid) {
      Message.success('后端校验通过')
      validationIssues.value = []
    } else {
      validationIssues.value = result.issues.map((issue) => `${issue.path}: ${issue.message}`)
      Message.error('后端校验未通过')
    }
  } catch (err) {
    Message.error(errorMessage(err))
  }
}

async function save(): Promise<void> {
  if (!validate()) {
    Message.warning('表单校验未通过，请先修正')
    return
  }
  saving.value = true
  try {
    await endpoints.saveConfig(props.instanceId, { ...payload(), restart: false })
    dirty.value = false
    needsRestart.value = true
    Message.success('配置已保存')
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <UiLoading :loading="loading">
      <UiAlert v-if="unavailable" type="warning" title="配置接口尚未实现">
        本版本后端对 <code>GET/PUT /api/v1/instances/:id/config</code> 返回 501，表单处于演示数据状态。
      </UiAlert>
      <UiAlert v-else-if="loadError" type="error" :title="loadError" />

      <UiAlert v-if="needsRestart" type="info" title="需要重启实例后生效">
        ServerSetting.json 在服务端启动时读取一次；保存后请到「控制台」标签执行重启。
      </UiAlert>

      <div class="flex flex-wrap items-center justify-between gap-2">
        <UiSegmented v-model="mode" :options="modeOptions" size="sm" />
        <div class="flex items-center gap-2">
          <UiBadge v-if="dirty" variant="outline" class="text-amber-300">有未保存改动</UiBadge>
          <UiButton size="sm" variant="secondary" :icon="ShieldAlert" @click="checkOnServer">
            仅校验
          </UiButton>
          <UiButton size="sm" :icon="Save" :loading="saving" @click="save">保存配置</UiButton>
        </div>
      </div>

      <!-- ------------------------------ form mode ------------------------------ -->
      <UiCard v-if="mode === 'form'" padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <Wand2 class="size-4 text-zinc-400" />
            <span class="font-medium">ServerSetting.json</span>
          </div>
        </template>

        <UiAlert
          v-if="gameModeInferred"
          type="warning"
          title="游戏模式映射为「推断值，待实测校验」（D5）"
          class="mb-4"
        >
          仅 <span class="font-mono">GameMode = 1 → Harmless</span> 已实测确认；其余数值由程序集字符串
          Creative / Harmless / Survival / Challenging / Cruel 的顺序推断，尚未逐一实测。
        </UiAlert>

        <UiForm :model="form" class="grid grid-cols-1 gap-4 md:grid-cols-2">
          <UiFormItem label="世界名 (WorldName)" required>
            <UiInput v-model="form.WorldName" placeholder="World" clearable />
          </UiFormItem>

          <UiFormItem label="种子 (Seed)">
            <UiInputNumber v-model="form.Seed" :min="0" :step="1" controls />
          </UiFormItem>

          <UiFormItem label="最大玩家数 (MaxPlayers)">
            <UiInputNumber v-model="form.MaxPlayers" :min="1" :max="256" :step="1" controls />
          </UiFormItem>

          <UiFormItem label="游戏模式 (GameMode)">
            <UiSelect v-model="form.GameMode" :options="gameModeOptions" searchable />
          </UiFormItem>

          <UiFormItem label="昼夜速度 (DaySpeed)">
            <UiInputNumber v-model="form.DaySpeed" :min="0.1" :step="0.1" :precision="2" controls />
          </UiFormItem>

          <UiFormItem label="恢复速度 (RecoverySpeed)">
            <UiInputNumber
              v-model="form.RecoverySpeed"
              :min="0.1"
              :step="0.1"
              :precision="2"
              controls
            />
          </UiFormItem>

          <UiFormItem label="允许 PVP">
            <div class="flex items-center gap-2">
              <UiSwitch v-model="form.PVP" />
              <span class="text-xs text-zinc-500">{{ form.PVP ? '已开启' : '已关闭' }}</span>
            </div>
          </UiFormItem>

          <UiFormItem label="季节循环 (Seasons)">
            <div class="flex items-center gap-2">
              <UiSwitch v-model="form.Seasons" />
              <span class="text-xs text-zinc-500">{{ form.Seasons ? '已开启' : '已关闭' }}</span>
            </div>
          </UiFormItem>

          <UiFormItem label="随面板自启 (AutoRun)">
            <div class="flex items-center gap-2">
              <UiSwitch v-model="form.AutoRun" />
              <span class="text-xs text-zinc-500">{{ form.AutoRun ? '已开启' : '已关闭' }}</span>
            </div>
          </UiFormItem>
        </UiForm>

        <div v-if="validationIssues.length" class="mt-4 space-y-1">
          <div v-for="issue in validationIssues" :key="issue" class="text-xs text-red-400">
            · {{ issue }}
          </div>
        </div>
      </UiCard>

      <!-- ---------------------------- advanced mode ---------------------------- -->
      <UiCard v-else padding="md">
        <template #header>
          <div class="flex items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <FileCode2 class="size-4 text-zinc-400" />
              <span class="font-medium">原始编辑（Settings.xml / Configs/*.json）</span>
            </div>
            <UiBadge v-if="rawSelected && rawDirty[rawSelected]" variant="outline" class="text-amber-300">
              未保存
            </UiBadge>
          </div>
        </template>

        <UiAlert type="info" class="mb-3" title="写盘前会由后端校验并备份">
          <span class="flex items-center gap-1">
            <Info class="size-3.5" />
            保存失败时后端返回 422 与校验详情；成功后会生成一份 .bak。修改 XML 前请确认结构完整。
          </span>
        </UiAlert>

        <UiEmpty
          v-if="rawFileOptions.length === 0"
          size="sm"
          title="未发现可编辑的原始配置文件"
          description="后端未返回 Settings.xml 或 Configs/*.json（接口可能为 501，或实例目录尚未初始化）。"
        />

        <div v-else class="space-y-3">
          <UiSelect v-model="rawSelected" :options="rawFileOptions" searchable />
          <UiTextarea
            v-model="rawContent"
            :rows="18"
            auto-size
            resize="vertical"
            class="font-mono text-xs"
          />
        </div>
      </UiCard>
    </UiLoading>
  </div>
</template>
