// Thin fetch wrapper for the AWX-compatible API. Session cookies carry
// authentication; mutating requests echo the csrftoken cookie.

export type Params = Record<string, string | number | boolean | undefined | null | (string | number)[]>

export interface Page<T> {
  count: number
  next: string | null
  previous: string | null
  results: T[]
}

export class ApiError extends Error {
  status: number
  body: unknown
  constructor(status: number, body: unknown) {
    super(describe(status, body))
    this.status = status
    this.body = body
  }
  /** Field errors as returned by DRF-style validation ({field: [msg]}). */
  get fields(): Record<string, string> {
    const out: Record<string, string> = {}
    if (this.body && typeof this.body === 'object' && !Array.isArray(this.body)) {
      for (const [k, v] of Object.entries(this.body as Record<string, unknown>)) {
        if (k === 'detail') continue
        out[k] = Array.isArray(v) ? v.map(String).join(' ') : typeof v === 'string' ? v : JSON.stringify(v)
      }
    }
    return out
  }
}

function describe(status: number, body: unknown): string {
  if (body && typeof body === 'object') {
    const b = body as Record<string, unknown>
    if (typeof b.detail === 'string') return b.detail
    if (typeof b.error === 'string') return b.error
    const first = Object.entries(b)[0]
    if (first) {
      const [k, v] = first
      const msg = Array.isArray(v) ? v.join(' ') : typeof v === 'string' ? v : JSON.stringify(v)
      return k === '__all__' || k === 'non_field_errors' ? msg : `${k}: ${msg}`
    }
  }
  if (typeof body === 'string' && body) return body
  return `Request failed (${status})`
}

export function cookie(name: string): string | undefined {
  const m = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'))
  return m ? decodeURIComponent(m[1]) : undefined
}

export function qs(params?: Params): string {
  if (!params) return ''
  const u = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    u.set(k, Array.isArray(v) ? v.join(',') : String(v))
  }
  const s = u.toString()
  return s ? '?' + s : ''
}

type Listener = () => void
const unauthorized = new Set<Listener>()
/** Subscribe to 401 responses (session expired). */
export function onUnauthorized(fn: Listener): () => void {
  unauthorized.add(fn)
  return () => unauthorized.delete(fn)
}

export function url(path: string): string {
  if (path.startsWith('/')) return path
  return '/api/v2/' + path
}

export async function request<T>(method: string, path: string, body?: unknown, opts: { quiet401?: boolean; signal?: AbortSignal } = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
    const t = cookie('csrftoken')
    if (t) headers['X-CSRFToken'] = t
  }
  const res = await fetch(url(path), {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
    signal: opts.signal,
  })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let data: unknown = text
  if (text && (res.headers.get('Content-Type') ?? '').includes('json')) {
    try {
      data = JSON.parse(text)
    } catch {
      /* keep text */
    }
  }
  if (!res.ok) {
    if (res.status === 401 && !opts.quiet401) unauthorized.forEach((f) => f())
    throw new ApiError(res.status, data)
  }
  return data as T
}

export const api = {
  get: <T>(path: string, params?: Params, signal?: AbortSignal) => request<T>('GET', path + qs(params), undefined, { signal }),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body ?? {}),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body: unknown) => request<T>('PATCH', path, body),
  del: (path: string) => request<void>('DELETE', path),
  options: <T>(path: string) => request<T>('OPTIONS', path),
  text: async (path: string): Promise<string> => {
    const res = await fetch(url(path), { credentials: 'same-origin' })
    if (!res.ok) throw new ApiError(res.status, await res.text())
    return res.text()
  },
}

/** Fetches every page of a list (bounded by max pages). */
export async function getAll<T>(path: string, params: Params = {}, maxPages = 20): Promise<T[]> {
  const out: T[] = []
  let page = 1
  for (;;) {
    const r = await api.get<Page<T>>(path, { page_size: 200, ...params, page })
    out.push(...r.results)
    if (!r.next || page >= maxPages) return out
    page++
  }
}
