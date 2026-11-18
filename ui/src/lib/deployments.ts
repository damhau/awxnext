// A requester's deployments in the portal (roadmap M15-09): their lifetime
// as day cells, their state in requesters' words, and their history.
import { tr, trn } from './i18n'
import type { CatalogRequest, GatePreview } from './portal'

export interface ConnectionEntry {
  label: string
  value: string
  copy: boolean
  secret: boolean
  error?: string
}

export interface LeftBehind {
  name?: string
  kind?: string
  why?: string
}

export interface Deployment {
  id: number
  name: string
  catalog_item: number | null
  item_name: string
  request: number | null
  owner: number | null
  team: number | null
  status: 'active' | 'deleting' | 'delete_failed' | 'deleted'
  explanation: string
  left_behind: unknown
  expires_at: string | null
  extensions: number
  created: string
  deleted_at: string | null
  connection: ConnectionEntry[]
  summary_fields: {
    owner?: { id: number; username: string; first_name?: string; last_name?: string }
    team?: { id: number; name: string }
    user_capabilities?: Partial<Record<'edit' | 'delete' | 'run' | 'extend' | 'hand_over' | 'reveal', boolean>>
  }
}

export interface ExtensionStep {
  days: number
  expires_at: string | null
  refused: string
  approvals: GatePreview[]
}

export interface ExtensionOptions {
  expires_at: string | null
  extensions: number
  max_extensions: number | null
  until: string | null
  steps: ExtensionStep[]
}

export interface DeploymentAction {
  id: string
  name: string
  description: string
  questions: { variable: string; question_name: string; type: string; dynamic?: boolean }[]
}

export type DayCell = 'gone' | 'today' | 'left' | 'added'

const day = 86_400_000

function startOfDay(t: number): number {
  const d = new Date(t)
  d.setHours(0, 0, 0, 0)
  return d.getTime()
}

/** Calendar days from now's date to the date of when: 0 today, 1 tomorrow, -1 yesterday. */
export function daysUntil(when: string | Date, now = Date.now()): number {
  return Math.round((startOfDay(new Date(when).getTime()) - startOfDay(now)) / day)
}

/**
 * A lifetime's days, a cell each, from the day it was made to the day it
 * expires: gone, today, left, and the days extensions added at the end.
 * The oldest gone days go first when there are too many; null when what's
 * left alone is still too many to draw.
 */
export function lifetimeCells(created: string, expires: string, added: number, now = Date.now(), most = 60): DayCell[] | null {
  const left = Math.max(0, daysUntil(expires, now))
  const gone = Math.max(0, -daysUntil(created, now))
  if (left + 1 > most) return null
  const cells: DayCell[] = [...Array<DayCell>(Math.min(gone, most - left - 1)).fill('gone'), 'today']
  for (let i = 0; i < left; i++) cells.push(i >= left - added ? 'added' : 'left')
  return cells
}

/** The days extensions added: those that went through. */
export function addedDays(requests: Pick<CatalogRequest, 'kind' | 'status' | 'lifetime_days'>[]): number {
  return requests.filter((r) => r.kind === 'extend' && r.status === 'successful').reduce((n, r) => n + (r.lifetime_days ?? 0), 0)
}

export type DeploymentCell = 'done' | 'soon' | 'running' | 'failed' | 'canceled'

/** A deployment's state in requesters' words, its tone and its list cell; it expires soon within 3 days. */
export function deploymentState(
  d: Pick<Deployment, 'status' | 'expires_at'>,
  now = Date.now(),
): { label: string; tone: 'neutral' | 'waiting' | 'ok' | 'denied' | 'fail'; cell: DeploymentCell } {
  switch (d.status) {
    case 'deleting':
      return { label: tr('Being deleted'), tone: 'neutral', cell: 'running' }
    case 'delete_failed':
      return { label: tr('Couldn’t be deleted'), tone: 'fail', cell: 'failed' }
    case 'deleted':
      return { label: tr('Deleted'), tone: 'denied', cell: 'canceled' }
  }
  if (!d.expires_at) return { label: tr('Kept until deleted'), tone: 'ok', cell: 'done' }
  const n = daysUntil(d.expires_at, now)
  const soon = n <= 3
  return { label: expiryText(n), tone: soon ? 'waiting' : 'neutral', cell: soon ? 'soon' : 'done' }
}

/** "Expires today", "Expires tomorrow", "Expires in 9 days". */
export function expiryText(days: number): string {
  if (days <= 0) return tr('Expires today')
  if (days === 1) return tr('Expires tomorrow')
  return tr('Expires in {n} days', { n: days })
}

/** Who can use a deployment: its owner, and its team, who sees it and runs its actions. */
export function usersText(d: Pick<Deployment, 'owner' | 'summary_fields'>, me: number): string {
  const team = d.summary_fields.team?.name
  const o = d.summary_fields.owner
  const owner = o ? [o.first_name, o.last_name].filter(Boolean).join(' ') || o.username : ''
  if (d.owner == null) return team ? tr('{team} owns it: its members see it and run its actions.', { team }) : ''
  const who = d.owner === me ? tr('You own it.') : tr('{owner} owns it.', { owner })
  return team ? `${who} ${tr('{team} can see it and run its actions.', { team })}` : `${who} ${tr('Nobody else can use it.')}`
}

/** What a deployment's extensions allow now, and which wait for whom. */
export function extensionNote(o: ExtensionOptions, names: (g: GatePreview[]) => string): string {
  if (o.max_extensions != null && o.extensions >= o.max_extensions) return tr('No extension left. The item’s admins can extend it further.')
  const open = o.steps.filter((s) => !s.refused)
  const now = open.filter((s) => s.approvals.length === 0).map((s) => trn(s.days, '+{n} day', '+{n} days'))
  const parts: string[] = []
  if (now.length) parts.push(trn(now.length, '{list} applies at once.', '{list} apply at once.', { list: now.join(tr(' and ')) }))
  for (const s of open.filter((x) => x.approvals.length > 0)) {
    parts.push(trn(s.days, '{n} day needs {names}.', '{n} days needs {names}.', { names: names(s.approvals) }))
  }
  if (!parts.length) return open.length === o.steps.length ? '' : (o.steps.find((s) => s.refused)?.refused ?? '')
  return parts.join(' ')
}

/** One entry of a deployment's history. */
export interface HistoryEntry {
  at: string
  text: string
}

/**
 * A deployment's history from its requests, newest first: who asked for
 * it, extended it, ran an action or asked to delete it, and how it went.
 */
export function deploymentHistory(
  requests: (Pick<CatalogRequest, 'id' | 'kind' | 'status' | 'created' | 'modified' | 'lifetime_days'> & { created_by?: number | null; who?: string })[],
  actions: Map<string, string>,
  me: number,
): HistoryEntry[] {
  const out: HistoryEntry[] = []
  for (const r of requests) {
    const mine = r.created_by === me
    const who = r.who || tr('Someone')
    const n = r.lifetime_days ?? 0
    const action = actions.get(r.kind) ?? r.kind
    let text: string
    switch (r.kind) {
      case 'new':
        text = mine ? tr('You asked for it: request {id}', { id: r.id }) : tr('{who} asked for it: request {id}', { who, id: r.id })
        if (r.status === 'successful') out.push({ at: r.modified, text: tr('Ready') })
        break
      case 'extend':
        if (r.status === 'successful') {
          text = mine ? trn(n, 'You extended it by {n} day', 'You extended it by {n} days') : trn(n, '{who} extended it by {n} day', '{who} extended it by {n} days', { who })
        } else {
          text = mine ? trn(n, 'You asked for {n} more day', 'You asked for {n} more days') : trn(n, '{who} asked for {n} more day', '{who} asked for {n} more days', { who })
        }
        break
      case 'delete':
        text = mine ? tr('You asked to delete it') : tr('{who} asked to delete it', { who })
        if (r.status === 'successful') out.push({ at: r.modified, text: tr('Deleted') })
        break
      default:
        text = mine ? tr('You ran {action}', { action }) : tr('{who} ran {action}', { who, action })
    }
    const how = statusSuffix(r.status)
    out.push({ at: r.created, text: how ? `${text} · ${how}` : text })
  }
  return out.sort((a, b) => b.at.localeCompare(a.at))
}

function statusSuffix(status: string): string {
  switch (status) {
    case 'awaiting_approval':
      return tr('waiting for approval')
    case 'denied':
      return tr('denied')
    case 'pending':
    case 'running':
      return tr('under way')
    case 'failed':
    case 'error':
      return tr('failed')
    case 'canceled':
      return tr('canceled')
  }
  return ''
}

/** What a failed teardown says it left, one line each. */
export function leftBehind(v: unknown): { name: string; why: string }[] {
  const list = Array.isArray(v) ? v : v && typeof v === 'object' ? Object.entries(v).map(([name, why]) => ({ name, why })) : v ? [v] : []
  return list.map((x) => {
    if (typeof x === 'string') return { name: x, why: '' }
    if (x && typeof x === 'object') {
      const o = x as Record<string, unknown>
      const name = String(o.name ?? o.id ?? o.resource ?? JSON.stringify(o))
      const why = o.why ?? o.reason ?? o.kind ?? ''
      return { name, why: typeof why === 'string' ? why : JSON.stringify(why) }
    }
    return { name: String(x), why: '' }
  })
}
