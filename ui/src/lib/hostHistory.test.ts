import { describe, expect, it } from 'vitest'
import { flaggedTasks, historyBuckets, hostFlags, HOUR, rankedFlags, taskCells, taskName, tinted, type HistoryResult, type HistoryTask, type HostHistory } from './hostHistory'

const at = (y: number, mo: number, d: number, h: number, mi = 0) => new Date(y, mo - 1, d, h, mi).getTime()
const result = (job: number, status: HistoryResult['status'], when: number, check_mode = false): HistoryResult => ({ job, status, check_mode, counter: 3, created: new Date(when).toISOString() })
const task = (over: Partial<HistoryTask> = {}): HistoryTask => ({ play: 'Hosts', role: '', task: 'Write config', results: [], flags: [], ...over })

describe('historyBuckets', () => {
  it('ends with the 4-hour block that holds now, aligned on the clock', () => {
    const b = historyBuckets(at(2026, 11, 22, 15, 27))
    expect(b).toHaveLength(30)
    expect(b[29]).toBe(at(2026, 11, 22, 12))
    expect(b[28]).toBe(at(2026, 11, 22, 8))
    expect(b[0]).toBe(at(2026, 11, 17, 16))
  })
  it('takes other sizes', () => {
    const b = historyBuckets(at(2026, 11, 22, 15, 27), 1, 3)
    expect(b).toEqual([at(2026, 11, 22, 13), at(2026, 11, 22, 14), at(2026, 11, 22, 15)])
  })
})

describe('taskCells', () => {
  const buckets = historyBuckets(at(2026, 11, 22, 15, 27))
  it('puts each result in its bucket, the worst one showing', () => {
    const cells = taskCells(
      task({ results: [result(1, 'ok', at(2026, 11, 22, 12, 5)), result(2, 'failed', at(2026, 11, 22, 13)), result(3, 'changed', at(2026, 11, 22, 14)), result(4, 'ok', at(2026, 11, 22, 9))] }),
      buckets,
    )
    expect(cells[29].state).toBe('failed')
    expect(cells[29].open?.job).toBe(2)
    expect(cells[29].results).toHaveLength(3)
    expect(cells[28].state).toBe('ok')
    expect(cells[27].state).toBe('empty')
    expect(cells[27].open).toBeUndefined()
  })
  it('opens the latest of equal results', () => {
    const cells = taskCells(task({ results: [result(1, 'ok', at(2026, 11, 22, 12, 5)), result(2, 'ok', at(2026, 11, 22, 13))] }), buckets)
    expect(cells[29].open?.job).toBe(2)
  })
  it('leaves out results outside the window', () => {
    const cells = taskCells(task({ results: [result(1, 'failed', at(2026, 11, 10, 12)), result(2, 'ok', buckets[0] + 30 * 4 * HOUR)] }), buckets)
    expect(cells.every((c) => c.state === 'empty')).toBe(true)
  })
})

describe('flags', () => {
  const failing = { type: 'new_failure' as const, job: 9, since: '2026-11-22T10:00:00Z', last_success: 8, failures: 1 }
  const notIdem = { type: 'not_idempotent' as const, runs: 4, job: 9 }
  const fixed = { type: 'drift' as const, open: false as const, check_mode: false as const, job: 5, at: '2026-11-21T10:00:00Z', previous: 4 }
  const open = { type: 'drift' as const, open: true as const, check_mode: true as const, id: 1, since: '2026-11-21T08:00:00Z', job: 6, last_job: 7, last_seen: '2026-11-22T08:00:00Z', diff: null }

  it('ranks a new failure first, then open drift, not idempotent, and drift put back', () => {
    expect(rankedFlags(task({ flags: [fixed, notIdem, open, failing] })).map((f) => f.type + (f.type === 'drift' ? String(f.open) : ''))).toEqual([
      'new_failure',
      'drifttrue',
      'not_idempotent',
      'driftfalse',
    ])
  })
  it('tints failing and non-idempotent rows', () => {
    expect(tinted(task({ flags: [failing] }))).toBe(true)
    expect(tinted(task({ flags: [notIdem] }))).toBe(true)
    expect(tinted(task({ flags: [open, fixed] }))).toBe(false)
  })
  it('sums up the host', () => {
    const open2 = { ...open, id: 2, since: '2026-11-20T08:00:00Z' }
    const h: HostHistory = {
      days: 5,
      since: '',
      until: '',
      retention_days: 30,
      templates: [
        { id: 1, name: 'site', tasks: [task({ flags: [{ ...failing, job: 12 }] }), task({ task: 'Stamp', flags: [notIdem] })] },
        { id: 2, name: 'base', tasks: [task({ flags: [failing, open] }), task({ task: 'Other', flags: [open2, notIdem] })] },
      ],
    }
    expect(hostFlags(h)).toEqual({ failingSince: 9, notIdempotent: 2, drifted: 2, driftSince: '2026-11-20T08:00:00Z' })
    expect(hostFlags(undefined)).toEqual({ notIdempotent: 0, drifted: 0 })
    expect(flaggedTasks(h).map((f) => f.flag.type)).toEqual(['new_failure', 'new_failure', 'drift', 'drift', 'not_idempotent', 'not_idempotent'])
  })
})

describe('taskName', () => {
  it('prefixes the role', () => {
    expect(taskName({ role: 'web', task: 'Install' })).toBe('web : Install')
    expect(taskName({ role: '', task: 'Install' })).toBe('Install')
  })
})
