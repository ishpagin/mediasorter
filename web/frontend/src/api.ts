// Typed client of the Go API (web/internal/api).

export interface Item {
  id: number
  kind: 'image' | 'video'
  name: string
  path: string
  taken?: string
  place?: string
  who?: string // named people, "+N" = other faces
  coll?: string // collections it is in
  tag?: string // top scene category
  group?: number // stack / copy group (stacks, copies pages)
  head?: string // group header, on the first item of a group
  face?: number // show this face instead of the whole frame
  note?: string // a page-specific first caption line (rank in a stack, search score...)
}

export interface Person {
  id: number
  name: string // "" = unnamed
  n: number // files
  face?: number // best face (thumbnail)
  years?: string
  hidden?: boolean
}

export interface PersonInfo {
  id: number
  name: string
  files: number
  hidden: boolean
  skipCollect: boolean
  co: Person[] // who appears with them, n = files together
}

// who is where on a file: one per person per file, ungrouped faces one by one
export interface Unit { file: number; path: string; person: number | null; faces: number[]; face: number }

export interface SimilarPair { sim: number; a: Person; b: Person; facesA: number[]; facesB: number[] }

export const personLabel = (p: { id: number; name?: string }) => p.name || `person_${p.id}`

export interface Count { label: string; n: number }

export interface FilesResp {
  items: Item[]
  years: string[]
  cats: Count[]
  hiddenVideo: number
  hiddenMoved: number
}

export interface GroupsResp extends FilesResp {
  groups: number
  extra: number // files "keep the best" would delete
  bytes: number // their size (copies)
  todo: number // photos without a quality score yet
  keep: Record<string, number> // group -> file to keep
  state: '' | 'noclip' | 'empty'
}

export interface Job { name: string | null; running: boolean; progress: number; message: string; error: string }

export interface CollectionCard {
  id: number; name: string; folder: string; files: number; videos: number; mb: number; zipMB: number; zipFolder?: string
}
export interface CollectionsResp { root: string; rootShown: string; collections: CollectionCard[] }

export interface Meta {
  files: number
  faces: number
  people: number
  collections: { id: number; name: string }[]
}

export interface Failure { path: string; error: string }

// the result of an action: a message for the toast, what failed, what Ctrl+Z undoes now
export interface ActionResp {
  message: string
  failed: Failure[] | null
  undo: string
  collection?: { id: number; name: string }
  person?: number // where the faces went (remap)
}

export interface CollectTarget { collection?: number; person?: number; new?: string }

async function get<T>(url: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch(url, { signal })
  if (!r.ok) throw new Error(`${r.status} ${(await r.text()).trim()}`)
  return r.json() as Promise<T>
}

async function post<T>(url: string, body: unknown = {}, method = 'POST'): Promise<T> {
  const r = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!r.ok) throw new Error((await r.text()).trim() || `${r.status}`)
  return r.json() as Promise<T>
}

export const api = {
  meta: () => get<Meta>('/api/meta'),
  files: (params: Record<string, string>, signal?: AbortSignal) =>
    get<FilesResp>('/api/files?' + new URLSearchParams(params), signal),
  del: (ids: number[]) => post<ActionResp>('/api/delete', { ids }),
  undo: () => post<ActionResp>('/api/undo'),
  undoState: () => get<ActionResp>('/api/undo'),
  collect: (ids: number[], copy: boolean, target: CollectTarget) =>
    post<ActionResp>('/api/collect', { ids, copy, ...target }),
  byPeople: (ids: number[], copy: boolean) => post<ActionResp>('/api/collect/by-people', { ids, copy }),
  collectPeople: (ids: number[]) =>
    get<{ id: number; name: string; n: number }[]>('/api/collect/people?ids=' + ids.join(',')),
  newCollection: (name: string) => post<ActionResp>('/api/collections', { name }),

  persons: (params: Record<string, string>) => get<Person[]>('/api/persons?' + new URLSearchParams(params)),
  personsAll: () => get<Person[]>('/api/persons/all'),
  personCreate: (name: string) => post<{ id: number }>('/api/persons', { name }),
  personPatch: (id: number, patch: { name?: string; hidden?: boolean; skipCollect?: boolean }) =>
    post<ActionResp>(`/api/persons/${id}`, patch, 'PATCH'),
  merge: (ids: number[]) => post<ActionResp>('/api/persons/merge', { ids }),
  hide: (ids: number[], hidden: boolean) => post<ActionResp>('/api/persons/hide', { ids, hidden }),
  person: (id: number) => get<PersonInfo>(`/api/person/${id}`),
  personFiles: (id: number, params: Record<string, string>, signal?: AbortSignal) =>
    get<FilesResp>(`/api/person/${id}/files?` + new URLSearchParams(params), signal),
  unlink: (id: number, files: number[]) => post<ActionResp>(`/api/person/${id}/unlink`, { files }),
  remap: (id: number, target: { to?: number; new?: string }) => post<ActionResp>(`/api/person/${id}/remap`, target),
  only: (params: Record<string, string>, signal?: AbortSignal) =>
    get<FilesResp>('/api/only?' + new URLSearchParams(params), signal),
  faces: (files: number[]) => get<Unit[]>('/api/faces?files=' + files.join(',')),
  assign: (changes: { faces: number[]; person: number | null }[]) => post<ActionResp>('/api/faces/assign', { changes }),
  similar: (params: Record<string, string>) => get<SimilarPair[]>('/api/similar?' + new URLSearchParams(params)),
  notSame: (a: number, b: number) => post<ActionResp>('/api/notsame', { a, b }),
  search: (params: Record<string, string>, signal?: AbortSignal) =>
    get<FilesResp>('/api/search?' + new URLSearchParams(params), signal),
  stacks: (params: Record<string, string>, signal?: AbortSignal) =>
    get<GroupsResp>('/api/stacks?' + new URLSearchParams(params), signal),
  copies: (params: Record<string, string>, signal?: AbortSignal) =>
    get<GroupsResp>('/api/copies?' + new URLSearchParams(params), signal),
  collections: () => get<CollectionsResp>('/api/collections'),
  collectionsRoot: (root: string) => post<ActionResp>('/api/collections/root', { root }, 'PUT'),
  collectionFiles: (id: number, signal?: AbortSignal) => get<FilesResp>(`/api/collections/${id}/files`, signal),
  collectionRemove: (id: number, files: number[]) => post<ActionResp>(`/api/collections/${id}/remove`, { files }),
  collectionZip: (id: number) => post<ActionResp>(`/api/collections/${id}/zip`),
  collectionFinish: (id: number, keepZip: boolean) => post<ActionResp>(`/api/collections/${id}/finish`, { keepZip }),
  collectionDelete: (id: number) => post<ActionResp>(`/api/collections/${id}`, {}, 'DELETE'),
  job: () => get<Job>('/api/jobs'),
  startJob: (name: string) => post<Job>('/api/jobs', { name }),
}

export const faceUrl = (id: number) => `/thumb/face/${id}`

export const previewUrl = (id: number) => `/preview/${id}`
export const originalUrl = (id: number) => `/original/${id}`
