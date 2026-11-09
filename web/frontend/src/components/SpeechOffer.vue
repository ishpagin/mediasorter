<script setup lang="ts">
// The offer to turn speech recognition on: while it is off here (no shared service, model not downloaded) and there are videos.
import { computed } from 'vue'
import { AudioLines, Download, RefreshCw } from '@lucide/vue'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter,
  AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { useActions } from '@/stores/actions'
import { useJobs } from '@/stores/jobs'

const actions = useActions()
const jobs = useJobs()
const videos = computed(() => (actions.meta?.speech === '' ? actions.meta.videos : 0))
const all = computed(() => (actions.meta?.speech === 'remote' || actions.meta?.speech === 'local' ? actions.meta.videos : 0))
const downloading = computed(() => !!jobs.job?.running && jobs.job.name === 'speech_setup')
</script>

<template>
  <section v-if="videos > 0" class="mx-6 mb-3 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-xl border bg-card px-4 py-3 text-sm">
    <AudioLines class="size-5 shrink-0 text-muted-foreground" />
    <div class="min-w-64 flex-1">
      <p class="font-medium">Искать и по словам в видео</p>
      <p class="text-muted-foreground">
        Программа распознает речь в {{ videos.toLocaleString('ru') }} видео: под видео появится текст, а поиск найдёт,
        где что сказали. Понимает русский. Скачать нужно 225 МБ, дальше всё работает без интернета.
      </p>
    </div>
    <Button size="sm" :disabled="!!jobs.job?.running" @click="jobs.start('speech_setup')">
      <Download />{{ downloading ? 'Скачивается…' : 'Скачать и распознать' }}
    </Button>
  </section>

  <section v-else-if="all > 0" class="mx-6 mb-3 flex justify-end">
    <AlertDialog>
      <AlertDialogTrigger as-child>
        <Button size="sm" variant="outline" :disabled="!!jobs.job?.running"><RefreshCw />Пересчитать речь во всех видео</Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Пересчитать речь во всех видео?</AlertDialogTitle>
          <AlertDialogDescription>
            Речь во всех {{ all.toLocaleString('ru') }} видео распознается заново. Это займёт несколько часов, ручная разметка голосов сохранится.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Отмена</AlertDialogCancel>
          <AlertDialogAction @click="jobs.start('speech_all')">Пересчитать</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </section>
</template>
