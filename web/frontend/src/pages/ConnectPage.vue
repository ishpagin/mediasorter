<script setup lang="ts">
// Desktop app: where the photos are sorted: on this computer (folders + one-time download of the models)
// or on a Mediasorter server on the network. Opened at start (?start=1 goes on by itself if already set up).
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { FolderPlus, Laptop, Loader2, Server, Trash2, TriangleAlert } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Progress } from '@/components/ui/progress'
import { desktop, type DesktopState } from '@/api'

const route = useRoute()
const router = useRouter()
const st = ref<DesktopState | null>(null)
const notDesktop = ref(false)
const mode = ref<'local' | 'remote'>('local')
const sources = ref<string[]>([])
const server = ref('')
const busy = ref(false)
const error = ref('')
let timer: ReturnType<typeof setTimeout> | null = null

async function load() {
  try {
    st.value = await desktop.state()
  } catch {
    notDesktop.value = true
    return
  }
  if (timer) clearTimeout(timer)
  if (st.value.setup.running) timer = setTimeout(load, 700)
}

onMounted(async () => {
  await load()
  const s = st.value
  if (!s) return
  sources.value = [...s.sources]
  server.value = s.server
  if (s.mode) mode.value = s.mode
  if (route.query.start) { // app start: go on where the user was
    if (s.mode === 'local' && s.sources.length) return void router.replace('/people')
    if (s.mode === 'remote' && s.server) await connect()
  }
})
onBeforeUnmount(() => { if (timer) clearTimeout(timer) })

async function addFolder() {
  error.value = ''
  const { path } = await desktop.pick()
  if (path && !sources.value.includes(path)) sources.value.push(path)
}

async function startLocal() {
  busy.value = true
  error.value = ''
  try {
    await desktop.local(sources.value)
    await load()
    if (st.value?.setup.ready) return void router.replace('/people')
    const wait = async () => { // models are downloading: stay here and show it
      await load()
      if (st.value?.setup.ready) return void router.replace('/people')
      if (!st.value?.setup.error) setTimeout(wait, 700)
    }
    await wait()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

async function connect() {
  busy.value = true
  error.value = ''
  try {
    const { url } = await desktop.remote(server.value)
    location.href = url
  } catch (e) {
    error.value = (e as Error).message
    busy.value = false
  }
}

const setup = computed(() => st.value?.setup)
const pct = computed(() => Math.round((setup.value?.progress ?? 0) * 100))
</script>

<template>
  <div class="flex min-h-full items-center justify-center overflow-y-auto bg-muted/40 px-4 py-10">
    <div class="w-full max-w-2xl">
      <div class="mb-8 flex items-center gap-3">
        <img src="/mark.svg" alt="" class="size-10" />
        <div>
          <h1 class="text-2xl font-semibold tracking-tight">Mediasorter</h1>
          <p class="text-sm text-muted-foreground">Где разбирать фото?</p>
        </div>
      </div>

      <p v-if="notDesktop" class="rounded-xl border bg-card p-5 text-sm">
        Эта страница — для приложения Mediasorter на компьютере. В браузере просто открой
        <RouterLink to="/people" class="underline">людей</RouterLink>.
      </p>

      <template v-else-if="st">
        <div class="grid gap-3 sm:grid-cols-2" role="radiogroup" aria-label="Где разбирать фото">
          <button v-for="o in [
                    { v: 'local', icon: Laptop, t: 'На этом компьютере', d: 'Фото в папках здесь или на NAS. Нейросети скачаются один раз (~2 ГБ).' },
                    { v: 'remote', icon: Server, t: 'Сервер в сети', d: 'Mediasorter уже работает на NAS или другом компьютере дома.' },
                  ] as const" :key="o.v" role="radio" :aria-checked="mode === o.v"
                  class="flex flex-col gap-1.5 rounded-xl border bg-card p-4 text-left transition hover:border-foreground/30"
                  :class="mode === o.v ? 'border-[var(--ms-pick)] ring-2 ring-[var(--ms-pick)]/25' : ''"
                  @click="mode = o.v; error = ''">
            <component :is="o.icon" class="size-5" />
            <span class="font-medium">{{ o.t }}</span>
            <span class="text-[13px] leading-snug text-muted-foreground">{{ o.d }}</span>
          </button>
        </div>

        <section v-if="mode === 'local'" class="mt-5 flex flex-col gap-3 rounded-xl border bg-card p-5">
          <div class="flex items-center justify-between gap-3">
            <h2 class="font-medium">Папки с фото и видео</h2>
            <Button size="sm" variant="outline" :disabled="busy || setup?.running" @click="addFolder"><FolderPlus />Добавить папку</Button>
          </div>
          <p v-if="!sources.length" class="text-sm text-muted-foreground">
            Выбери одну или несколько папок — подпапки войдут сами. Сетевую папку NAS тоже можно.
          </p>
          <ul class="flex flex-col gap-1">
            <li v-for="(s, i) in sources" :key="s" class="flex items-center gap-2 rounded-md bg-muted/60 px-3 py-1.5 text-sm">
              <span class="min-w-0 flex-1 truncate" :title="s">{{ s }}</span>
              <Button size="icon-sm" variant="ghost" :aria-label="`Убрать ${s}`" :disabled="busy || setup?.running" @click="sources.splice(i, 1)"><Trash2 /></Button>
            </li>
          </ul>
          <p class="text-xs text-muted-foreground">
            Из этих папок программа только читает. Файлы меняются лишь по твоим действиям (удаление, подборки), и
            удалённое можно вернуть.
          </p>
          <div v-if="setup?.running || (busy && !setup?.ready)" class="flex flex-col gap-1.5" aria-live="polite">
            <div class="flex items-center gap-2 text-sm"><Loader2 class="size-4 animate-spin" />{{ setup?.message || 'Подготовка…' }}</div>
            <Progress :model-value="pct" class="h-1.5" />
            <p class="text-xs text-muted-foreground">Можно закрыть окно — скачивание продолжится со следующего запуска.</p>
          </div>
          <p v-if="setup?.error" class="flex items-start gap-2 text-sm text-destructive"><TriangleAlert class="mt-0.5 size-4 shrink-0" />{{ setup.error }}</p>
          <div class="flex items-center justify-end gap-2">
            <Button v-if="st.mode === 'local' && st.sources.length && !busy" variant="ghost" @click="router.replace('/people')">Отмена</Button>
            <Button :disabled="!sources.length || busy || setup?.running" @click="startLocal">
              {{ st.mode === 'local' ? 'Сохранить и обновить' : 'Начать' }}
            </Button>
          </div>
        </section>

        <form v-else class="mt-5 flex flex-col gap-3 rounded-xl border bg-card p-5" @submit.prevent="connect">
          <label for="srv" class="font-medium">Адрес сервера</label>
          <Input id="srv" v-model="server" placeholder="http://192.168.1.10:8501" autocomplete="url" spellcheck="false" />
          <div v-if="st.recent?.length" class="flex flex-wrap gap-2">
            <button v-for="r in st.recent" :key="r" type="button" class="rounded-full border px-3 py-1 text-xs hover:bg-accent"
                    @click="server = r">{{ r }}</button>
          </div>
          <p class="text-xs text-muted-foreground">Тот же адрес, что открываешь в браузере. Вернуться сюда — меню «Mediasorter → Подключение…» (Ctrl+K).</p>
          <div class="flex justify-end">
            <Button type="submit" :disabled="!server.trim() || busy"><Loader2 v-if="busy" class="animate-spin" />Подключиться</Button>
          </div>
        </form>

        <p v-if="error" class="mt-3 flex items-start gap-2 text-sm text-destructive" role="alert"><TriangleAlert class="mt-0.5 size-4 shrink-0" />{{ error }}</p>
      </template>
    </div>
  </div>
</template>
