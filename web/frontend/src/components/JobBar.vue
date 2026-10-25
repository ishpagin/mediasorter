<script setup lang="ts">
// Progress of the long job on the Python side (stacks, quality, clusters).
import { Progress } from '@/components/ui/progress'
import { useJobs } from '@/stores/jobs'
const jobs = useJobs()
</script>

<template>
  <div v-if="jobs.job?.running" class="flex items-center gap-3 text-sm">
    <span class="whitespace-nowrap">{{ jobs.label(jobs.job.name) }}… {{ Math.round(jobs.job.progress * 100) }}%</span>
    <Progress :model-value="Math.round(jobs.job.progress * 100)" class="h-1.5 max-w-md" />
  </div>
</template>
