// The filter language used by list pages:
//
//   deploy status:failed inventory:prod-* label:web -label:legacy
//
// Free words search names. "key:value" terms map to API filters through a
// per-page table; "*" makes a prefix/suffix/contains match; a leading "-"
// negates (not__ filters). Quoted values may contain spaces.

export interface Term {
  key: string | null
  value: string
  negate: boolean
  raw: string
}

export function tokenize(input: string): Term[] {
  const out: Term[] = []
  const re = /(-?)(?:([a-z_][a-z0-9_.]*):)?(?:"([^"]*)"|(\S+))/gi
  for (const m of input.matchAll(re)) {
    const [raw, neg, key, quoted, bare] = m
    const value = quoted ?? bare ?? ''
    if (!key && !value) continue
    out.push({ key: key ? key.toLowerCase() : null, value, negate: neg === '-', raw })
  }
  return out
}

export interface KeySpec {
  /** API field to filter, e.g. "status" or "inventory__name". */
  field: string
  /** Lookup used when the value has no "*". Defaults to exact. */
  lookup?: 'exact' | 'iexact' | 'icontains' | 'in'
  /** Value mapping, e.g. "failed" -> "failed,error". */
  map?: (v: string) => string
  help?: string
}

export type KeyTable = Record<string, KeySpec>

export interface Compiled {
  params: Record<string, string>
  search: string
  unknown: string[]
}

/** Turns a query string into API params. */
export function compile(input: string, keys: KeyTable, searchParam = 'search'): Compiled {
  const params: Record<string, string> = {}
  const words: string[] = []
  const unknown: string[] = []
  for (const t of tokenize(input)) {
    if (!t.key) {
      words.push(t.value)
      continue
    }
    const spec = keys[t.key]
    if (!spec) {
      unknown.push(t.key)
      continue
    }
    let v = spec.map ? spec.map(t.value) : t.value
    let lookup: string = spec.lookup ?? 'exact'
    const star = v.includes('*')
    if (star) {
      const core = v.replace(/\*/g, '')
      if (v.startsWith('*') && v.endsWith('*')) lookup = 'icontains'
      else if (v.endsWith('*')) lookup = 'istartswith'
      else if (v.startsWith('*')) lookup = 'iendswith'
      else lookup = 'icontains'
      v = core
    } else if (v.includes(',') && lookup === 'exact') {
      lookup = 'in'
    }
    const name = (t.negate ? 'not__' : '') + spec.field + (lookup === 'exact' ? '' : '__' + lookup)
    params[name] = v
  }
  if (words.length) params[searchParam] = words.join(' ')
  return { params, search: words.join(' '), unknown }
}

/** Replaces or adds a key:value term in a query string. */
export function setTerm(input: string, key: string, value: string | null): string {
  const kept = tokenize(input).filter((t) => t.key !== key)
  const raws = kept.map((t) => t.raw)
  if (value !== null && value !== '') raws.push(`${key}:${/\s/.test(value) ? `"${value}"` : value}`)
  return raws.join(' ')
}

export function getTerm(input: string, key: string): string | null {
  const t = tokenize(input).find((x) => x.key === key && !x.negate)
  return t ? t.value : null
}

/** Status shorthands shared by job and template lists. */
export function statusValue(v: string): string {
  switch (v.toLowerCase()) {
    case 'failed':
    case 'failing':
      return 'failed,error'
    case 'running':
    case 'active':
      return 'pending,waiting,running'
    case 'ok':
    case 'success':
      return 'successful'
  }
  return v.toLowerCase()
}
