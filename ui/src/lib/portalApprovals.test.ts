import { describe, expect, it } from 'vitest'
import { afterAnswer, answerText, approvalShort, approvalTitle, decision, firstName, ruleReads, timeoutNote, type PortalApproval } from './portalApprovals'

const sam = { id: 3, username: 'sam', name: 'Sam Okafor' }

const approval = (over: Partial<PortalApproval> = {}, request: Partial<PortalApproval['request']> = {}): PortalApproval => ({
  id: 40,
  status: 'pending',
  timed_out: false,
  created: '2026-11-19T13:22:00Z',
  expires: '2026-11-21T13:22:00Z',
  kind: 'rule',
  rule: 'Large VMs',
  condition: 'size == "large"',
  approvers: [{ id: 2, username: 'priya', name: 'Priya Shah' }],
  fallback: false,
  required: 1,
  approved: 0,
  answers: [],
  can_answer: true,
  cannot_answer: '',
  own: false,
  quota: null,
  ...over,
  request: {
    id: 1043,
    kind: 'new',
    status: 'awaiting_approval',
    created: '2026-11-19T13:22:00Z',
    reason: 'Load test',
    lifetime_days: 7,
    item: { id: 5, name: 'Linux VM', kind: 'deployment', icon: 'server' },
    team: { id: 4, name: 'Web team' },
    requester: sam,
    deployment: null,
    action: null,
    answers: [],
    ...request,
  },
})

const dep = { id: 8, name: 'dev-jo-etl', status: 'active', created: '', expires_at: null, extensions: 1, max_extensions: 2, until: null }

describe('approvalTitle and approvalShort', () => {
  it('say who asks for what, by the kind of request', () => {
    expect(approvalTitle(approval())).toBe('Sam Okafor asks for Linux VM')
    expect(approvalTitle(approval({}, { kind: 'extend', deployment: dep }))).toBe('Sam Okafor asks for 7 more days on dev-jo-etl')
    expect(approvalTitle(approval({}, { kind: 'delete', deployment: dep }))).toBe('Sam Okafor asks to delete dev-jo-etl')
    expect(approvalTitle(approval({}, { kind: 'resize', deployment: dep, action: { id: 'resize', name: 'Resize' } }))).toBe('Sam Okafor asks to run Resize on dev-jo-etl')
    expect(approvalShort(approval())).toBe('Linux VM')
    expect(approvalShort(approval({}, { kind: 'extend', lifetime_days: 1, deployment: dep }))).toBe('Extend dev-jo-etl by 1 day')
  })
})

describe('ruleReads', () => {
  it('finds the names a condition reads, outside its strings', () => {
    expect([...ruleReads('size == "large" && env in ["prod", "size"]')]).toEqual(['size', 'env'])
    expect([...ruleReads('answers.region == "eu" or not true')]).toEqual(['region'])
    expect([...ruleReads('extend.days >= 7')]).toEqual(['extend'])
    expect([...ruleReads('kind == "extend" && request.lifetime_days > 3')]).toEqual(['kind', 'lifetime_days'])
  })
})

it('words an answer', () => {
  expect([answerText(['a', 'b']), answerText(true), answerText('$encrypted$'), answerText(8), answerText(null)]).toEqual(['a, b', 'Yes', '••••••', '8', ''])
})

it('says what a timeout does', () => {
  const date = (s: string) => s.slice(0, 10)
  expect(timeoutNote(approval(), date)).toBe('If nobody answers by 2026-11-21, the request is denied.')
  expect(timeoutNote(approval({}, { kind: 'extend', deployment: dep }), date)).toBe(
    'If nobody answers by 2026-11-21, the extension is denied. dev-jo-etl keeps its current expiry.',
  )
  expect(timeoutNote(approval({ expires: null }), date)).toBe('It waits until someone answers.')
})

describe('decision', () => {
  const yes = { user: 2, username: 'priya', approved: true, comment: '', created: '2026-11-19T14:42:00Z' }
  const no = { user: 6, username: 'omar', approved: false, comment: 'Use a medium one', created: '2026-11-19T14:50:00Z' }
  it('is the user’s own answer first', () => {
    expect(decision(approval({ answers: [yes] }), 2)).toMatchObject({ tone: 'ok', title: 'You approved it', mine: true })
  })
  it('is otherwise the answer that denied it, or the last one once settled', () => {
    expect(decision(approval({ status: 'failed', answers: [yes, no] }), 9)).toMatchObject({ tone: 'denied', title: 'omar denied it', comment: 'Use a medium one' })
    expect(decision(approval({ status: 'successful', answers: [yes] }), 9)).toMatchObject({ title: 'priya approved it', mine: false })
  })
  it('says when nobody answered in time, and is null while it waits', () => {
    expect(decision(approval({ status: 'failed', timed_out: true }), 9)?.title).toBe('Nobody answered in time, so it was denied.')
    expect(decision(approval(), 9)).toBeNull()
  })
})

it('says what happens after an answer', () => {
  expect(afterAnswer(approval(), false)).toBe('Sam sees your comment under My requests.')
  expect(afterAnswer(approval({ status: 'pending', required: 2, approved: 1 }), true)).toBe('It still needs 1 more approval.')
  expect(afterAnswer(approval({ status: 'successful' }), true)).toBe('Sam sees it under My requests.')
  expect(firstName('  Priya Shah ')).toBe('Priya')
})
