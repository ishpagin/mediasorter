<script setup lang="ts">
// Collections: gather files to send someone → zip → send → "sent — delete". Contents of the chosen one below.
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { CheckCheck, Eye, FileArchive, FolderOpen, Trash2, Undo2 } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter,
  AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import ListView from '@/components/ListView.vue'
import { api, type CollectionCard, type CollectionsResp } from '@/api'
import { useFileList } from '@/composables/useFileList'
import { useQuery } from '@/composables/useQuery'
import { useActions } from '@/stores/actions'

const { q, set } = useQuery()
const actions = useActions()
const data = ref<CollectionsResp | null>(null)
const root = ref('')
async function load() {
  data.value = await api.collections()
  root.value = data.value.rootShown
}
watch(() => actions.version, load, { immediate: true })

const open = computed(() => {
  const list = data.value?.collections ?? []
  return list.find((c) => c.id === Number(q('c'))) ?? list[0] ?? null
})
const files = useFileList((_, signal) => api.collectionFiles(open.value!.id, signal), () => (open.value ? { c: String(open.value.id) } : null))

async function saveRoot() {
  if (data.value && root.value.trim() && root.value.trim() !== data.value.rootShown)
    await actions.run(() => api.collectionsRoot(root.value.trim()), false)
}
async function copyPath(path: string) {
  try {
    await navigator.clipboard.writeText(path)
    toast.success('Путь скопирован', { description: path })
  } catch {
    toast.info('Папка', { description: path })
  }
}
const zip = (c: CollectionCard) => actions.run(() => api.collectionZip(c.id), false)
const removeEmpty = (c: CollectionCard) => actions.run(() => api.collectionDelete(c.id), false)
const uncollect = (ids: number[]) => actions.run(() => api.collectionRemove(open.value!.id, ids), false)

// the target stays set while the dialog closes (closing happens before the button's click handler)
const finishing = ref<CollectionCard | null>(null)
const finishOpen = ref(false)
const keepZip = ref(false)
const sure = ref(false)
function askFinish(c: CollectionCard) {
  finishing.value = c
  keepZip.value = false
  sure.value = false
  finishOpen.value = true
}
async function finish() {
  const c = finishing.value
  finishOpen.value = false
  if (!c) return
  await actions.run(() => api.collectionFinish(c.id, keepZip.value))
  await actions.loadMeta()
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-col gap-3 px-6 pb-3 pt-4">
      <div>
        <h1 class="text-xl font-semibold tracking-tight">Подборки</h1>
        <p class="text-[13px] text-muted-foreground">
          Собери файлы для отправки (кнопка «В …» в любом списке или в просмотре), упакуй в ZIP, отправь — и удали.
        </p>
      </div>
      <form class="flex max-w-3xl items-center gap-2 text-sm" @submit.prevent="saveRoot">
        <span class="whitespace-nowrap text-muted-foreground">Где хранить подборки</span>
        <Input v-model="root" class="h-8" @blur="saveRoot" />
        <Button v-if="data" type="button" size="sm" variant="outline" @click="copyPath(data.rootShown)"><FolderOpen />Путь</Button>
      </form>
      <p v-if="data && !data.collections.length" class="py-6 text-muted-foreground">
        Подборок пока нет. Создай её в боковой панели или просто нажми «В …» у выделенных файлов.
      </p>
      <div v-else class="flex max-h-[38vh] flex-col gap-2 overflow-y-auto pr-1">
        <div v-for="c in data?.collections ?? []" :key="c.id"
             class="flex flex-wrap items-center gap-3 rounded-xl border bg-card px-4 py-3"
             :class="{ 'border-[var(--ms-pick)]': open?.id === c.id }">
          <div class="mr-auto min-w-0">
            <div class="font-medium">{{ c.name }}
              <span class="font-normal text-muted-foreground">· {{ c.files }} файлов ({{ c.videos }} видео) · {{ c.mb.toFixed(0) }} МБ
                <template v-if="c.zipMB"> · ZIP {{ c.zipMB.toFixed(0) }} МБ</template></span>
            </div>
            <div class="truncate text-xs text-muted-foreground" :title="c.folder">{{ c.folder }}</div>
          </div>
          <Button size="sm" variant="ghost" :disabled="open?.id === c.id" @click="set('c', c.id)"><Eye />Показать</Button>
          <Button size="sm" variant="outline" @click="copyPath(c.folder)"><FolderOpen />Папка</Button>
          <Button size="sm" variant="outline" :disabled="!c.files || actions.busy" title="Архив рядом с папкой — останется отправить его" @click="zip(c)"><FileArchive />ZIP</Button>
          <Button v-if="c.files" size="sm" :disabled="actions.busy" @click="askFinish(c)"><CheckCheck />Отправлено — удалить</Button>
          <Button v-else size="sm" variant="ghost" @click="removeEmpty(c)"><Trash2 />Удалить пустую</Button>
        </div>
      </div>
      <div v-if="open" class="flex items-baseline gap-3">
        <h2 class="text-base font-semibold">Содержимое «{{ open.name }}»</h2>
        <span class="text-[13px] text-muted-foreground">«Убрать из подборки»: копия просто исчезнет, перемещённый файл вернётся в архив.</span>
      </div>
    </header>
    <ListView v-if="open" :data="files.data.value" :loading="files.loading.value" :error="files.error.value" :collect="false"
              empty="В подборке пока пусто.">
      <template #actions="{ ids }">
        <Button variant="outline" size="sm" :disabled="actions.busy" @click="uncollect(ids)"><Undo2 />Убрать из подборки</Button>
      </template>
    </ListView>

    <AlertDialog v-model:open="finishOpen">
      <AlertDialogContent v-if="finishing">
        <AlertDialogHeader>
          <AlertDialogTitle>Отправлено — удалить «{{ finishing.name }}»?</AlertDialogTitle>
          <AlertDialogDescription>
            {{ finishing.files }} файлов ({{ finishing.mb.toFixed(0) }} МБ) уйдут в карантин, подборка закроется.
            Копии и файлы, которые есть в других подборках, останутся там. Ctrl+Z вернёт удалённое.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <label class="flex items-center gap-2 text-sm"><Checkbox v-model="keepZip" />ZIP-архив оставить</label>
        <label class="flex items-center gap-2 text-sm"><Checkbox v-model="sure" />Да, я отправил(а) эти файлы</label>
        <AlertDialogFooter>
          <AlertDialogCancel>Отмена</AlertDialogCancel>
          <AlertDialogAction :disabled="!sure" class="bg-destructive text-white hover:bg-destructive/90" @click="finish">Удалить</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </div>
</template>
