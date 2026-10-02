<template>
  <div class="flex flex-col gap-4">
    <div v-if="bundle?.running" class="rounded border border-amber-600/40 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
      实例正在运行：世界参数/权限/封禁/密码/限制等分区不可修改（需先停止实例）；Settings.xml 按键可热修改。
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <button
        v-for="s in sections"
        :key="s"
        class="rounded border px-3 py-1.5 text-xs transition"
        :class="
          active === s
            ? 'border-emerald-500 text-emerald-300'
            : 'border-zinc-700 text-zinc-400 hover:text-zinc-200'
        "
        @click="active = s"
      >
        {{ sectionLabel(s) }}
      </button>
      <Button variant="ghost" size="sm" :loading="loading" class="ml-auto" @click="load">
        重新读取
      </Button>
    </div>

    <p v-if="!bundle" class="text-sm text-zinc-500">加载配置…</p>

    <div v-else class="flex flex-col gap-3">
      <p v-if="sectionNote" class="text-xs text-zinc-500">
        <span v-if="!sectionEffective" class="mr-1 rounded bg-amber-900/50 px-1.5 py-0.5 text-amber-300">
          源码/遗留
        </span>
        {{ sectionNote }}
      </p>

      <!-- 面板/服务端设置（Settings.xml） -->
      <div v-if="active === 'settings'" class="flex flex-col gap-3">
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">ServerPort（游戏 UDP 端口）</span>
            <input v-model.number="settingsForm.ServerPort" type="number" class="input" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">LogMode</span>
            <input v-model="settingsForm.LogMode" class="input" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">EnableMod (True/False)</span>
            <select v-model="settingsForm.EnableMod" class="input">
              <option value="True">True</option>
              <option value="False">False</option>
            </select>
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">AllowLanConnection</span>
            <select v-model="settingsForm.AllowLanConnection" class="input">
              <option value="True">True</option>
              <option value="False">False</option>
            </select>
          </label>
        </div>
        <p class="text-xs text-zinc-500">
          仅写回上述键；其余 {{ otherSettingKeys }} 个未管理条目原样不变。
        </p>
        <div class="flex items-center gap-2">
          <Button variant="primary" size="sm" :loading="saving" @click="saveSettings">保存</Button>
        </div>
      </div>

      <!-- 世界设置（ServerSetting.json） -->
      <div v-else-if="active === 'serverSetting'" class="flex flex-col gap-3">
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">世界名 WorldName</span>
            <input v-model="worldForm.WorldName" class="input" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">最大人数 WorldMaxPlayers</span>
            <input v-model.number="worldForm.WorldMaxPlayers" type="number" min="1" max="1024" class="input" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">游戏模式 GameMode</span>
            <select v-model.number="worldForm.GameMode" class="input">
              <option :value="0">Creative</option>
              <option :value="1">Cruel</option>
              <option :value="2">Survival</option>
              <option :value="3">Adventure</option>
            </select>
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">世界密码 WorldPassword</span>
            <input v-model="worldForm.WorldPassword" class="input" placeholder="留空=无密码" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">世界种子 WorldSeed</span>
            <input v-model="worldForm.WorldSeed" class="input" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">世界路径 WorldPath</span>
            <input v-model="worldForm.WorldPath" class="input" >
          </label>
        </div>
        <div class="flex flex-wrap gap-4 text-xs text-zinc-300">
          <label class="flex items-center gap-2">
            <input v-model="worldForm.Autorun" type="checkbox" class="accent-emerald-500" >
            无人值守开服 Autorun
          </label>
          <label class="flex items-center gap-2">
            <input v-model="worldForm.AutoGenerateWorld" type="checkbox" class="accent-emerald-500" >
            自动生成世界 AutoGenerateWorld
          </label>
          <label class="flex items-center gap-2">
            <input v-model="worldForm.PVPEnabled" type="checkbox" class="accent-emerald-500" >
            PVP
          </label>
          <label class="flex items-center gap-2">
            <input v-model="worldForm.SeasonChanging" type="checkbox" class="accent-emerald-500" >
            季节变化
          </label>
          <label class="flex items-center gap-2">
            <input v-model="worldForm.CheckLogin" type="checkbox" class="accent-emerald-500" >
            CheckLogin
          </label>
        </div>
        <div class="flex items-center gap-2">
          <Button variant="primary" size="sm" :loading="saving" @click="saveWorld">保存</Button>
          <span class="text-xs text-zinc-500">密码以 ******** 回显表示保持不变；重新输入即覆盖。</span>
        </div>
      </div>

      <!-- 玩家权限（LevelConfig.json） -->
      <div v-else-if="active === 'level'" class="flex flex-col gap-3">
        <table class="w-full text-left text-xs">
          <thead class="text-zinc-500">
            <tr><th class="py-1">玩家</th><th>等级</th><th class="w-20" /></tr>
          </thead>
          <tbody>
            <tr v-for="(lvl, name) in bundle.sections.level" :key="name" class="border-t border-zinc-800">
              <td class="py-1 text-zinc-200">{{ name }}</td>
              <td class="text-zinc-300">{{ lvl }}</td>
              <td>
                <button class="text-red-400 hover:underline" @click="removeLevel(String(name))">删除</button>
              </td>
            </tr>
            <tr v-if="Object.keys(bundle.sections.level).length === 0">
              <td colspan="3" class="py-2 text-zinc-500">暂无权限条目</td>
            </tr>
          </tbody>
        </table>
        <div class="flex flex-wrap items-end gap-2">
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">玩家名</span>
            <input v-model="levelAdd.name" class="input w-40" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">等级 (0-100)</span>
            <input v-model.number="levelAdd.level" type="number" min="0" max="100" class="input w-28" >
          </label>
          <Button variant="outline" size="sm" @click="addLevel">新增/更新</Button>
        </div>
      </div>

      <!-- 封禁列表（BanConfig.json） -->
      <div v-else-if="active === 'ban'" class="flex flex-col gap-4">
        <div v-for="list in banLists" :key="list.key" class="flex flex-col gap-1">
          <div class="text-xs text-zinc-400">{{ list.label }}</div>
          <div class="flex flex-wrap gap-1">
            <span
              v-for="(item, i) in bundle.sections.ban[list.key] ?? []"
              :key="item + i"
              class="rounded bg-zinc-800 px-2 py-0.5 text-xs text-zinc-200"
            >
              {{ item }}
              <button class="ml-1 text-red-400" @click="removeBan(list.key, i)">×</button>
            </span>
            <span v-if="(bundle.sections.ban[list.key] ?? []).length === 0" class="text-xs text-zinc-500">（空）</span>
          </div>
          <div class="flex gap-2">
            <input v-model="banAdd[list.key]" class="input w-56" :placeholder="list.placeholder" >
            <Button variant="outline" size="sm" @click="addBan(list.key)">添加</Button>
          </div>
        </div>
        <p class="text-xs text-zinc-500">
          运行中的服务端需重启后重新加载封禁列表（或在控制台用 /ban 命令即时生效）。
        </p>
      </div>

      <!-- 限制插件（LimitConfig.json） -->
      <div v-else-if="active === 'limit'" class="flex flex-col gap-3">
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">水流范围 WaterLength</span>
            <input v-model.number="limitForm.WaterLength" type="number" min="0" class="input" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">岩浆范围 MagmaLength</span>
            <input v-model.number="limitForm.MagmaLength" type="number" min="0" class="input" >
          </label>
        </div>
        <div class="flex flex-wrap gap-4 text-xs text-zinc-300">
          <label class="flex items-center gap-2">
            <input v-model="limitForm.LimitBlockBreak" type="checkbox" class="accent-emerald-500" >
            限制方块破坏 LimitBlockBreak
          </label>
          <label class="flex items-center gap-2">
            <input v-model="limitForm.LimitExplode" type="checkbox" class="accent-emerald-500" >
            限制爆炸 LimitExplode
          </label>
        </div>
        <div class="flex items-center gap-2">
          <Button variant="primary" size="sm" :loading="saving" @click="saveLimit">保存</Button>
        </div>
      </div>

      <!-- 玩家密码（Password.json） -->
      <div v-else-if="active === 'password'" class="flex flex-col gap-3">
        <label class="flex items-center gap-2 text-xs text-zinc-300">
          <input v-model="passwordForm.IsUse" type="checkbox" class="accent-emerald-500" >
          启用玩家密码验证 IsUse
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-xs text-zinc-400">默认密码 DefaultPassword</span>
          <input v-model="passwordForm.DefaultPassword" class="input w-56" >
        </label>

        <div class="text-xs text-zinc-400">按玩家密码 PlayerPassword</div>
        <table class="w-full text-left text-xs">
          <tbody>
            <tr v-for="(_, name) in passwordForm.PlayerPassword" :key="name" class="border-t border-zinc-800">
              <td class="py-1 text-zinc-200">{{ name }}</td>
              <td><input v-model="passwordForm.PlayerPassword[String(name)]" class="input w-48" ></td>
              <td class="w-16">
                <button class="text-red-400 hover:underline" @click="removePlayerPassword(String(name))">删除</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div class="flex flex-wrap items-end gap-2">
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">玩家名</span>
            <input v-model="passwordAdd.name" class="input w-40" >
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-zinc-400">密码</span>
            <input v-model="passwordAdd.password" class="input w-40" >
          </label>
          <Button variant="outline" size="sm" @click="addPlayerPassword">设置</Button>
        </div>
        <div class="flex items-center gap-2">
          <Button variant="primary" size="sm" :loading="saving" @click="savePassword">保存</Button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue';
import { Button } from 'fuxsto-design';
import { Dialog } from '~/components/ui/dialog';
import type {
  ConfigBundle,
  ConfigSection,
  BanConfig,
  LimitConfig,
  PasswordConfig,
  ServerSettingConfig,
  LevelConfig,
} from '@sc-panel/shared';
import { api, ApiRequestError } from '~/composables/useApi';

const props = defineProps<{ instanceId: string }>();

const bundle = ref<ConfigBundle | null>(null);
const loading = ref(false);
const saving = ref(false);
const active = ref<ConfigSection>('settings');

const sections: ConfigSection[] = ['settings', 'serverSetting', 'level', 'ban', 'limit', 'password'];

const settingsForm = reactive<Record<string, string | number>>({
  ServerPort: 0,
  LogMode: '',
  EnableMod: 'True',
  AllowLanConnection: 'True',
});
const worldForm = reactive<ServerSettingConfig>({});
const levelAdd = reactive({ name: '', level: 100 });
const limitForm = reactive<LimitConfig>({
  WaterLength: 7,
  MagmaLength: 4,
  LimitBlockBreak: true,
  LimitExplode: false,
});
const passwordForm = reactive<PasswordConfig>({
  IsUse: false,
  DefaultPassword: '',
  PlayerPassword: {},
});
const passwordAdd = reactive({ name: '', password: '' });
const banAdd = reactive<Record<string, string>>({
  BanUserList: '',
  BanUserIpList: '',
  BanIpList: '',
});

const banLists = [
  { key: 'BanUserList', label: '封禁玩家名', placeholder: '玩家名' },
  { key: 'BanIpList', label: '封禁 IP', placeholder: '192.168.1.10' },
  { key: 'BanUserIpList', label: '封禁玩家所在 IP', placeholder: '玩家名' },
] as const;

const currentMeta = computed(() => bundle.value?.meta.find((m) => m.key === active.value));
const sectionLabel = (s: ConfigSection): string =>
  bundle.value?.meta.find((m) => m.key === s)?.label ?? s;
const sectionNote = computed(() => currentMeta.value?.note ?? '');
const sectionEffective = computed(() => currentMeta.value?.effective ?? true);
const otherSettingKeys = computed(() => {
  if (!bundle.value) return 0;
  const managed = new Set(['ServerPort', 'LogMode', 'EnableMod', 'AllowLanConnection']);
  return Object.keys(bundle.value.sections.settings).filter((k) => !managed.has(k)).length;
});

async function load(): Promise<void> {
  loading.value = true;
  try {
    bundle.value = await api.get<ConfigBundle>(`/instances/${props.instanceId}/config`);
    const s = bundle.value.sections;
    settingsForm.ServerPort = Number(s.settings.ServerPort ?? 0);
    settingsForm.LogMode = s.settings.LogMode ?? '0';
    settingsForm.EnableMod = s.settings.EnableMod ?? 'True';
    settingsForm.AllowLanConnection = s.settings.AllowLanConnection ?? 'True';

    Object.assign(worldForm, s.serverSetting);
    if (s.limit) Object.assign(limitForm, s.limit);
    if (s.password) {
      Object.assign(passwordForm, s.password);
      passwordForm.PlayerPassword = { ...(s.password.PlayerPassword ?? {}) };
    }
  } catch (err) {
    Dialog.error({ title: '读取配置失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
  } finally {
    loading.value = false;
  }
}

async function putSection(section: ConfigSection, body: unknown): Promise<void> {
  saving.value = true;
  try {
    bundle.value = await api.put<ConfigBundle>(`/instances/${props.instanceId}/config/${section}`, body);
    Dialog.success({ title: '已保存', content: '配置已写入并保留未知字段。' });
  } catch (err) {
    Dialog.error({ title: '保存失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
  } finally {
    saving.value = false;
  }
}

function saveSettings(): void {
  void putSection('settings', {
    ServerPort: String(settingsForm.ServerPort),
    LogMode: String(settingsForm.LogMode),
    EnableMod: String(settingsForm.EnableMod),
    AllowLanConnection: String(settingsForm.AllowLanConnection),
  });
}

function saveWorld(): void {
  const body: ServerSettingConfig = {
    WorldName: worldForm.WorldName,
    WorldMaxPlayers: worldForm.WorldMaxPlayers,
    GameMode: worldForm.GameMode,
    WorldSeed: worldForm.WorldSeed,
    WorldPath: worldForm.WorldPath,
    Autorun: worldForm.Autorun,
    AutoGenerateWorld: worldForm.AutoGenerateWorld,
    PVPEnabled: worldForm.PVPEnabled,
    SeasonChanging: worldForm.SeasonChanging,
    CheckLogin: worldForm.CheckLogin,
  };
  if (worldForm.WorldPassword !== undefined) body.WorldPassword = worldForm.WorldPassword;
  void putSection('serverSetting', body);
}

function addLevel(): void {
  if (!levelAdd.name.trim()) {
    Dialog.error({ title: '无法新增', content: '请填写玩家名' });
    return;
  }
  void putSection('level', { [levelAdd.name.trim()]: levelAdd.level } as LevelConfig).then(() => {
    levelAdd.name = '';
  });
}

function removeLevel(name: string): void {
  void putSection('level', { [name]: 0 } as LevelConfig);
}

function addBan(key: string): void {
  const value = banAdd[key]?.trim();
  if (!value) return;
  const list = [...((bundle.value?.sections.ban[key as keyof BanConfig] as string[]) ?? [])];
  if (!list.includes(value)) list.push(value);
  void putSection('ban', { [key]: list }).then(() => {
    banAdd[key] = '';
  });
}

function removeBan(key: string, index: number): void {
  const list = [...((bundle.value?.sections.ban[key as keyof BanConfig] as string[]) ?? [])];
  list.splice(index, 1);
  void putSection('ban', { [key]: list });
}

function saveLimit(): void {
  void putSection('limit', { ...limitForm });
}

function addPlayerPassword(): void {
  if (!passwordAdd.name.trim()) return;
  passwordForm.PlayerPassword[passwordAdd.name.trim()] = passwordAdd.password;
  passwordAdd.name = '';
  passwordAdd.password = '';
}

function removePlayerPassword(name: string): void {
  const entries = Object.entries(passwordForm.PlayerPassword).filter(([k]) => k !== name);
  passwordForm.PlayerPassword = Object.fromEntries(entries);
}

function savePassword(): void {
  void putSection('password', {
    IsUse: passwordForm.IsUse,
    DefaultPassword: passwordForm.DefaultPassword,
    PlayerPassword: passwordForm.PlayerPassword,
  } as PasswordConfig);
}

onMounted(load);
</script>

<style scoped>
.input {
  border-radius: 0.375rem;
  border: 1px solid rgb(63 63 70);
  background: rgb(9 9 11);
  padding: 0.375rem 0.625rem;
  font-size: 0.8125rem;
  color: rgb(228 228 231);
  outline: none;
}
.input:focus {
  border-color: rgb(16 185 129);
}
</style>
