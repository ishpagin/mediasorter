<script setup lang="ts">
// Search by a description (Russian or English): SigLIP2 compares the text with every picture.
import { ref, watch } from 'vue'
import { watchDebounced } from '@vueuse/core'
import { Search } from '@lucide/vue'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import ListView from '@/components/ListView.vue'
import { api } from '@/api'
import { useFileList } from '@/composables/useFileList'
import { useQuery } from '@/composables/useQuery'

const { q, set, route } = useQuery()
const text = ref(q('q'))
watchDebounced(text, (v) => set('q', v.trim()), { debounce: 500 })
watch(() => route.query.q, (v) => { if ((v ?? '') !== text.value.trim()) text.value = (v as string) ?? '' })
const examples = ['закат на море', 'торт со свечами', 'кошка на диване', 'паспорт', 'снег в лесу', 'чек из магазина']

const { data, loading, error } = useFileList(api.search, () =>
  (q('q') ? { q: q('q'), nopeople: q('nopeople') } : null))
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-wrap items-center gap-3 px-6 pb-3 pt-4">
      <h1 class="mr-3 text-xl font-semibold tracking-tight">Поиск</h1>
      <div class="relative min-w-72 flex-1">
        <Search class="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="text" autofocus placeholder="Что ищем: закат на море, торт со свечами, паспорт…" class="h-10 pl-9 text-base" aria-label="Что ищем" />
      </div>
      <label class="flex items-center gap-2 text-sm">
        <Switch :model-value="q('nopeople') === '1'" @update:model-value="(v) => set('nopeople', v)" />Только без людей
      </label>
    </header>
    <div v-if="!q('q')" class="flex flex-col gap-3 px-6 py-8 text-muted-foreground">
      <p>Опиши словами, что на фото — на русском или английском. Например:</p>
      <div class="flex flex-wrap gap-2">
        <button v-for="e in examples" :key="e" class="rounded-full border px-3 py-1 text-sm hover:bg-accent hover:text-foreground" @click="text = e">{{ e }}</button>
      </div>
    </div>
    <ListView v-else :data="data" :loading="loading" :error="error" empty="Ничего похожего не нашлось." />
  </div>
</template>
