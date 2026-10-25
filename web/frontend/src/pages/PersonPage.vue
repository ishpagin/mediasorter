<script setup lang="ts">
// One person: their files (alone / without other known people / with others / together with chosen people),
// rename, hide, "this is me", "this is really someone else", "this is not them" on chosen files.
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { ArrowLeft, Eye, EyeOff, Link2, UserX } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import ListView from '@/components/ListView.vue'
import PersonPicker from '@/components/PersonPicker.vue'
import { api, personLabel, type PersonInfo } from '@/api'
import { useFileList } from '@/composables/useFileList'
import { useQuery } from '@/composables/useQuery'
import { useActions } from '@/stores/actions'

const { q, set } = useQuery()
const router = useRouter()
const actions = useActions()

const MODES = [
  { v: 'all', label: 'все' },
  { v: 'alone', label: 'только он(а) в кадре', title: 'На фото нет ни одного другого лица, даже незнакомого' },
  { v: 'noknown', label: 'без других знакомых', title: 'Незнакомые на фоне допускаются' },
  { v: 'with', label: 'с другими' },
]
const id = computed(() => Number(q('id')) || 0)
const mode = computed(() => (MODES.some((m) => m.v === q('mode')) ? q('mode') : 'all'))
const whole = computed(() => q('whole') !== '0')
const withIds = computed(() => q('with').split(',').map(Number).filter(Boolean))

const info = ref<PersonInfo | null>(null)
const missing = ref('')
const name = ref('')
async function loadInfo() {
  if (!id.value) return
  try {
    info.value = await api.person(id.value)
    name.value = info.value.name
    missing.value = ''
  } catch (e) {
    info.value = null
    missing.value = (e as Error).message.replace(/^\d+\s*/, '')
  }
}
watch(() => [id.value, actions.version], loadInfo, { immediate: true })

const { data, loading, error } = useFileList((p, signal) => api.personFiles(id.value, p, signal),
  () => (id.value ? { mode: mode.value, with: withIds.value.join(','), whole: whole.value ? '' : '0' } : null))

async function saveName() {
  if (!info.value || name.value.trim() === info.value.name) return
  await api.personPatch(id.value, { name: name.value.trim() })
  info.value.name = name.value.trim()
  toast.success('Имя сохранено')
}
async function toggleHidden() {
  await actions.run(() => api.personPatch(id.value, { hidden: !info.value?.hidden }), false)
}
async function setSkip(v: boolean) {
  await api.personPatch(id.value, { skipCollect: v })
  if (info.value) info.value.skipCollect = v
}

const coShown = ref(12)
function toggleWith(cid: number) {
  const next = withIds.value.includes(cid) ? withIds.value.filter((x) => x !== cid) : [...withIds.value, cid]
  set('with', next.join(','))
}

const remapOpen = ref(false)
const remapTo = ref<number | null | undefined>(undefined)
const remapNew = ref('')
async function remap() {
  const r = await actions.run(() => api.remap(id.value, remapNew.value.trim() ? { new: remapNew.value.trim() } : { to: remapTo.value! }))
  remapOpen.value = false
  if (r?.person) void router.push({ path: '/person', query: { id: r.person } })
}

async function unlink(ids: number[]) {
  await actions.run(() => api.unlink(id.value, ids))
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-col gap-3 px-6 pb-3 pt-4">
      <RouterLink to="/people" class="flex w-fit items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft class="size-4" />Ко всем людям
      </RouterLink>
      <p v-if="missing" class="text-muted-foreground">{{ missing }}</p>
      <template v-if="info">
        <div class="flex flex-wrap items-center gap-3">
          <Input v-model="name" :placeholder="`person_${info.id}`" class="h-9 w-72 text-lg font-semibold" aria-label="Имя"
                 @keydown.enter="($event.target as HTMLInputElement).blur()" @blur="saveName" />
          <span class="text-sm text-muted-foreground">#{{ info.id }} · {{ info.files.toLocaleString('ru') }} файлов</span>
          <Button size="sm" variant="outline" @click="toggleHidden"><Eye v-if="info.hidden" /><EyeOff v-else />{{ info.hidden ? 'Показать' : 'Скрыть' }}</Button>
          <label class="flex items-center gap-2 text-sm" title="«По людям» не будет создавать подборку для этого человека">
            <Switch :model-value="info.skipCollect" @update:model-value="setSkip" />Не раскладывать по подборкам (это я)
          </label>
          <Popover v-model:open="remapOpen">
            <PopoverTrigger as-child><Button size="sm" variant="outline"><Link2 />Это на самом деле другой человек…</Button></PopoverTrigger>
            <PopoverContent class="flex w-80 flex-col gap-2">
              <p class="text-sm">Все лица «{{ personLabel(info) }}» ({{ info.files }} файлов) перейдут к выбранному человеку. Ctrl+Z — вернуть.</p>
              <PersonPicker v-model="remapTo" :exclude="[info.id]" />
              <Input v-model="remapNew" placeholder="или новый человек: имя" />
              <Button :disabled="remapTo == null && !remapNew.trim()" @click="remap">Перенести всё</Button>
            </PopoverContent>
          </Popover>
        </div>
        <p v-if="!info.name" class="text-[13px] text-muted-foreground">«По людям» раскладывает только по подписанным людям — дай имя, чтобы у него была своя подборка.</p>
        <div class="flex flex-wrap items-center gap-3">
          <div class="inline-flex rounded-lg border p-0.5">
            <Button v-for="m in MODES" :key="m.v" size="xs" :variant="mode === m.v ? 'secondary' : 'ghost'" :title="m.title"
                    @click="set('mode', m.v, 'all')">{{ m.label }}</Button>
          </div>
          <label class="flex items-center gap-2 text-sm" title="Показывать весь кадр, а не только лицо">
            <Switch :model-value="whole" @update:model-value="(v) => set('whole', v ? '' : '0')" />Целые фото
          </label>
        </div>
        <div v-if="info.co.length" class="flex flex-wrap items-center gap-1.5 text-sm">
          <span class="mr-1 text-muted-foreground">С кем в кадре:</span>
          <Button v-for="c in info.co.slice(0, coShown)" :key="c.id" size="xs" :variant="withIds.includes(c.id) ? 'default' : 'outline'"
                  @click="toggleWith(c.id)">{{ c.name }} <span class="opacity-60">{{ c.n }}</span></Button>
          <Button v-if="info.co.length > coShown" size="xs" variant="link" @click="coShown = info.co.length">ещё {{ info.co.length - coShown }}</Button>
        </div>
        <p v-if="withIds.length && mode === 'alone'" class="text-sm text-amber-500">«Только он(а) в кадре» и «с кем в кадре» вместе ничего не найдут — выбери другой режим.</p>
      </template>
    </header>
    <ListView v-if="info" :data="data" :loading="loading" :error="error" :person="info.id">
      <template #actions="{ ids }">
        <Button variant="outline" size="sm" :disabled="actions.busy" @click="unlink(ids)"><UserX />Это не {{ info.name || 'он(а)' }}</Button>
      </template>
    </ListView>
  </div>
</template>
