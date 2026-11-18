import { describe, expect, it } from 'vitest'
import {
  answersOf,
  approvalHint,
  categories,
  denial,
  lifetimeChoices,
  lifetimeHint,
  matches,
  namesText,
  prefill,
  requestBody,
  requestSteps,
  requestState,
  surveyQuestions,
  timeoutText,
  waitingApprovals,
  type RequestApproval,
} from './portal'

const approval = (over: Partial<RequestApproval>): RequestApproval => ({
  id: 1,
  status: 'pending',
  timed_out: false,
  expires: '2026-11-20T10:00:00Z',
  rule: 'Large VMs',
  approvers: [],
  required: 1,
  approved: 0,
  answers: [],
  ...over,
})

describe('portal', () => {
  it('counts the items of each category, those without one last', () => {
    expect(categories([{ category: 'Network' }, { category: '' }, { category: 'Compute' }, { category: 'Network' }])).toEqual([
      { name: 'Compute', n: 1 },
      { name: 'Network', n: 2 },
      { name: '', n: 1 },
    ])
  })

  it('finds items by every word of a search', () => {
    const vm = { name: 'Linux VM', description: 'RHEL 9 or Ubuntu', category: 'Compute' }
    expect(matches(vm, '')).toBe(true)
    expect(matches(vm, 'linux rhel')).toBe(true)
    expect(matches(vm, 'compute')).toBe(true)
    expect(matches(vm, 'linux windows')).toBe(false)
  })

  it('says when an item needs approval', () => {
    expect(approvalHint([]).tone).toBe('none')
    expect(approvalHint([{ name: 'Large VMs', condition: 'size > 5' }])).toEqual({ tone: 'some', text: 'Approval for Large VMs' })
    expect(approvalHint([{ name: 'Large VMs', condition: 'size > 5' }, { name: 'All', condition: ' ' }]).tone).toBe('always')
  })

  it('says how long what an item makes is kept', () => {
    expect(lifetimeHint({ kind: 'run', max_lifetime_days: null })).toBe('Runs once')
    expect(lifetimeHint({ kind: 'deployment', max_lifetime_days: null })).toBe('Kept until deleted')
    expect(lifetimeHint({ kind: 'deployment', max_lifetime_days: 30 })).toBe('Up to 30 days')
  })

  it('offers lifetimes within the longest, and for good only without a longest or a default', () => {
    expect(lifetimeChoices(10, 5)).toEqual([1, 3, 5, 7, 10])
    expect(lifetimeChoices(null, 2)).toEqual([1, 2, 3, 7, 14, 30, 90])
    expect(lifetimeChoices(null, null)).toEqual([1, 3, 7, 14, 30, 90, null])
  })

  it('names a few people, then counts the others', () => {
    expect(namesText([])).toBe('')
    expect(namesText(['Priya'])).toBe('Priya')
    expect(namesText(['Priya', 'Omar'])).toBe('Priya and Omar')
    expect(namesText(['Priya', 'Omar', 'Lena'])).toBe('Priya, Omar and Lena')
    expect(namesText(['Priya', 'Omar', 'Lena', 'Dana'])).toBe('Priya, Omar and 2 others')
    expect(namesText(['Priya', 'Omar', 'Lena', 'Dana'], 3)).toBe('Priya, Omar, Lena and Dana')
  })

  it('words a request’s state', () => {
    expect(requestState('awaiting_approval')).toEqual({ label: 'Waiting for approval', tone: 'waiting' })
    expect(requestState('running').tone).toBe('running')
    expect(requestState('error').tone).toBe('fail')
  })

  it('lays a request out as four steps', () => {
    const states = (status: string, approvals: RequestApproval[] = []) => requestSteps({ status, approvals }).map((s) => s.state)
    expect(states('running')).toEqual(['done', 'skipped', 'running', 'pending'])
    expect(states('awaiting_approval', [approval({})])).toEqual(['done', 'waiting', 'pending', 'pending'])
    expect(states('denied', [approval({ status: 'failed' })])).toEqual(['done', 'denied', 'pending', 'pending'])
    expect(states('successful', [approval({ status: 'successful' })])).toEqual(['done', 'done', 'done', 'done'])
    expect(states('failed')).toEqual(['done', 'skipped', 'failed', 'pending'])
  })

  it('finds who denied a request, or that nobody answered', () => {
    expect(denial({ approvals: [approval({ status: 'successful' })] })).toBeNull()
    const no = { user: 2, username: 'lena', approved: false, comment: 'Use the internal zone.', created: '2026-11-13T09:12:00Z' }
    expect(denial({ approvals: [approval({ answers: [no] })] })).toEqual({ by: 'lena', at: no.created, comment: no.comment, timedOut: false })
    expect(denial({ approvals: [approval({ timed_out: true })] })?.timedOut).toBe(true)
    expect(waitingApprovals({ approvals: [approval({}), approval({ status: 'successful' })] })).toHaveLength(1)
  })

  it('gives dynamic questions their choices from the item', () => {
    const qs = surveyQuestions([{ variable: 'a', dynamic: true }, { variable: 'b', dynamic: false }])
    expect(qs[0].choices_from).toEqual({ type: 'source' })
    expect(qs[1].choices_from).toBeUndefined()
  })

  it('fills a form again without passwords or what it no longer asks', () => {
    const asked = [
      { variable: 'name', type: 'text' },
      { variable: 'pw', type: 'password' },
      { variable: 'size', type: 'integer' },
    ]
    expect(prefill({ name: 'api', pw: '$encrypted$', size: 4, gone: 1 }, asked)).toEqual({ name: 'api', size: 4 })
    expect(prefill(undefined, asked)).toEqual({})
  })

  it('sends the answers shown, or their defaults', () => {
    const shown = [
      { variable: 'name', type: 'text' },
      { variable: 'size', type: 'integer', default: 2 },
      { variable: 'pw', type: 'password', default: '$encrypted$' },
      { variable: 'tags', type: 'multiselect' },
      { variable: 'note', type: 'text' },
    ]
    expect(answersOf(shown, { name: 'api', size: '8', tags: 'a\nb', note: '' })).toEqual({ name: 'api', size: 8, tags: ['a', 'b'] })
    expect(answersOf(shown, {})).toEqual({ size: 2 })
  })

  it('sends only what was set', () => {
    expect(requestBody({ a: 1 }, null, undefined, '  ')).toEqual({ answers: { a: 1 } })
    expect(requestBody({}, 3, null, ' For the demo ')).toEqual({ answers: {}, team: 3, lifetime_days: null, reason: 'For the demo' })
  })

  it('words a timeout', () => {
    expect(timeoutText(172800)).toBe('2 days')
    expect(timeoutText(7200)).toBe('2 hours')
    expect(timeoutText(90000)).toBe('25 hours')
    expect(timeoutText(600)).toBe('10 minutes')
  })
})
