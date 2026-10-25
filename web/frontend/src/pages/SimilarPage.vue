<script setup lang="ts">
// Pairs of people with similar mean faces: often one person split in two (years, glasses, beard). Relatives too:
// "different people" hides the pair for good.
import { computed, ref, watch } from 'vue'
import { useIntersectionObserver } from '@vueuse/core'
import { Merge, UserX } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Slider } from '@/components/ui/slider'
import { api, faceUrl, personLabel, type SimilarPair } from '@/api'
import { useQuery } from '@/composables/useQuery'
import { useActions } from '@/stores/actions'

const { q, set } = useQuery()
const actions = useActions()
const thr = computed(() => Number(q('min', '0.45')) || 0.45)
const minFiles = computed(() => Number(q('files', '3')) || 3)
const slider = ref([thr.value])
watch(thr, (v) => { slider.value = [v] })

const pairs = ref<SimilarPair[]>([])
const loading = ref(false)
const shown = ref(40)
const sentinel = ref<HTMLElement>()
useIntersectionObserver(sentinel, ([e]) => { if (e?.isIntersecting) shown.value += 40 }, { rootMargin: '600px' })

async function load() {
  loading.value = true
  try {
    pairs.value = await api.similar({ min: String(thr.value), files: String(minFiles.value) })
  } finally {
    loading.value = false
  }
}
watch(() => [thr.value, minFiles.value, actions.version], () => { shown.value = 40; void load() }, { immediate: true })

async function merge(p: SimilarPair) {
  await actions.run(() => api.merge([p.a.id, p.b.id]), false)
}
async function different(p: SimilarPair) {
  await actions.run(() => api.notSame(p.a.id, p.b.id), false)
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-wrap items-center gap-6 px-6 pb-3 pt-4">
      <div class="mr-auto">
        <h1 class="text-xl font-semibold tracking-tight">Похожие люди</h1>
        <p class="text-[13px] text-muted-foreground">Часто это один человек, разбитый на два. Бывают и родственники — для них «Это разные люди».</p>
      </div>
      <label class="flex w-72 items-center gap-3 text-sm">Похожесть от {{ slider[0].toFixed(2) }}
        <Slider v-model="slider" :min="0.3" :max="0.8" :step="0.01" class="flex-1" @value-commit="(v: number[]) => set('min', v[0].toFixed(2), '0.45')" />
      </label>
      <label class="flex items-center gap-2 text-sm">Мин. файлов у каждого
        <Input type="number" min="1" :model-value="minFiles" class="h-8 w-20"
               @update:model-value="(v) => set('files', Math.max(1, Number(v) || 1), '3')" />
      </label>
    </header>
    <div class="flex-1 overflow-y-auto px-6 pb-8">
      <p class="pb-3 text-sm text-muted-foreground">{{ loading ? 'Сравниваю людей…' : `Найдено пар: ${pairs.length}` }}</p>
      <div class="flex flex-col gap-3">
        <div v-for="p in pairs.slice(0, shown)" :key="p.a.id + ':' + p.b.id"
             class="grid grid-cols-[1fr_auto_1fr] items-center gap-4 rounded-xl border bg-card p-3">
          <div v-for="(side, k) in [{ person: p.a, faces: p.facesA }, { person: p.b, faces: p.facesB }]" :key="k"
               :class="k === 1 ? 'order-3' : ''" class="flex flex-col gap-2">
            <RouterLink :to="{ path: '/person', query: { id: side.person.id } }" class="font-medium hover:underline">
              {{ personLabel(side.person) }} <span class="font-normal text-muted-foreground">· {{ side.person.n }} файлов</span>
            </RouterLink>
            <div class="flex gap-1.5">
              <img v-for="f in side.faces" :key="f" :src="faceUrl(f)" alt="" loading="lazy" class="size-20 rounded-lg object-cover" />
            </div>
          </div>
          <div class="order-2 flex flex-col items-center gap-2">
            <span class="text-2xl font-semibold tabular-nums">{{ p.sim.toFixed(2) }}</span>
            <Button size="sm" :disabled="actions.busy" @click="merge(p)"><Merge />Объединить</Button>
            <Button size="sm" variant="ghost" :disabled="actions.busy" @click="different(p)"><UserX />Это разные люди</Button>
          </div>
        </div>
      </div>
      <div ref="sentinel" class="h-px" />
    </div>
  </div>
</template>
