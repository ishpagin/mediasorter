<script setup lang="ts">
// Voices on the "similar people" page: pairs that sound alike (one person or not) and voices with a guessed person.
// The program never merges people by itself: a contradiction (409) goes to the user.
import { ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { Check, Merge, UserX, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import {
  AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { api, faceUrl, originalUrl, personLabel, type ActionResp, type Person, type VoiceConflict, type VoicePairs, type VoicePick } from '@/api'
import { useActions } from '@/stores/actions'

const actions = useActions()
const data = ref<VoicePairs>({ pairs: [], suggests: [] })
const loading = ref(false)
const failed = ref('')

async function load() {
  loading.value = true
  failed.value = ''
  try {
    data.value = await api.voicePairs()
  } catch (e) {
    failed.value = (e as Error).message
  } finally {
    loading.value = false
  }
}
watch(() => actions.version, () => void load(), { immediate: true })

const src = (v: VoicePick) => `${originalUrl(v.file)}#t=${v.start},${v.end}`
const who = (v: VoicePick) => (v.person ? personLabel({ id: v.person, name: v.name }) : 'без человека')

const conflict = ref<{ a: VoicePick; b: VoicePick; people: VoiceConflict } | null>(null)
const choosing = ref(false) // second step: which voice is not his
const open = ref(false)
watch(open, (o) => { if (!o) choosing.value = false })

async function same(a: VoicePick, b: VoicePick) {
  if (actions.busy) return
  try {
    const r = await api.voicesSame(a.voice, b.voice)
    if ('conflict' in r) {
      conflict.value = { a, b, people: r.conflict }
      open.value = true
      return
    }
    await actions.run(() => Promise.resolve(r as ActionResp), false)
  } catch (e) {
    toast.error((e as Error).message)
  }
}
const different = (a: VoicePick, b: VoicePick) => actions.run(() => api.voicesNotSame(a.voice, b.voice), false)

async function mergePeople() {
  const c = conflict.value
  open.value = false
  if (c) await actions.run(() => api.merge([c.people.a.id, c.people.b.id]), false)
}
async function unlink(v: VoicePick) {
  open.value = false
  await actions.run(() => api.voicePerson(v.voice, { clear: true }), false)
}
const yes = (v: VoicePick, p: Person) => actions.run(() => api.voicePerson(v.voice, { person: p.id }), false)
const no = (v: VoicePick, p: Person) => actions.run(() => api.voiceReject(v.voice, p.id), false)
</script>

<template>
  <section class="flex flex-col gap-3 pt-8">
    <h2 class="text-lg font-semibold tracking-tight">Голоса</h2>
    <p v-if="failed" class="text-sm text-destructive">Не удалось загрузить голоса: {{ failed }}</p>
    <p v-else-if="loading && !data.pairs.length && !data.suggests.length" class="text-sm text-muted-foreground">Сравниваю голоса…</p>
    <p v-else-if="!data.pairs.length && !data.suggests.length" class="text-sm text-muted-foreground">Похожих голосов пока нет.</p>

    <div v-for="p in data.pairs" :key="p.a.voice + ':' + p.b.voice"
         class="grid grid-cols-[1fr_auto_1fr] items-center gap-4 rounded-xl border bg-card p-3">
      <div v-for="(v, k) in [p.a, p.b]" :key="k" :class="k === 1 ? 'order-3' : ''" class="flex flex-col gap-2">
        <span class="text-sm font-medium">{{ who(v) }}</span>
        <audio controls preload="none" :src="src(v)" class="w-full" />
      </div>
      <div class="order-2 flex flex-col items-center gap-2">
        <span class="text-2xl font-semibold tabular-nums">{{ p.sim.toFixed(2) }}</span>
        <Button size="sm" :disabled="actions.busy" @click="same(p.a, p.b)"><Merge />Один человек</Button>
        <Button size="sm" variant="ghost" :disabled="actions.busy" @click="different(p.a, p.b)"><UserX />Разные</Button>
      </div>
    </div>

    <template v-if="data.suggests.length">
      <h2 class="pt-4 text-lg font-semibold tracking-tight">Чей это голос</h2>
      <div v-for="s in data.suggests" :key="s.voice.voice" class="flex flex-wrap items-center gap-4 rounded-xl border bg-card p-3">
        <audio controls preload="none" :src="src(s.voice)" class="w-80 max-w-full" />
        <div class="flex items-center gap-2">
          <img v-if="s.person.face" :src="faceUrl(s.person.face)" alt="" loading="lazy" class="size-12 rounded-lg object-cover" />
          <RouterLink :to="{ path: '/person', query: { id: s.person.id } }" class="font-medium hover:underline">{{ personLabel(s.person) }}</RouterLink>
        </div>
        <div class="ml-auto flex gap-2">
          <Button size="sm" :disabled="actions.busy" @click="yes(s.voice, s.person)"><Check />Да, это он</Button>
          <Button size="sm" variant="ghost" :disabled="actions.busy" @click="no(s.voice, s.person)"><X />Нет</Button>
        </div>
      </div>
    </template>
  </section>

  <AlertDialog v-model:open="open">
    <AlertDialogContent v-if="conflict">
      <AlertDialogHeader>
        <AlertDialogTitle>Эти голоса привязаны к разным людям</AlertDialogTitle>
        <AlertDialogDescription>
          Эти голоса привязаны к разным людям: {{ personLabel(conflict.people.a) }} и {{ personLabel(conflict.people.b) }}. Где ошибка?
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter class="flex-wrap">
        <AlertDialogCancel>Отмена</AlertDialogCancel>
        <template v-if="!choosing">
          <Button variant="outline" @click="choosing = true">Голос не его</Button>
          <Button @click="mergePeople">Это один человек</Button>
        </template>
        <template v-else>
          <Button variant="outline" @click="unlink(conflict.a)">Отвязать голос ({{ personLabel(conflict.people.a) }})</Button>
          <Button variant="outline" @click="unlink(conflict.b)">Отвязать голос ({{ personLabel(conflict.people.b) }})</Button>
        </template>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
