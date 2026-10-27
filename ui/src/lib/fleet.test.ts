import { describe, expect, it } from 'vitest'
import type { Job } from '../api/types'
import { fleetRows, hourBuckets } from './fleet'

const NOW = Date.parse('2026-10-27T10:34:00Z')

function job(id: number, tpl: number, status: string, started: string, type = 'job', inventory = 'redbox'): Job {
  return {
    id,
    type,
    status,
    started,
    finished: null,
    name: `tpl-${tpl}`,
    unified_job_template: tpl,
    summary_fields: { unified_job_template: { id: tpl, name: `tpl-${tpl}` }, inventory: { id: 1, name: inventory } },
  } as unknown as Job
}

describe('hourBuckets', () => {
  it('ends with the current hour, oldest first', () => {
    const b = hourBuckets(NOW, 24)
    expect(b).toHaveLength(24)
    expect(b[23]).toBe(Date.parse('2026-10-27T10:00:00Z'))
    expect(b[0]).toBe(Date.parse('2026-10-26T11:00:00Z'))
  })
})

describe('fleetRows', () => {
  const b = hourBuckets(NOW, 24)

  it('keeps the worst result of an hour and links to it', () => {
    const rows = fleetRows([job(1, 7, 'successful', '2026-10-27T09:05:00Z'), job(2, 7, 'failed', '2026-10-27T09:10:00Z'), job(3, 7, 'successful', '2026-10-27T09:50:00Z')], b)
    const cell = rows[0].cells[22]
    expect(cell.state).toBe('failed')
    expect(cell.job?.id).toBe(2)
    expect([cell.runs, cell.fails]).toEqual([3, 1])
  })

  it('opens the latest run when results are equal', () => {
    const rows = fleetRows([job(1, 7, 'successful', '2026-10-27T09:05:00Z'), job(2, 7, 'successful', '2026-10-27T09:40:00Z')], b)
    expect(rows[0].cells[22].job?.id).toBe(2)
  })

  it('puts failing templates first and drops runs outside the window', () => {
    const rows = fleetRows(
      [
        job(1, 1, 'successful', '2026-10-27T08:00:00Z'),
        job(2, 1, 'successful', '2026-10-27T09:00:00Z'),
        job(3, 2, 'error', '2026-10-27T10:20:00Z'),
        job(4, 3, 'failed', '2026-10-25T10:00:00Z'),
        job(5, 4, 'running', '2026-10-27T10:30:00Z', 'workflow_job'),
      ],
      b,
    )
    expect(rows.map((r) => r.name)).toEqual(['tpl-2', 'tpl-1', 'tpl-4'])
    expect(rows[0].cells[23].state).toBe('error')
    expect(rows[2]).toMatchObject({ type: 'workflow_job_template', inventory: 'workflow' })
    expect(rows[2].cells[23].state).toBe('running')
  })
})
