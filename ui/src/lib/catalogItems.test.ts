import { describe, expect, it } from 'vitest'
import {
  actionID,
  approvalRule,
  approverText,
  deploymentStatus,
  errorSection,
  itemBody,
  itemForm,
  itemRefs,
  lifetimeError,
  newAction,
  newRule,
  parseDays,
  questionEdits,
  questionOverrides,
  questionSummary,
  requestKind,
  requestStatus,
  ruleEdit,
  stepLabels,
  stepRows,
  surveyChoices,
  surveyPath,
  timeoutText,
  type CatalogItem,
  type SurveyQuestion,
} from './catalogItems'

const spec: SurveyQuestion[] = [
  { variable: 'name', question_name: 'Name', type: 'text', required: true },
  { variable: 'size', question_name: 'Size', type: 'multiplechoice', choices: 'small\nmedium\nlarge', default: 'small', required: true },
  { variable: 'vlan', question_name: 'VLAN', type: 'multiplechoice', choices_from: { type: 'source', source: 3 } },
  { variable: 'disks', question_name: 'Disks', type: 'integer', default: 1 },
  { variable: 'secret', question_name: 'Secret', type: 'password' },
  { variable: 'fqdn', question_name: 'FQDN', type: 'text', value_from: 'name + ".lab"' },
]

describe('questionEdits and questionOverrides', () => {
  it('lists the questions an item can change, from its overrides', () => {
    const rows = questionEdits(spec, { size: { choices: ['small', 'medium'] }, disks: { value: 2, visible: true }, secret: { hidden: true } })
    expect(rows.map((r) => r.variable)).toEqual(['name', 'size', 'vlan', 'disks', 'secret'])
    expect(rows[1]).toMatchObject({ mode: 'asked', choices: ['small', 'medium'] })
    expect(rows[3]).toMatchObject({ mode: 'fixed', value: '2', visible: true })
    expect(rows[4]).toMatchObject({ mode: 'hidden' })
  })

  it('sends only what differs from the survey', () => {
    const rows = questionEdits(spec)
    expect(questionOverrides(spec, rows)).toEqual({})
    rows[0].question_name = ' Database name '
    rows[1].choices = ['large', 'small']
    rows[1].default = 'small'
    rows[2].filter = 'item.site == "gva"'
    rows[3] = { ...rows[3], mode: 'fixed', value: '4', visible: true }
    rows[4] = { ...rows[4], mode: 'fixed', value: 'hunter2', visible: true }
    expect(questionOverrides(spec, rows)).toEqual({
      name: { question_name: 'Database name' },
      size: { choices: ['small', 'large'] },
      vlan: { filter: 'item.site == "gva"' },
      disks: { value: 4, visible: true },
      secret: { value: 'hunter2' },
    })
  })

  it('keeps every choice as no narrowing, and a hidden question’s new default', () => {
    const rows = questionEdits(spec)
    rows[1] = { ...rows[1], mode: 'hidden', choices: ['small', 'medium', 'large'], default: 'large' }
    expect(questionOverrides(spec, rows)).toEqual({ size: { hidden: true, default: 'large' } })
  })
})

it('words what requesters get of a question', () => {
  const rows = questionEdits(spec, { name: { question_name: 'DB name' }, size: { choices: ['small'] }, disks: { value: 2 }, secret: { value: '$encrypted$' } })
  expect(questionSummary(spec[0], rows[0])).toBe('Asked as “DB name”')
  expect(questionSummary(spec[1], rows[1])).toBe('Asked, 1 of 3 choices')
  expect(questionSummary(spec[3], rows[3])).toBe('Fixed to 2, not shown')
  expect(questionSummary(spec[4], rows[4])).toBe('Fixed, never shown')
})

it('reads the survey’s choices as a list or as lines', () => {
  expect(surveyChoices(spec[1])).toEqual(['small', 'medium', 'large'])
  expect(surveyChoices({ ...spec[1], choices: ['a', 'b'] })).toEqual(['a', 'b'])
})

it('parses extension days', () => {
  expect(parseDays('1, 3 7')).toEqual({ days: [1, 3, 7], error: null })
  expect(parseDays('')).toEqual({ days: [], error: null })
  expect(parseDays('1, 0').error).toBeTruthy()
  expect(parseDays('3, 3').error).toBe('3 is there twice.')
})

it('words approvers and timeouts', () => {
  expect(approverText({ name: 'r', condition: '', approvers: 'team_admins' })).toBe('The Team Admins of the request’s team')
  expect(approverText({ name: 'r', condition: '', approvers: 'team', team: 4 }, 'Platform')).toBe('Members of Platform')
  expect(timeoutText(undefined)).toBe('7 days')
  expect(timeoutText(86400)).toBe('1 day')
  expect(timeoutText(7200)).toBe('2 hours')
})

it('turns step labels into rows and back, without empty ones', () => {
  expect(stepRows({ 'Create the database': 'Database' })).toEqual([{ step: 'Create the database', label: 'Database' }])
  expect(stepLabels([{ step: ' Play ', label: ' Label ' }, { step: 'x', label: '' }])).toEqual({ Play: 'Label' })
})

it('suggests an action id from its name', () => {
  expect(actionID('Add a disk')).toBe('add_a_disk')
  expect(actionID('Redémarrer')).toBe('redemarrer')
  expect(actionID('2 Restart')).toBe('restart')
  expect(actionID('Delete')).toBe('delete_it')
})

it('puts an API error in its section', () => {
  expect(errorSection('questions')).toBe('questions')
  expect(errorSection('approval_rules')).toBe('approvals')
  expect(errorSection('connection')).toBe('deployment')
  expect(errorSection('name')).toBe('item')
})

const item: CatalogItem = {
  id: 4,
  name: 'PostgreSQL database',
  description: '',
  unified_job_template: 13,
  kind: 'deployment',
  icon: 'server',
  category: 'Databases',
  owner_team: null,
  questions: { size: { choices: ['small', 'medium'] } },
  approval_rules: [{ name: 'Large', condition: 'size == "large"', approvers: 'team', team: 3, required: 2, timeout: 172800 }],
  freeze_exception_team: 3,
  notification_template: null,
  deployment_name: 'answers.name',
  default_lifetime_days: 7,
  max_lifetime_days: 30,
  extension_days: [1, 3, 7],
  max_extensions: 2,
  teardown_template: 16,
  connection: [{ label: 'Host', value: 'host', copy: true }],
  actions: [{ id: 'restart', name: 'Restart', description: '', template: 14 }],
  expiry_waits_for_freeze: false,
  max_deployments: 5,
  max_requests_per_day: null,
  over_quota: 'refuse',
  step_labels: {},
}
const names = (resource: string, id: number) => ({ id, name: `${resource} ${id}` })

describe('itemForm and itemBody', () => {
  it('lists the references an item names, once each', () => {
    expect(itemRefs(item)).toEqual({ teams: [3], templates: [13, 16, 14], notifications: [] })
  })

  it('gives back the item it was made from', () => {
    const v = itemForm(item, names, spec)
    expect(v.rules[0]).toMatchObject({ approvers: 'team', team: { id: 3, name: 'teams 3' }, required: '2', timeout_hours: '48' })
    expect(v.actions[0].template).toEqual({ id: 14, name: 'templates 14' })
    const body = itemBody(v, spec)
    for (const k of Object.keys(body) as (keyof CatalogItem)[]) if (k !== 'questions') expect(body[k], k).toEqual(item[k])
    expect(body.questions).toEqual(item.questions)
  })

  it('keeps a deployment until deleted, clearing its lifetime and extensions', () => {
    const v = itemForm(item, names, spec)
    expect(v.lifetime).toBe('limited')
    expect(itemForm({ ...item, default_lifetime_days: null, max_lifetime_days: null }, names).lifetime).toBe('forever')
    const body = itemBody({ ...v, lifetime: 'forever', expiry_waits_for_freeze: true }, spec)
    expect(body).toMatchObject({ default_lifetime_days: null, max_lifetime_days: null, extension_days: [], max_extensions: null, expiry_waits_for_freeze: false, connection: item.connection })
  })

  it('asks a deployment that expires for a default or a longest lifetime', () => {
    const v = { kind: 'deployment' as const, lifetime: 'limited' as const, default_lifetime_days: '', max_lifetime_days: ' ' }
    expect(lifetimeError(v)).toBe('Give a default or a longest lifetime.')
    expect(lifetimeError({ ...v, max_lifetime_days: '30' })).toBeNull()
    expect(lifetimeError({ ...v, lifetime: 'forever' })).toBeNull()
    expect(lifetimeError({ ...v, kind: 'run_once' })).toBeNull()
  })

  it('leaves the questions out while the survey is unknown', () => {
    expect(itemBody(itemForm(item, names), null)).not.toHaveProperty('questions')
  })

  it('clears what only a deployment has for a run-once item', () => {
    const body = itemBody({ ...itemForm(item, names, spec), kind: 'run_once' }, spec)
    expect(body).toMatchObject({ deployment_name: '', default_lifetime_days: null, max_lifetime_days: null, extension_days: [], max_extensions: null, teardown_template: null, connection: [], actions: [], max_deployments: null, expiry_waits_for_freeze: false })
  })

  it('drops empty connection lines, and a new item starts empty', () => {
    const v = itemForm(null, names)
    expect(v).toMatchObject({ kind: 'run_once', lifetime: 'forever', template: null, rules: [], over_quota: 'refuse' })
    v.kind = 'deployment'
    v.connection = [{ label: ' ', value: '', copy: true }, { label: 'Host', value: 'host', copy: false }]
    expect(itemBody(v, []).connection).toEqual([{ label: 'Host', value: 'host', copy: false }])
  })
})

it('sends a rule’s team or users only for those approvers', () => {
  const r = { ...newRule(), name: ' Big ', approvers: 'team_admins' as const, team: { id: 3, name: 'Web' }, users_from: 'x' }
  expect(approvalRule(r)).toEqual({ name: 'Big', condition: '', approvers: 'team_admins', required: 1, timeout: 604800 })
  expect(approvalRule({ ...r, approvers: 'users', users_from: ' answers.owner ' })).toMatchObject({ users_from: 'answers.owner' })
  expect(ruleEdit({ name: 'x', condition: '', approvers: 'users', users_from: 'a' }).timeout_hours).toBe('168')
  expect(newAction()).toMatchObject({ auto: true, template: null })
})

it('words statuses and kinds', () => {
  expect(requestStatus('awaiting_approval')).toEqual({ cell: 'pending', label: 'Awaiting approval' })
  expect(requestStatus('denied').cell).toBe('failed')
  expect(deploymentStatus('delete_failed')).toEqual({ cell: 'failed', label: 'Couldn’t be deleted' })
  expect(deploymentStatus('deleted').cell).toBe('none')
  expect(requestKind('extend')).toBe('Extension')
  expect(requestKind('restart', [{ id: 'restart', name: 'Restart' }])).toBe('Restart')
  expect(requestKind('resize')).toBe('resize')
})

it('finds a template’s survey by its type', () => {
  expect(surveyPath({ id: 3, name: 'w', type: 'workflow_job_template' })).toBe('workflow_job_templates/3/')
  expect(surveyPath({ id: 4, name: 'j', type: 'job_template' })).toBe('job_templates/4/')
})
