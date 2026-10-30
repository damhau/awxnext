import { describe, expect, it } from 'vitest'
import { bulkMessage, bulkOutcome, nameList, nextSelection, offerSelectAll } from './bulk'

type R = { id: number; name: string }
const key = (r: R) => r.id

describe('bulk selection', () => {
  it('keeps rows selected on another page and adds the page’s', () => {
    const picked = new Map([[1, { id: 1, name: 'a' }]])
    const page = [{ id: 2, name: 'b' }, { id: 3, name: 'c' }]
    const next = nextSelection(picked, new Set([1, 3]), page, key)
    expect([...next.values()].map((r) => r.name)).toEqual(['a', 'c'])
  })
  it('drops rows unselected', () => {
    const picked = new Map([[1, { id: 1, name: 'a' }], [2, { id: 2, name: 'b' }]])
    expect([...nextSelection(picked, new Set([2]), [], key).keys()]).toEqual([2])
  })
  it('offers to select all only when the page is selected and more match', () => {
    const sel = new Set([1, 2])
    expect(offerSelectAll(2, [1, 2], (k) => sel.has(k as number), 30)).toBe(true)
    expect(offerSelectAll(2, [1, 2], (k) => sel.has(k as number), 2)).toBe(false)
    expect(offerSelectAll(1, [1, 2], (k) => k === 1, 30)).toBe(false)
    expect(offerSelectAll(0, [], () => false, 30)).toBe(false)
  })
})

describe('bulk outcome', () => {
  it('reports success with the noun', () => {
    const o = bulkOutcome([{ status: 'fulfilled', value: 1 }])
    expect(bulkMessage('Delete', o, ['project', 'projects'])).toBe('Delete: done for 1 project')
    expect(bulkMessage('Sync', bulkOutcome([{ status: 'fulfilled', value: 1 }, { status: 'fulfilled', value: 1 }]), ['project', 'projects'])).toBe('Sync: done for 2 projects')
  })
  it('reports failures with the first error', () => {
    const o = bulkOutcome([
      { status: 'fulfilled', value: 1 },
      { status: 'rejected', reason: new Error('You do not have permission to perform this action.') },
      { status: 'rejected', reason: 'other' },
    ])
    expect(o).toEqual({ done: 1, failed: 2, error: 'You do not have permission to perform this action.' })
    expect(bulkMessage('Delete', o, ['user', 'users'])).toBe('Delete: 2 of 3 failed — You do not have permission to perform this action.')
  })
})

describe('names in a confirmation', () => {
  it('lists a few, and counts the rest', () => {
    expect(nameList(['a'])).toBe('a')
    expect(nameList(['a', 'b', 'c'])).toBe('a, b and c')
    expect(nameList(['a', 'b', 'c', 'd'], 2)).toBe('a, b and 2 more')
  })
})
