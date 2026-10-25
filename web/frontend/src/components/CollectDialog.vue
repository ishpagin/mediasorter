<script setup lang="ts">
// "To collection…": a person on the selected files (their own collection), any collection, or a new one;
// move or copy (the default comes from the sidebar switch).
import { computed, ref, watch } from 'vue'
import { Bookmark, Plus, User } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api } from '@/api'
import { useActions } from '@/stores/actions'
import { useSettings } from '@/stores/settings'

const props = defineProps<{ ids: number[] }>()
const open = defineModel<boolean>('open', { required: true })
const actions = useActions()
const settings = useSettings()

const copy = ref(settings.copyMode)
const people = ref<{ id: number; name: string; n: number }[]>([])
const chosen = ref<string>('')
const newName = ref('')
const verb = computed(() => (copy.value ? 'Скопировать' : 'Переместить'))

watch(open, async (o) => {
  if (!o) return
  copy.value = settings.copyMode
  chosen.value = ''
  newName.value = ''
  people.value = await api.collectPeople(props.ids)
}, { immediate: true })

async function go(target: { collection?: number; person?: number; new?: string }) {
  open.value = false
  await actions.collect(props.ids, target, copy.value)
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-xl">
      <DialogHeader>
        <DialogTitle>В подборку: {{ ids.length.toLocaleString('ru') }} {{ ids.length === 1 ? 'файл' : 'файлов' }}</DialogTitle>
        <DialogDescription>Скопировать — оригинал останется в архиве. Переместить — файл уедет в папку подборки.</DialogDescription>
      </DialogHeader>
      <div class="inline-flex w-fit rounded-lg border p-0.5">
        <Button size="sm" :variant="copy ? 'ghost' : 'secondary'" @click="copy = false">Переместить</Button>
        <Button size="sm" :variant="copy ? 'secondary' : 'ghost'" @click="copy = true">Скопировать</Button>
      </div>

      <section v-if="people.length" class="flex flex-col gap-2">
        <h4 class="text-sm font-medium">Люди на фото — в подборку человека (создаётся сама)</h4>
        <div class="flex flex-wrap gap-2">
          <Button v-for="p in people" :key="p.id" variant="outline" size="sm" @click="go({ person: p.id })">
            <User />{{ p.name }}<span v-if="ids.length > 1" class="text-muted-foreground">{{ p.n }}/{{ ids.length }}</span>
          </Button>
        </div>
      </section>

      <section v-if="actions.meta?.collections.length" class="flex flex-col gap-2">
        <h4 class="text-sm font-medium">Любая подборка</h4>
        <div class="flex gap-2">
          <Select v-model="chosen">
            <SelectTrigger class="flex-1"><SelectValue placeholder="Выбери подборку" /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="c in actions.meta.collections" :key="c.id" :value="String(c.id)">{{ c.name }}</SelectItem>
            </SelectContent>
          </Select>
          <Button :disabled="!chosen" @click="go({ collection: Number(chosen) })"><Bookmark />{{ verb }}</Button>
        </div>
      </section>

      <form class="flex flex-col gap-2" @submit.prevent="newName.trim() && go({ new: newName.trim() })">
        <h4 class="text-sm font-medium">Новая подборка</h4>
        <div class="flex gap-2">
          <Input v-model="newName" placeholder="Например: Маме, с моря 2024" class="flex-1" />
          <Button type="submit" :disabled="!newName.trim()"><Plus />Создать и {{ verb.toLowerCase() }}</Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>
</template>
