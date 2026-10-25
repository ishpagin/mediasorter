<script setup lang="ts">
// Copies of one shot (X.jpg and X(0).jpg, HEIC and its JPEG, RAW + JPEG, resized and re-sent): keep one.
import { computed, onMounted } from 'vue'
import { CheckCheck, Search, Star } from '@lucide/vue'
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
const KINDS = [{ v: 'all', l: 'все' }, { v: 'same', l: 'точные дубли', t: 'Одинаковые разрешение и размер файла: удалять безопаснее всего' }, { v: 'diff', l: 'разные форматы и размеры' }]
const kind = computed(() => (KINDS.some((k) => k.v === q('kind')) ? q('kind') : 'all'))
const keepPref = computed(() => (q('keep') === 'original' ? 'original' : 'jpeg'))

const { data, loading, error } = useFileList(api.copies, () => ({ kind: kind.value === 'all' ? '' : kind.value, keep: keepPref.value, year: q('year') }))
const keep = useKeep(data)
const gb = computed(() => ((data.value?.bytes ?? 0) / 2 ** 30).toFixed(1))
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-col gap-3 px-6 pb-3 pt-4">
      <div class="flex flex-wrap items-center gap-3">
        <div class="mr-auto">
          <h1 class="text-xl font-semibold tracking-tight">Копии одного кадра</h1>
          <p class="text-[13px] text-muted-foreground">X.jpg и X(0).jpg, HEIC и его JPEG, RAW + JPEG, уменьшенные и пересланные копии. Первая в группе остаётся.</p>
        </div>
        <Select :model-value="q('year') || ALL" @update:model-value="(v) => set('year', String(v), ALL)">
          <SelectTrigger size="sm" class="w-32" aria-label="Год"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem :value="ALL">Все годы</SelectItem>
            <SelectItem v-for="y in data?.years ?? []" :key="y" :value="y">{{ y }}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="flex flex-wrap items-center gap-3 text-sm">
        <div class="inline-flex rounded-lg border p-0.5">
          <Button v-for="k in KINDS" :key="k.v" size="xs" :variant="kind === k.v ? 'secondary' : 'ghost'" :title="k.t" @click="set('kind', k.v, 'all')">{{ k.l }}</Button>
        </div>
        <span class="text-muted-foreground">Если кадр одинаковый, оставлять</span>
        <div class="inline-flex rounded-lg border p-0.5">
          <Button size="xs" :variant="keepPref === 'jpeg' ? 'secondary' : 'ghost'" @click="set('keep', 'jpeg', 'jpeg')">JPEG (открывается везде)</Button>
          <Button size="xs" :variant="keepPref === 'original' ? 'secondary' : 'ghost'" @click="set('keep', 'original', 'jpeg')">оригинал HEIC / RAW</Button>
        </div>
        <Button v-if="data?.groups" size="sm" variant="destructive" class="ml-auto" @click="keep.plan('best', [])"><CheckCheck />Оставить по одному во всех группах</Button>
      </div>
      <JobBar />
      <p v-if="data?.groups" class="text-[13px] text-muted-foreground">
        {{ data.groups.toLocaleString('ru') }} групп · если оставить по одному, уйдёт {{ data.extra.toLocaleString('ru') }} файлов ({{ gb }} ГБ)
      </p>
      <p v-if="data?.state === 'noclip'" class="text-muted-foreground">Сначала нужно посчитать векторы картинок (это делает ночной разбор).</p>
      <div v-if="data?.state === 'empty'" class="flex items-center gap-3">
        <span class="text-muted-foreground">Копии ещё не искали.</span>
        <Button size="sm" :disabled="!!jobs.job?.running" @click="jobs.start('stacks_build')"><Search />Найти копии</Button>
      </div>
    </header>
    <ListView :data="data" :loading="loading" :error="error">
      <template #actions="{ ids }">
        <Button variant="destructive" size="sm" @click="keep.plan('best', ids)"><Star />Оставить по одному в этих группах</Button>
        <Button variant="destructive" size="sm" @click="keep.plan('selected', ids)"><CheckCheck />Оставить выбранные, остальные копии удалить</Button>
      </template>
    </ListView>
    <KeepConfirm v-model:open="keep.open.value" :count="keep.doomed.value.length" :what="keep.what.value" @confirm="keep.confirm" />
  </div>
</template>
