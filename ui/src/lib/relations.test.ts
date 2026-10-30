import { describe, expect, it } from 'vitest'
import { moveEarlier, relationOps } from './relations'

describe('relationOps', () => {
  it('sends only the differences of an unordered list', () => {
    expect(relationOps([1, 2, 3], [3, 4, 1], false)).toEqual({ detach: [2], attach: [4] })
    expect(relationOps([], [], false)).toEqual({ detach: [], attach: [] })
  })
  it('appends to an ordered list that keeps its order', () => {
    expect(relationOps([1, 2], [1, 2, 3], true)).toEqual({ detach: [], attach: [3] })
    expect(relationOps([1, 2, 3], [1, 3], true)).toEqual({ detach: [2], attach: [] })
    expect(relationOps([1, 2], [2, 5], true)).toEqual({ detach: [1], attach: [5] })
  })
  it('rebuilds an ordered list whose order changes', () => {
    expect(relationOps([1, 2], [2, 1], true)).toEqual({ detach: [1, 2], attach: [2, 1] })
    expect(relationOps([1, 2, 3], [3, 1, 4], true)).toEqual({ detach: [1, 2, 3], attach: [3, 1, 4] })
  })
})

describe('moveEarlier', () => {
  it('swaps an item with the one before it', () => {
    expect(moveEarlier(['a', 'b', 'c'], 2)).toEqual(['a', 'c', 'b'])
    expect(moveEarlier(['a', 'b', 'c'], 1)).toEqual(['b', 'a', 'c'])
  })
  it('leaves the first item, and indexes out of range, alone', () => {
    const list = ['a', 'b']
    expect(moveEarlier(list, 0)).toBe(list)
    expect(moveEarlier(list, 5)).toBe(list)
  })
})
