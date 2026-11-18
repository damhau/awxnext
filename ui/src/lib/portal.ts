// The self-service portal (roadmap M15-08): what its pages work out from
// the catalog's API, for requesters.
import { tr, trn } from './i18n'

export interface Brand {
  organization: number
  title: string
  logo: string
}

export interface NeedsYou {
  requests: { id: number; item_name: string; kind: string; status: string; created: string }[]
  deployments: { id: number; name: string; expires_at: string }[]
  approvals: number
}

export interface PortalInfo {
  request_only: boolean
  brand: Brand | null
  needs_you: NeedsYou
}

export interface ApprovalRule {
  name: string
  condition: string
}

export interface PortalItem {
  id: number
  name: string
  description: string
  category: string
  icon: string
  kind: 'run' | 'deployment'
  max_lifetime_days: number | null
  default_lifetime_days: number | null
  max_extensions?: number | null
  approval_rules?: ApprovalRule[]
  summary_fields: {
    freeze?: { id: number; name: string; kind: string; until: string }
    owner_team?: { id: number; name: string }
    organization?: { id: number; name: string }
  }
}

export interface Person {
  id: number
  username: string
  name: string
}

export interface GatePreview {
  kind: 'rule' | 'freeze' | 'quota'
  rule: string
  why: string
  approvers: Person[]
  required: number
  fallback: boolean
  timeout: number
}

export interface Preview {
  approvals: GatePreview[]
  refused: { detail: string } | null
  quota: QuotaHolder | null
}

export interface QuotaHolder {
  team: { id: number; name: string } | null
  user: { id: number; username: string } | null
  deployments: number
  requests: number
  max_deployments: number | null
  max_requests_per_day: number | null
}

export interface ApprovalAnswer {
  user: number | null
  username: string
  approved: boolean
  comment: string
  created: string
}

export interface RequestApproval {
  id: number
  status: string
  timed_out: boolean
  expires: string | null
  rule: string
  approvers: Person[]
  required: number
  approved: number
  answers: ApprovalAnswer[]
}

export interface CatalogRequest {
  id: number
  catalog_item: number | null
  item_name: string
  kind: string
  status: string
  created: string
  modified: string
  explanation: string
  reason: string
  lifetime_days: number | null
  answers: Record<string, unknown>
  answer_labels: Record<string, unknown>
  approvals: RequestApproval[]
  deployment: number | null
  job: number | null
  summary_fields: { team?: { id: number; name: string }; deployment?: { id: number; name: string } }
}

export type StepState = 'done' | 'skipped' | 'running' | 'waiting' | 'pending' | 'denied' | 'failed' | 'canceled'

export interface Progress {
  steps: { name: string; label: string; state: StepState }[]
  percent: number | null
  job: { id: number; type: string; status: string; can_see: boolean } | null
}

export const unfinished = ['awaiting_approval', 'pending', 'running']

/** The catalog's categories, with how many items each holds, by name. */
export function categories(items: Pick<PortalItem, 'category'>[]): { name: string; n: number }[] {
  const n = new Map<string, number>()
  for (const i of items) n.set(i.category, (n.get(i.category) ?? 0) + 1)
  return [...n].map(([name, count]) => ({ name, n: count })).sort((a, b) => (a.name === '' ? 1 : b.name === '' ? -1 : a.name.localeCompare(b.name)))
}

/** Whether an item matches every word of a search. */
export function matches(item: Pick<PortalItem, 'name' | 'description' | 'category'>, query: string): boolean {
  const text = `${item.name} ${item.description} ${item.category}`.toLowerCase()
  return query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((w) => text.includes(w))
}

/** What a card says of an item's approvals: none, always, or under some rules. */
export function approvalHint(rules: ApprovalRule[] | undefined): { tone: 'none' | 'some' | 'always'; text: string } {
  if (!rules?.length) return { tone: 'none', text: tr('No approval') }
  if (rules.some((r) => !r.condition.trim())) return { tone: 'always', text: tr('Needs approval') }
  return { tone: 'some', text: tr('Approval for {rules}', { rules: rules.map((r) => r.name).join(', ') }) }
}

/** How long what an item makes is kept. */
export function lifetimeHint(item: Pick<PortalItem, 'kind' | 'max_lifetime_days'>): string {
  if (item.kind !== 'deployment') return tr('Runs once')
  if (item.max_lifetime_days == null) return tr('Kept until deleted')
  return trn(item.max_lifetime_days, 'Up to {n} day', 'Up to {n} days')
}

/**
 * The lifetimes to pick from, in days, within the item's longest. Null
 * keeps it until deleted, offered only when the item has neither a longest
 * nor a default.
 */
export function lifetimeChoices(max: number | null, def: number | null): (number | null)[] {
  const days = new Set([1, 3, 7, 14, 30, 90].filter((d) => max == null || d <= max))
  if (max != null) days.add(max)
  if (def != null) days.add(def)
  const out: (number | null)[] = [...days].sort((a, b) => a - b)
  if (max == null && def == null) out.push(null)
  return out
}

/** A lifetime choice's words. */
export function lifetimeLabel(days: number | null): string {
  return days == null ? tr('Until deleted') : trn(days, '{n} day', '{n} days')
}

/** When a deployment kept for days from now expires. */
export function expiresOn(days: number, now = Date.now()): Date {
  return new Date(now + days * 86_400_000)
}

/** "Priya Shah", "Priya Shah and Omar Haddad", "Priya Shah, Omar Haddad and 3 others". */
export function namesText(names: string[], most = 2): string {
  if (names.length <= 1) return names[0] ?? ''
  if (names.length === 2) return tr('{a} and {b}', { a: names[0], b: names[1] })
  if (names.length <= most + 1) return tr('{list} and {last}', { list: names.slice(0, -1).join(', '), last: names[names.length - 1] })
  return trn(names.length - most, '{list} and {n} other', '{list} and {n} others', { list: names.slice(0, most).join(', ') })
}

/** A request's state in requesters' words, and its tone. */
export function requestState(status: string): { label: string; tone: 'waiting' | 'running' | 'ok' | 'denied' | 'fail' } {
  switch (status) {
    case 'awaiting_approval':
      return { label: tr('Waiting for approval'), tone: 'waiting' }
    case 'denied':
      return { label: tr('Denied'), tone: 'denied' }
    case 'pending':
      return { label: tr('Starting'), tone: 'running' }
    case 'running':
      return { label: tr('In progress'), tone: 'running' }
    case 'successful':
      return { label: tr('Done'), tone: 'ok' }
    case 'canceled':
      return { label: tr('Canceled'), tone: 'denied' }
    default:
      return { label: tr('Failed'), tone: 'fail' }
  }
}

/** A request's four steps: sent, approval, the run, done. */
export function requestSteps(r: Pick<CatalogRequest, 'status' | 'approvals'>): { key: 'sent' | 'approval' | 'run' | 'done'; state: StepState }[] {
  const s = r.status
  const approval: StepState = r.approvals.length === 0 ? 'skipped' : s === 'awaiting_approval' ? 'waiting' : s === 'denied' ? 'denied' : 'done'
  const run: StepState =
    s === 'pending' || s === 'running'
      ? 'running'
      : s === 'successful'
        ? 'done'
        : s === 'failed'
          ? 'failed'
          : s === 'canceled'
            ? 'canceled'
            : 'pending'
  return [
    { key: 'sent', state: 'done' },
    { key: 'approval', state: approval },
    { key: 'run', state: run },
    { key: 'done', state: s === 'successful' ? 'done' : 'pending' },
  ]
}

/** The approvals a request still waits for. */
export function waitingApprovals(r: Pick<CatalogRequest, 'approvals'>): RequestApproval[] {
  return r.approvals.filter((a) => a.status === 'pending')
}

/** Who denied a request, and why; timedOut when nobody answered in time. */
export function denial(r: Pick<CatalogRequest, 'approvals'>): { by: string; at: string; comment: string; timedOut: boolean } | null {
  for (const a of r.approvals) {
    const no = a.answers.find((x) => !x.approved)
    if (no) return { by: no.username, at: no.created, comment: no.comment, timedOut: false }
    if (a.timed_out) return { by: '', at: a.expires ?? '', comment: '', timedOut: true }
  }
  return null
}

/** A form's questions as the survey fields take them: a dynamic question gets its choices from the item. */
export function surveyQuestions<Q extends { dynamic?: boolean }>(questions: Q[]): (Q & { choices_from?: { type: 'source' } })[] {
  return questions.map((q) => (q.dynamic ? { ...q, choices_from: { type: 'source' as const } } : q))
}

/** A request's answers to fill the form again with, without masked passwords or what the form doesn't ask. */
export function prefill(answers: Record<string, unknown> | undefined, asked: { variable: string; type: string }[]): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const q of asked) {
    const v = answers?.[q.variable]
    if (v === undefined || q.type === 'password' || v === '$encrypted$') continue
    out[q.variable] = v
  }
  return out
}

/**
 * The answers to send for the questions shown: each one given, or its
 * default; numbers as numbers, and a password left masked is not sent.
 */
export function answersOf(shown: { variable: string; type: string; default?: unknown }[], values: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const q of shown) {
    let v = values[q.variable] ?? q.default
    if (v === undefined || v === null || v === '' || v === '$encrypted$') continue
    if ((q.type === 'integer' || q.type === 'float') && typeof v === 'string' && v.trim() !== '' && !isNaN(Number(v))) v = Number(v)
    if (q.type === 'multiselect' && typeof v === 'string') v = v.split('\n').filter(Boolean)
    out[q.variable] = v
  }
  return out
}

/** A request's body: the answers given, and the team, lifetime and reason when set. */
export function requestBody(answers: Record<string, unknown>, team: number | null, lifetime: number | null | undefined, reason: string): Record<string, unknown> {
  const body: Record<string, unknown> = { answers }
  if (team != null) body.team = team
  if (lifetime !== undefined) body.lifetime_days = lifetime
  if (reason.trim()) body.reason = reason.trim()
  return body
}

/** Waiting time in words: "2 days", "5 hours", "30 minutes". */
export function timeoutText(seconds: number): string {
  if (seconds >= 86_400 && seconds % 86_400 === 0) return trn(seconds / 86_400, '{n} day', '{n} days')
  if (seconds >= 3600) return trn(Math.round(seconds / 3600), '{n} hour', '{n} hours')
  return trn(Math.max(1, Math.round(seconds / 60)), '{n} minute', '{n} minutes')
}
