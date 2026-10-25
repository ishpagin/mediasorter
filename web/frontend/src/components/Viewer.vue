<script setup lang="ts">
// Full-screen viewer: the original (the stored preview shows at once), ← → / Tab between groups, a strip of
// the current group (stack / copies) at the bottom, F = full screen, Esc = close.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronLeft, ChevronRight, Maximize, Trash2, TriangleAlert, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { originalUrl, previewUrl, type Item } from '../api'

const props = defineProps<{ items: Item[] }>()
const id = defineModel<number | null>({ required: true })
const emit = defineEmits<{ delete: [ids: number[]]; undo: [] }>()

const root = ref<HTMLElement>()
const strip = ref<HTMLElement>()
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

function toggleFull() {
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
  else root.value?.requestFullscreen().catch(() => {})
}

function onKey(e: KeyboardEvent) {
  const ctrl = e.ctrlKey || e.metaKey
  const fn: (() => void) | undefined = ctrl
    ? ({ KeyZ: () => emit('undo'), KeyD: remove } as Record<string, () => void>)[e.code] // Ctrl+D: delete, as in the old UI
    : ({
        ArrowRight: () => step(1), ArrowLeft: () => step(-1), Escape: close,
        Tab: () => jumpGroup(e.shiftKey ? -1 : 1), Delete: remove, Backspace: remove,
      } as Record<string, () => void>)[e.key] ?? (e.code === 'KeyF' ? toggleFull : undefined)
  if (fn) { e.preventDefault(); e.stopPropagation(); fn() }
}
onMounted(() => window.addEventListener('keydown', onKey, true))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey, true))

const info = computed(() => {
  const it = item.value
  if (!it) return ''
  return [it.name, it.who, (it.taken ?? '').slice(0, 10), it.place].filter(Boolean).join(' · ')
})
</script>

<template>
  <Teleport to="body">
    <div v-if="item" ref="root" class="viewer" :class="{ 'has-strip': group.length > 1 }">
      <header class="top">
        <span class="pos">{{ index + 1 }} / {{ items.length.toLocaleString('ru') }}</span>
        <span class="info" :title="item.path">{{ info }}</span>
        <span v-if="failed" class="warn"><TriangleAlert class="inline size-3.5" /> оригинал недоступен</span>
        <span class="keys">← → листать · Tab группы · Delete удалить · F экран · Esc</span>
        <slot name="actions" :id="item.id" />
        <Button variant="ghost" size="sm" class="text-red-400 hover:text-red-300" @click="remove"><Trash2 />Удалить</Button>
        <Button variant="ghost" size="icon-sm" aria-label="Во весь экран" title="Во весь экран (F)" @click="toggleFull"><Maximize /></Button>
        <Button variant="ghost" size="icon-sm" aria-label="Закрыть" title="Закрыть (Esc)" @click="close"><X /></Button>
      </header>
      <div class="stage" @click.self="close">
        <video v-if="item.kind === 'video'" :key="item.id" :src="originalUrl(item.id)" controls autoplay class="media" />
        <img v-else :key="item.id" :src="full ?? previewUrl(item.id)" alt="" class="media" draggable="false" />
        <button class="nav prev" :disabled="index <= 0" aria-label="Предыдущее" @click="step(-1)"><ChevronLeft class="mx-auto size-6" /></button>
        <button class="nav next" :disabled="index >= items.length - 1" aria-label="Следующее" @click="step(1)"><ChevronRight class="mx-auto size-6" /></button>
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
  background: #000; color: #f4f4f5; animation: v-in 160ms ease-out both;
}
@keyframes v-in { from { opacity: 0; } }
.top {
  display: flex; align-items: center; gap: 12px; padding: 6px 12px; font-size: 13px;
  background: rgba(24, 24, 27, .92); border-bottom: 1px solid #27272a;
}
.top :deep(button) { color: #f4f4f5; }
.top :deep(button:hover) { background: #27272a; }
.pos { font-variant-numeric: tabular-nums; color: #a1a1aa; }
.info { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.warn { color: #fbbf24; white-space: nowrap; }
.keys { margin-left: auto; color: #71717a; white-space: nowrap; font-size: 12px; }
.stage { position: relative; min-height: 0; display: grid; place-items: center; overflow: hidden; }
.media { max-width: 100%; max-height: 100%; object-fit: contain; animation: m-in 220ms var(--ms-ease) both; }
@keyframes m-in { from { opacity: .4; transform: scale(.985); } }
.nav {
  position: absolute; top: 50%; transform: translateY(-50%); width: 48px; height: 96px; border: none; border-radius: 12px;
  background: rgba(24, 24, 27, .45); color: #fff; font-size: 20px; cursor: pointer;
}
.nav:hover:not(:disabled) { background: rgba(39, 39, 42, .9); }
.nav:disabled { opacity: .2; cursor: default; }
.prev { left: 16px; }
.next { right: 16px; }
.strip {
  display: flex; gap: 8px; justify-content: safe center; overflow-x: auto; padding: 10px 16px;
  background: rgba(24, 24, 27, .92); border-top: 1px solid #27272a; scrollbar-width: thin;
}
.thumb {
  flex: 0 0 auto; width: 64px; height: 64px; padding: 0; border: none; border-radius: 8px; overflow: hidden; cursor: pointer;
  background: #27272a; opacity: .5; outline: 2px solid transparent; outline-offset: 2px;
  transition: opacity 120ms var(--ms-ease), outline-color 120ms var(--ms-ease);
}
.thumb img { width: 100%; height: 100%; object-fit: cover; display: block; }
.thumb:hover { opacity: .85; }
.thumb.cur { opacity: 1; outline-color: var(--ms-pick); }
</style>
