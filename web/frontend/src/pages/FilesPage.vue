<script setup lang="ts">
// Files pages: all files, without people, only strangers, videos. Filters live in the URL query.
import { computed, ref, watch } from 'vue'
import { watchDebounced } from '@vueuse/core'
import { MapPin } from '@lucide/vue'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import ListView from '@/components/ListView.vue'
import { api } from '@/api'
import { useFileList } from '@/composables/useFileList'
import { useQuery } from '@/composables/useQuery'

const props = defineProps<{ view: string; title: string }>()
const { q, set, route } = useQuery()

const ALL = 'all' // "no filter" in the selects (an empty value is reserved by them)
const place = ref(q('place'))
watchDebounced(place, (v) => set('place', v.trim()), { debounce: 350 })
watch(() => route.query.place, (v) => { if ((v ?? '') !== place.value.trim()) place.value = (v as string) ?? '' })

const { data, loading, error } = useFileList(api.files, () => ({
  view: props.view, year: q('year'), cat: q('cat'), place: q('place'),
}))
const cats = computed(() => data.value?.cats ?? [])
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-wrap items-center gap-3 px-6 pb-3 pt-4">
      <h1 class="mr-auto text-xl font-semibold tracking-tight">{{ title }}</h1>
      <Select :model-value="q('year') || ALL" @update:model-value="(v) => set('year', String(v), ALL)">
        <SelectTrigger size="sm" class="w-36" aria-label="Год"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem :value="ALL">Все годы</SelectItem>
          <SelectItem v-for="y in data?.years ?? []" :key="y" :value="y">{{ y }}</SelectItem>
        </SelectContent>
      </Select>
      <Select v-if="cats.length" :model-value="q('cat') || ALL" @update:model-value="(v) => set('cat', String(v), ALL)">
        <SelectTrigger size="sm" class="w-60" aria-label="Сюжет"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem :value="ALL">Все сюжеты</SelectItem>
          <SelectItem v-for="c in cats" :key="c.label" :value="c.label">
            {{ c.label }} <span class="text-muted-foreground">{{ c.n.toLocaleString('ru') }}</span>
          </SelectItem>
        </SelectContent>
      </Select>
      <div class="relative">
        <MapPin class="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="place" placeholder="Место: город или страна" class="h-8 w-60 pl-8" aria-label="Место" />
      </div>
    </header>
    <ListView :data="data" :loading="loading" :error="error" :videos-page="view === 'videos'" />
  </div>
</template>
