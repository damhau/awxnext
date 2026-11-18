// Approvals in the portal (roadmap M15-09): what an approver reads before
// answering a request, in requesters' words.
import { tr, trn } from './i18n'
import type { ApprovalAnswer, Person, QuotaHolder } from './portal'

export interface PortalApproval {
  id: number
  status: string
  timed_out: boolean
  created: string
  expires: string | null
  kind: 'rule' | 'freeze' | 'quota'
  rule: string
  condition: string
  approvers: Person[]
  fallback: boolean
  required: number
  approved: number
  answers: (ApprovalAnswer & { as_admin?: boolean })[]
  can_answer: boolean
  cannot_answer: string
  own: boolean
  request: {
    id: number
    kind: string
    status: string
    created: string
    reason: string
    lifetime_days: number | null
    item: { id: number | null; name: string; kind: string | null; icon: string }
    team: { id: number; name: string } | null
    requester: Person | null
    deployment: {
      id: number
      name: string
      status: string
      created: string
      expires_at: string | null
      extensions: number
      max_extensions: number | null
      until: string | null
    } | null
    action: { id: string; name: string } | null
    answers: { variable: string; question: string; value: unknown }[]
  }
  quota: QuotaHolder | null
}

/** The first word of a name, to address its owner. */
export function firstName(name: string): string {
  return name.trim().split(/\s+/)[0] ?? ''
}

function requester(a: PortalApproval): string {
  return a.request.requester?.name ?? tr('Someone')
}

/** The approval's title: who asks for what. */
export function approvalTitle(a: PortalApproval): string {
  const r = a.request
  const who = requester(a)
  const dep = r.deployment?.name ?? r.item.name
  switch (r.kind) {
    case 'new':
      return tr('{who} asks for {item}', { who, item: r.item.name })
    case 'extend':
      return trn(r.lifetime_days ?? 0, '{who} asks for {n} more day on {deployment}', '{who} asks for {n} more days on {deployment}', { who, deployment: dep })
    case 'delete':
      return tr('{who} asks to delete {deployment}', { who, deployment: dep })
    default:
      return tr('{who} asks to run {action} on {deployment}', { who, action: r.action?.name ?? r.kind, deployment: dep })
  }
}

/** The approval in a few words, for the list. */
export function approvalShort(a: PortalApproval): string {
  const r = a.request
  const dep = r.deployment?.name ?? r.item.name
  switch (r.kind) {
    case 'new':
      return r.item.name
    case 'extend':
      return trn(r.lifetime_days ?? 0, 'Extend {deployment} by {n} day', 'Extend {deployment} by {n} days', { deployment: dep })
    case 'delete':
      return tr('Delete {deployment}', { deployment: dep })
    default:
      return tr('{action} on {deployment}', { action: r.action?.name ?? r.kind, deployment: dep })
  }
}

const notNames = new Set(['true', 'false', 'nil', 'null', 'and', 'or', 'not', 'in', 'matches', 'contains', 'startsWith', 'endsWith', 'len', 'now', 'answers'])

/** The answers a rule's condition reads: the names it uses, outside strings. */
export function ruleReads(condition: string): Set<string> {
  const bare = condition.replace(/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`[^`]*`/g, ' ')
  const out = new Set<string>()
  for (const m of bare.matchAll(/(?<![\w.])([A-Za-z_]\w*)(?:\.(\w+))?/g)) {
    const [, head, field] = m
    if ((head === 'answers' || head === 'request') && field) out.add(field)
    else if (!notNames.has(head)) out.add(head)
  }
  return out
}

/** An answer as the approver reads it: lists joined, passwords masked, yes or no. */
export function answerText(v: unknown): string {
  if (v === '$encrypted$') return '••••••'
  if (Array.isArray(v)) return v.map(answerText).join(', ')
  if (typeof v === 'boolean') return v ? tr('Yes') : tr('No')
  if (v && typeof v === 'object') return JSON.stringify(v)
  return String(v ?? '')
}

/** What happens if nobody answers by the approval's timeout. */
export function timeoutNote(a: PortalApproval, date: (s: string) => string): string {
  if (!a.expires) return tr('It waits until someone answers.')
  const when = date(a.expires)
  if (a.request.kind === 'extend') {
    return tr('If nobody answers by {date}, the extension is denied. {deployment} keeps its current expiry.', { date: when, deployment: a.request.deployment?.name ?? '' })
  }
  return tr('If nobody answers by {date}, the request is denied.', { date: when })
}

/** The answer that settled an approval, or the user's own, and what it means. */
export function decision(a: PortalApproval, me: number): { tone: 'ok' | 'denied'; title: string; mine: boolean; at: string; comment: string } | null {
  const mine = a.answers.find((x) => x.user === me)
  const no = a.answers.find((x) => !x.approved)
  const settled = mine ?? no ?? (a.status !== 'pending' ? a.answers[a.answers.length - 1] : undefined)
  if (!settled) {
    if (a.timed_out) return { tone: 'denied', title: tr('Nobody answered in time, so it was denied.'), mine: false, at: a.expires ?? '', comment: '' }
    return null
  }
  const isMine = settled.user === me
  const title = settled.approved
    ? isMine
      ? tr('You approved it')
      : tr('{who} approved it', { who: settled.username })
    : isMine
      ? tr('You denied it')
      : tr('{who} denied it', { who: settled.username })
  return { tone: settled.approved ? 'ok' : 'denied', title, mine: isMine, at: settled.created, comment: settled.comment }
}

/** What happens next once the user answered. */
export function afterAnswer(a: PortalApproval, approved: boolean): string {
  const who = firstName(requester(a))
  if (!approved) return tr('{who} sees your comment under My requests.', { who })
  if (a.status === 'pending') {
    const n = a.required - a.approved
    if (n > 0) return trn(n, 'It still needs {n} more approval.', 'It still needs {n} more approvals.')
  }
  return tr('{who} sees it under My requests.', { who })
}
