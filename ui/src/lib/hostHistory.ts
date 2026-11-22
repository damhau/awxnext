import type { CellState } from './cells'

export type ResultStatus = 'ok' | 'changed' | 'failed' | 'unreachable' | 'skipped'

export interface HistoryResult {
  job: number
  status: ResultStatus
  check_mode: boolean
  counter: number
  created: string
}

export type HistoryFlag =
  | { type: 'not_idempotent'; runs: number; job: number }
  | { type: 'new_failure'; job: number; since: string; last_success: number; failures: number }
  | { type: 'drift'; open: false; check_mode: false; job: number; at: string; previous: number }
  | { type: 'drift'; open: true; check_mode: true; id: number; since: string; job: number | null; last_job: number | null; last_seen: string; diff: unknown }

export type OpenDrift = Extract<HistoryFlag, { open: true }>

export interface HistoryTask {
  play: string
  role: string
  task: string
  results: HistoryResult[]
  flags: HistoryFlag[]
}

export interface HistoryTemplate {
  id: number
  name: string
  tasks: HistoryTask[]
}

export interface HostHistory {
  days: number
  since: string
  until: string
  retention_days: number
  templates: HistoryTemplate[]
}

export const HOUR = 3600_000

/** Start times of n buckets of `hours`, oldest first, aligned on the local clock; the last one holds `now`. */
export function historyBuckets(now: number, hours = 4, n = 30): number[] {
  const d = new Date(now)
  d.setHours(d.getHours() - (d.getHours() % hours), 0, 0, 0)
  const last = d.getTime()
  return Array.from({ length: n }, (_, i) => {
    const t = new Date(last)
    t.setHours(t.getHours() - (n - 1 - i) * hours)
    return t.getTime()
  })
}

const rank: Record<ResultStatus, number> = { skipped: 1, ok: 2, changed: 3, failed: 4, unreachable: 5 }

export interface HistoryCell {
  start: number
  end: number
  /** The worst result in the bucket, as the job page's matrix ranks them. */
  state: CellState
  results: HistoryResult[]
  /** The result a click opens: the worst, the latest on a tie. */
  open?: HistoryResult
}

/** A task's results in each bucket. */
export function taskCells(task: HistoryTask, buckets: number[], hours = 4): HistoryCell[] {
  const cells: HistoryCell[] = buckets.map((start, i) => ({ start, end: buckets[i + 1] ?? start + hours * HOUR, state: 'empty', results: [] }))
  for (const r of task.results) {
    const at = Date.parse(r.created)
    const cell = cells.find((c) => at >= c.start && at < c.end)
    if (!cell) continue
    cell.results.push(r)
    if (!cell.open || rank[r.status] >= rank[cell.open.status]) cell.open = r
  }
  for (const c of cells) if (c.open) c.state = c.open.status
  return cells
}

/** A task's name as the job output shows it, with its role. */
export function taskName(t: Pick<HistoryTask, 'role' | 'task'>): string {
  return t.role ? `${t.role} : ${t.task}` : t.task
}

const priority = (f: HistoryFlag) => (f.type === 'new_failure' ? 0 : f.type === 'drift' && f.open ? 1 : f.type === 'not_idempotent' ? 2 : 3)

/** A task's flags, the one its row shows first: a new failure, open drift, not idempotent, then drift a run put back. */
export function rankedFlags(t: HistoryTask): HistoryFlag[] {
  return [...t.flags].sort((a, b) => priority(a) - priority(b))
}

/** Rows that need a look are tinted: a new failure, or a task that changes on every run. */
export function tinted(t: HistoryTask): boolean {
  return t.flags.some((f) => f.type === 'new_failure' || f.type === 'not_idempotent')
}

export interface HostFlags {
  /** The earliest run of the host's current failures, when a task failed after succeeding. */
  failingSince?: number
  notIdempotent: number
  /** When the earliest open drift was found. */
  driftSince?: string
  drifted: number
}

/** What the host's title says about it. */
export function hostFlags(h: HostHistory | undefined): HostFlags {
  const out: HostFlags = { notIdempotent: 0, drifted: 0 }
  for (const tpl of h?.templates ?? [])
    for (const t of tpl.tasks)
      for (const f of t.flags) {
        if (f.type === 'new_failure') out.failingSince = Math.min(out.failingSince ?? Infinity, f.job)
        else if (f.type === 'not_idempotent') out.notIdempotent++
        else if (f.open) {
          out.drifted++
          if (!out.driftSince || f.since < out.driftSince) out.driftSince = f.since
        }
      }
  return out
}

/** Every flag of the host, with its task, in the order the card explains them. */
export function flaggedTasks(h: HostHistory | undefined): { template: HistoryTemplate; task: HistoryTask; flag: HistoryFlag }[] {
  const out: { template: HistoryTemplate; task: HistoryTask; flag: HistoryFlag }[] = []
  for (const template of h?.templates ?? []) for (const task of template.tasks) for (const flag of task.flags) out.push({ template, task, flag })
  return out.sort((a, b) => priority(a.flag) - priority(b.flag))
}
