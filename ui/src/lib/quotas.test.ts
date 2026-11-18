import { describe, expect, it } from 'vitest'
import { hasQuota, holderName, quotaSlots } from './quotas'

describe('quota slots', () => {
  it('draws the used and free places', () => {
    expect(quotaSlots(2, 5)).toEqual(['used', 'used', 'free', 'free', 'free'])
  })
  it('marks the place a request would take', () => {
    expect(quotaSlots(3, 5, 1)).toEqual(['used', 'used', 'used', 'adding', 'free'])
  })
  it('marks the places past the quota', () => {
    expect(quotaSlots(5, 5, 1)).toEqual(['used', 'used', 'used', 'used', 'used', 'over'])
    expect(quotaSlots(6, 5)).toEqual(['used', 'used', 'used', 'used', 'used', 'over'])
  })
  it('gives up on a quota too large to draw', () => {
    expect(quotaSlots(3, 50)).toBeNull()
    expect(quotaSlots(19, 20, 1)).toHaveLength(20)
  })
})

describe('quota holders and items', () => {
  it('names a team, or the user without one', () => {
    expect(holderName({ team: { id: 1, name: 'Web' }, user: null })).toBe('Web')
    expect(holderName({ team: null, user: { id: 2, username: 'sam' } })).toBe('sam')
  })
  it('counts a deployment quota only on an item that creates deployments', () => {
    expect(hasQuota({ kind: 'deployment', max_deployments: 3, max_requests_per_day: null })).toBe(true)
    expect(hasQuota({ kind: 'run_once', max_deployments: 3, max_requests_per_day: null })).toBe(false)
    expect(hasQuota({ kind: 'run_once', max_deployments: null, max_requests_per_day: 10 })).toBe(true)
  })
})
