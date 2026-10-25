import { useRoute, useRouter } from 'vue-router'

// Filters live in the URL query: F5, back / forward and bookmarks keep them. Defaults stay out of the URL.
export function useQuery() {
  const route = useRoute()
  const router = useRouter()
  const q = (k: string, def = '') => (typeof route.query[k] === 'string' ? (route.query[k] as string) : def)
  function set(k: string, v: string | number | boolean | null | undefined, def: string = '') {
    const s = v === null || v === undefined || v === false ? '' : v === true ? '1' : String(v)
    void router.replace({ query: { ...route.query, [k]: s && s !== def ? s : undefined } })
  }
  return { q, set, route }
}
