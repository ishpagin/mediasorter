import { ref, watch, type Ref } from 'vue'
import type { FilesResp } from '@/api'
import { useActions } from '@/stores/actions'
import { useSettings } from '@/stores/settings'

// A file list that reloads when its parameters, the sidebar filters or the files themselves change
// (after a delete / undo / collect); a newer request cancels the older one.
export function useFileList<T extends FilesResp = FilesResp>(
  fetch: (params: Record<string, string>, signal: AbortSignal) => Promise<T>,
  params: () => Record<string, string> | null,
) {
  const settings = useSettings()
  const actions = useActions()
  const data = ref<T | null>(null) as Ref<T | null>
  const loading = ref(false)
  const error = ref('')
  let ctl: AbortController | null = null

  async function load() {
    const p = params()
    ctl?.abort()
    if (p === null) { data.value = null; return } // nothing to show yet (e.g. no people chosen)
    ctl = new AbortController()
    loading.value = true
    error.value = ''
    try {
      data.value = await fetch({
        ...p, video: settings.showVideo ? '1' : '', moved: settings.hideMoved ? '' : '1',
      }, ctl.signal)
    } catch (e) {
      if ((e as Error).name !== 'AbortError') error.value = `Не удалось загрузить список: ${(e as Error).message}`
    } finally {
      loading.value = false
    }
  }
  watch(() => [JSON.stringify(params()), settings.showVideo, settings.hideMoved, actions.version], load, { immediate: true })
  return { data, loading, error, reload: load }
}
