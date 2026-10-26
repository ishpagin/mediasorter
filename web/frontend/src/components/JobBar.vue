<script setup lang="ts">
// Progress of the long job of the analysis (scan, people, stacks, quality).
import { Progress } from '@/components/ui/progress'
import { useJobs } from '@/stores/jobs'
const jobs = useJobs()
</script>

<template>
  <div v-if="jobs.job?.running" class="flex flex-col gap-1.5 text-sm" aria-live="polite">
    <span class="whitespace-nowrap">{{ jobs.label(jobs.job.name) }}… {{ Math.round(jobs.job.progress * 100) }}%</span>
    <Progress :model-value="Math.round(jobs.job.progress * 100)" class="h-1.5 max-w-md" />
    <span v-if="jobs.job.message" class="truncate text-xs text-muted-foreground" :title="jobs.job.message">{{ jobs.job.message }}</span>
  </div>
</template>
