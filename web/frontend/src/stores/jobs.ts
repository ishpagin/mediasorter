import { defineStore } from 'pinia'
import { ref } from 'vue'
import { toast } from 'vue-sonner'
import { api, type Job } from '@/api'
import { useActions } from './actions'

const LABELS: Record<string, string> = {
  scan: 'Разбор фото', cluster: 'Пересчёт кластеров', tag: 'Сюжеты', stacks_build: 'Сборка стопок',
  stacks_score: 'Оценка качества',
}

// the long job of the analysis (one at a time): start, follow the progress, refresh lists at the end
export const useJobs = defineStore('jobs', () => {
  const job = ref<Job | null>(null)
  const actions = useActions()
  let timer: ReturnType<typeof setTimeout> | null = null

  async function poll() {
    try {
      job.value = await api.job()
    } catch {
      job.value = null
      return
    }
    if (job.value.running) { timer = setTimeout(poll, 1500); return }
    timer = null
    const label = LABELS[job.value.name ?? ''] ?? 'Задача'
    if (job.value.error) toast.error(`${label}: ошибка`, { description: job.value.error })
    else if (job.value.name) toast.success(job.value.message || `${label}: готово`)
    actions.version++
  }

  async function start(name: string) {
    try {
      job.value = await api.startJob(name)
      if (!timer) timer = setTimeout(poll, 1000)
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  async function check() { // a job started before this page was opened
    try {
      job.value = await api.job()
      if (job.value.running && !timer) timer = setTimeout(poll, 1500)
    } catch { /* the analysis side may be restarting */ }
  }

  const label = (name: string | null) => LABELS[name ?? ''] ?? 'Задача'
  return { job, start, check, label }
})
