<script setup lang="ts">
// Trash: what was deleted (it sits in the quarantine), return some of it or erase everything for good.
import { computed, ref, watch } from 'vue'
import { FileX, RotateCcw, Trash2, Video } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter,
  AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { api, trashPreviewUrl, type TrashResp } from '@/api'
import { useActions } from '@/stores/actions'

const actions = useActions()
const data = ref<TrashResp | null>(null)
const error = ref('')
const picked = ref(new Set<number>())
const shown = ref(300) // tens of thousands of cards would choke the page

async function load() {
  try {
    data.value = await api.trash()
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  }
  const ids = new Set(data.value?.items.map((i) => i.id))
  picked.value = new Set([...picked.value].filter((id) => ids.has(id)))
}
watch(() => actions.version, load, { immediate: true })

const items = computed(() => data.value?.items.slice(0, shown.value) ?? [])
const gb = (b: number) => (b / 2 ** 30).toFixed(1).replace('.', ',') + ' ГБ'
const mb = (b: number) => (b >= 2 ** 30 ? gb(b) : Math.max(1, Math.round(b / 2 ** 20)) + ' МБ')
const folder = (p: string) => p.slice(0, p.lastIndexOf('/')).replace(/^\/mnt\/[^/]+\//, '')

function toggle(id: number) {
  const s = new Set(picked.value)
  if (!s.delete(id)) s.add(id)
  picked.value = s
}
async function restore() {
  await actions.run(() => api.trashRestore([...picked.value]), false)
  picked.value = new Set()
  await actions.loadMeta()
}
const emptyOpen = ref(false)
async function empty() {
  emptyOpen.value = false
  await actions.run(() => api.trashEmpty(), false)
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-wrap items-end gap-3 px-3 md:px-6 pb-3 pt-4">
      <div class="mr-auto">
        <h1 class="text-xl font-semibold tracking-tight">Корзина</h1>
        <p class="text-[13px] text-muted-foreground">
          <template v-if="data">{{ data.items.length.toLocaleString('ru') }} файлов · {{ gb(data.bytes) }}. </template>
          Удалённое лежит здесь, пока корзину не почистят. Нажми на файлы, чтобы выбрать их.
        </p>
      </div>
      <Button size="sm" variant="outline" :disabled="!picked.size || actions.busy" @click="restore">
        <RotateCcw />Вернуть{{ picked.size ? ` (${picked.size})` : '' }}
      </Button>
      <Button v-if="data?.canEmpty" size="sm" variant="destructive" :disabled="!data.items.length || actions.busy"
              @click="emptyOpen = true">
        <Trash2 />Почистить корзину
      </Button>
    </header>

    <p v-if="error" class="px-3 md:px-6 text-destructive">{{ error }}</p>
    <p v-else-if="data && !data.items.length" class="px-3 md:px-6 py-6 text-muted-foreground">Корзина пуста.</p>
    <div v-else class="min-h-0 flex-1 overflow-y-auto px-3 md:px-6 pb-6">
      <div class="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-3">
        <button v-for="it in items" :key="it.id" type="button" :aria-pressed="picked.has(it.id)" :title="it.path"
                class="flex flex-col overflow-hidden rounded-xl border bg-card text-left outline-offset-2"
                :class="{ 'border-[var(--ms-pick)] ring-2 ring-[var(--ms-pick)]': picked.has(it.id) }"
                @click="toggle(it.id)">
          <div class="relative grid aspect-square place-items-center bg-muted">
            <img v-if="it.preview" :src="trashPreviewUrl(it.id)" alt="" loading="lazy" class="size-full object-cover" />
            <FileX v-else class="size-8 opacity-40" />
            <Video v-if="it.kind === 'video'" class="absolute right-1.5 top-1.5 size-4 text-white drop-shadow" />
          </div>
          <div class="flex flex-col gap-0.5 px-2.5 py-2 text-xs">
            <span class="truncate font-medium">{{ it.name }}</span>
            <span class="truncate text-muted-foreground">{{ folder(it.path) }}</span>
            <span class="text-muted-foreground">
              {{ it.at.slice(0, 10) }}<template v-if="it.size"> · {{ mb(it.size) }}</template>
              <template v-if="!it.inQuarantine"> · только запись</template>
            </span>
          </div>
        </button>
      </div>
      <div v-if="data && data.items.length > shown" class="mt-4 flex justify-center">
        <Button size="sm" variant="outline" @click="shown += 300">Показать ещё ({{ data.items.length - shown }})</Button>
      </div>
    </div>

    <AlertDialog v-model:open="emptyOpen">
      <AlertDialogContent v-if="data">
        <AlertDialogHeader>
          <AlertDialogTitle>Удалить навсегда {{ data.items.length.toLocaleString('ru') }} файлов?</AlertDialogTitle>
          <AlertDialogDescription>
            {{ gb(data.bytes) }} сотрутся с диска без возможности вернуть, Ctrl+Z для них перестанет работать.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Отмена</AlertDialogCancel>
          <AlertDialogAction class="bg-destructive text-white hover:bg-destructive/90" @click="empty">Удалить навсегда</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </div>
</template>
