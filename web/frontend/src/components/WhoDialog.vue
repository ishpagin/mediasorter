<script setup lang="ts">
// "Who is on the photo": fix recognition by hand. One choice per person per file (videos: one face per person),
// ungrouped faces one by one; "everyone at once" also reaches the faces that do not fit (60 are shown).
// src: the person whose page this is: their faces come first and "only theirs" is the default scope.
import { computed, ref, watch } from 'vue'
import { UserPlus } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import PersonPicker from './PersonPicker.vue'
import { api, faceUrl, type Unit } from '@/api'
import { useActions } from '@/stores/actions'
import { usePeople } from '@/stores/people'

const SHOW = 60
const props = defineProps<{ files: number[]; src?: number | null }>()
const open = defineModel<boolean>('open', { required: true })
const actions = useActions()
const people = usePeople()

const units = ref<Unit[]>([])
const target = ref<(number | null)[]>([]) // chosen person per unit
const bulk = ref<number | null | undefined>(undefined)
const onlySrc = ref(true)
const newName = ref('')
const loading = ref(false)

watch(open, async (o) => {
  if (!o) return
  loading.value = true
  bulk.value = undefined
  onlySrc.value = true
  newName.value = ''
  const u = await api.faces(props.files)
  if (props.src != null) u.sort((a, b) => Number(a.person !== props.src) - Number(b.person !== props.src)) // theirs first
  units.value = u
  target.value = u.map((x) => x.person)
  loading.value = false
}, { immediate: true })

const inScope = (u: Unit) => props.src == null || !onlySrc.value || u.person === props.src
function setAll(pid: number | null) {
  units.value.forEach((u, i) => { if (inScope(u)) target.value[i] = pid })
}
watch(bulk, (v) => { if (v !== undefined) setAll(v) })
watch(onlySrc, () => { if (bulk.value !== undefined) setAll(bulk.value) })

async function addPerson() {
  const name = newName.value.trim()
  if (!name) return
  const { id } = await api.personCreate(name)
  await people.load(true)
  newName.value = ''
  bulk.value = id
  setAll(id)
}

const changes = computed(() => {
  const by = new Map<number | null, number[]>()
  units.value.forEach((u, i) => {
    const t = target.value[i]
    if (t !== u.person) by.set(t, [...(by.get(t) ?? []), ...u.faces])
  })
  return [...by].map(([person, faces]) => ({ person, faces }))
})
const nChanged = computed(() => changes.value.reduce((n, c) => n + c.faces.length, 0))
const srcName = computed(() => people.all.find((p) => p.id === props.src)?.name || `person_${props.src}`)

async function save() {
  open.value = false
  await actions.run(() => api.assign(changes.value))
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90vh] overflow-y-auto sm:max-w-5xl">
      <DialogHeader>
        <DialogTitle>Кто на фото</DialogTitle>
        <DialogDescription>Исправления запоминаются: пересчёт кластеров их не тронет.</DialogDescription>
      </DialogHeader>
      <p v-if="loading" class="text-sm text-muted-foreground">Загружаю лица…</p>
      <p v-else-if="!units.length" class="text-sm text-muted-foreground">На выбранных файлах не найдено ни одного лица.</p>
      <template v-else>
        <div class="flex flex-wrap items-center gap-3">
          <span class="text-sm">Быстро: поставить всем сразу</span>
          <PersonPicker v-model="bulk" none="— никто из знакомых —" placeholder="выбрать…" size="sm" />
          <div v-if="src != null" class="inline-flex rounded-lg border p-0.5">
            <Button size="xs" :variant="onlySrc ? 'secondary' : 'ghost'" @click="onlySrc = true">только лицам «{{ srcName }}»</Button>
            <Button size="xs" :variant="onlySrc ? 'ghost' : 'secondary'" @click="onlySrc = false">всем лицам на фото</Button>
          </div>
          <form class="ml-auto flex gap-2" @submit.prevent="addPerson">
            <Input v-model="newName" placeholder="Нет в списке? Имя" class="h-8 w-44" />
            <Button type="submit" size="sm" variant="outline" :disabled="!newName.trim()"><UserPlus />Добавить</Button>
          </form>
        </div>
        <div class="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-3">
          <div v-for="(u, i) in units.slice(0, SHOW)" :key="u.file + ':' + u.faces[0]"
               class="flex flex-col gap-1.5 rounded-xl border p-2" :class="{ 'border-[var(--ms-pick)]': target[i] !== u.person }">
            <img :src="faceUrl(u.face)" alt="" class="aspect-square w-full rounded-lg object-cover" loading="lazy" />
            <span class="truncate text-xs text-muted-foreground" :title="u.path">{{ u.path.split('/').pop() }}</span>
            <PersonPicker v-model="target[i]" none="— никто из знакомых —" size="sm" block />
          </div>
        </div>
        <p v-if="units.length > SHOW" class="text-sm text-muted-foreground">
          Ещё {{ units.length - SHOW }} лиц не поместились — к ним применится только «поставить всем сразу».
        </p>
      </template>
      <DialogFooter>
        <Button variant="ghost" @click="open = false">Отмена</Button>
        <Button :disabled="!nChanged || actions.busy" @click="save">Сохранить{{ nChanged ? ` (${nChanged} лиц)` : '' }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
