<script setup lang="ts">
// People: every person found, most files first. Name them right on the card; merge, hide, "files with only them".
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useIntersectionObserver, watchDebounced } from '@vueuse/core'
import { toast } from 'vue-sonner'
import { Eye, EyeOff, Merge, Search, UserPlus } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Checkbox } from '@/components/ui/checkbox'
import { api, faceUrl, type Person } from '@/api'
import { useQuery } from '@/composables/useQuery'
import { useActions } from '@/stores/actions'
import { useSettings } from '@/stores/settings'

const { q, set } = useQuery()
const router = useRouter()
const actions = useActions()
const settings = useSettings()

const search = ref(q('q'))
watchDebounced(search, (v) => set('q', v.trim()), { debounce: 300 })
const minFiles = computed({ get: () => Number(q('min', '2')) || 2, set: (v: number) => set('min', v, '2') })
const hidden = computed({ get: () => q('hidden') === '1', set: (v: boolean) => set('hidden', v) })

const people = ref<Person[]>([])
const loading = ref(false)
const selected = ref<number[]>([])
const shown = ref(120) // cards rendered; grows on scroll
const sentinel = ref<HTMLElement>()
useIntersectionObserver(sentinel, ([e]) => { if (e?.isIntersecting) shown.value += 120 }, { rootMargin: '800px' })

async function load() {
  loading.value = true
  try {
    people.value = await api.persons({ q: q('q'), min: String(minFiles.value), hidden: hidden.value ? '1' : '', video: settings.showVideo ? '1' : '' })
  } finally {
    loading.value = false
  }
  selected.value = selected.value.filter((id) => people.value.some((p) => p.id === id))
}
watch(() => [q('q'), q('min'), q('hidden'), settings.showVideo, actions.version], () => { shown.value = 120; void load() }, { immediate: true })

const unit = computed(() => (settings.showVideo ? 'файлов' : 'фото'))
const names = ref<Record<number, string>>({})
watch(people, (ps) => { names.value = Object.fromEntries(ps.map((p) => [p.id, p.name])) })

async function saveName(p: Person) {
  const v = (names.value[p.id] ?? '').trim()
  if (v === p.name) return
  await api.personPatch(p.id, { name: v })
  p.name = v
  toast.success(v ? `Сохранено: ${v}` : 'Имя убрано')
}

function toggle(id: number, on: boolean) {
  selected.value = on ? [...selected.value, id] : selected.value.filter((x) => x !== id)
}
const ordered = computed(() => people.value.filter((p) => selected.value.includes(p.id)).map((p) => p.id)) // most files first

async function merge() {
  await actions.run(() => api.merge(ordered.value), false)
  selected.value = []
}
async function hideSelected() {
  await actions.run(() => api.hide(ordered.value, !hidden.value), false)
  selected.value = []
}
function onlyThem() {
  void router.push({ path: '/only', query: { ids: ordered.value.join(',') } })
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-wrap items-center gap-3 px-6 pb-3 pt-4">
      <h1 class="mr-auto text-xl font-semibold tracking-tight">Люди</h1>
      <div class="relative">
        <Search class="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="search" placeholder="Имя или номер" class="h-8 w-56 pl-8" aria-label="Поиск по имени" />
      </div>
      <label class="flex items-center gap-2 text-sm">Мин. файлов
        <Input type="number" min="1" :model-value="minFiles" class="h-8 w-20"
               @update:model-value="(v) => (minFiles = Math.max(1, Number(v) || 1))" />
      </label>
      <label class="flex items-center gap-2 text-sm"><Switch v-model="hidden" />Скрытые</label>
    </header>
    <div class="flex min-h-12 flex-wrap items-center gap-2 border-b px-6 py-1.5 text-sm"
         :class="{ 'bg-[color-mix(in_srgb,var(--ms-pick)_12%,var(--background))]': selected.length }">
      <template v-if="selected.length">
        <span class="mr-2 font-semibold">Отмечено {{ selected.length }}</span>
        <Button size="sm" variant="outline" :disabled="selected.length < 2" title="Все отмеченные — один человек; остаётся тот, у кого больше файлов" @click="merge"><Merge />Объединить</Button>
        <Button size="sm" variant="outline" @click="hideSelected"><Eye v-if="hidden" /><EyeOff v-else />{{ hidden ? 'Показать' : 'Скрыть' }}</Button>
        <Button size="sm" variant="outline" title="Фото и видео, где в кадре только отмеченные люди" @click="onlyThem"><UserPlus />Файлы только с ними</Button>
        <Button size="sm" variant="ghost" @click="selected = []">Снять</Button>
      </template>
      <span v-else class="text-[13px] text-muted-foreground">
        {{ people.length.toLocaleString('ru') }} людей · подпиши имя прямо на карточке · отметь несколько, чтобы объединить или скрыть
      </span>
    </div>
    <div class="flex-1 overflow-y-auto px-6 py-4">
      <p v-if="!loading && !people.length" class="py-16 text-center text-muted-foreground">Никого не нашлось.</p>
      <div class="grid grid-cols-[repeat(auto-fill,minmax(168px,1fr))] gap-3">
        <div v-for="p in people.slice(0, shown)" :key="p.id"
             class="group flex flex-col gap-2 rounded-xl border bg-card p-2 transition-colors"
             :class="{ 'border-[var(--ms-pick)]': selected.includes(p.id) }">
          <RouterLink :to="{ path: '/person', query: { id: p.id } }" class="block overflow-hidden rounded-lg bg-muted">
            <img v-if="p.face" :src="faceUrl(p.face)" alt="" loading="lazy"
                 class="aspect-square w-full object-cover transition-transform duration-300 group-hover:scale-[1.03]" />
            <div v-else class="aspect-square" />
          </RouterLink>
          <Input v-model="names[p.id]" :placeholder="`person_${p.id}`" class="h-8" aria-label="Имя"
                 @keydown.enter="($event.target as HTMLInputElement).blur()" @blur="saveName(p)" />
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Checkbox :model-value="selected.includes(p.id)" aria-label="Отметить"
                      @update:model-value="(v) => toggle(p.id, v === true)" />
            <span class="truncate">#{{ p.id }} · {{ p.n.toLocaleString('ru') }} {{ unit }}<template v-if="p.years"> · {{ p.years }}</template></span>
          </div>
        </div>
      </div>
      <div ref="sentinel" class="h-px" />
    </div>
  </div>
</template>
