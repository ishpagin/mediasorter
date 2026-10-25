import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { api, type Person } from '@/api'
import { useActions } from './actions'

// every person with faces (for pickers); reloaded after any change (merge, rename, faces fixed by hand)
export const usePeople = defineStore('people', () => {
  const all = ref<Person[]>([])
  const actions = useActions()
  let loaded = -1
  async function load(force = false) {
    if (!force && loaded === actions.version) return
    loaded = actions.version
    all.value = await api.personsAll()
  }
  watch(() => actions.version, () => { if (loaded >= 0) void load() })
  return { all, load }
})
