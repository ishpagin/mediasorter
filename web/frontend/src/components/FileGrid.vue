<script setup lang="ts">
// Explorer-like grid of files: only the visible rows are in the DOM (lists of tens of thousands),
// click / Ctrl / Shift selection, rubber band (by geometry, so it reaches rows outside the DOM), keyboard.
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { Bookmark, BookmarkPlus, Check, Copy, Trash2, User, UserPen, Users, Video, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import Viewer from './Viewer.vue'
import CollectDialog from './CollectDialog.vue'
import WhoDialog from './WhoDialog.vue'
import { faceUrl, previewUrl, type Item } from '@/api'
import { useActions } from '@/stores/actions'
import { useSettings } from '@/stores/settings'
import { useQuery } from '@/composables/useQuery'

// collect: offer the collection actions (off on the collections page itself);
// person: the person whose page this is ("who is on the photo" fixes their faces first)
const props = withDefaults(defineProps<{ items: Item[]; collect?: boolean; person?: number | null }>(),
  { collect: true, person: null })
const actions = useActions()
const settings = useSettings()

// deleted here: hidden at once, before the page reloads its list
const gone = shallowRef(new Set<number>())
watch(() => props.items, () => { gone.value = new Set() })
const list = computed(() => (gone.value.size ? props.items.filter((it) => !gone.value.has(it.id)) : props.items))

const GAP = 12, MIN_TILE = 168, HEAD = 44, OVERSCAN = 600

const scroller = ref<HTMLElement>()
const space = ref<HTMLElement>()
const width = ref(0)
const viewH = ref(0)
const scrollTop = ref(0)
useResizeObserver(scroller, (e) => {
  width.value = e[0].contentRect.width
  viewH.value = e[0].contentRect.height
})

type Row = { kind: 'row'; y: number; h: number; ids: number[] }
type Head = { kind: 'head'; y: number; h: number; text: string }
type Entry = Row | Head

// rows and group headers with their y; every item knows its row and column (for the rubber band)
const hasNote = computed(() => list.value.some((it) => it.note))
const CAP = computed(() => (hasNote.value ? 60 : 44)) // caption lines under a tile

const layout = computed(() => {
  const cols = Math.max(1, Math.floor((width.value + GAP) / (MIN_TILE + GAP)))
  const tile = width.value ? (width.value - GAP * (cols - 1)) / cols : MIN_TILE
  const rowH = tile + CAP.value
  const entries: Entry[] = []
  let y = 0, row: number[] = [], group: number | undefined
  const flush = () => {
    if (!row.length) return
    entries.push({ kind: 'row', y, h: rowH, ids: row })
    y += rowH + GAP
    row = []
  }
  list.value.forEach((it, i) => {
    if (it.group !== undefined && (i === 0 || it.group !== group)) {
      flush()
      entries.push({ kind: 'head', y, h: HEAD, text: it.head ?? '' })
      y += HEAD
    }
    group = it.group
    row.push(it.id)
    if (row.length === cols) flush()
  })
  flush()
  return { cols, tile, rowH, entries, height: y }
})

const byId = computed(() => new Map(list.value.map((it) => [it.id, it])))
const order = computed(() => list.value.map((it) => it.id))

const visible = computed(() => {
  const { entries } = layout.value
  const top = scrollTop.value - OVERSCAN, bottom = scrollTop.value + viewH.value + OVERSCAN
  let lo = 0, hi = entries.length
  while (lo < hi) { // first entry that ends below `top`
    const mid = (lo + hi) >> 1
    if (entries[mid].y + entries[mid].h < top) lo = mid + 1
    else hi = mid
  }
  const out: Entry[] = []
  for (let i = lo; i < entries.length && entries[i].y < bottom; i++) out.push(entries[i])
  return out
})

// ---------------------------------------------------------------- selection

const sel = shallowRef(new Set<number>())
const anchor = ref<number | null>(null)
const setSel = (s: Set<number>) => { sel.value = s }
watch(list, (items) => { // drop what is gone (deleted, filtered out)
  const ids = new Set(items.map((it) => it.id))
  const kept = new Set([...sel.value].filter((id) => ids.has(id)))
  if (kept.size !== sel.value.size) setSel(kept)
  if (anchor.value !== null && !ids.has(anchor.value)) anchor.value = null
})

function click(e: MouseEvent, id: number) {
  if (justDragged) return
  const s = new Set(sel.value)
  const a = e.shiftKey && anchor.value !== null ? order.value.indexOf(anchor.value) : -1
  if (a >= 0) { // shift: add the range from the last picked tile, keep what is selected already
    const b = order.value.indexOf(id)
    for (let i = Math.min(a, b); i <= Math.max(a, b); i++) s.add(order.value[i])
  } else if (e.shiftKey || e.ctrlKey || e.metaKey) {
    if (s.has(id)) s.delete(id)
    else s.add(id)
  } else {
    s.clear()
    s.add(id)
  }
  anchor.value = id
  setSel(s)
}

// ---------------------------------------------------------------- rubber band

const band = ref<{ l: number; t: number; w: number; h: number } | null>(null)
let justDragged = false

function idsInRect(l: number, t: number, r: number, b: number): { ids: number[]; first: number; last: number } {
  const { entries, tile, cols } = layout.value
  const ids: number[] = []
  let first = -1, last = -1
  for (const e of entries) {
    if (e.kind !== 'row' || e.y + e.h <= t || e.y >= b) continue
    for (let c = 0; c < Math.min(cols, e.ids.length); c++) {
      const x0 = c * (tile + GAP)
      if (x0 + tile > l && x0 < r) {
        const id = e.ids[c]
        ids.push(id)
        const i = order.value.indexOf(id)
        if (first < 0 || i < first) first = i
        if (i > last) last = i
      }
    }
  }
  return { ids, first, last }
}

function onDown(e: MouseEvent) {
  if (e.button !== 0 || (e.target as HTMLElement).closest('.tile, button')) return
  const el = scroller.value!, sp = space.value!
  const box = el.getBoundingClientRect()
  const at = () => sp.getBoundingClientRect() // the tiles' coordinate space: moves with scrolling
  const x0 = e.clientX - at().left, y0 = e.clientY - at().top
  const base = e.ctrlKey || e.metaKey ? new Set(sel.value) : new Set<number>()
  let mx = e.clientX, my = e.clientY, dragging = false, raf = 0
  e.preventDefault()

  const update = () => {
    const r0 = at()
    const x = mx - r0.left, y = my - r0.top
    if (!dragging && Math.hypot(x - x0, y - y0) < 6) return
    dragging = true
    const l = Math.min(x0, x), t = Math.min(y0, y), r = Math.max(x0, x), b = Math.max(y0, y)
    band.value = { l, t, w: r - l, h: b - t }
    const hit = idsInRect(l, t, r, b)
    setSel(new Set([...base, ...hit.ids]))
    if (hit.ids.length) anchor.value = order.value[y >= y0 ? hit.last : hit.first] // band end: Shift+click from here
  }
  const tick = () => { // auto-scroll near the top / bottom edge
    const edge = 48
    const dy = my < box.top + edge ? -(box.top + edge - my) : my > box.bottom - edge ? my - (box.bottom - edge) : 0
    if (dragging && dy) { el.scrollTop += dy * 0.5; update() }
    raf = requestAnimationFrame(tick)
  }
  const move = (ev: MouseEvent) => { mx = ev.clientX; my = ev.clientY; update() }
  const up = () => {
    window.removeEventListener('mousemove', move)
    window.removeEventListener('mouseup', up)
    cancelAnimationFrame(raf)
    band.value = null
    if (dragging) { justDragged = true; setTimeout(() => (justDragged = false)) }
    else if (!(e.ctrlKey || e.metaKey)) setSel(new Set()) // a click on empty space clears, like Explorer
  }
  window.addEventListener('mousemove', move)
  window.addEventListener('mouseup', up)
  raf = requestAnimationFrame(tick)
}

// ---------------------------------------------------------------- viewer + keyboard

const query = useQuery()
const viewing = ref<number | null>(Number(query.q('view')) || null) // ?view=<id>: a link opens the file right away
watch(viewing, (v) => query.set('view', v))

function open(id: number) {
  viewing.value = id
  setSel(new Set([id]))
  anchor.value = id
}

function reveal(id: number) { // scroll the grid so the file is visible (after the viewer moved on)
  const el = scroller.value
  const e = layout.value.entries.find((x) => x.kind === 'row' && x.ids.includes(id))
  if (!el || !e) return
  if (e.y < el.scrollTop) el.scrollTop = e.y - GAP
  else if (e.y + e.h > el.scrollTop + el.clientHeight) el.scrollTop = e.y + e.h - el.clientHeight + GAP
}

function onKey(e: KeyboardEvent) {
  if (viewing.value !== null) return // the viewer has the keyboard
  const t = e.target as HTMLElement
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return
  if (document.querySelector('[role="dialog"]')) return
  const ctrl = e.ctrlKey || e.metaKey
  if (ctrl && e.code === 'KeyA') { e.preventDefault(); setSel(new Set(order.value)) }
  else if (e.key === 'Escape') setSel(new Set())
  else if (e.key === 'Enter' && sel.value.size) {
    e.preventDefault()
    open(anchor.value !== null && sel.value.has(anchor.value) ? anchor.value : [...sel.value][0])
  } else if ((e.key === 'Delete' || e.key === 'Backspace') && sel.value.size) {
    e.preventDefault()
    void remove([...sel.value])
  }
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

const date = (it: Item) => (it.taken ?? '').slice(0, 10)
const line2 = (it: Item) => [date(it), it.place].filter(Boolean).join(' · ')
const title = (it: Item) =>
  [it.name, it.who && `Люди: ${it.who}`, it.coll && `Подборки: ${it.coll}`, line2(it), it.tag && `Сюжет: ${it.tag}`]
    .filter(Boolean).join('\n')

const selectedIds = computed(() => [...sel.value])

// ---------------------------------------------------------------- actions

async function remove(ids: number[]) {
  gone.value = new Set([...gone.value, ...ids])
  const r = await actions.del(ids)
  if (!r) gone.value = new Set([...gone.value].filter((id) => !ids.includes(id))) // failed: show them again
}

const collectName = computed(() =>
  actions.meta?.collections.find((c) => c.id === settings.collection)?.name ?? 'новую подборку')
const dialogIds = ref<number[]>([])
const dialogOpen = ref(false)
function collectTo(ids: number[]) {
  dialogIds.value = ids
  dialogOpen.value = true
}
const whoIds = ref<number[]>([])
const whoOpen = ref(false)
function who(ids: number[]) {
  whoIds.value = ids
  whoOpen.value = true
}
defineExpose({ selectedIds, clear: () => setSel(new Set()) })
</script>

<template>
  <div class="grid-wrap">
    <div class="selbar" :class="{ on: sel.size }">
      <template v-if="sel.size">
        <Button variant="ghost" size="icon-sm" aria-label="Снять выделение" title="Снять выделение (Esc)" @click="setSel(new Set())"><X /></Button>
        <span class="count">Выбрано {{ sel.size.toLocaleString('ru') }}</span>
        <Button variant="destructive" size="sm" :disabled="actions.busy" @click="remove(selectedIds)"><Trash2 />Удалить</Button>
        <template v-if="collect">
          <Button variant="outline" size="sm" :disabled="actions.busy" @click="actions.collect(selectedIds)">
            <Copy v-if="settings.copyMode" /><Bookmark v-else />{{ settings.copyMode ? 'Копия в' : 'В' }} «{{ collectName }}»
          </Button>
          <Button variant="outline" size="sm" @click="collectTo(selectedIds)"><BookmarkPlus />В подборку…</Button>
          <Button variant="outline" size="sm" :disabled="actions.busy" @click="actions.byPeople(selectedIds)"><Users />По людям</Button>
        </template>
        <Button variant="outline" size="sm" @click="who(selectedIds)"><UserPen />Кто на фото</Button>
        <slot name="actions" :ids="selectedIds" />
      </template>
      <span v-else class="hint">
        {{ list.length.toLocaleString('ru') }} файлов · клик, Ctrl, Shift или рамка мышью — выбрать ·
        двойной клик — открыть · Ctrl+A — все · Delete — удалить · Ctrl+Z — вернуть
      </span>
    </div>
    <div ref="scroller" class="scroller" @scroll="scrollTop = ($event.target as HTMLElement).scrollTop" @mousedown="onDown">
      <div ref="space" class="space" :style="{ height: layout.height + 'px' }">
        <template v-for="e in visible" :key="e.kind + e.y">
          <div v-if="e.kind === 'head'" class="head" :style="{ top: e.y + 'px' }">{{ e.text }}</div>
          <template v-else>
            <div v-for="(id, c) in e.ids" :key="id" class="tile" :class="{ sel: sel.has(id) }"
                 :style="{ top: e.y + 'px', left: c * (layout.tile + GAP) + 'px', width: layout.tile + 'px' }"
                 :title="title(byId.get(id)!)" @click="click($event, id)" @dblclick="open(id)">
              <div class="img" :style="{ height: layout.tile + 'px' }">
                <img :src="byId.get(id)!.face ? faceUrl(byId.get(id)!.face!) : previewUrl(id)" alt="" loading="lazy" decoding="async" draggable="false" />
                <span v-if="byId.get(id)!.kind === 'video'" class="badge"><Video class="size-3.5" /></span>
                <span class="check"><Check class="size-3.5" :stroke-width="3" /></span>
              </div>
              <div class="cap">
                <div v-if="hasNote" class="l0">{{ byId.get(id)!.note }}</div>
                <div class="l1">
                  <template v-if="byId.get(id)!.who"><User class="ico" />{{ byId.get(id)!.who }}</template>
                  <template v-else>{{ byId.get(id)!.name }}</template>
                </div>
                <div class="l2">
                  <Bookmark v-if="byId.get(id)!.coll" class="ico" />{{ line2(byId.get(id)!) }}
                </div>
              </div>
            </div>
          </template>
        </template>
        <div v-if="band" class="band" :style="{ left: band.l + 'px', top: band.t + 'px', width: band.w + 'px', height: band.h + 'px' }" />
      </div>
    </div>
    <Viewer v-if="viewing !== null" v-model="viewing" :items="list"
            @update:model-value="(id) => { if (id !== null) { setSel(new Set([id])); anchor = id; reveal(id) } }"
            @delete="remove" @undo="actions.undo()">
      <template v-if="collect" #actions="{ id }">
        <Button variant="ghost" size="sm" :disabled="actions.busy" @click="actions.collect([id])">
          <Copy v-if="settings.copyMode" /><Bookmark v-else />{{ settings.copyMode ? 'Копия в' : 'В' }} «{{ collectName }}»
        </Button>
        <Button variant="ghost" size="sm" @click="collectTo([id])"><BookmarkPlus />Куда…</Button>
        <Button variant="ghost" size="sm" :disabled="actions.busy" @click="actions.byPeople([id])"><Users />По людям</Button>
        <Button variant="ghost" size="sm" @click="who([id])"><UserPen />Кто это</Button>
      </template>
      <template v-else #actions="{ id }">
        <Button variant="ghost" size="sm" @click="who([id])"><UserPen />Кто это</Button>
      </template>
    </Viewer>
    <CollectDialog v-model:open="dialogOpen" :ids="dialogIds" />
    <WhoDialog v-model:open="whoOpen" :files="whoIds" :src="person" />
  </div>
</template>

<style scoped>
.grid-wrap { display: flex; flex-direction: column; min-height: 0; flex: 1; }
.selbar {
  display: flex; align-items: center; gap: 8px; min-height: 48px; padding: 6px 24px; flex-wrap: wrap;
  border-bottom: 1px solid var(--border); font-size: 14px;
}
.selbar.on { background: color-mix(in srgb, var(--ms-pick) 12%, var(--background)); }
.count { font-weight: 600; margin-right: 8px; }
.hint { color: var(--muted-foreground); font-size: 13px; }
.scroller { flex: 1; overflow-y: auto; padding: 16px 24px 32px; user-select: none; position: relative; }
.space { position: relative; }
.head {
  position: absolute; left: 0; right: 0; height: 44px; padding-top: 14px;
  font-size: 14px; font-weight: 600; color: var(--foreground);
}
.tile { position: absolute; cursor: pointer; border-radius: 10px; }
.img {
  position: relative; overflow: hidden; border-radius: 10px; background: var(--muted);
  outline: 3px solid transparent; outline-offset: -3px; transition: outline-color 120ms var(--ms-ease);
}
.img img { width: 100%; height: 100%; object-fit: cover; display: block; transition: transform 300ms var(--ms-ease); }
.tile:hover img { transform: scale(1.03); }
.tile.sel .img { outline-color: var(--ms-pick); }
.check {
  position: absolute; top: 8px; left: 8px; width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center;
  background: var(--ms-pick); color: #fff; font-size: 12px;
  opacity: 0; transform: scale(.6); transition: opacity 120ms var(--ms-ease), transform 120ms var(--ms-ease);
}
.tile.sel .check { opacity: 1; transform: none; }
.badge {
  position: absolute; right: 8px; bottom: 8px; padding: 3px 6px; border-radius: 6px; display: flex;
  background: rgba(0, 0, 0, .6); color: #fff;
}
.cap { padding: 6px 2px 0; font-size: 12px; line-height: 16px; }
.cap .ico { display: inline; width: 11px; height: 11px; margin: -2px 4px 0 0; vertical-align: middle; }
.l0, .l1, .l2 { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.l0 { color: var(--foreground); font-weight: 500; }
.l1 { color: var(--foreground); }
.l2 { color: var(--muted-foreground); }
.band {
  position: absolute; pointer-events: none; border-radius: 4px;
  border: 1px solid var(--ms-pick); background: color-mix(in srgb, var(--ms-pick) 15%, transparent);
}
</style>
