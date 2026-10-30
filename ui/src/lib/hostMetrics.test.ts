import { describe, expect, it } from 'vitest'
import { metricsQuery, monthLabel } from './hostMetrics'

describe('host metrics', () => {
  it('queries the hosts automated, deleted ones only when asked', () => {
    expect(metricsQuery('', false, 1, 25)).toEqual({ order_by: '-last_automation', page: 1, page_size: 25, deleted: 'false' })
    expect(metricsQuery(' web ', true, 2, 50)).toEqual({ order_by: '-last_automation', page: 2, page_size: 50, search: 'web' })
  })
  it('names the summary months', () => {
    expect(monthLabel('2026-10-01')).toBe('October 2026')
    expect(monthLabel('2025-01-01')).toBe('January 2025')
    expect(monthLabel('soon')).toBe('soon')
  })
})
