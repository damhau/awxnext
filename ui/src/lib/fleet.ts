import type { Job } from '../api/types'
import { statusKind } from './format'

/** What a fleet cell shows: the worst result of the runs in its hour. */
export type FleetState = 'idle' | 'ok' | 'cancel' | 'pending' | 'run' | 'fail' | 'error'

const rank: Record<FleetState, number> = { idle: 0, ok: 1, cancel: 2, pending: 3, run: 4, fail: 5, error: 6 }

const HOUR = 3600_000

export interface FleetCell {
  start: number
  state: FleetState
  runs: number
  fails: number
  /** The run a click opens: the worst one, the latest on a tie. */
  job?: Job
}

export interface FleetRow {
  key: string
  id: number | null
  type: 'job_template' | 'workflow_job_template'
  name: string
  inventory: string
  cells: FleetCell[]
  runs: number
  fails: number
}

/** Start times of the last n hours, oldest first; the last one is the current hour. */
export function hourBuckets(now: number, n = 24): number[] {
  const d = new Date(now)
  d.setMinutes(0, 0, 0)
  const last = d.getTime()
  return Array.from({ length: n }, (_, i) => last - (n - 1 - i) * HOUR)
}

function stateOf(status: string): FleetState {
  const k = statusKind(status)
  return k === 'none' ? 'idle' : k
}

/** Templates x hours: one row per template that ran, failing templates first. */
export function fleetRows(jobs: Job[], buckets: number[]): FleetRow[] {
  const rows = new Map<string, FleetRow>()
  const first = buckets[0]
  for (const j of jobs) {
    const at = Date.parse(j.started ?? j.created ?? '')
    const i = Math.floor((at - first) / HOUR)
    if (!(i >= 0 && i < buckets.length)) continue
    const workflow = j.type === 'workflow_job'
    const key = `${j.type}:${j.unified_job_template ?? j.name}`
    let row = rows.get(key)
    if (!row) {
      row = {
        key,
        id: j.unified_job_template,
        type: workflow ? 'workflow_job_template' : 'job_template',
        name: j.summary_fields.unified_job_template?.name ?? j.name,
        inventory: workflow ? 'workflow' : (j.summary_fields.inventory?.name ?? ''),
        cells: buckets.map((start) => ({ start, state: 'idle', runs: 0, fails: 0 })),
        runs: 0,
        fails: 0,
      }
      rows.set(key, row)
    }
    const cell = row.cells[i]
    const s = stateOf(j.status)
    const failed = s === 'fail' || s === 'error'
    cell.runs++
    row.runs++
    if (failed) {
      cell.fails++
      row.fails++
    }
    const cur = cell.job ? stateOf(cell.job.status) : 'idle'
    const later = !cell.job || Date.parse(cell.job.started ?? cell.job.created ?? '') <= at
    if (rank[s] > rank[cur] || (rank[s] === rank[cur] && later)) {
      cell.state = s
      cell.job = j
    }
  }
  return [...rows.values()].sort((a, b) => b.fails - a.fails || b.runs - a.runs || a.name.localeCompare(b.name))
}
