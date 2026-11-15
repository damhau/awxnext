// Event streams and rules (roadmap M14-05): what their pages compute
// without the API. The completion of CEL expressions from a stream's
// inferred schema, the errors the server's check gives, how decisions are
// shown, and the setup snippets of each kind of sender.
import type { CellState } from './cells'
import { asIs, tr } from './i18n'

/** A value's shape, as GET event_streams/N/schema/ describes it. */
export interface SchemaNode {
  type: string
  fields?: Record<string, SchemaNode>
  items?: SchemaNode
  values?: SchemaNode
}

export interface StreamSchema {
  events: number
  event: SchemaNode
}

const dyn: SchemaNode = { type: 'dyn' }

function mergeNode(a: SchemaNode | undefined, b: SchemaNode | undefined): SchemaNode {
  if (!a) return b ?? dyn
  if (!b) return a
  if (a.type === 'dyn' && !a.fields) return b
  if (b.type === 'dyn' && !b.fields) return a
  if ((a.type === 'int' && b.type === 'number') || (a.type === 'number' && b.type === 'int')) return { type: 'number' }
  if (a.type !== b.type) return dyn
  const out: SchemaNode = { type: a.type }
  if (a.fields || b.fields) {
    out.fields = { ...a.fields }
    for (const [k, v] of Object.entries(b.fields ?? {})) out.fields[k] = mergeNode(out.fields[k], v)
  }
  if (a.items || b.items) out.items = mergeNode(a.items, b.items)
  if (a.values || b.values) out.values = mergeNode(a.values, b.values)
  return out
}

/** The schema of a rule reading several streams: a field one stream lacks
 * comes from the others, and fields that disagree are dynamic. */
export function mergeSchemas(list: StreamSchema[]): StreamSchema | null {
  if (!list.length) return null
  return list.reduce((a, b) => ({ events: a.events + b.events, event: mergeNode(a.event, b.event) }))
}

export interface Completion {
  label: string
  /** The value's type, or "method". */
  detail: string
  /** What replaces the text from `from` to the cursor. */
  insert: string
  from: number
}

const ident = /^[A-Za-z_][A-Za-z0-9_]*$/

const METHODS: Record<string, string[]> = {
  string: ['contains(', 'startsWith(', 'endsWith(', 'matches(', 'lowerAscii()', 'upperAscii()', 'split(', 'trim()', 'size()'],
  list: ['exists(x, ', 'all(x, ', 'exists_one(x, ', 'filter(x, ', 'map(x, ', 'size()'],
  map: ['exists(k, ', 'all(k, ', 'size()'],
}

const ROOTS: Completion[] = [
  { label: asIs('event'), detail: 'object', insert: 'event', from: 0 },
  { label: asIs('events'), detail: 'list', insert: 'events', from: 0 },
  { label: asIs('has('), detail: 'macro', insert: 'has(', from: 0 },
  { label: asIs('size('), detail: 'function', insert: 'size(', from: 0 },
  { label: asIs('string('), detail: 'function', insert: 'string(', from: 0 },
  { label: asIs('int('), detail: 'function', insert: 'int(', from: 0 },
  { label: asIs('timestamp('), detail: 'function', insert: 'timestamp(', from: 0 },
  { label: asIs('duration('), detail: 'function', insert: 'duration(', from: 0 },
]

/** The path written just before `end`: event.data["a-b"].list[0]. Null when
 * it doesn't start at event or events. */
export function pathBefore(text: string, end: number): string[] | null {
  const segs: string[] = []
  let i = end
  for (;;) {
    if (text[i - 1] === ']') {
      const open = text.lastIndexOf('[', i - 1)
      if (open < 0) return null
      const inner = text.slice(open + 1, i - 1).trim()
      const quoted = inner.match(/^"([^"]*)"$|^'([^']*)'$/)
      if (quoted) segs.unshift(quoted[1] ?? quoted[2])
      else if (/^\d+$/.test(inner)) segs.unshift('[]')
      else return null
      i = open
      continue
    }
    const m = text.slice(0, i).match(/[A-Za-z_][A-Za-z0-9_]*$/)
    if (!m) return null
    segs.unshift(m[0])
    i -= m[0].length
    if (text[i - 1] !== '.') break
    i--
  }
  const root = segs.shift()
  if (root === 'event') return segs
  if (root === 'events' && segs[0] === '[]') return segs.slice(1)
  return null
}

/** The schema node at a path, or null when the schema doesn't know it. */
export function nodeAt(event: SchemaNode, path: string[]): SchemaNode | null {
  let n: SchemaNode | undefined = event
  for (const seg of path) {
    if (!n) return null
    if (seg === '[]') n = n.items ?? n.values
    else if (n.fields) n = n.fields[seg]
    else n = n.values
  }
  return n ?? null
}

/** Whether text ends inside a string literal. */
export function inString(text: string): boolean {
  let quote = ''
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (quote) {
      if (c === '\\') i++
      else if (c === quote) quote = ''
    } else if (c === '"' || c === "'") quote = c
  }
  return quote !== ''
}

/** What may be written at the cursor: the fields of the object before a
 * dot, a value's methods, or, at the start of a name, event and the main
 * functions. */
export function completionsAt(schema: StreamSchema | null, text: string, cursor: number): Completion[] {
  const before = text.slice(0, cursor)
  if (inString(before)) return []
  const word = before.match(/[A-Za-z_][A-Za-z0-9_]*$/)?.[0] ?? ''
  const start = cursor - word.length
  if (/\d$/.test(before.slice(0, start))) return []
  const dot = text[start - 1] === '.'
  const prefix = word.toLowerCase()
  if (!dot) {
    if (!word || /[A-Za-z0-9_\]]$/.test(before.slice(0, start))) return []
    return ROOTS.filter((c) => c.label.startsWith(prefix) && c.label !== word).map((c) => ({ ...c, from: start }))
  }
  if (!schema) return []
  const path = pathBefore(text, start - 1)
  if (!path) return []
  const node = nodeAt(schema.event, path)
  if (!node) return []
  const out: Completion[] = []
  for (const [name, f] of Object.entries(node.fields ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
    if (!name.toLowerCase().startsWith(prefix)) continue
    // A field that isn't a name is read with brackets: event.data["a-b"].
    out.push(ident.test(name) ? { label: name, detail: f.type, insert: name, from: start } : { label: name, detail: f.type, insert: `[${JSON.stringify(name)}]`, from: start - 1 })
  }
  for (const m of METHODS[node.type] ?? []) if (m.toLowerCase().startsWith(prefix)) out.push({ label: m, detail: tr('method'), insert: m, from: start })
  return out
}

export interface CelError {
  message: string
  line?: number
  column?: number
}

/** The errors of a check, as cel-go writes them ("ERROR: <input>:1:13:
 * message" and the source with a caret), or a sentence of awxnext's. */
export function parseCelErrors(text: string | null | undefined): CelError[] {
  if (!text) return []
  const out: CelError[] = []
  for (const line of text.split('\n')) {
    const m = line.match(/^ERROR: <input>:(\d+):(\d+): (.*)$/)
    if (m) out.push({ line: Number(m[1]), column: Number(m[2]), message: m[3] })
    else if (!line.startsWith(' |') && line.trim()) out.push({ message: line.trim() })
  }
  return out
}

/** The character offset of a 1-based line and column. */
export function offsetAt(text: string, line: number, column: number): number {
  const lines = text.split('\n')
  let off = 0
  for (let i = 0; i < line - 1 && i < lines.length; i++) off += lines[i].length + 1
  return Math.min(text.length, off + Math.max(0, column - 1))
}

/** A filter split around the part that failed, by its offsets in
 * characters: [before, failed, after]. */
export function splitMiss(filter: string, start: number | undefined, end: number | undefined): [string, string, string] | null {
  if (start === undefined || end === undefined || start < 0 || end < start) return null
  const chars = Array.from(filter)
  if (end > chars.length) return null
  return [chars.slice(0, start).join(''), chars.slice(start, end).join(''), chars.slice(end).join('')]
}

export interface MissDetails {
  expression?: string
  start?: number
  end?: number
  error?: string
  values?: SeenValue[]
}

/** The filter around the part that failed, when that part is still where
 * the decision found it: a rule's filter may have changed since. */
export function missParts(filter: string, miss: MissDetails | undefined): [string, string, string] | null {
  if (!miss?.expression) return null
  const parts = splitMiss(filter, miss.start, miss.end)
  if (!parts) return null
  const squash = (s: string) => s.replace(/\s+/g, '')
  const expr = miss.expression
  // The server clips a long part with an ellipsis.
  const same = expr.endsWith('…') ? squash(parts[1]).startsWith(squash(expr.slice(0, -1))) : squash(parts[1]) === squash(expr)
  return same ? parts : null
}

export interface SeenValue {
  expression: string
  value?: unknown
  summary?: string
  error?: string
}

/** One operand the failed part read, as a line: name = value. */
export function seenText(v: SeenValue): string {
  if (v.error) return `${v.expression}: ${v.error}`
  if (v.summary) return `${v.expression} ${v.summary}`
  return `${v.expression} = ${JSON.stringify(v.value)}`
}

/** How a decision's outcome shows: its cell, its label, and what kind it is. */
export interface OutcomeView {
  cell: CellState
  label: string
  /** acted, would (shadow, backtest), waits, held, missed or failed. */
  kind: 'acted' | 'would' | 'waits' | 'held' | 'missed' | 'failed'
}

export function outcomeView(outcome: string): OutcomeView {
  switch (outcome) {
    case 'launched':
      return { cell: 'ok', label: tr('Launched'), kind: 'acted' }
    case 'notified':
      return { cell: 'ok', label: tr('Notified'), kind: 'acted' }
    case 'posted':
      return { cell: 'ok', label: tr('Posted'), kind: 'acted' }
    case 'shadow':
      return { cell: 'changed', label: tr('Shadow'), kind: 'would' }
    case 'fired':
      return { cell: 'changed', label: tr('Would fire'), kind: 'would' }
    case 'awaiting_approval':
      return { cell: 'pending', label: tr('Awaiting approval'), kind: 'waits' }
    case 'waiting':
      return { cell: 'pending', label: tr('Waiting'), kind: 'waits' }
    case 'held':
      return { cell: 'canceled', label: tr('Held back'), kind: 'held' }
    case 'throttled':
      return { cell: 'skipped', label: tr('Throttled'), kind: 'held' }
    case 'deduplicated':
      return { cell: 'skipped', label: tr('Deduplicated'), kind: 'held' }
    case 'dropped':
      return { cell: 'skipped', label: tr('Dropped'), kind: 'held' }
    case 'denied':
      return { cell: 'canceled', label: tr('Denied'), kind: 'held' }
    case 'timed_out':
      return { cell: 'canceled', label: tr('Timed out'), kind: 'held' }
    case 'canceled':
      return { cell: 'canceled', label: tr('Canceled'), kind: 'held' }
    case 'not_matched':
      return { cell: 'none', label: tr('Not matched'), kind: 'missed' }
    case 'refused':
      return { cell: 'failed', label: tr('Refused'), kind: 'failed' }
    case 'error':
      return { cell: 'error', label: tr('Error'), kind: 'failed' }
    case 'failed':
      return { cell: 'failed', label: tr('Failed'), kind: 'failed' }
  }
  return { cell: 'empty', label: outcome, kind: 'missed' }
}

/** The outcomes behind each choice of the decision log's filter. */
export const OUTCOME_FILTERS: { value: string; label: string; outcomes: string[] }[] = [
  { value: '', label: tr('All'), outcomes: [] },
  { value: 'acted', label: tr('Acted'), outcomes: ['launched', 'notified', 'posted', 'shadow', 'awaiting_approval'] },
  { value: 'held', label: tr('Held back'), outcomes: ['held', 'throttled', 'deduplicated', 'dropped', 'denied', 'timed_out', 'canceled', 'waiting'] },
  { value: 'failed', label: tr('Failed'), outcomes: ['refused', 'failed', 'error'] },
  { value: 'missed', label: tr('Not matched'), outcomes: ['not_matched'] },
]

export const GUARDRAIL_LABELS: Record<string, string> = {
  circuit_breaker: tr('Circuit breaker'),
  maintenance_window: tr('Maintenance window'),
  change_freeze: tr('Change freeze'),
  max_concurrent_runs: tr('Concurrent runs'),
  host_lock: tr('Host lock'),
  max_group_share: tr('Group share'),
}

export const STREAM_TYPES: [string, string][] = [
  ['alertmanager', asIs('Alertmanager')],
  ['grafana', asIs('Grafana')],
  ['github', asIs('GitHub')],
  ['gitlab', asIs('GitLab')],
  ['servicenow', asIs('ServiceNow')],
  ['cloudevents', asIs('CloudEvents')],
  ['generic', tr('Generic JSON')],
]

/** The credential type a sender of this stream type usually signs with. */
export function usualCredential(streamType: string): string {
  switch (streamType) {
    case 'github':
      return 'event_stream_github'
    case 'gitlab':
      return 'event_stream_gitlab'
  }
  return 'event_stream_token'
}

export interface Snippet {
  key: string
  title: string
  /** Steps to follow, before the code. */
  steps?: string[]
  code: string
  language: string
}

/** How to point each kind of sender at a stream's URL, given the namespace
 * of its credential's type. */
export function setupSnippets(url: string, credential: string): Snippet[] {
  const auth: Record<string, string> = {
    event_stream_token: `-H 'Authorization: Bearer <token>'`,
    event_stream_basic: `-u '<username>:<password>'`,
    event_stream_gitlab: `-H 'X-Gitlab-Token: <token>'`,
  }
  const http: Record<string, string> = {
    event_stream_token: '        http_config:\n          authorization:\n            type: Bearer\n            credentials_file: /etc/alertmanager/awxnext-token\n',
    event_stream_basic: '        http_config:\n          basic_auth:\n            username: <username>\n            password_file: /etc/alertmanager/awxnext-password\n',
  }
  const out: Snippet[] = [
    {
      key: 'alertmanager',
      title: asIs('Alertmanager'),
      steps: [tr('Add a receiver to alertmanager.yml, and route the alerts to remediate to it.'), tr('Use a Token or Basic Event Stream credential: Alertmanager can’t sign its requests.')],
      language: 'yaml',
      code: `receivers:\n  - name: awxnext\n    webhook_configs:\n      - url: ${url}\n        send_resolved: true\n        max_alerts: 0\n${http[credential] ?? http.event_stream_token}`,
    },
    {
      key: 'github',
      title: asIs('GitHub'),
      steps: [
        tr('In the repository’s or organization’s Settings, Webhooks, add a webhook.'),
        tr('Payload URL: the URL below. Content type: application/json.'),
        tr('Secret: the secret of the stream’s GitHub Event Stream credential.'),
        tr('Choose the events to send.'),
      ],
      language: 'text',
      code: url,
    },
    {
      key: 'gitlab',
      title: asIs('GitLab'),
      steps: [
        tr('In the project’s or group’s Settings, Webhooks, add a webhook.'),
        tr('URL: the URL below.'),
        tr('Secret token: the token of the stream’s GitLab Event Stream credential.'),
        tr('Choose the triggers to send.'),
      ],
      language: 'text',
      code: url,
    },
  ]
  const curl = auth[credential]
  if (curl)
    out.push({
      key: 'curl',
      title: asIs('curl'),
      steps: [tr('Send a test event. In test mode, the stream keeps it and shows it, and rules don’t see it.')],
      language: 'sh',
      code: `curl ${curl} -H 'Content-Type: application/json' \\\n  ${url} \\\n  -d '{"id": "test-1", "type": "test", "data": {"host": "web-01", "severity": "critical"}}'`,
    })
  return out
}

/** A throttle or breaker window as a number of a unit. */
export const UNITS: [number, string][] = [
  [1, tr('seconds')],
  [60, tr('minutes')],
  [3600, tr('hours')],
  [86400, tr('days')],
]

export function splitSeconds(s: number): { n: number; unit: number } {
  for (const [u] of [...UNITS].reverse()) if (s > 0 && s % u === 0) return { n: s / u, unit: u }
  return { n: s, unit: 1 }
}

/** A draft rule as the form holds it. */
export interface RuleDraft {
  name: string
  description: string
  organization: number | null
  streams: number[]
  enabled: boolean
  mode: string
  filter: string
  key: string
  throttle: string
  throttle_window: number
  action: string
  unified_job_template: number | null
  vars: [string, string][]
  limit: string
  notification_template: number | null
  message: string
  target_stream: number | null
  event_type: string
  event_data: string
  max_concurrent_runs: number
  host_lock: boolean
  max_group_share: number
  breaker_firings: number
  breaker_window: number
  breaker_notification_template: number | null
  window_action: string
  approval_timeout: number
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function draftOf(r?: Record<string, any>): RuleDraft {
  return {
    name: r?.name ?? '',
    description: r?.description ?? '',
    organization: r?.organization ?? null,
    streams: r?.streams ?? [],
    enabled: r?.enabled ?? true,
    mode: r?.mode ?? 'shadow',
    filter: r?.filter ?? '',
    key: r?.key ?? '',
    throttle: r?.throttle ?? '',
    throttle_window: r?.throttle_window || 3600,
    action: r?.action ?? 'launch',
    unified_job_template: r?.unified_job_template ?? null,
    vars: Object.entries((r?.extra_vars ?? {}) as Record<string, string>),
    limit: r?.limit ?? '',
    notification_template: r?.notification_template ?? null,
    message: r?.message ?? '',
    target_stream: r?.target_stream ?? null,
    event_type: r?.event_type ?? '',
    event_data: r?.event_data ?? '',
    max_concurrent_runs: r?.max_concurrent_runs ?? 0,
    host_lock: r?.host_lock ?? false,
    max_group_share: r?.max_group_share ?? 0,
    breaker_firings: r?.breaker_firings ?? 0,
    breaker_window: r?.breaker_window ?? 3600,
    breaker_notification_template: r?.breaker_notification_template ?? null,
    window_action: r?.window_action ?? 'hold',
    approval_timeout: r?.approval_timeout ?? 3600,
  }
}

/** The fields of the draft's action only: another action's are cleared, so
 * that a rule switched from launching to notifying keeps no template. */
export function ruleBody(d: RuleDraft): Record<string, unknown> {
  const launch = d.action === 'launch'
  const vars: Record<string, string> = {}
  for (const [k, v] of d.vars) if (k.trim()) vars[k.trim()] = v
  return {
    name: d.name,
    description: d.description,
    organization: d.organization,
    streams: d.streams,
    enabled: d.enabled,
    mode: d.mode,
    filter: d.filter,
    key: d.key,
    throttle: d.throttle,
    throttle_window: d.throttle ? d.throttle_window : 0,
    action: d.action,
    unified_job_template: launch ? d.unified_job_template : null,
    extra_vars: launch ? vars : {},
    limit: launch ? d.limit : '',
    notification_template: d.action === 'notify' ? d.notification_template : null,
    message: d.action === 'notify' ? d.message : '',
    target_stream: d.action === 'post_event' ? d.target_stream : null,
    event_type: d.action === 'post_event' ? d.event_type : '',
    event_data: d.action === 'post_event' ? d.event_data : '',
    max_concurrent_runs: launch ? d.max_concurrent_runs : 0,
    host_lock: launch && d.host_lock,
    max_group_share: launch ? d.max_group_share : 0,
    breaker_firings: d.breaker_firings,
    breaker_window: d.breaker_window,
    breaker_notification_template: d.breaker_notification_template,
    window_action: d.window_action,
    approval_timeout: d.approval_timeout,
  }
}

/** What POST event_rules/check/ takes of a draft. */
export function checkBody(d: RuleDraft, events = 20): Record<string, unknown> {
  const b = ruleBody(d)
  // A variable whose name is still being written is left out, not refused.
  const vars = Object.fromEntries(Object.entries(b.extra_vars as Record<string, string>).filter(([k]) => ident.test(k)))
  return { streams: b.streams, filter: b.filter, key: b.key, action: b.action, extra_vars: vars, limit: b.limit, message: b.message, event_type: b.event_type, event_data: b.event_data, events }
}

/** The errors of a save, or of the check, by the editor's fields:
 * extra_vars' "name: message" go to the variable they name. */
export function fieldErrors(errors: Record<string, unknown>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(errors)) {
    const list = Array.isArray(v) ? v.map(String) : [String(v)]
    if (k !== 'extra_vars') {
      out[k] = list.join('\n')
      continue
    }
    for (const msg of list) {
      const m = msg.match(/^([A-Za-z_][A-Za-z0-9_]*): ([\s\S]*)$/)
      if (m) out[`extra_vars.${m[1]}`] = m[2]
      else out.extra_vars = out.extra_vars ? `${out.extra_vars}\n${msg}` : msg
    }
  }
  return out
}

/** One line on what a rule does. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function actionSummary(r: Record<string, any>): string {
  const sf = r.summary_fields ?? {}
  switch (r.action) {
    case 'launch':
      return tr('Launch {name}', { name: sf.unified_job_template?.name ?? `#${r.unified_job_template ?? '?'}` })
    case 'notify':
      return tr('Notify {name}', { name: sf.notification_template?.name ?? `#${r.notification_template ?? '?'}` })
    case 'post_event':
      return tr('Post to {name}', { name: sf.target_stream?.name ?? `#${r.target_stream ?? '?'}` })
  }
  return r.action
}

export const MODE_LABELS: Record<string, string> = {
  shadow: tr('Shadow'),
  approval: tr('Approval'),
  auto: tr('Auto'),
}
