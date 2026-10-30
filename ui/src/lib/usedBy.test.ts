import { describe, expect, it } from 'vitest'
import { countKinds, deleteEffects, distinct, howUsed, inUse, linkTo, summarize, type Dependent } from './usedBy'

const d = (type: string, id: number, field: string, effect: 'deleted' | 'cleared' = 'cleared', name = `${type} ${id}`): Dependent => ({
  type,
  resource: type + 's',
  id,
  name,
  field,
  effect,
})

describe('used by', () => {
  it('says how and where', () => {
    expect(howUsed(d('job_template', 4, 'inventory'))).toBe('as its inventory')
    expect(howUsed(d('workflow_job_template', 2, 'node_inventory'))).toBe('as a step’s inventory')
    expect(howUsed(d('workflow_job_template', 2, 'node_template'))).toBe('runs it in a step')
    expect(howUsed(d('x', 1, 'some_field'))).toBe('as some field')
    expect(linkTo(d('job_template', 4, 'inventory'))).toBe('/templates/job/4')
    expect(linkTo(d('workflow_job_template', 2, 'node_template'))).toBe('/templates/workflow/2')
    expect(linkTo(d('schedule', 9, 'inventory', 'cleared', 'nightly run'))).toBe('/schedules?q=nightly%20run')
    expect(linkTo(d('inventory_source', 3, 'source_project', 'deleted'))).toBe('/inventory-sources/3')
  })
  it('counts by kind', () => {
    expect(countKinds([d('job_template', 1, 'inventory'), d('job_template', 2, 'inventory'), d('schedule', 3, 'inventory')])).toBe('2 job templates and 1 schedule')
    expect(countKinds([d('inventory', 1, 'input_inventories')])).toBe('1 inventory')
    expect(countKinds([])).toBe('')
  })
  it('splits what is deleted from what loses it, once each', () => {
    const wf = d('workflow_job_template', 2, 'inventory')
    const u = { count: 4, hidden: 1, results: [d('schedule', 3, 'unified_job_template', 'deleted'), wf, { ...wf, field: 'node_inventory' }, d('job_template', 1, 'inventory')] }
    const e = deleteEffects(u)
    expect(e.deleted.map((x) => x.id)).toEqual([3])
    expect(e.cleared.map((x) => x.id)).toEqual([2, 1])
    expect(e.hidden).toBe(1)
    expect(distinct(u.results)).toHaveLength(3)
  })
})

describe('used by, in a bulk delete', () => {
  it('summarizes each row', () => {
    expect(summarize({ count: 2, hidden: 0, results: [d('job_template', 1, 'inventory'), d('schedule', 2, 'inventory')] })).toBe('1 job template and 1 schedule')
    expect(summarize({ count: 1, hidden: 2, results: [d('job_template', 1, 'inventory')] })).toBe('1 job template, and 2 you cannot see')
    expect(summarize({ count: 0, hidden: 3, results: [] })).toBe('3 you cannot see')
    expect(inUse({ count: 0, hidden: 0, results: [] })).toBe(false)
    expect(inUse({ count: 0, hidden: 1, results: [] })).toBe(true)
    expect(inUse(null)).toBe(false)
  })
})
