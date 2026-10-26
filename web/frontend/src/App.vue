<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  Bookmark, Copy, FolderSync, Image, Images, Layers, Link, Moon, Plug, Plus, RefreshCw, Search, Sun, Undo2, UserMinus,
  UserPlus, Users, Video,
} from '@lucide/vue'
import { useRoute } from 'vue-router'
import { desktop } from '@/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import 'vue-sonner/style.css'
import { useSettings } from './stores/settings'
import { useActions } from './stores/actions'
import { useJobs } from './stores/jobs'
import JobBar from '@/components/JobBar.vue'

const settings = useSettings()
const actions = useActions()
const jobs = useJobs()
const route = useRoute()
const isDesktop = ref(false) // the desktop app (local mode) has its own settings page
const meta = computed(() => actions.meta)
const newName = ref('')
const newOpen = ref(false)
const current = computed({ // the selects work with strings
  get: () => (settings.collection === null ? '' : String(settings.collection)),
  set: (v: string) => { settings.collection = v ? Number(v) : null },
})
async function createCollection() {
  if (!newName.value.trim()) return
  await actions.newCollection(newName.value.trim())
  newName.value = ''
  newOpen.value = false
}
const dark = ref(document.documentElement.classList.contains('dark'))

const nav = [
  { title: 'Люди', items: [
    { to: '/people', label: 'Люди', icon: Users },
    { to: '/only', label: 'Только выбранные', icon: UserPlus },
    { to: '/similar', label: 'Похожие люди', icon: Link },
  ] },
  { title: 'Файлы', items: [
    { to: '/files', label: 'Все файлы', icon: Images },
    { to: '/search', label: 'Поиск', icon: Search },
    { to: '/no-people', label: 'Без людей', icon: Image },
    { to: '/strangers', label: 'Только посторонние', icon: UserMinus },
    { to: '/videos', label: 'Видео', icon: Video },
  ] },
  { title: 'Чистка', items: [
    { to: '/stacks', label: 'Стопки похожих', icon: Layers },
    { to: '/copies', label: 'Копии', icon: Copy },
  ] },
  { title: 'Отправить', items: [
    { to: '/collections', label: 'Подборки', icon: Bookmark },
  ] },
]

function toggleTheme() {
  dark.value = !dark.value
  document.documentElement.classList.toggle('dark', dark.value)
  localStorage.setItem('ms-theme', dark.value ? 'dark' : 'light')
}

// Ctrl+Z anywhere (also on pages without a grid); the viewer handles its own keys first
function onKey(e: KeyboardEvent) {
  if (!(e.ctrlKey || e.metaKey) || e.code !== 'KeyZ' || e.shiftKey) return
  const t = e.target as HTMLElement
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return // undo of typing
  if (document.querySelector('[role="dialog"], [role="alertdialog"]')) return
  e.preventDefault()
  void actions.undo()
}
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

onMounted(() => {
  window.addEventListener('keydown', onKey)
  void actions.loadMeta()
  void actions.refreshUndo()
  void jobs.check()
  desktop.state().then(() => { isDesktop.value = true }, () => {})
})
</script>

<template>
  <TooltipProvider :delay-duration="400">
    <RouterView v-if="route.meta.bare" />
    <div v-else class="grid h-full grid-cols-[248px_1fr]">
      <aside class="flex flex-col gap-4 overflow-y-auto border-r bg-sidebar px-3 py-4 text-sidebar-foreground">
        <RouterLink to="/files" class="flex items-center gap-2.5 px-2 py-1 text-[17px] font-semibold">
          <img src="/mark.svg" alt="" width="28" height="28" />
          Mediasorter
        </RouterLink>
        <nav class="flex flex-col gap-3.5">
          <section v-for="g in nav" :key="g.title">
            <h3 class="mb-1 px-2.5 text-xs font-medium text-muted-foreground">{{ g.title }}</h3>
            <RouterLink v-for="it in g.items" :key="it.to" :to="it.to"
                        class="flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-sm transition-colors hover:bg-sidebar-accent"
                        active-class="!bg-sidebar-primary !text-sidebar-primary-foreground">
              <component :is="it.icon" class="size-4 opacity-80" />{{ it.label }}
            </RouterLink>
          </section>
        </nav>
        <div class="mt-auto flex flex-col gap-3 px-1.5 text-[13px]">
          <Button v-if="actions.undoLabel" variant="secondary" size="sm" class="justify-start" :disabled="actions.busy"
                  :title="'Отменить ' + actions.undoLabel" @click="actions.undo()">
            <Undo2 /><span class="truncate">Отменить {{ actions.undoLabel }}</span><kbd class="ml-auto text-[11px] opacity-60">Ctrl+Z</kbd>
          </Button>
          <div class="flex flex-col gap-1.5">
            <span class="text-xs text-muted-foreground">Подборка для кнопки «В …»</span>
            <div class="flex gap-1.5">
              <Select v-model="current">
                <SelectTrigger size="sm" class="min-w-0 flex-1"><SelectValue placeholder="создать новую" /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="c in meta?.collections ?? []" :key="c.id" :value="String(c.id)">{{ c.name }}</SelectItem>
                </SelectContent>
              </Select>
              <Popover v-model:open="newOpen">
                <PopoverTrigger as-child>
                  <Button variant="outline" size="icon-sm" aria-label="Новая подборка" title="Новая подборка"><Plus /></Button>
                </PopoverTrigger>
                <PopoverContent side="top" class="w-64">
                  <form class="flex flex-col gap-2" @submit.prevent="createCollection">
                    <span class="text-sm font-medium">Новая подборка</span>
                    <Input v-model="newName" placeholder="Например: Маме" />
                    <Button type="submit" size="sm" :disabled="!newName.trim()">Создать</Button>
                  </form>
                </PopoverContent>
              </Popover>
            </div>
            <div class="inline-flex w-fit rounded-lg border p-0.5">
              <Button size="xs" :variant="settings.copyMode ? 'ghost' : 'secondary'" @click="settings.copyMode = false">
                <Bookmark />Перемещать
              </Button>
              <Button size="xs" :variant="settings.copyMode ? 'secondary' : 'ghost'" @click="settings.copyMode = true">
                <Copy />Копировать
              </Button>
            </div>
          </div>
          <JobBar />
          <Button variant="ghost" size="sm" class="justify-start" :disabled="!!jobs.job?.running"
                  title="Найти новые и изменённые файлы, разобрать их, обновить людей, сюжеты и стопки" @click="jobs.start('scan')">
            <FolderSync />Обновить архив
          </Button>
          <Button variant="ghost" size="sm" class="justify-start" :disabled="!!jobs.job?.running"
                  title="Имена, объединения и ручные исправления сохраняются" @click="jobs.start('cluster')">
            <RefreshCw />Пересчитать кластеры
          </Button>
          <Button v-if="isDesktop" as-child variant="ghost" size="sm" class="justify-start">
            <RouterLink to="/connect"><Plug />Папки и подключение</RouterLink>
          </Button>
          <label class="flex cursor-pointer items-center gap-2.5">
            <Switch v-model="settings.showVideo" />Показывать и видео
          </label>
          <label class="flex cursor-pointer items-center gap-2.5">
            <Switch v-model="settings.hideMoved" />Скрывать перемещённое в подборки
          </label>
          <div class="flex items-center justify-between text-xs text-muted-foreground">
            <span v-if="meta">{{ meta.files.toLocaleString('ru') }} файлов · {{ meta.people.toLocaleString('ru') }} людей</span>
            <Button variant="ghost" size="icon-sm" :aria-label="dark ? 'Светлая тема' : 'Тёмная тема'"
                    :title="dark ? 'Светлая тема' : 'Тёмная тема'" @click="toggleTheme">
              <Sun v-if="dark" /><Moon v-else />
            </Button>
          </div>
        </div>
      </aside>
      <main class="flex h-full min-w-0 flex-col overflow-hidden">
        <RouterView />
      </main>
    </div>
    <Toaster position="bottom-right" :theme="dark ? 'dark' : 'light'" />
  </TooltipProvider>
</template>
