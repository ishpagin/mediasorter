import { computed, ref, type Ref } from 'vue'
import type { GroupsResp } from '@/api'
import { useActions } from '@/stores/actions'

// "Keep the best" / "keep the selected" on the stacks and copies pages: what would be deleted, then confirm.
// Only files visible in the list count (files moved into collections or hidden videos are never touched).
export function useKeep(data: Ref<GroupsResp | null>) {
  const actions = useActions()
  const doomed = ref<number[]>([])
  const open = ref(false)
  const what = ref('')

  const groupOf = computed(() => new Map((data.value?.items ?? []).map((it) => [it.id, it.group!])))

  function plan(mode: 'best' | 'selected', selected: number[]) {
    const d = data.value
    if (!d) return
    const groups = selected.length ? new Set(selected.map((id) => groupOf.value.get(id))) : null // none = all
    const keep = new Set<number>()
    for (const it of d.items) {
      if (groups && !groups.has(it.group!)) continue
      if (mode === 'selected' ? selected.includes(it.id) : d.keep[String(it.group)] === it.id) keep.add(it.id)
    }
    doomed.value = d.items
      .filter((it) => (!groups || groups.has(it.group!)) && !keep.has(it.id)
        && (mode === 'selected' || d.keep[String(it.group)] !== undefined)) // unscored stacks: nothing to keep yet
      .map((it) => it.id)
    what.value = mode === 'best'
      ? (groups ? 'Останется лучший в каждой из выбранных групп' : 'Останется лучший в каждой группе')
      : 'Останутся выбранные, остальное из их групп удалится'
    open.value = true
  }

  async function confirm() {
    open.value = false
    if (doomed.value.length) await actions.del(doomed.value)
  }
  return { doomed, open, what, plan, confirm }
}
