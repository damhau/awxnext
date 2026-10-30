/** An execution environment's image contents, as read from its registry. */
export interface Collection {
  name: string
  version: string
  path: string
}

export interface Contents {
  image: string
  digest: string
  ansible_core: string
  ansible: string
  python: string
  collections: Collection[]
  ansible_cfg: string
  ansible_cfg_path: string
  os: string
  architecture: string
  created: string
}

/** execution_environments/N/contents/ */
export interface ContentsRead {
  status: 'none' | 'running' | 'successful' | 'failed'
  image?: string
  digest?: string
  error?: string
  started?: string
  finished?: string | null
  contents?: Contents | null
  current_image?: string
}

/** The collections whose name or path holds q (any case). */
export function filterCollections(list: Collection[], q: string): Collection[] {
  const s = q.trim().toLowerCase()
  if (!s) return list
  return list.filter((c) => c.name.toLowerCase().includes(s) || c.path.toLowerCase().includes(s))
}

/** The read is of another image than the environment's now (it changed). */
export function readOfOtherImage(r: ContentsRead): boolean {
  return !!r.image && !!r.current_image && r.image !== r.current_image
}

/** A digest shortened for display: sha256:0123456789ab. */
export function shortDigest(d: string | undefined): string {
  if (!d) return ''
  const [alg, hex] = d.split(':')
  return hex ? `${alg}:${hex.slice(0, 12)}` : d.slice(0, 19)
}
