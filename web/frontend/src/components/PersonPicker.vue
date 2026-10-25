<script setup lang="ts">
// Pick a person by typing a name (Cyrillic, any case) or a number; only the first 50 matches are listed.
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { ChevronsUpDown, Check } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { personLabel, type Person } from '@/api'
import { usePeople } from '@/stores/people'

const props = withDefaults(defineProps<{
  placeholder?: string
  exclude?: number[]
  none?: string // a "nobody" choice (value null) with this label
  size?: 'sm' | 'default'
  block?: boolean // fill the width of a narrow parent (a card)
}>(), { placeholder: 'Выбери человека', exclude: () => [], none: '', size: 'default', block: false })
// undefined = nothing chosen yet (shows the placeholder); null = "nobody" (the `none` choice)
const model = defineModel<number | null | undefined>({ default: undefined })

const people = usePeople()
onMounted(() => void people.load())
const open = ref(false)
const query = ref('')
const input = ref<InstanceType<typeof Input>>()

const current = computed(() => people.all.find((p) => p.id === model.value))
const matches = computed(() => {
  const q = query.value.trim().toLowerCase()
  const out: Person[] = []
  for (const p of people.all) {
    if (props.exclude.includes(p.id)) continue
    if (q && !personLabel(p).toLowerCase().includes(q) && String(p.id) !== q) continue
    out.push(p)
    if (out.length >= 50) break
  }
  return out
})
watch(open, async (o) => {
  if (!o) return
  query.value = ''
  await nextTick()
  ;(input.value?.$el as HTMLInputElement | undefined)?.focus()
})
function pick(id: number | null) {
  model.value = id
  open.value = false
}
</script>

<template>
  <Popover v-model:open="open">
    <PopoverTrigger as-child>
      <Button variant="outline" :size="size" class="justify-between font-normal" :class="block ? 'w-full min-w-0' : 'min-w-48'">
        <span class="truncate" :class="{ 'text-muted-foreground': model === undefined }">{{
          model === undefined ? placeholder : model === null ? (none || placeholder) : current ? personLabel(current) : `#${model}` }}</span>
        <ChevronsUpDown class="opacity-50" />
      </Button>
    </PopoverTrigger>
    <PopoverContent class="w-72 p-1" align="start">
      <Input ref="input" v-model="query" placeholder="Имя или номер" class="mb-1 h-8"
             @keydown.enter.prevent="matches[0] && pick(matches[0].id)" />
      <div class="max-h-72 overflow-y-auto">
        <button v-if="none" class="item" @click="pick(null)">{{ none }}<Check v-if="model === null" class="ml-auto size-4" /></button>
        <button v-for="p in matches" :key="p.id" class="item" @click="pick(p.id)">
          <span class="truncate">{{ personLabel(p) }}</span>
          <span class="ml-auto text-xs text-muted-foreground">{{ p.n }}</span>
          <Check v-if="model === p.id" class="size-4" />
        </button>
        <p v-if="!matches.length" class="px-2 py-3 text-sm text-muted-foreground">Никого не нашлось</p>
      </div>
    </PopoverContent>
  </Popover>
</template>

<style scoped>
.item {
  display: flex; width: 100%; align-items: center; gap: 8px; padding: 6px 8px; border-radius: 6px;
  font-size: 14px; text-align: left; cursor: pointer;
}
.item:hover { background: var(--accent); }
</style>
