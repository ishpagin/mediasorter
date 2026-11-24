<script setup lang="ts">
// Full-screen viewer: the original (the stored preview shows at once), ↑ ↓ (and ← → on photos) / Tab between groups, a strip of
// the current group (stack / copies) at the bottom, what is said in a video on the right, F = full screen, Esc = close.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Captions, ChevronLeft, ChevronRight, ExternalLink, Maximize, Trash2, TriangleAlert, X } from '@lucide/vue'
import { useRoute } from 'vue-router'
import { Button } from '@/components/ui/button'
import SpeechPanel from './SpeechPanel.vue'
import { api, originalUrl, previewUrl, type Item, type Voice, type Segment } from '../api'

const props = defineProps<{ items: Item[] }>()
const id = defineModel<number | null>({ required: true })
const emit = defineEmits<{ delete: [ids: number[]]; undo: [] }>()

const root = ref<HTMLElement>()
const strip = ref<HTMLElement>()
const video = ref<HTMLVideoElement>()
const order = computed(() => props.items.map((it) => it.id))
const index = computed(() => order.value.indexOf(id.value ?? -1))
const item = computed(() => props.items[index.value])
const full = ref<string | null>(null) // loaded original for the current photo
const failed = ref(false)

const group = computed(() => {
  const g = item.value?.group
  return g === undefined ? [] : props.items.filter((it) => it.group === g)
})

function load() {
  full.value = null
  failed.value = false
  const it = item.value
  if (!it || it.kind === 'video') return
  const want = it.id
  const img = new Image()
  img.onload = () => { if (id.value === want) full.value = img.src }
  img.onerror = () => { if (id.value === want) failed.value = true }
  img.src = originalUrl(want)
  for (const k of [1, -1]) { // neighbours are probably next
    const n = props.items[index.value + k]
    if (n && n.kind !== 'video') new Image().src = originalUrl(n.id)
  }
}
watch(id, async () => {
  load()
  await nextTick()
  strip.value?.querySelector('.cur')?.scrollIntoView({ block: 'nearest', inline: 'center' })
}, { immediate: true })
watch(() => props.items, () => { if (index.value < 0) close() }) // the file is gone (deleted)

// what is said in the video, the phrase being said now is highlighted, a click plays from it
const said = ref<Segment[]>([])
const voices = ref<Voice[]>([])
const now = ref(-1)
const showText = ref(true)
const route = useRoute()
const query = computed(() => String(route.query.q ?? '')) // opened from the search: its words are marked
const hasText = computed(() => showText.value && said.value.length > 0)
watch(() => item.value?.id, async (want) => {
  said.value = []
  voices.value = []
  now.value = -1
  if (want === undefined || item.value?.kind !== 'video') return
  await loadSpeech(want)
}, { immediate: true })
async function loadSpeech(want: number) {
  try {
    const s = await api.speech(want)
    if (id.value === want) { said.value = s.segments; voices.value = s.voices }
  } catch { /* no text, no panel */ }
}
function onTime() {
  const t = video.value?.currentTime ?? 0
  let i = -1
  while (i + 1 < said.value.length && said.value[i + 1].start <= t + 0.05) i++
  now.value = i
}
function playFrom(t: number) {
  const v = video.value
  if (!v) return
  v.currentTime = t
  v.play().catch(() => {})
}

function step(k: number) {
  const next = order.value[index.value + k]
  if (next !== undefined) id.value = next
}

// Tab / Shift+Tab: first file of the next / previous group; no groups here: next / previous file
function jumpGroup(k: number) {
  const cur = item.value
  if (!cur || cur.group === undefined) return step(k)
  let i = index.value
  while (props.items[i] && props.items[i].group === cur.group) i += k
  if (!props.items[i]) return
  if (k < 0) while (i > 0 && props.items[i - 1].group === props.items[i].group) i--
  id.value = props.items[i].id
}

function close() {
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
  id.value = null
}

function remove() {
  const cur = id.value
  if (cur === null) return
  const next = order.value[index.value + 1] ?? order.value[index.value - 1] ?? null
  emit('delete', [cur])
  id.value = next
}

let leaving = false // our own exit from full screen (F), not Esc
function toggleFull() {
  if (document.fullscreenElement) { leaving = true; document.exitFullscreen().catch(() => {}) }
  else root.value?.requestFullscreen().catch(() => {})
}
// the browser eats the Esc that leaves full screen, so treat that exit as Esc
function onFullscreen() {
  if (document.fullscreenElement) return
  if (!leaving) close()
  leaving = false
}

// YouTube-like keys on a video; ↑ ↓ list files (as in TikTok), so ← → are free for seeking
function videoKey(e: KeyboardEvent): (() => void) | undefined {
  const v = video.value
  if (!v || item.value?.kind !== 'video') return
  const seek = (s: number) => () => { v.currentTime = Math.max(0, v.currentTime + s) }
  const frame = (k: number) => () => { v.pause(); v.currentTime += k / 30 }
  const speed = (k: number) => () => { v.playbackRate = Math.min(2, Math.max(0.25, v.playbackRate + k * 0.25)) }
  const toggle = () => { if (v.paused) v.play().catch(() => {}); else v.pause() }
  if (/^Digit\d$/.test(e.code)) return () => { v.currentTime = (v.duration * Number(e.code[5])) / 10 }
  return ({
    Space: toggle, KeyK: toggle, KeyM: () => { v.muted = !v.muted },
    ArrowLeft: seek(-5), ArrowRight: seek(5), KeyJ: seek(-10), KeyL: seek(10),
    Comma: e.shiftKey ? speed(-1) : frame(-1), Period: e.shiftKey ? speed(1) : frame(1),
  } as Record<string, () => void>)[e.code]
}

function onKey(e: KeyboardEvent) {
  if ((e.target as HTMLElement)?.tagName === 'INPUT' && e.key !== 'Escape') return // typing in the text search
  const ctrl = e.ctrlKey || e.metaKey
  const fn: (() => void) | undefined = ctrl
    ? ({ KeyZ: () => emit('undo'), KeyD: remove } as Record<string, () => void>)[e.code] // Ctrl+D: delete, as in the old UI
    : videoKey(e) ?? ({
        ArrowDown: () => step(1), ArrowUp: () => step(-1), ArrowRight: () => step(1), ArrowLeft: () => step(-1), Escape: close,
        Tab: () => jumpGroup(e.shiftKey ? -1 : 1), Delete: remove, Backspace: remove,
      } as Record<string, () => void>)[e.key] ?? (e.code === 'KeyF' ? toggleFull : undefined)
  if (fn) { e.preventDefault(); e.stopPropagation(); fn() }
}
onMounted(() => {
  window.addEventListener('keydown', onKey, true)
  document.addEventListener('fullscreenchange', onFullscreen)
  root.value?.focus() // keys work without clicking into the viewer first
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey, true)
  clearTimeout(tapTimer)
  document.removeEventListener('fullscreenchange', onFullscreen)
})

// ---------------------------------------------------------------- touch
// swipe ← → lists, swipe down closes, pinch / double tap zooms a photo, a tap hides the bars

const zoom = ref({ s: 1, x: 0, y: 0 }) // the photo's zoom and pan
const drag = ref({ x: 0, y: 0, on: false }) // a swipe in progress
const bare = ref(false) // bars hidden
watch(id, () => { zoom.value = { s: 1, x: 0, y: 0 }; drag.value = { x: 0, y: 0, on: false } })
const pts = new Map<number, { x: number; y: number }>()
let st = { x: 0, y: 0, t: 0, s: 1, zx: 0, zy: 0, d: 0, mx: 0, my: 0 }
let axis: '' | 'x' | 'y' | 'no' | 'pan' | 'pinch' = ''
let lastTap = 0, tapTimer = 0, touched = 0

function centre() {
  const r = root.value!.querySelector('.stage')!.getBoundingClientRect()
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 }
}
function begin() { // (re)start from the fingers that are down now
  const [a, b] = [...pts.values()]
  st = { x: a.x, y: a.y, t: Date.now(), s: zoom.value.s, zx: zoom.value.x, zy: zoom.value.y, d: 0, mx: a.x, my: a.y }
  if (b) st = { ...st, d: Math.hypot(a.x - b.x, a.y - b.y), mx: (a.x + b.x) / 2, my: (a.y + b.y) / 2 }
}
function down(e: PointerEvent) {
  if (e.pointerType === 'mouse') return
  touched = Date.now()
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
  if (item.value?.kind === 'video' && e.clientY > r.bottom - 72) return // the video's own controls
  try { (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId) } catch { /* the pointer is already gone */ }
  pts.set(e.pointerId, { x: e.clientX, y: e.clientY })
  begin()
  axis = pts.size === 2 && item.value?.kind !== 'video' ? 'pinch' : pts.size === 1 ? '' : 'no'
}
function move(e: PointerEvent) {
  if (!pts.has(e.pointerId)) return
  pts.set(e.pointerId, { x: e.clientX, y: e.clientY })
  const [a, b] = [...pts.values()]
  if (axis === 'pinch' && b) { // zoom around the point between the fingers
    const s = Math.min(5, Math.max(1, (st.s * Math.hypot(a.x - b.x, a.y - b.y)) / st.d))
    const c = centre(), mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2
    zoom.value = { s, x: mx - c.x - ((st.mx - c.x - st.zx) * s) / st.s, y: my - c.y - ((st.my - c.y - st.zy) * s) / st.s }
    return
  }
  const dx = a.x - st.x, dy = a.y - st.y
  if (zoom.value.s > 1) { axis = 'pan'; zoom.value = { ...zoom.value, x: st.zx + dx, y: st.zy + dy }; return }
  if (!axis && Math.hypot(dx, dy) > 10) axis = Math.abs(dx) > Math.abs(dy) ? 'x' : dy > 0 ? 'y' : 'no'
  if (axis === 'x') drag.value = { x: dx, y: 0, on: true }
  else if (axis === 'y') drag.value = { x: 0, y: Math.max(0, dy), on: true }
}
function up(e: PointerEvent) {
  if (!pts.has(e.pointerId)) return
  pts.delete(e.pointerId)
  if (pts.size) { begin(); axis = zoom.value.s > 1 ? 'pan' : 'no'; return } // one finger left after a pinch
  const { x: dx, y: dy } = drag.value, fast = Date.now() - st.t < 250
  drag.value = { x: 0, y: 0, on: false }
  if (zoom.value.s < 1.05) zoom.value = { s: 1, x: 0, y: 0 }
  if (axis === 'x' && (Math.abs(dx) > 80 || (fast && Math.abs(dx) > 30))) step(dx < 0 ? 1 : -1)
  else if (axis === 'y' && (dy > 120 || (fast && dy > 40))) close()
  else if (!axis && Date.now() - st.t < 300) tap(e)
  axis = ''
}
function tap(e: PointerEvent) {
  if (item.value?.kind === 'video') return // a tap on a video is the player's
  clearTimeout(tapTimer)
  if (Date.now() - lastTap < 300) { // double tap: zoom in to that point, or back out
    lastTap = 0
    const c = centre()
    zoom.value = zoom.value.s > 1 ? { s: 1, x: 0, y: 0 } : { s: 2.5, x: -(e.clientX - c.x) * 1.5, y: -(e.clientY - c.y) * 1.5 }
    return
  }
  lastTap = Date.now()
  tapTimer = window.setTimeout(() => { bare.value = !bare.value }, 300)
}
const mediaStyle = computed(() => {
  const { s, x, y } = zoom.value, d = drag.value
  return {
    transform: `translate(${x + d.x}px, ${y + d.y}px) scale(${s * (1 - d.y / 1500)})`,
    transition: d.on || pts.size ? 'none' : 'transform 220ms var(--ms-ease)',
  }
})
// a tap on the dark area closes on a desktop; on a phone that tap is ours
const stageClick = () => { if (Date.now() - touched > 800) close() }

const info = computed(() => {
  const it = item.value
  if (!it) return ''
  return [it.name, it.who, (it.taken ?? '').slice(0, 10), it.place].filter(Boolean).join(' · ')
})
</script>

<template>
  <Teleport to="body">
    <div v-if="item" ref="root" tabindex="-1" class="viewer" @dragstart.prevent @dragover.prevent @drop.prevent" :class="{ 'has-strip': group.length > 1, bare }"
         :style="drag.y ? { backgroundColor: `rgba(0, 0, 0, ${1 - Math.min(0.7, drag.y / 400)})` } : undefined">
      <header class="top">
        <span class="pos">{{ index + 1 }} / {{ items.length.toLocaleString('ru') }}</span>
        <span class="info" :title="item.path">{{ info }}</span>
        <span v-if="failed" class="warn"><TriangleAlert class="inline size-3.5" /> оригинал недоступен</span>
        <span v-if="item.kind === 'video'" class="keys">↑ ↓ листать · ← → J L перемотка · Space пауза · M звук · F экран · Esc</span>
        <span v-else class="keys">↑ ↓ ← → листать · Tab группы · Delete удалить · F экран · Esc</span>
        <div class="acts">
          <slot name="actions" :id="item.id" />
          <Button v-if="said.length" variant="ghost" size="sm" :aria-pressed="showText" title="Что говорят в видео"
                  @click="showText = !showText"><Captions />Текст</Button>
          <Button variant="ghost" size="sm" class="first text-red-400 hover:text-red-300" @click="remove"><Trash2 />Удалить</Button>
          <Button as-child variant="ghost" size="sm" class="first">
            <a :href="originalUrl(item.id)" target="_blank" rel="noopener" title="Открыть оригинал в новой вкладке"><ExternalLink />Оригинал</a>
          </Button>
          <Button variant="ghost" size="icon-sm" class="first" aria-label="Во весь экран" title="Во весь экран (F)" @click="toggleFull"><Maximize /></Button>
        </div>
        <Button variant="ghost" size="icon-sm" class="close" aria-label="Закрыть" title="Закрыть (Esc)" @click="close"><X /></Button>
      </header>
      <div class="stage" :class="{ 'with-text': hasText }" @click.self="stageClick">
        <video v-if="item.kind === 'video'" ref="video" :key="item.id" :src="originalUrl(item.id) + (item.at ? `#t=${item.at}` : '')" controls autoplay class="media"
               :style="mediaStyle" @timeupdate="onTime"
               @pointerdown="down" @pointermove="move" @pointerup="up" @pointercancel="up" />
        <img v-else :key="item.id" :src="full ?? previewUrl(item.id)" alt="" class="media" draggable="false"
             @contextmenu="Date.now() - touched < 800 && $event.preventDefault()" :style="mediaStyle" @pointerdown="down" @pointermove="move" @pointerup="up" @pointercancel="up" />
        <button class="nav prev" :disabled="index <= 0" aria-label="Предыдущее" @click="step(-1)"><ChevronLeft class="mx-auto size-6" /></button>
        <button class="nav next" :disabled="index >= items.length - 1" aria-label="Следующее" @click="step(1)"><ChevronRight class="mx-auto size-6" /></button>
        <SpeechPanel v-if="hasText" :said="said" :voices="voices" :now="now" :query="query" @play="playFrom" @changed="loadSpeech(item!.id)" />
      </div>
      <div v-if="group.length > 1" ref="strip" class="strip">
        <button v-for="g in group" :key="g.id" class="thumb" :class="{ cur: g.id === item.id }" :title="g.name" @click="id = g.id">
          <img :src="previewUrl(g.id)" alt="" loading="lazy" />
        </button>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.viewer {
  position: fixed; inset: 0; z-index: 1100; display: grid; grid-template-rows: auto 1fr auto;
  grid-template-columns: minmax(0, 1fr); /* a long nowrap header would widen the column past the screen */
  background: #000; color: #f4f4f5; animation: v-in 160ms ease-out both;
}
.viewer:focus { outline: none; }
@keyframes v-in { from { opacity: 0; } }
.top {
  display: flex; align-items: center; gap: 12px; padding: 6px 12px; font-size: 13px;
  background: rgba(24, 24, 27, .92); border-bottom: 1px solid #27272a;
}
.top :deep(button) { color: #f4f4f5; }
@media (hover: hover) and (pointer: fine) {
  .top :deep(button:hover) { background: #27272a; }
  .nav:hover:not(:disabled) { background: rgba(39, 39, 42, .9); }
  .thumb:hover { opacity: .85; }
}
.acts { display: flex; align-items: center; gap: 12px; }
.top, .strip { transition: opacity 200ms var(--ms-ease); }
.viewer.bare .top, .viewer.bare .strip { opacity: 0; pointer-events: none; } /* hidden but in place: the photo does not jump */
@media (pointer: coarse) {
  .keys, .nav { display: none; } /* keys and arrows: the fingers swipe */
  .media { touch-action: none; -webkit-touch-callout: none; }
}
@media (max-width: 767px) {
  .top { flex-wrap: wrap; row-gap: 0; gap: 4px; padding: max(4px, env(safe-area-inset-top)) 4px 4px 12px; }
  .acts { flex: 1 1 0; min-width: 0; gap: 2px; overflow-x: auto; scrollbar-width: none; } .acts .first { order: -1; } /* delete and full screen in view, the rest scrolls */
  .acts > * { flex-shrink: 0; }
  .info { order: 3; flex-basis: 100%; font-size: 12px; color: #a1a1aa; padding-bottom: 4px; }
  .warn { order: 4; }
  .close { width: 44px; height: 44px; }
}
.pos { font-variant-numeric: tabular-nums; color: #a1a1aa; white-space: nowrap; }
.info { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.warn { color: #fbbf24; white-space: nowrap; }
.keys { margin-left: auto; color: #71717a; white-space: nowrap; font-size: 12px; }
.stage { position: relative; min-height: 0; display: grid; grid-template: 100% / 100%; place-items: center; overflow: hidden; }
.media { width: 100%; height: 100%; object-fit: contain; -webkit-user-select: none; user-select: none; -webkit-user-drag: none; /* upscales small previews too */ animation: m-in 220ms var(--ms-ease) both; }
@keyframes m-in { from { opacity: .4; transform: scale(.985); } }
.nav {
  position: absolute; top: 50%; transform: translateY(-50%); width: 48px; height: 96px; border: none; border-radius: 12px;
  background: rgba(24, 24, 27, .45); color: #fff; font-size: 20px; cursor: pointer;
}
.nav:disabled { opacity: .2; cursor: default; }
.prev { left: 16px; }
.next { right: 16px; }
.stage.with-text { grid-template: 100% / minmax(0, 1fr) 340px; }
.stage.with-text .next { right: 356px; }
@media (max-width: 800px) {
  .stage.with-text { grid-template: minmax(0, 1fr) 38% / 100%; }
  .stage.with-text .next { right: 16px; }
}
.strip {
  display: flex; gap: 8px; justify-content: safe center; overflow-x: auto; padding: 10px 16px;
  background: rgba(24, 24, 27, .92); border-top: 1px solid #27272a; scrollbar-width: thin;
  padding-bottom: calc(10px + env(safe-area-inset-bottom));
}
.thumb {
  flex: 0 0 auto; width: 64px; height: 64px; padding: 0; border: none; border-radius: 8px; overflow: hidden; cursor: pointer;
  background: #27272a; opacity: .5; outline: 2px solid transparent; outline-offset: 2px;
  transition: opacity 120ms var(--ms-ease), outline-color 120ms var(--ms-ease);
}
.thumb img { width: 100%; height: 100%; object-fit: cover; display: block; }
.thumb.cur { opacity: 1; outline-color: var(--ms-pick); }
</style>
