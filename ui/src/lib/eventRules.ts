// Event streams and rules (roadmap M14-05): what their pages compute
// without the API. The completion of CEL expressions from a stream's
// inferred schema, the errors the server's check gives, how decisions are
// shown, and the setup snippets of each kind of sender.
import type { CellState } from './cells'
import { duration } from './format'
import { asIs, tr, trn } from './i18n'

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

/** CEL's functions, completed at the start of a word. */
export const CEL_FUNCTIONS: Completion[] = [
  { label: asIs('has('), detail: 'macro', insert: 'has(', from: 0 },
  { label: asIs('size('), detail: 'function', insert: 'size(', from: 0 },
  { label: asIs('string('), detail: 'function', insert: 'string(', from: 0 },
  { label: asIs('int('), detail: 'function', insert: 'int(', from: 0 },
  { label: asIs('timestamp('), detail: 'function', insert: 'timestamp(', from: 0 },
  { label: asIs('duration('), detail: 'function', insert: 'duration(', from: 0 },
]

const ROOTS: Completion[] = [
  { label: asIs('event'), detail: 'object', insert: 'event', from: 0 },
  { label: asIs('events'), detail: 'list', insert: 'events', from: 0 },
  ...CEL_FUNCTIONS,
]

/** The path written just before `end`: event.data["a-b"].list[0]. Null when
 * it doesn't start at event or events. */
export function pathBefore(text: string, end: number): string[] | null {
  const segs = segmentsBefore(text, end)
  if (!segs) return null
  const root = segs.shift()
  if (root === 'event') return segs
  if (root === 'events' && segs[0] === '[]') return segs.slice(1)
  return null
}

/** The segments of the path written just before `end`, its root first; an
 * index is "[]". */
export function segmentsBefore(text: string, end: number): string[] | null {
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
  return segs
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
  return celCompletions(text, cursor, ROOTS, (segs) => {
    if (!schema) return null
    const root = segs[0]
    const path = root === 'event' ? segs.slice(1) : root === 'events' && segs[1] === '[]' ? segs.slice(2) : null
    return path ? nodeAt(schema.event, path) : null
  })
}

/** The completions of the word at the cursor: a root or a function at the
 * start of a path, the fields and methods of the node `resolve` finds for
 * the path before a dot. */
export function celCompletions(text: string, cursor: number, roots: Completion[], resolve: (segments: string[]) => SchemaNode | null): Completion[] {
  const before = text.slice(0, cursor)
  if (inString(before)) return []
  const word = before.match(/[A-Za-z_][A-Za-z0-9_]*$/)?.[0] ?? ''
  const start = cursor - word.length
  if (/\d$/.test(before.slice(0, start))) return []
  const dot = text[start - 1] === '.'
  const prefix = word.toLowerCase()
  if (!dot) {
    if (!word || /[A-Za-z0-9_\]]$/.test(before.slice(0, start))) return []
    return roots.filter((c) => c.label.startsWith(prefix) && c.label !== word).map((c) => ({ ...c, from: start }))
  }
  const segs = segmentsBefore(text, start - 1)
  if (!segs) return []
  const node = resolve(segs)
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
    case 'logged':
      return { cell: 'ok', label: tr('Logged'), kind: 'acted' }
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
    case 'expired':
      return { cell: 'skipped', label: tr('Expired'), kind: 'held' }
    case 'cleared':
      return { cell: 'skipped', label: tr('Cleared'), kind: 'held' }
    case 'ignored':
      return { cell: 'none', label: tr('Ignored'), kind: 'held' }
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
  { value: 'acted', label: tr('Acted'), outcomes: ['launched', 'notified', 'posted', 'logged', 'shadow', 'awaiting_approval'] },
  { value: 'held', label: tr('Held back'), outcomes: ['held', 'throttled', 'deduplicated', 'dropped', 'denied', 'timed_out', 'canceled', 'waiting', 'expired', 'cleared', 'ignored'] },
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
  check_escalation: tr('Escalated'),
}

/** How a check's outcome shows. resolved and unresolved are shadow mode's:
 * whether the key resolved by itself, with no action. */
export function checkOutcomeView(outcome: string): { cell: CellState; label: string } {
  switch (outcome) {
    case 'pending':
      return { cell: 'running', label: tr('Checking') }
    case 'fixed':
      return { cell: 'ok', label: tr('Fixed') }
    case 'not_fixed':
      return { cell: 'failed', label: tr('Not fixed') }
    case 'action_failed':
      return { cell: 'failed', label: tr('Action failed') }
    case 'error':
      return { cell: 'error', label: tr('Check error') }
    case 'canceled':
      return { cell: 'canceled', label: tr('Canceled') }
    case 'resolved':
      return { cell: 'changed', label: tr('Resolved anyway') }
    case 'unresolved':
      return { cell: 'skipped', label: tr('Not resolved') }
  }
  return { cell: 'empty', label: outcome }
}

export const CHECK_STATE_LABELS: Record<string, string> = {
  acting: tr('Action under way'),
  watching: tr('Watching'),
  checking: tr('Check job running'),
  settled: tr('Settled'),
}

/** A success rate, from 0 to 1, as a whole percentage; a dash when nothing
 * was checked. */
export function rateText(rate: number | null | undefined): string {
  if (rate === null || rate === undefined || isNaN(rate)) return '—'
  return tr('{n}%', { n: Math.round(rate * 100) })
}

/** The counts of a rule's checks, as summary_fields.checks gives them. */
export interface CheckCounts {
  days: number
  checked: number
  fixed: number
  not_fixed: number
  action_failed: number
  errors: number
  pending: number
  success_rate: number | null
  shadow?: { checked: number; resolved: number; resolved_rate: number | null }
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
  retries: number
  retry_delay: number
  post_artifacts: string
  artifacts_stream: number | null
  max_concurrent_runs: number
  host_lock: boolean
  max_group_share: number
  breaker_firings: number
  breaker_window: number
  breaker_notification_template: number | null
  window_action: string
  approval_timeout: number
  pattern: string
  conditions: string[]
  pattern_count: number
  pattern_window: number
  check: string
  check_condition: string
  check_window: number
  check_template: number | null
  check_notification_template: number | null
  check_approval: boolean
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
    retries: r?.retries ?? 0,
    retry_delay: r?.retry_delay ?? 0,
    post_artifacts: r?.post_artifacts ?? '',
    artifacts_stream: r?.artifacts_stream ?? null,
    max_concurrent_runs: r?.max_concurrent_runs ?? 0,
    host_lock: r?.host_lock ?? false,
    max_group_share: r?.max_group_share ?? 0,
    breaker_firings: r?.breaker_firings ?? 0,
    breaker_window: r?.breaker_window ?? 3600,
    breaker_notification_template: r?.breaker_notification_template ?? null,
    window_action: r?.window_action ?? 'hold',
    approval_timeout: r?.approval_timeout ?? 3600,
    pattern: r?.pattern ?? '',
    conditions: r?.conditions?.length ? r.conditions : ['', ''],
    pattern_count: r?.pattern_count || 3,
    pattern_window: r?.pattern_window || 600,
    check: r?.check ?? '',
    check_condition: r?.check_condition ?? '',
    check_window: r?.check ? (r.check_window ?? 0) : 600,
    check_template: r?.check_template ?? null,
    check_notification_template: r?.check_notification_template ?? null,
    check_approval: r?.check_approval ?? false,
  }
}

/** The fields of the draft's action and check only: another action's are
 * cleared, so that a rule switched from launching to notifying keeps no
 * template, and so are another check's. */
export function ruleBody(d: RuleDraft): Record<string, unknown> {
  const launch = d.action === 'launch'
  const conditions = hasConditions(d.pattern) ? (d.pattern === 'absence' ? d.conditions.slice(0, 2) : d.conditions) : []
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
    throttle: d.pattern ? '' : d.throttle,
    throttle_window: d.throttle && !d.pattern ? d.throttle_window : 0,
    pattern: d.pattern,
    conditions,
    pattern_count: d.pattern === 'count' ? d.pattern_count : 0,
    pattern_window: d.pattern ? d.pattern_window : 0,
    action: d.action,
    unified_job_template: launch ? d.unified_job_template : null,
    extra_vars: launch ? vars : {},
    limit: launch ? d.limit : '',
    notification_template: d.action === 'notify' ? d.notification_template : null,
    message: d.action === 'notify' || d.action === 'log' ? d.message : '',
    target_stream: d.action === 'post_event' ? d.target_stream : null,
    event_type: d.action === 'post_event' ? d.event_type : '',
    event_data: d.action === 'post_event' ? d.event_data : '',
    retries: launch ? d.retries : 0,
    retry_delay: launch && d.retries ? d.retry_delay : 0,
    post_artifacts: launch ? d.post_artifacts : '',
    artifacts_stream: launch && d.post_artifacts ? d.artifacts_stream : null,
    max_concurrent_runs: launch ? d.max_concurrent_runs : 0,
    host_lock: launch && d.host_lock,
    max_group_share: launch ? d.max_group_share : 0,
    breaker_firings: d.breaker_firings,
    breaker_window: d.breaker_window,
    breaker_notification_template: d.breaker_notification_template,
    window_action: d.window_action,
    approval_timeout: d.approval_timeout,
    check: d.check,
    check_condition: d.check === 'event' ? d.check_condition : '',
    check_window: d.check ? d.check_window : 0,
    check_template: d.check === 'template' ? d.check_template : null,
    check_notification_template: d.check ? d.check_notification_template : null,
    check_approval: !!d.check && d.check_approval,
  }
}

/** What POST event_rules/check/ takes of a draft. */
export function checkBody(d: RuleDraft, events = 20): Record<string, unknown> {
  const b = ruleBody(d)
  // A variable whose name is still being written is left out, not refused.
  const vars = Object.fromEntries(Object.entries(b.extra_vars as Record<string, string>).filter(([k]) => ident.test(k)))
  return {
    streams: b.streams,
    filter: b.filter,
    key: b.key,
    action: b.action,
    extra_vars: vars,
    limit: b.limit,
    message: b.message,
    event_type: b.event_type,
    event_data: b.event_data,
    pattern: b.pattern,
    conditions: b.conditions,
    check: b.check,
    check_condition: b.check_condition,
    events,
  }
}

/** The errors of a save, or of the check, by the editor's fields:
 * extra_vars' "name: message" go to the variable they name, and
 * conditions' "N: message" to the Nth condition. */
export function fieldErrors(errors: Record<string, unknown>): Record<string, string> {
  const out: Record<string, string> = {}
  const add = (k: string, msg: string) => (out[k] = out[k] ? `${out[k]}\n${msg}` : msg)
  for (const [k, v] of Object.entries(errors)) {
    const list = Array.isArray(v) ? v.map(String) : [String(v)]
    if (k !== 'extra_vars' && k !== 'conditions') {
      out[k] = list.join('\n')
      continue
    }
    for (const msg of list) {
      const m = k === 'extra_vars' ? msg.match(/^([A-Za-z_][A-Za-z0-9_]*): ([\s\S]*)$/) : msg.match(/^(\d+): ([\s\S]*)$/)
      if (!m) add(k, msg)
      else if (k === 'extra_vars') out[`extra_vars.${m[1]}`] = m[2]
      else out[`conditions.${Number(m[1]) - 1}`] = m[2]
    }
  }
  return out
}

/** The patterns a rule can wait for, per key, within a window. */
export const PATTERNS: [string, string][] = [
  ['', tr('None: each event on its own')],
  ['count', tr('Count: a number of events')],
  ['all', tr('All of: every condition, in any order')],
  ['sequence', tr('Sequence: the conditions in order')],
  ['absence', tr('Absence: an event not followed by another')],
]

export const PATTERN_LABELS: Record<string, string> = {
  count: tr('Count'),
  all: tr('All of'),
  sequence: tr('Sequence'),
  absence: tr('Absence'),
}

/** Whether a pattern has conditions of its own. */
export function hasConditions(pattern: string): boolean {
  return pattern === 'all' || pattern === 'sequence' || pattern === 'absence'
}

/** The label of a pattern's condition, from 0. */
export function conditionLabel(pattern: string, i: number): string {
  if (pattern === 'sequence') return tr('Step {n}', { n: i + 1 })
  if (pattern === 'absence') return i === 0 ? tr('An event matching') : tr('Not followed by one matching')
  return tr('Condition {n}', { n: i + 1 })
}

/** The conditions of a draft once its pattern changes: an absence has two,
 * the others two at least. */
export function conditionsFor(pattern: string, conditions: string[]): string[] {
  const out = conditions.length < 2 ? [...conditions, ...Array(2 - conditions.length).fill('')] : conditions
  return pattern === 'absence' ? out.slice(0, 2) : out
}

/** One line on what a rule's pattern waits for. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function patternSummary(r: Record<string, any>): string {
  const window = duration(r.pattern_window)
  const n = (r.conditions ?? []).length
  switch (r.pattern) {
    case 'count':
      return tr('{n} events of a key within {window}', { n: r.pattern_count, window })
    case 'all':
      return tr('All of {n} conditions within {window}, in any order', { n, window })
    case 'sequence':
      return tr('{n} steps in order within {window}', { n, window })
    case 'absence':
      return tr('Condition 1 not followed by condition 2 within {window}', { window })
  }
  return tr('None: acts on each event')
}

/** A key's window, as decisions' details.pattern and the windows' progress
 * give it. */
export interface PatternProgress {
  kind: string
  events: number
  seen?: number
  needed?: number
  /** The conditions of an all pattern that the window holds, from 1. */
  conditions?: number[]
  until?: string
}

const maxProgressCells = 30

/** A window's progress as cells: one per event, condition or step it
 * needs, the ones it has filled, all of them ok once complete. An absence
 * has one per event waiting. */
export function progressCells(p: PatternProgress | undefined): CellState[] {
  if (!p) return []
  const needed = p.needed ?? 0
  const done = needed > 0 && (p.seen ?? 0) >= needed
  const filled: CellState = done ? 'ok' : 'running'
  switch (p.kind) {
    case 'count':
    case 'sequence':
      if (needed > maxProgressCells) return []
      return Array.from({ length: needed }, (_, i) => (i < (p.seen ?? 0) ? filled : 'empty'))
    case 'all': {
      const have = new Set(p.conditions ?? [])
      return Array.from({ length: needed }, (_, i) => (have.has(i + 1) ? filled : 'empty'))
    }
  }
  return Array.from({ length: Math.min(p.events, maxProgressCells) }, () => 'running')
}

/** A window's progress in words: "3 of 5 events". */
export function progressText(p: PatternProgress): string {
  const vars = { seen: p.seen ?? 0, needed: p.needed ?? 0 }
  switch (p.kind) {
    case 'count':
      return tr('{seen} of {needed} events', vars)
    case 'all':
      return tr('{seen} of {needed} conditions', vars)
    case 'sequence':
      return tr('Step {seen} of {needed}', vars)
  }
  return trn(p.events, '{n} event waiting for condition 2', '{n} events waiting for condition 2')
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
    case 'log':
      return tr('Log a message')
  }
  return r.action
}

export const MODE_LABELS: Record<string, string> = {
  shadow: tr('Shadow'),
  approval: tr('Approval'),
  auto: tr('Auto'),
}
