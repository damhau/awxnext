import { tr } from './i18n'
import { tokenize } from './query'
// The collection hub's pages (roadmap M13-05): versions, install lines,
// module and plugin docs as ansible-doc reads them, and file diffs.

/** A semantic version's parts; prerelease sorts before its release. */
function parse(v: string): { core: number[]; pre: string[] } {
  const [main, ...rest] = v.replace(/\+.*$/, '').split('-')
  return { core: main.split('.').map((n) => Number(n) || 0), pre: rest.length ? rest.join('-').split('.') : [] }
}

/** Compares two semantic versions: negative when a is older. */
export function compareVersions(a: string, b: string): number {
  const x = parse(a)
  const y = parse(b)
  for (let i = 0; i < 3; i++) if ((x.core[i] ?? 0) !== (y.core[i] ?? 0)) return (x.core[i] ?? 0) - (y.core[i] ?? 0)
  if (!x.pre.length || !y.pre.length) return y.pre.length - x.pre.length
  for (let i = 0; i < Math.max(x.pre.length, y.pre.length); i++) {
    const p = x.pre[i]
    const q = y.pre[i]
    if (p === undefined) return -1
    if (q === undefined) return 1
    const pn = /^\d+$/.test(p)
    const qn = /^\d+$/.test(q)
    if (pn && qn && Number(p) !== Number(q)) return Number(p) - Number(q)
    if (pn !== qn) return pn ? -1 : 1
    if (p !== q) return p < q ? -1 : 1
  }
  return 0
}

/** Newest first. */
export function sortVersions<T extends { version: string }>(vs: T[]): T[] {
  return [...vs].sort((a, b) => compareVersions(b.version, a.version))
}

/** Where ansible-galaxy installs a repository's collections from. */
export function galaxyServer(origin: string, repository = 'published'): string {
  return `${origin.replace(/\/$/, '')}/api/galaxy/content/${encodeURIComponent(repository)}/`
}

export function installCommand(fqcn: string, version: string, server: string): string {
  return `ansible-galaxy collection install ${fqcn}:==${version} \\\n  --server ${server}`
}

/** The entry of a requirements.yml. */
export function requirementsEntry(fqcn: string, version: string, server?: string): string {
  return `collections:\n  - name: ${fqcn}\n    version: "${version}"` + (server ? `\n    source: ${server}` : '')
}

/** A collection's dependencies, by name. */
export function dependencies(meta: Record<string, unknown> | undefined): { name: string; spec: string }[] {
  const deps = (meta?.dependencies ?? {}) as Record<string, unknown>
  return Object.entries(deps)
    .map(([name, spec]) => ({ name, spec: String(spec ?? '*') }))
    .sort((a, b) => a.name.localeCompare(b.name))
}

export const PLUGIN_TYPES: [string, string][] = [
  ['module', tr('Modules')],
  ['role', tr('Roles')],
  ['action', tr('Action plugins')],
  ['become', tr('Become plugins')],
  ['cache', tr('Cache plugins')],
  ['callback', tr('Callback plugins')],
  ['cliconf', tr('CLI configuration plugins')],
  ['connection', tr('Connection plugins')],
  ['filter', tr('Filters')],
  ['httpapi', tr('HTTP API plugins')],
  ['inventory', tr('Inventory plugins')],
  ['lookup', tr('Lookup plugins')],
  ['netconf', tr('Netconf plugins')],
  ['shell', tr('Shell plugins')],
  ['strategy', tr('Strategy plugins')],
  ['test', tr('Tests')],
  ['vars', tr('Vars plugins')],
]

export interface DocEntry {
  type: string
  name: string
  short_description: string
  error: string
}

/** The docs' index by plugin type, in PLUGIN_TYPES' order. */
export function groupContents(contents: DocEntry[]): { type: string; label: string; items: DocEntry[] }[] {
  const order = new Map(PLUGIN_TYPES.map(([t], i) => [t, i]))
  const by = new Map<string, DocEntry[]>()
  for (const c of contents) by.set(c.type, [...(by.get(c.type) ?? []), c])
  return [...by.entries()]
    .sort(([a], [b]) => (order.get(a) ?? 99) - (order.get(b) ?? 99) || a.localeCompare(b))
    .map(([type, items]) => ({ type, label: PLUGIN_TYPES.find(([t]) => t === type)?.[1] ?? type, items: [...items].sort((a, b) => a.name.localeCompare(b.name)) }))
}

/** A description, a string or a list of paragraphs. */
export function paragraphs(v: unknown): string[] {
  if (v === undefined || v === null || v === '') return []
  if (Array.isArray(v)) return v.map((p) => (typeof p === 'string' ? p : JSON.stringify(p)))
  return [typeof v === 'string' ? v : JSON.stringify(v)]
}

export interface OptionRow {
  name: string
  /** The option's path, such as config.port, for its anchor. */
  path: string
  depth: number
  description: string[]
  type: string
  required: boolean
  default?: string
  choices: string[]
  aliases: string[]
  returned?: string
  sample?: string
}

const shown = (v: unknown): string => (typeof v === 'string' ? v : JSON.stringify(v))

/** An options or return values tree as rows, suboptions under their
 * option. */
export function optionRows(tree: unknown, depth = 0, prefix = ''): OptionRow[] {
  if (!tree || typeof tree !== 'object' || Array.isArray(tree)) return []
  const rows: OptionRow[] = []
  for (const [name, raw] of Object.entries(tree as Record<string, unknown>).sort(([a], [b]) => a.localeCompare(b))) {
    const o = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
    const path = prefix ? `${prefix}.${name}` : name
    let type = typeof o.type === 'string' ? o.type : ''
    if (type === 'list' && typeof o.elements === 'string') type = `list / ${o.elements}`
    const choices = Array.isArray(o.choices) ? o.choices.map(shown) : o.choices && typeof o.choices === 'object' ? Object.keys(o.choices) : []
    rows.push({
      name,
      path,
      depth,
      description: paragraphs(o.description),
      type,
      required: o.required === true,
      default: o.default === undefined || o.default === null ? undefined : shown(o.default),
      choices,
      aliases: Array.isArray(o.aliases) ? o.aliases.map(String) : [],
      returned: typeof o.returned === 'string' ? o.returned : undefined,
      sample: o.sample === undefined ? undefined : shown(o.sample),
    })
    rows.push(...optionRows(o.suboptions ?? o.contains, depth + 1, path))
  }
  return rows
}

export type Markup =
  | { kind: 'text' | 'code' | 'italic' | 'bold' | 'value' | 'env'; text: string }
  | { kind: 'link'; text: string; href: string }
  | { kind: 'module' | 'plugin'; text: string; fqcn: string; type: string }
  | { kind: 'option' | 'return'; text: string; name: string; plugin: string; value?: string }
  | { kind: 'rule' }

const safeURL = (u: string) => (/^https?:\/\//i.test(u.trim()) ? u.trim() : null)
const unescape = (s: string) => s.replace(/\\(.)/g, '$1')

/** Ansible's documentation markup (C(), I(), O(), ...) in a description,
 * as ansible-doc's parts. */
export function ansibleMarkup(s: string): Markup[] {
  const out: Markup[] = []
  const re = /\b(RV|[IBMULCOVEP])\(((?:\\.|[^\\)])*)\)|\bHORIZONTALLINE\b/g
  let last = 0
  for (const m of s.matchAll(re)) {
    if (m.index! > last) out.push({ kind: 'text', text: s.slice(last, m.index) })
    last = m.index! + m[0].length
    const [whole, tag, rawArg] = m
    if (!tag) {
      out.push({ kind: 'rule' })
      continue
    }
    const arg = ['O', 'V', 'RV', 'E', 'P'].includes(tag) ? unescape(rawArg) : rawArg
    switch (tag) {
      case 'C':
        out.push({ kind: 'code', text: arg })
        break
      case 'I':
        out.push({ kind: 'italic', text: arg })
        break
      case 'B':
        out.push({ kind: 'bold', text: arg })
        break
      case 'V':
        out.push({ kind: 'value', text: arg })
        break
      case 'E':
        out.push({ kind: 'env', text: arg })
        break
      case 'U': {
        const href = safeURL(arg)
        out.push(href ? { kind: 'link', text: arg, href } : { kind: 'code', text: arg })
        break
      }
      case 'L': {
        const i = arg.lastIndexOf(',')
        const text = i >= 0 ? arg.slice(0, i).trim() : arg
        const href = i >= 0 ? safeURL(arg.slice(i + 1)) : null
        out.push(href ? { kind: 'link', text, href } : { kind: 'text', text })
        break
      }
      case 'M':
        out.push({ kind: 'module', text: arg, fqcn: arg, type: 'module' })
        break
      case 'P': {
        const [fqcn, type = 'module'] = arg.split('#')
        out.push({ kind: 'plugin', text: arg, fqcn, type })
        break
      }
      case 'O':
      case 'RV': {
        // O(name), O(name=value), O(fqcn#type:name=value)
        const [ref, value] = splitValue(arg)
        const colon = ref.indexOf(':')
        const plugin = colon >= 0 && ref.slice(0, colon).includes('#') ? ref.slice(0, colon) : ''
        const name = plugin ? ref.slice(colon + 1) : ref
        out.push({ kind: tag === 'O' ? 'option' : 'return', text: value === undefined ? name : `${name}=${value}`, name, plugin, value })
        break
      }
      default:
        out.push({ kind: 'text', text: whole })
    }
  }
  if (last < s.length) out.push({ kind: 'text', text: s.slice(last) })
  return out
}

function splitValue(s: string): [string, string | undefined] {
  const i = s.indexOf('=')
  return i < 0 ? [s, undefined] : [s.slice(0, i), s.slice(i + 1)]
}

/** A module or plugin's page, from a fully qualified name in its docs:
 * those of the same collection link to their page. */
export function docLink(fqcn: string, type: string, collection: string, base: string): string | null {
  const prefix = collection + '.'
  if (!fqcn.startsWith(prefix)) return null
  return `${base}/${encodeURIComponent(type)}/${encodeURIComponent(fqcn.slice(prefix.length))}`
}

export type DocsState = 'pending' | 'reading' | 'read' | 'none' | 'failed'

export function docsStateLabel(state: string): string {
  switch (state) {
    case 'pending':
      return tr('Waiting to be read')
    case 'reading':
      return tr('Being read')
    case 'read':
      return tr('Read')
    case 'none':
      return tr('Not read')
    case 'failed':
      return tr('Could not be read')
  }
  return state
}

/** "2 added · 1 removed · 3 changed", or that nothing changed. */
export function diffSummary(c: { added: number; removed: number; changed: number }): string {
  const parts = [c.added && tr('{n} added', { n: c.added }), c.removed && tr('{n} removed', { n: c.removed }), c.changed && tr('{n} changed', { n: c.changed })].filter(Boolean)
  return parts.length ? parts.join(' · ') : tr('No file changed')
}

/** Where a version is: published, waiting for approval, rejected, or a
 * remote's repository. */
export function repositoryLabel(repo: string): string {
  switch (repo) {
    case 'published':
      return tr('Published')
    case 'staging':
      return tr('Awaiting approval')
    case 'rejected':
      return tr('Rejected')
  }
  return tr('Mirrored in {repository}', { repository: repo })
}

/** The repository clients install a version from: published, else a
 * remote's, else none (staged or rejected). */
export function installRepository(repos: string[]): string | null {
  if (repos.includes('published')) return 'published'
  return repos.find((r) => r !== 'staging' && r !== 'rejected') ?? null
}

/** The value of a key:value term of a list filter, if it has one. */
export function termValue(q: string, key: string): string | null {
  const t = tokenize(q).find((t) => t.key === key && !t.negate)
  return t ? t.value : null
}

/** The filter with key:value instead of the key's terms; null removes
 * them. */
export function setTerm(q: string, key: string, value: string | null): string {
  const kept = tokenize(q)
    .filter((t) => t.key !== key)
    .map((t) => t.raw)
  if (value !== null) kept.push(`${key}:${/\s/.test(value) ? `"${value}"` : value}`)
  return kept.join(' ')
}

export interface DiffRow {
  kind: 'hunk' | 'add' | 'del' | 'ctx' | 'note'
  text: string
  a?: number
  b?: number
}

/** A unified diff's lines with their numbers in the old and new file. */
export function numberedDiff(diff: string): DiffRow[] {
  const rows: DiffRow[] = []
  let a = 0
  let b = 0
  for (const line of diff.replace(/\n$/, '').split('\n')) {
    const h = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/)
    if (h) {
      a = Number(h[1]) || 1
      b = Number(h[2]) || 1
      rows.push({ kind: 'hunk', text: line })
    } else if (line.startsWith('\\')) rows.push({ kind: 'note', text: line.slice(2) })
    else if (line.startsWith('+')) rows.push({ kind: 'add', text: line.slice(1), b: b++ })
    else if (line.startsWith('-')) rows.push({ kind: 'del', text: line.slice(1), a: a++ })
    else rows.push({ kind: 'ctx', text: line.slice(1), a: a++, b: b++ })
  }
  return rows
}

/** The text of an import's log, for copying. */
export function importLogText(messages: { level?: string; message?: string; time?: string }[]): string {
  return messages.map((m) => `${m.level ?? 'INFO'} ${m.message ?? ''}`).join('\n')
}

/** An artifact's size: "812 B", "14.2 KiB", "3.1 MiB". */
export function sizeText(n: number): string {
  if (n < 1024) return tr('{n} B', { n })
  if (n < 1024 * 1024) return tr('{n} KiB', { n: (n / 1024).toFixed(1) })
  return tr('{n} MiB', { n: (n / 1024 / 1024).toFixed(1) })
}
