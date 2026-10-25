<script setup lang="ts">
// Stacks of similar shots (series, repeats from several devices): the best first; keep only the best.
import { computed, onMounted } from 'vue'
import { CheckCheck, Copy, RefreshCw, Star } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import ListView from '@/components/ListView.vue'
import KeepConfirm from '@/components/KeepConfirm.vue'
import JobBar from '@/components/JobBar.vue'
import { api } from '@/api'
import { useFileList } from '@/composables/useFileList'
import { useKeep } from '@/composables/useKeep'
import { useQuery } from '@/composables/useQuery'
import { useJobs } from '@/stores/jobs'

const { q, set } = useQuery()
const jobs = useJobs()
onMounted(() => void jobs.check())
const ALL = 'all'
const SORTS = { new: 'Сначала новые', old: 'Сначала старые', big: 'Сначала большие' } as const
const sort = computed(() => (q('sort') in SORTS ? q('sort') : 'new'))

const { data, loading, error } = useFileList(api.stacks, () => ({ sort: sort.value, year: q('year') }))
const keep = useKeep(data)
const busy = computed(() => !!jobs.job?.running)
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-col gap-3 px-6 pb-3 pt-4">
      <div class="flex flex-wrap items-center gap-3">
        <div class="mr-auto">
          <h1 class="text-xl font-semibold tracking-tight">Стопки похожих</h1>
          <p class="text-[13px] text-muted-foreground">Серии и повторы одного момента, в том числе с разных телефонов. Лучший кадр — первым.</p>
        </div>
        <Select :model-value="sort" @update:model-value="(v) => set('sort', String(v), 'new')">
          <SelectTrigger size="sm" class="w-44" aria-label="Порядок"><SelectValue /></SelectTrigger>
          <SelectContent><SelectItem v-for="(l, k) in SORTS" :key="k" :value="k">{{ l }}</SelectItem></SelectContent>
        </Select>
        <Select :model-value="q('year') || ALL" @update:model-value="(v) => set('year', String(v), ALL)">
          <SelectTrigger size="sm" class="w-32" aria-label="Год"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem :value="ALL">Все годы</SelectItem>
            <SelectItem v-for="y in data?.years ?? []" :key="y" :value="y">{{ y }}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="flex flex-wrap items-center gap-2 text-sm">
        <Button size="sm" :variant="data?.state === 'empty' ? 'default' : 'outline'" :disabled="busy" @click="jobs.start('stacks_build')">
          <RefreshCw />{{ data?.state === 'empty' ? 'Собрать стопки' : 'Собрать стопки заново' }}
        </Button>
        <Button v-if="data?.todo" size="sm" :disabled="busy" title="Читает оригиналы (только чтение): резкость кадра и лиц, экспозиция, «красота»"
                @click="jobs.start('stacks_score')"><Star />Оценить качество ({{ data.todo.toLocaleString('ru') }} фото)</Button>
        <Button v-if="data?.groups" size="sm" variant="destructive" @click="keep.plan('best', [])"><CheckCheck />Оставить только лучшие во всех стопках</Button>
        <RouterLink to="/copies" class="ml-auto flex items-center gap-1.5 text-muted-foreground hover:text-foreground"><Copy class="size-4" />Копии одного кадра — в разделе «Копии»</RouterLink>
      </div>
      <JobBar />
      <p v-if="data?.groups" class="text-[13px] text-muted-foreground">
        {{ data.groups.toLocaleString('ru') }} стопок · если оставить только лучшие, уйдёт {{ data.extra.toLocaleString('ru') }}
        <template v-if="data.todo"> · ещё {{ data.todo.toLocaleString('ru') }} фото без оценки</template>
      </p>
      <p v-if="data?.state === 'noclip'" class="text-muted-foreground">Сначала нужно посчитать векторы картинок (это делает ночной разбор).</p>
      <p v-if="data?.state === 'empty'" class="text-muted-foreground">Стопок ещё нет — нажми «Собрать стопки» (около минуты).</p>
    </header>
    <ListView :data="data" :loading="loading" :error="error">
      <template #actions="{ ids }">
        <Button variant="destructive" size="sm" @click="keep.plan('best', ids)"><Star />Оставить лучшие в этих стопках</Button>
        <Button variant="destructive" size="sm" @click="keep.plan('selected', ids)"><CheckCheck />Оставить выбранные, остальное удалить</Button>
      </template>
    </ListView>
    <KeepConfirm v-model:open="keep.open.value" :count="keep.doomed.value.length" :what="keep.what.value" @confirm="keep.confirm" />
  </div>
</template>
