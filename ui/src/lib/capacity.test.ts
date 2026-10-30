import { describe, expect, it } from 'vitest'
import { adjustedCapacity, describeAdjustment } from './capacity'

describe('capacity adjustment', () => {
  it('interpolates as the server does', () => {
    expect(adjustedCapacity(8, 61, 0)).toBe(8)
    expect(adjustedCapacity(61, 8, 1)).toBe(61)
    expect(adjustedCapacity(8, 61, 0.5)).toBe(34)
    expect(adjustedCapacity(16, 61, 0.29)).toBe(29)
    expect(adjustedCapacity(5, 5, 0.3)).toBe(5)
  })
  it('names the resource at each end', () => {
    expect(describeAdjustment(8, 61)).toBe('0% sizes capacity by CPU (8 forks), 100% by memory (61 forks).')
    expect(describeAdjustment(64, 20)).toBe('0% sizes capacity by memory (20 forks), 100% by CPU (64 forks).')
    expect(describeAdjustment(1, 1)).toBe('CPU and memory both allow 1 fork.')
    expect(describeAdjustment(0, 0)).toBe('Capacity is measured at the next health check.')
  })
})
