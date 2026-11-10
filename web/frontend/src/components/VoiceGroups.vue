<script setup lang="ts">
// Voices heard in several videos without a name: one card per voice with a few short phrases, "who is it?".
// The answer goes to the whole group. Answered cards leave the list, nothing else reloads.
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { Check, Pause, Play, UserPlus, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import PersonPicker from './PersonPicker.vue'
import { api, faceUrl, originalUrl, personLabel, previewUrl, type ActionResp, type VoiceClip, type VoiceGroup } from '@/api'
import { usePeople } from '@/stores/people'

const people = usePeople()
const groups = ref<VoiceGroup[]>([])
const loading = ref(true)
const failed = ref('')
const shown = ref(20)
const busy = ref(false)
const names = ref<Record<number, string>>({})

onMounted(async () => {
  try {
    groups.value = await api.voiceGroups()
  } catch (e) {
    failed.value = (e as Error).message
  } finally {
    loading.value = false
  }
})

// one player for every phrase: plays just the phrase and stops
const audio = new Audio()
const playing = ref('')
let stopAt = 0
const key = (c: VoiceClip) => `${c.voice}:${c.start}`
function stop() {
  audio.pause()
  playing.value = ''
}
audio.addEventListener('timeupdate', () => { if (playing.value && audio.currentTime >= stopAt) stop() })
audio.addEventListener('ended', stop)
audio.addEventListener('error', () => { if (playing.value) toast.error('Не получилось проиграть'); stop() })
onBeforeUnmount(() => { stop(); audio.removeAttribute('src') })
async function play(c: VoiceClip) {
  const k = key(c)
  const again = playing.value === k
  stop()
  if (again) return
  playing.value = k
  stopAt = Math.min(c.end, c.start + 12) + 0.3
  audio.src = `${originalUrl(c.file)}#t=${Math.max(0, c.start - 0.3)}`
  try {
    await audio.play()
  } catch {
    if (playing.value === k) stop()
  }
}
const secs = (c: VoiceClip) => Math.max(1, Math.round(Math.min(c.end - c.start, 12)))

async function act(g: VoiceGroup, fn: () => Promise<Pick<ActionResp, 'message'>>, done = true) {
  if (busy.value) return
  busy.value = true
  try {
    toast.success((await fn()).message)
    if (done) groups.value = groups.value.filter((x) => x !== g)
  } catch (e) {
    toast.error((e as Error).message)
  } finally {
    busy.value = false
  }
}
const voice = (g: VoiceGroup) => g.clips[0]!.voice // a mark on one voice spreads to its group
const assign = (g: VoiceGroup, p: number | null | undefined) => p && act(g, () => api.voicePerson(voice(g), { person: p }))
const create = (g: VoiceGroup) => act(g, () => api.voicePerson(voice(g), { create: true }))
async function reject(g: VoiceGroup) {
  const p = g.suggest!
  await act(g, () => api.voiceGroupReject(g.cluster, p.id), false)
  g.suggest = undefined
}
async function rename(g: VoiceGroup) {
  const name = names.value[g.cluster]?.trim()
  if (!name || !g.person) return
  await act(g, () => api.personPatch(g.person!, { name }).then(() => ({ message: `Теперь это ${name}` })))
  void people.load(true)
}
const skip = (g: VoiceGroup) => { groups.value = groups.value.filter((x) => x !== g) }
</script>

<template>
  <section class="flex flex-col gap-3">
    <p v-if="failed" class="text-sm text-destructive">Не удалось загрузить голоса: {{ failed }}</p>
    <p v-else-if="loading" class="text-sm text-muted-foreground">Собираю голоса…</p>
    <p v-else-if="!groups.length" class="text-sm text-muted-foreground">Все голоса, которые звучат в нескольких видео, уже подписаны.</p>
    <p v-else class="text-sm text-muted-foreground">
      Голосов без имени: {{ groups.length }}. Послушай пару фраз и скажи, кто это. Ответ сразу применится ко всем видео с этим голосом.
    </p>

    <div v-for="g in groups.slice(0, shown)" :key="g.cluster" class="flex flex-col gap-3 rounded-xl border bg-card p-4">
      <div class="flex items-baseline gap-2">
        <span class="font-medium">{{ g.person ? personLabel({ id: g.person }) : 'Голос без имени' }}</span>
        <span class="text-sm text-muted-foreground">слышно в {{ g.files.toLocaleString('ru') }} видео</span>
      </div>

      <ul class="flex flex-col gap-2">
        <li v-for="c in g.clips" :key="key(c)" class="flex items-center gap-3">
          <button class="relative size-14 shrink-0 overflow-hidden rounded-lg bg-muted"
                  :aria-label="playing === key(c) ? 'Остановить' : 'Послушать'" @click="play(c)">
            <img :src="previewUrl(c.file)" alt="" loading="lazy" class="size-full object-cover" />
            <span class="absolute inset-0 grid place-items-center bg-black/45 text-white">
              <Pause v-if="playing === key(c)" class="size-5" /><Play v-else class="size-5" />
            </span>
          </button>
          <div class="min-w-0">
            <p class="truncate text-sm">"{{ c.text }}"</p>
            <p class="text-xs text-muted-foreground">{{ c.taken || 'без даты' }} · {{ secs(c) }} с</p>
          </div>
        </li>
      </ul>

      <div class="flex flex-wrap items-center gap-2">
        <template v-if="g.suggest">
          <img v-if="g.suggest.face" :src="faceUrl(g.suggest.face)" alt="" loading="lazy" class="size-9 rounded-md object-cover" />
          <span class="text-sm">Это {{ personLabel(g.suggest) }}?</span>
          <Button size="sm" :disabled="busy" @click="assign(g, g.suggest.id)"><Check />Да</Button>
          <Button size="sm" variant="outline" :disabled="busy" @click="reject(g)"><X />Нет</Button>
          <span class="mx-1 h-5 w-px bg-border" />
        </template>
        <div class="w-56"><PersonPicker size="sm" block placeholder="Кто это?" @update:model-value="assign(g, $event)" /></div>
        <form v-if="g.person" class="flex gap-2" @submit.prevent="rename(g)">
          <Input v-model="names[g.cluster]" placeholder="или дать имя" class="h-8 w-40" />
          <Button size="sm" variant="outline" type="submit" :disabled="busy || !names[g.cluster]?.trim()">Сохранить</Button>
        </form>
        <Button v-else size="sm" variant="outline" :disabled="busy" @click="create(g)"><UserPlus />Новый человек</Button>
        <Button size="sm" variant="ghost" class="ml-auto" @click="skip(g)">Пропустить</Button>
      </div>
    </div>
    <Button v-if="groups.length > shown" variant="outline" class="self-start" @click="shown += 20">Ещё {{ Math.min(20, groups.length - shown) }}</Button>
  </section>
</template>
