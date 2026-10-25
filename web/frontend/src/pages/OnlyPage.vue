<script setup lang="ts">
// Files where only the chosen people are in the frame (and nobody else; strangers in the background optional).
import { computed, onMounted, ref } from 'vue'
import { X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import ListView from '@/components/ListView.vue'
import PersonPicker from '@/components/PersonPicker.vue'
import { api, personLabel } from '@/api'
import { useFileList } from '@/composables/useFileList'
import { useQuery } from '@/composables/useQuery'
import { usePeople } from '@/stores/people'

const { q, set } = useQuery()
const people = usePeople()
onMounted(() => void people.load())

const ids = computed(() => q('ids').split(',').map(Number).filter(Boolean))
const all = computed(() => q('all') === '1')
const strangers = computed(() => q('strangers') === '1')
const whole = computed(() => q('whole') !== '0')
const adding = ref<number | null | undefined>(undefined)

function add(id: number | null | undefined) {
  if (id != null && !ids.value.includes(id)) set('ids', [...ids.value, id].join(','))
  adding.value = undefined
}
const remove = (id: number) => set('ids', ids.value.filter((x) => x !== id).join(','))
const nameOf = (id: number) => { const p = people.all.find((x) => x.id === id); return p ? personLabel(p) : `#${id}` }

const { data, loading, error } = useFileList(api.only, () => (ids.value.length
  ? { ids: ids.value.join(','), all: all.value ? '1' : '', strangers: strangers.value ? '1' : '', whole: whole.value ? '' : '0' }
  : null))
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex flex-col gap-3 px-6 pb-3 pt-4">
      <h1 class="text-xl font-semibold tracking-tight">Только выбранные люди</h1>
      <div class="flex flex-wrap items-center gap-2">
        <Button v-for="id in ids" :key="id" size="sm" variant="secondary" @click="remove(id)">{{ nameOf(id) }}<X /></Button>
        <PersonPicker :model-value="adding" placeholder="Добавить человека" :exclude="ids" size="sm" @update:model-value="add" />
      </div>
      <div class="flex flex-wrap items-center gap-4 text-sm">
        <label class="flex items-center gap-2" title="Только файлы, где есть каждый из выбранных (например, вы вдвоём)">
          <Switch :model-value="all" @update:model-value="(v) => set('all', v)" />Все выбранные в кадре
        </label>
        <label class="flex items-center gap-2" title="Выключено: в кадре нет ни одного лица, кроме выбранных людей">
          <Switch :model-value="strangers" @update:model-value="(v) => set('strangers', v)" />Незнакомые на фоне допустимы
        </label>
        <label class="flex items-center gap-2">
          <Switch :model-value="whole" @update:model-value="(v) => set('whole', v ? '' : '0')" />Целые фото
        </label>
      </div>
    </header>
    <p v-if="!ids.length" class="px-6 py-10 text-muted-foreground">Выбери одного или нескольких людей.</p>
    <ListView v-else :data="data" :loading="loading" :error="error" />
  </div>
</template>
