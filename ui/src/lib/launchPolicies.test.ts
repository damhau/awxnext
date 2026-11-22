import { describe, expect, it } from 'vitest'
import { attachedPath, fieldGroups, fieldsTree, launchVerdict, outcomeView, POLICY_EXAMPLES, policyCompletions, refusalLines, type PolicyField } from './launchPolicies'

const fields: PolicyField[] = [
  { path: 'launch.type', type: 'string', help: '' },
  { path: 'launch.inventory', type: 'object', help: '' },
  { path: 'launch.inventory.name', type: 'string', help: '' },
  { path: 'launch.inventory.hosts', type: 'int', help: '' },
  { path: 'launch.credentials', type: 'list of object', help: '' },
  { path: 'launch.credentials[].kind', type: 'string', help: '' },
  { path: 'launch.user', type: 'object', help: '' },
  { path: 'launch.user.teams', type: 'list of string', help: '' },
  { path: 'launch.extra_vars', type: 'map', help: '' },
  { path: 'now', type: 'timestamp', help: '' },
]

const labels = (text: string, cursor = text.length) => policyCompletions(fieldsTree(fields))(text, cursor).map((c) => c.label)

describe('completion', () => {
  it('builds the tree of the fields', () => {
    const t = fieldsTree(fields)
    expect(t.fields?.inventory.fields?.hosts.type).toBe('int')
    expect(t.fields?.credentials.items?.fields?.kind.type).toBe('string')
    expect(t.fields?.user.fields?.teams).toEqual({ type: 'list', items: { type: 'string' } })
    expect(t.fields?.extra_vars.type).toBe('map')
  })
  it('completes the roots, the fields and the methods', () => {
    expect(labels('la')).toEqual(['launch'])
    expect(labels('win')).toEqual(['window('])
    expect(labels('launch.inv')).toEqual(['inventory'])
    expect(labels('launch.inventory.')).toEqual(['hosts', 'name'])
    expect(labels('launch.credentials[0].k')).toEqual(['kind'])
    expect(labels('launch.user.teams.ex')).toEqual(['exists(x, ', 'exists_one(x, '])
    expect(labels('event.da')).toEqual([])
    expect(labels('"launch.')).toEqual([])
  })
  it('inserts window with now', () => {
    expect(policyCompletions(null)('!win', 4)[0]).toMatchObject({ insert: 'window(now, ', from: 1 })
  })
})

describe('decisions', () => {
  it('names each outcome', () => {
    expect(outcomeView('refused')).toEqual({ label: 'Refused', state: 'failed' })
    expect(outcomeView('would_refuse').state).toBe('changed')
    expect(outcomeView('allowed').state).toBe('ok')
  })
  it('splits a check into refusals and shadow refusals', () => {
    const d = (id: number, outcome: string) => ({ id, name: `p${id}`, mode: '', outcome, message: '', error: '' })
    const v = launchVerdict([d(1, 'allowed'), d(2, 'refused'), d(3, 'would_refuse')])
    expect(v.refused.map((x) => x.id)).toEqual([2])
    expect(v.shadow.map((x) => x.id)).toEqual([3])
    expect(launchVerdict(undefined)).toEqual({ refused: [], shadow: [] })
    expect(refusalLines([d(2, 'refused'), { ...d(4, 'refused'), message: 'Give a limit.' }, d(3, 'would_refuse')])).toEqual(['p2', 'p4: Give a limit.'])
  })
})

describe('the rest', () => {
  it('groups the fields by object', () => {
    expect(fieldGroups(fields).map((g) => g.group)).toEqual(['launch', 'launch.inventory', 'launch.credentials', 'launch.user', ''])
  })
  it('links what a policy is attached to', () => {
    expect(attachedPath({ type: 'workflow_job_template', id: 3 })).toBe('/templates/workflow/3')
    expect(attachedPath({ type: 'inventory', id: 2 })).toBe('/inventories/2')
  })
  it('has the guide’s five examples', () => {
    expect(POLICY_EXAMPLES).toHaveLength(5)
  })
})
