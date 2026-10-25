<script setup lang="ts">
// The common part of every file list: notes about hidden files, a loading bar, errors, the empty state, the grid.
import { ImageOff } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import FileGrid from './FileGrid.vue'
import type { FilesResp } from '@/api'
import { useSettings } from '@/stores/settings'

// collect defaults to true: an absent boolean prop would be false in Vue, hiding the collection buttons
withDefaults(defineProps<{
  data: FilesResp | null
  loading: boolean
  error: string
  videosPage?: boolean // the videos page: never says "videos hidden"
  person?: number | null
  collect?: boolean
  empty?: string
}>(), { collect: true, person: null, empty: undefined })
const settings = useSettings()
</script>

<template>
  <div v-if="data && ((data.hiddenVideo && !videosPage) || data.hiddenMoved)"
       class="flex flex-wrap items-center gap-6 px-6 pb-2 text-[13px] text-muted-foreground">
    <span v-if="data.hiddenVideo && !videosPage">
      Скрыто видео: {{ data.hiddenVideo.toLocaleString('ru') }}
      <Button variant="link" size="xs" class="px-1" @click="settings.showVideo = true">показать</Button>
    </span>
    <span v-if="data.hiddenMoved">
      Скрыто перемещённых в подборки: {{ data.hiddenMoved.toLocaleString('ru') }}
      <Button variant="link" size="xs" class="px-1" @click="settings.hideMoved = false">показать</Button>
    </span>
  </div>
  <div class="relative mx-6 h-0.5 shrink-0 overflow-hidden" :class="{ 'opacity-0': !loading }">
    <div class="loading-bar absolute inset-y-0 w-1/3 rounded bg-[var(--ms-pick)]" />
  </div>
  <p v-if="error" class="px-6 text-destructive">{{ error }}</p>
  <div v-else-if="data && !data.items.length && !loading" class="flex flex-col items-center gap-2 px-6 py-16 text-muted-foreground">
    <ImageOff class="size-8" />
    <p>{{ empty ?? 'Под эти фильтры ничего не нашлось.' }}</p>
  </div>
  <FileGrid v-else-if="data" :items="data.items" :person="person" :collect="collect">
    <template #actions="{ ids }"><slot name="actions" :ids="ids" /></template>
  </FileGrid>
</template>

<style scoped>
.loading-bar { animation: slide 1.1s var(--ms-ease) infinite; }
@keyframes slide { from { left: -33%; } to { left: 100%; } }
</style>
