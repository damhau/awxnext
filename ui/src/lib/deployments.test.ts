import { describe, expect, it } from 'vitest'
import {
  addedDays,
  daysUntil,
  deploymentHistory,
  deploymentState,
  expiryText,
  extensionNote,
  leftBehind,
  lifetimeCells,
  usersText,
  type ExtensionOptions,
} from './deployments'
import type { GatePreview } from './portal'

const now = new Date(2026, 10, 19, 14, 0).getTime()
const at = (d: number, h = 12) => new Date(2026, 10, d, h, 0).toISOString()

describe('daysUntil', () => {
  it('counts calendar days, not 24-hour spans', () => {
    expect(daysUntil(at(19, 23), now)).toBe(0)
    expect(daysUntil(at(20, 1), now)).toBe(1)
    expect(daysUntil(at(21, 18), now)).toBe(2)
    expect(daysUntil(at(18, 23), now)).toBe(-1)
  })
})

describe('lifetimeCells', () => {
  it('draws the days gone, today, the days left and those extensions added at the end', () => {
    expect(lifetimeCells(at(14), at(23, 18), 3, now)).toEqual(['gone', 'gone', 'gone', 'gone', 'gone', 'today', 'left', 'added', 'added', 'added'])
  })
  it('counts added days only among the days left', () => {
    expect(lifetimeCells(at(17), at(20), 5, now)).toEqual(['gone', 'gone', 'today', 'added'])
  })
  it('drops the oldest days first, and gives up when the days left are too many', () => {
    expect(lifetimeCells(at(1), at(21), 0, now, 5)).toEqual(['gone', 'gone', 'today', 'left', 'left'])
    expect(lifetimeCells(at(19), '2027-11-19T12:00:00Z', 0, now)).toBeNull()
  })
  it('stops at today once it has expired', () => {
    expect(lifetimeCells(at(17), at(18), 0, now)).toEqual(['gone', 'gone', 'today'])
  })
})

it('adds up the extensions that went through', () => {
  expect(
    addedDays([
      { kind: 'extend', status: 'successful', lifetime_days: 3 },
      { kind: 'extend', status: 'awaiting_approval', lifetime_days: 7 },
      { kind: 'new', status: 'successful', lifetime_days: 14 },
      { kind: 'extend', status: 'successful', lifetime_days: 1 },
    ]),
  ).toBe(4)
})

describe('deploymentState', () => {
  it('says when an active one expires, and warns within three days', () => {
    expect(deploymentState({ status: 'active', expires_at: at(21, 18) }, now)).toEqual({ label: 'Expires in 2 days', tone: 'waiting', cell: 'soon' })
    expect(deploymentState({ status: 'active', expires_at: at(28) }, now)).toMatchObject({ label: 'Expires in 9 days', tone: 'neutral', cell: 'done' })
    expect(deploymentState({ status: 'active', expires_at: null }, now).label).toBe('Kept until deleted')
  })
  it('names a teardown under way, failed, or done', () => {
    expect(deploymentState({ status: 'deleting', expires_at: null }).cell).toBe('running')
    expect(deploymentState({ status: 'delete_failed', expires_at: null })).toMatchObject({ tone: 'fail', cell: 'failed' })
    expect(deploymentState({ status: 'deleted', expires_at: null }).label).toBe('Deleted')
  })
})

it('words the days before expiry', () => {
  expect([0, 1, 4].map(expiryText)).toEqual(['Expires today', 'Expires tomorrow', 'Expires in 4 days'])
})

describe('usersText', () => {
  const sf = { owner: { id: 3, username: 'sam', first_name: 'Sam', last_name: 'Okafor' }, team: { id: 4, name: 'Web team' } }
  it('names the owner and the team who can use it', () => {
    expect(usersText({ owner: 3, summary_fields: sf }, 3)).toBe('You own it. Web team can see it and run its actions.')
    expect(usersText({ owner: 3, summary_fields: sf }, 9)).toBe('Sam Okafor owns it. Web team can see it and run its actions.')
    expect(usersText({ owner: 3, summary_fields: { owner: sf.owner } }, 3)).toBe('You own it. Nobody else can use it.')
    expect(usersText({ owner: null, summary_fields: { team: sf.team } }, 3)).toBe('Web team owns it: its members see it and run its actions.')
  })
})

describe('extensionNote', () => {
  const priya: GatePreview = { kind: 'rule', rule: 'Long', why: '', approvers: [{ id: 2, username: 'priya', name: 'Priya Shah' }], required: 1, fallback: false, timeout: 0 }
  const opts = (over: Partial<ExtensionOptions>): ExtensionOptions => ({
    expires_at: at(21),
    extensions: 0,
    max_extensions: 2,
    until: at(30),
    steps: [
      { days: 1, expires_at: at(22), refused: '', approvals: [] },
      { days: 3, expires_at: at(24), refused: '', approvals: [] },
      { days: 7, expires_at: at(28), refused: '', approvals: [priya] },
    ],
    ...over,
  })
  const names = (g: GatePreview[]) => g.flatMap((x) => x.approvers.map((p) => p.name)).join(', ')
  it('says which extensions apply at once and which wait for whom', () => {
    expect(extensionNote(opts({}), names)).toBe('+1 day and +3 days apply at once. 7 days needs Priya Shah.')
  })
  it('says when none is left', () => {
    expect(extensionNote(opts({ extensions: 2 }), names)).toBe('No extension left. The item’s admins can extend it further.')
  })
  it('gives the reason when every step is refused', () => {
    const refused = opts({ steps: [{ days: 7, expires_at: at(28), refused: 'It would outlive its longest lifetime.', approvals: [] }] })
    expect(extensionNote(refused, names)).toBe('It would outlive its longest lifetime.')
  })
})

it('tells a deployment’s history from its requests, newest first', () => {
  const actions = new Map([['resize', 'Resize']])
  const h = deploymentHistory(
    [
      { id: 7, kind: 'new', status: 'successful', created: at(12, 9), modified: at(12, 10), lifetime_days: 7, created_by: 3 },
      { id: 9, kind: 'extend', status: 'successful', created: at(14), modified: at(14), lifetime_days: 3, created_by: 3 },
      { id: 11, kind: 'resize', status: 'awaiting_approval', created: at(15), modified: at(15), lifetime_days: null, created_by: 5, who: 'Jo Lee' },
      { id: 12, kind: 'delete', status: 'failed', created: at(16), modified: at(16), lifetime_days: null, created_by: 3 },
      { id: 13, kind: 'delete', status: 'successful', created: at(17), modified: at(18), lifetime_days: null, created_by: 5, who: 'Jo Lee' },
    ],
    actions,
    3,
  )
  expect(h.map((e) => e.text)).toEqual([
    'Deleted',
    'Jo Lee asked to delete it',
    'You asked to delete it · failed',
    'Jo Lee ran Resize · waiting for approval',
    'You extended it by 3 days',
    'Ready',
    'You asked for it: request 7',
  ])
})

it('lists what a failed teardown left', () => {
  expect(leftBehind(['lb-orders'])).toEqual([{ name: 'lb-orders', why: '' }])
  expect(leftBehind({ 'lb-orders': 'another pool uses it' })).toEqual([{ name: 'lb-orders', why: 'another pool uses it' }])
  expect(leftBehind([{ name: 'lb', reason: 'in use' }])).toEqual([{ name: 'lb', why: 'in use' }])
  expect(leftBehind(null)).toEqual([])
})
