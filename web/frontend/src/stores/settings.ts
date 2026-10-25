import { defineStore } from 'pinia'
import { useStorage } from '@vueuse/core'

// per-browser preferences (the old UI kept them per tab)
export const useSettings = defineStore('settings', () => {
  const showVideo = useStorage('ms-show-video', false) // off: a bulk delete never hits videos by accident
  const hideMoved = useStorage('ms-hide-moved', true) // files moved into collections are not in the lists
  const collection = useStorage<number | null>('ms-collection', null) // where "to collection" puts files
  const copyMode = useStorage('ms-copy-mode', false) // collections: copy instead of move
  return { showVideo, hideMoved, collection, copyMode }
})
