import { describe, expect, it } from 'vitest'
import { itemLabel, loopProgress, type Iteration } from './loops'

const it_ = (index: number, status: string): Iteration => ({ index, job: { id: index + 1, type: 'job', name: 'x', status, failed: status === 'failed', elapsed: 0 } })

describe('loop progress', () => {
  it('waits until every item has finished', () => {
    expect(loopProgress(['a', 'b'], [])).toEqual({ status: 'pending', done: 0, failed: 0, total: 2 })
    expect(loopProgress(['a', 'b'], [it_(0, 'successful')]).status).toBe('running')
    expect(loopProgress(['a', 'b'], [it_(0, 'failed'), it_(1, 'running')])).toEqual({ status: 'running', done: 1, failed: 1, total: 2 })
  })
  it('fails when any item failed', () => {
    expect(loopProgress(['a', 'b'], [it_(0, 'successful'), it_(1, 'canceled')]).status).toBe('failed')
    expect(loopProgress(['a', 'b'], [it_(0, 'successful'), it_(1, 'successful')]).status).toBe('successful')
  })
  it('succeeds with no items', () => {
    expect(loopProgress([], []).status).toBe('successful')
  })
  it('labels items', () => {
    expect(itemLabel('eu-west', 0)).toBe('eu-west')
    expect(itemLabel(3, 1)).toBe('3')
    expect(itemLabel({ name: 'a' }, 2)).toBe('#3')
    expect(itemLabel('x'.repeat(61), 0)).toBe('#1')
  })
})
