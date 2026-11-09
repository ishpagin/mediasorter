<script setup lang="ts">
// What is said in a video: phrases in paragraphs (a pause over 2 s, a new line of a dialogue or ~280 characters start one), the phrase
// being said now highlighted, a click plays from it. Words of the search (or of the box here) are marked.
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { ChevronDown, ChevronUp, Copy, Search } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { api, personLabel, type ActionResp, type Segment, type Voice } from '../api'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import PersonPicker from './PersonPicker.vue'
import { useActions } from '@/stores/actions'

const props = defineProps<{ said: Segment[]; voices: Voice[]; now: number; query: string }>()
const emit = defineEmits<{ play: [t: number]; changed: [] }>()
const actions = useActions()

const voiceOf = (id?: number) => props.voices.find((v) => v.id === id)
const caption = (v: Voice) => (v.person ? personLabel({ id: v.person, name: v.name })
  : v.suggest ? `${v.suggestName || personLabel({ id: v.suggest })}?` : `Голос ${v.n}`)
// mark a voice right here, then read the text again
async function mark(fn: () => Promise<ActionResp>) {
  if (await actions.run(fn, false)) emit('changed')
}
const accept = (v: Voice) => mark(() => api.voicePerson(v.id, { person: v.suggest }))
const reject = (v: Voice, person: number) => mark(() => api.voiceReject(v.id, person))
const clear = (v: Voice) => mark(() => api.voicePerson(v.id, { clear: true })) // takes the person off and remembers "not his voice"
const create = (v: Voice) => mark(() => api.voicePerson(v.id, { create: true }))
const setPerson = (v: Voice, p: number | null | undefined) => p && mark(() => api.voicePerson(v.id, { person: p }))

const root = ref<HTMLElement>()
const find = ref('')
const hit = ref(0) // which match the arrows are on

const mmss = (t: number) => `${Math.floor(t / 60)}:${String(Math.floor(t % 60)).padStart(2, '0')}`
const norm = (s: string) => s.toLowerCase().replaceAll('ё', 'е') // same length: marks map back onto the text

// the same crude stem the server searches with: "свечи" marks "свечами"
const terms = computed(() => norm(find.value.trim() || props.query).split(/\s+/)
  .map((w) => w.replace(/^[.,!?:;"()]+|[.,!?:;"()]+$/g, ''))
  .filter((w) => [...w].length >= 2)
  .map((w) => ([...w].length > 4 && 'аеиоуыэюяйь'.includes(w.at(-1)!) ? w.slice(0, -1) : w)))
const re = computed(() => (terms.value.length
  ? new RegExp(terms.value.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|'), 'g') : null))

function parts(text: string): { t: string; mark: boolean }[] {
  if (!re.value) return [{ t: text, mark: false }]
  const out: { t: string; mark: boolean }[] = []
  let at = 0
  for (const m of norm(text).matchAll(re.value)) {
    if (m.index > at) out.push({ t: text.slice(at, m.index), mark: false })
    out.push({ t: text.slice(m.index, m.index + m[0].length), mark: true })
    at = m.index + m[0].length
  }
  if (at < text.length) out.push({ t: text.slice(at), mark: false })
  return out
}

const paragraphs = computed(() => {
  const out: { start: number; len: number; voice?: Voice; items: { i: number; s: Segment }[] }[] = []
  props.said.forEach((s, i) => {
    const prev = props.said[i - 1]
    const last = out.at(-1)
    if (!prev || !last || s.start - prev.end > 2 || /^[—-]/.test(s.text) || last.len > 280 || s.voice !== prev.voice)
      out.push({ start: s.start, len: 0, voice: voiceOf(s.voice), items: [] })
    out.at(-1)!.items.push({ i, s })
    out.at(-1)!.len += s.text.length
  })
  return out
})

const matches = computed(() => (re.value
  ? props.said.flatMap((s, i) => { re.value!.lastIndex = 0; return re.value!.test(norm(s.text)) ? [i] : [] }) : []))

async function show(sel: string) {
  await nextTick()
  root.value?.querySelector(sel)?.scrollIntoView({ block: 'nearest' })
}
function step(k: number) {
  if (!matches.value.length) return
  hit.value = (hit.value + k + matches.value.length) % matches.value.length
  void show(`[data-i="${matches.value[hit.value]}"]`)
}
watch(find, () => { hit.value = 0; if (matches.value.length) void show(`[data-i="${matches.value[0]}"]`) })
watch(() => props.now, () => void show('.cur'))
onMounted(() => { if (props.now < 0 && matches.value.length) void show(`[data-i="${matches.value[0]}"]`) })

async function copy() {
  const text = paragraphs.value.map((p) => p.items.map((x) => x.s.text).join(' ')).join('\n\n')
  try {
    await navigator.clipboard.writeText(text)
    toast.success('Текст скопирован')
  } catch {
    toast.error('Не получилось скопировать')
  }
}
</script>

<template>
  <aside ref="root" class="said" aria-label="Что говорят в видео">
    <div class="bar">
      <label class="find">
        <Search class="size-3.5 shrink-0 opacity-60" />
        <input v-model="find" type="search" placeholder="Найти в тексте" aria-label="Найти в тексте"
               @keydown.enter.prevent="step($event.shiftKey ? -1 : 1)" @keydown.esc.stop="find = ''" />
      </label>
      <template v-if="find.trim()">
        <span class="count">{{ matches.length ? `${hit + 1} из ${matches.length}` : 'нет' }}</span>
        <button class="icon" aria-label="Предыдущее" title="Предыдущее (Shift+Enter)" @click="step(-1)"><ChevronUp class="size-4" /></button>
        <button class="icon" aria-label="Следующее" title="Следующее (Enter)" @click="step(1)"><ChevronDown class="size-4" /></button>
      </template>
      <button class="icon" aria-label="Скопировать текст" title="Скопировать текст" @click="copy"><Copy class="size-4" /></button>
    </div>
    <p v-for="p in paragraphs" :key="p.start" class="para">
      <span class="at">{{ mmss(p.start) }}
        <Popover v-if="p.voice">
          <PopoverTrigger as-child>
            <button class="who" :class="{ guess: !p.voice.person && p.voice.suggest }" title="Кто говорит">{{ caption(p.voice) }}</button>
          </PopoverTrigger>
          <PopoverContent class="w-64 space-y-1 p-2" align="start">
            <template v-if="!p.voice.person && p.voice.suggest">
              <Button size="sm" class="w-full" @click="accept(p.voice)">
                Да, это {{ p.voice.suggestName || personLabel({ id: p.voice.suggest }) }}
              </Button>
              <Button size="sm" variant="outline" class="w-full" @click="reject(p.voice, p.voice.suggest!)">Нет</Button>
            </template>
            <PersonPicker :model-value="p.voice.person" size="sm" block placeholder="Выбрать человека"
                          @update:model-value="setPerson(p.voice, $event)" />
            <Button size="sm" variant="outline" class="w-full" @click="create(p.voice)">Новый человек</Button>
            <Button v-if="p.voice.person" size="sm" variant="outline" class="w-full"
                    @click="clear(p.voice)">Голос не его</Button>
          </PopoverContent>
        </Popover>
      </span>
      <template v-for="x in p.items" :key="x.i">
        <!-- a span, not a button: a button is an inline block, every phrase would start a new line -->
        <span :data-i="x.i" class="phrase" :class="{ cur: x.i === now, found: find.trim() && matches[hit] === x.i }"
              role="button" tabindex="0" @click="emit('play', x.s.start)" @keydown.enter="emit('play', x.s.start)">
          <template v-for="(part, k) in parts(x.s.text)" :key="k"><mark v-if="part.mark">{{ part.t }}</mark><template v-else>{{ part.t }}</template></template>
        </span>{{ ' ' }}
      </template>
    </p>
  </aside>
</template>

<style scoped>
.said {
  place-self: stretch; overflow-y: auto; padding: 0 12px 12px; background: rgba(24, 24, 27, .92);
  border-left: 1px solid #27272a; scrollbar-width: thin; color: #a1a1aa; font-size: 14px; line-height: 1.55;
}
.bar {
  position: sticky; top: 0; z-index: 1; display: flex; align-items: center; gap: 4px; margin: 0 -12px 4px; padding: 8px 12px;
  background: #18181b; border-bottom: 1px solid #27272a;
}
.find {
  display: flex; flex: 1; min-width: 0; align-items: center; gap: 6px; height: 28px; padding: 0 8px;
  border: 1px solid #3f3f46; border-radius: 8px; color: #f4f4f5;
}
.find:focus-within { border-color: #71717a; }
.find input { flex: 1; min-width: 0; border: none; outline: none; background: none; color: inherit; font-size: 13px; }
.count { font-size: 12px; color: #71717a; white-space: nowrap; font-variant-numeric: tabular-nums; }
.icon {
  display: grid; place-items: center; width: 28px; height: 28px; flex-shrink: 0; border: none; border-radius: 8px;
  background: none; color: #a1a1aa; cursor: pointer;
}
.icon:hover { background: #27272a; color: #f4f4f5; }
.who {
  margin-left: 8px; padding: 0 6px; border-radius: 4px; background: #27272a; color: #e4e4e7; font-size: 12px; cursor: pointer;
}
.who.guess { color: #fbbf24; }
.who:hover { background: #3f3f46; }
.who:focus-visible { outline: 2px solid var(--ms-pick); }
.para { margin: 0; padding: 8px 0; }
.at {
  display: block; margin-bottom: 2px; font-size: 12px; color: #71717a; font-variant-numeric: tabular-nums;
}
.phrase {
  padding: 1px 2px; margin: 0 -2px; border-radius: 4px; cursor: pointer; transition: background-color 120ms var(--ms-ease), color 120ms var(--ms-ease);
  -webkit-box-decoration-break: clone; box-decoration-break: clone;
}
.phrase:hover { background: #27272a; color: #f4f4f5; }
.phrase:focus-visible { outline: 2px solid var(--ms-pick); }
.phrase.cur { color: #f4f4f5; background: #3f3f46; }
.phrase.found { outline: 1px solid #fbbf24; }
mark { background: rgba(251, 191, 36, .28); color: #fde68a; border-radius: 2px; }
@media (max-width: 800px) {
  .said { border-left: none; border-top: 1px solid #27272a; }
}
</style>
