<script setup lang="ts">
// Bulk deletion of the stacks / copies pages still asks first; Ctrl+Z brings everything back too.
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter,
  AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'

defineProps<{ count: number; what: string }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ confirm: [] }>()
</script>

<template>
  <AlertDialog v-model:open="open">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ count ? `Удалить ${count.toLocaleString('ru')} файлов?` : 'Нечего удалять' }}</AlertDialogTitle>
        <AlertDialogDescription>
          {{ what }}. {{ count ? 'Файлы уйдут в карантин, Ctrl+Z вернёт их.' : 'В этих группах ещё нет оценки качества или всё уже оставлено.' }}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>Отмена</AlertDialogCancel>
        <AlertDialogAction v-if="count" class="bg-destructive text-white hover:bg-destructive/90" @click="emit('confirm')">Удалить</AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
