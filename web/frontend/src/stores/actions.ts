import { defineStore } from 'pinia'
import { ref } from 'vue'
import { toast } from 'vue-sonner'
import { api, type ActionResp, type CollectTarget, type Meta } from '@/api'
import { useSettings } from './settings'

// Actions that change files (delete, undo, collections) and what the pages need to know after them.
export const useActions = defineStore('actions', () => {
  const settings = useSettings()
  const meta = ref<Meta | null>(null)
  const undoLabel = ref('') // what Ctrl+Z undoes now ("" = nothing)
  const version = ref(0) // bumps after every change: lists reload
  const busy = ref(false)

  async function loadMeta() {
    meta.value = await api.meta()
    if (settings.collection !== null && !meta.value.collections.some((c) => c.id === settings.collection))
      settings.collection = null // deleted meanwhile
  }

  async function refreshUndo() {
    undoLabel.value = (await api.undoState()).undo
  }

  function report(r: ActionResp, withUndo: boolean) {
    undoLabel.value = r.undo
    const failed = r.failed ?? []
    const description = failed.length
      ? `Не получилось: ${failed.slice(0, 3).map((f) => `${f.path.split('/').pop()} — ${f.error}`).join('; ')}`
        + (failed.length > 3 ? ` и ещё ${failed.length - 3}` : '')
      : undefined
    const show = failed.length ? toast.warning : toast.success
    show(r.message, {
      description,
      duration: failed.length ? 10000 : 5000,
      action: withUndo && r.undo ? { label: 'Отменить', onClick: () => void undo() } : undefined,
    })
  }

  async function run(fn: () => Promise<ActionResp>, withUndo = true) {
    if (busy.value) return
    busy.value = true
    try {
      const r = await fn()
      report(r, withUndo)
      version.value++
      return r
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      busy.value = false
    }
  }

  const del = (ids: number[]) => run(() => api.del(ids))
  const undo = () => run(() => api.undo(), false)

  async function collect(ids: number[], target: CollectTarget = {}, copy = settings.copyMode) {
    const t = target.collection === undefined && !target.person && !target.new && settings.collection !== null
      ? { collection: settings.collection }
      : target
    const r = await run(() => api.collect(ids, copy, t))
    if (r?.collection) settings.collection = r.collection.id // a collection chosen or made here becomes the current one
    await loadMeta()
  }

  async function byPeople(ids: number[]) {
    await run(() => api.byPeople(ids, settings.copyMode))
    await loadMeta()
  }

  async function newCollection(name: string) {
    const r = await run(() => api.newCollection(name), false)
    if (r?.collection) settings.collection = r.collection.id
    await loadMeta()
  }

  return { meta, undoLabel, version, busy, loadMeta, refreshUndo, run, del, undo, collect, byPeople, newCollection }
})
