import { tr } from './i18n'

// How each kind of setting value is edited: amounts in a unit, secrets,
// lists, variables, small forms and records. The API's values do not
// change: these turn them into what the controls show, and back.

const isObject = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)

/** The control that edits a setting. */
export type Kind =
  | 'readonly'
  | 'secret'
  | 'switch'
  | 'amount'
  | 'choice'
  | 'object'
  | 'chips'
  | 'checks'
  | 'pairs'
  | 'fields'
  | 'records'
  | 'paths'
  | 'ldap-search'
  | 'image'
  | 'lines'
  | 'text'
  | 'json'
  | 'oidc-mapping'
  | 'archive-credential'

/** What the layout says of a row's control: see Row in settingsLayout.ts. */
export interface ControlHints {
  control?: Kind
  options?: [string, string][]
  lines?: boolean
}

/** Settings stored encrypted: the API returns $encrypted$ once they are set. */
export const isSecret = (key: string) => /(_SECRET|_PASSWORD|_PRIVATE_KEY)$/.test(key)
export const ENCRYPTED = '$encrypted$'

/** The control of a setting, from what the layout says and its type. */
export function controlKind(key: string, type: string | undefined, hints: ControlHints, writable: boolean, value?: unknown): Kind {
  if (!writable) return 'readonly'
  if (hints.control) return hints.control
  if (isSecret(key)) return 'secret'
  switch (type) {
    case 'boolean':
      return 'switch'
    case 'integer':
    case 'float':
      return 'amount'
    case 'choice':
      return 'choice'
    case 'field':
      return 'object'
    case 'list':
      if (hints.options) return 'checks'
      // A list of words; one of something else, or not a list yet (AWX's SAML extra data), stays JSON.
      return Array.isArray(value) && value.every((x) => typeof x === 'string' || typeof x === 'number') ? 'chips' : 'json'
    case 'nested object':
    case 'dict':
      return 'json'
  }
  return hints.lines ? 'lines' : 'text'
}

/** Durations are stored in seconds, sizes in bytes. */
export type UnitKind = 'duration' | 'size'

/** A kind's units: each one's name and how many stored units it is. */
export function unitsOf(kind: UnitKind): [string, number][] {
  return kind === 'duration'
    ? [
        [tr('seconds'), 1],
        [tr('minutes'), 60],
        [tr('hours'), 3600],
        [tr('days'), 86400],
      ]
    : [
        [tr('bytes'), 1],
        [tr('KiB'), 1024],
        [tr('MiB'), 1048576],
        [tr('GiB'), 1073741824],
      ]
}

/** The largest unit a value is a whole number of (7200 seconds: hours);
 * minutes or MiB for 0 or no value. */
export function bestUnit(value: unknown, kind: UnitKind): number {
  const units = unitsOf(kind)
  if (typeof value !== 'number' || !value) return kind === 'duration' ? 60 : 1048576
  for (let i = units.length - 1; i > 0; i--) if (value % units[i][1] === 0) return units[i][1]
  return 1
}

/** A value shown in a unit. */
export const amountText = (value: unknown, factor = 1) => (typeof value === 'number' ? String(value / factor) : '')

/** An amount typed in a unit, as the value stored: whole, or null when the field is empty or not a number. */
export function amountValue(text: string, factor = 1): number | null {
  if (text.trim() === '') return null
  const n = Number(text)
  return Number.isFinite(n) ? Math.round(n * factor) : null
}

/** A name and a value: an environment variable, a parameter. */
export interface Pair {
  key: string
  value: string
}

export function pairsOf(v: unknown): Pair[] {
  if (!isObject(v)) return []
  return Object.entries(v).map(([key, value]) => ({ key, value: typeof value === 'string' ? value : JSON.stringify(value) }))
}

/** The pairs as an object, without those that have no name. With `coerce`,
 * whole numbers and true or false are stored as such (LDAP's options);
 * otherwise every value is a string (environment variables). */
export function pairsValue(pairs: Pair[], coerce = false): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const p of pairs) {
    const k = p.key.trim()
    if (!k) continue
    const v = p.value
    out[k] = !coerce ? v : /^-?\d+$/.test(v.trim()) ? Number(v) : v === 'true' ? true : v === 'false' ? false : v
  }
  return out
}

/** Items typed into a list, one per line or separated by commas, added to it once each. */
export function addItems(list: string[], text: string): string[] {
  const out = [...list]
  for (const s of text.split(/[\n,]/)) {
    const t = s.trim()
    if (t && !out.includes(t)) out.push(t)
  }
  return out
}

/** A host directory mounted into job containers: HOST-DIR[:CONTAINER-DIR[:OPTIONS]]. */
export interface Mount {
  host: string
  container: string
  options: string
}

export function mountsOf(v: unknown): Mount[] {
  if (!Array.isArray(v)) return []
  return v.map((s) => {
    const [host = '', container = '', options = ''] = String(s).split(':')
    return { host, container, options }
  })
}

/** The mounts as AWX writes them; options need the container path, the host's when none is given. */
export function mountsValue(list: Mount[]): string[] {
  return list.flatMap((m) => {
    const host = m.host.trim()
    if (!host) return []
    const options = m.options.trim()
    const container = m.container.trim() || (options ? host : '')
    return [[host, container, options].filter(Boolean).join(':')]
  })
}

/** An LDAP search: [base DN, scope, filter]. */
export interface Search {
  base: string
  scope: string
  filter: string
}

export const searchScopes = ['SCOPE_SUBTREE', 'SCOPE_ONELEVEL', 'SCOPE_BASE']

/** A search setting's searches: one search, or a list of them (AWX's LDAPSearchUnion). */
export function searchesOf(v: unknown): Search[] {
  if (!Array.isArray(v) || !v.length) return []
  const one = (x: unknown[]): Search => ({ base: String(x[0] ?? ''), scope: String(x[1] ?? 'SCOPE_SUBTREE'), filter: String(x[2] ?? '') })
  return Array.isArray(v[0]) ? v.filter(Array.isArray).map(one) : [one(v)]
}

/** The searches as stored: none, one, or a list of several. */
export function searchesValue(list: Search[]): unknown[] {
  const kept = list.filter((s) => s.base.trim() || s.filter.trim()).map((s) => [s.base.trim(), s.scope, s.filter.trim()])
  return kept.length === 1 ? kept[0] : kept
}

/** A field of an object setting edited as a form. */
export interface Field {
  key: string
  label: string
  kind?: 'text' | 'lines' | 'switch' | 'choice'
  choices?: [string, string][]
  placeholder?: string
  /** Stored when the field is emptied, rather than leaving the key out. */
  empty?: unknown
}

/** A field's value in an object: text, or on and off. */
export function fieldValue(obj: unknown, f: Field): string | boolean {
  const v = isObject(obj) ? obj[f.key] : undefined
  if (f.kind === 'switch') return v === true
  if (Array.isArray(v)) return v.map(String).join(', ')
  return typeof v === 'string' || typeof v === 'number' ? String(v) : ''
}

/** The object with a field set; the keys the form does not show are kept. */
export function withField(obj: unknown, f: Field, v: string | boolean): Record<string, unknown> {
  const out = { ...(isObject(obj) ? obj : {}) }
  if (v === '') {
    if ('empty' in f) out[f.key] = f.empty
    else delete out[f.key]
  } else out[f.key] = v
  return out
}

/** Objects by name: SAML's identity providers, its organization by language. */
export interface NamedRecord {
  name: string
  value: Record<string, unknown>
}

export function recordsOf(v: unknown): NamedRecord[] {
  return isObject(v) ? Object.entries(v).map(([name, value]) => ({ name, value: isObject(value) ? value : {} })) : []
}

export function recordsValue(list: NamedRecord[]): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const r of list) if (r.name.trim()) out[r.name.trim()] = r.value
  return out
}
