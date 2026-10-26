import { useEffect, useMemo, useRef, useState } from 'react'
import { api, type Page } from '../../api/client'
import type { JobEvent } from '../../api/types'
import { eventGroup, useLive } from '../../api/live'
import { stripAnsi } from '../../lib/ansi'
import { jobApi } from '../../lib/format'

const MAX_EVENTS = 60_000

export function eventsPath(type: string, id: number | string): string {
  return `${jobApi(type, id)}${type === 'job' ? 'job_events' : 'events'}/`
}

/**
 * All events of a job, in counter order, kept current while it runs: stored
 * events are paged in, live ones arrive over the websocket, and a final fetch
 * after the job finishes fills any gap.
 */
export function useJobEvents(type: string, id: number, running: boolean) {
  const store = useRef(new Map<number, JobEvent>())
  const [version, setVersion] = useState(0)
  const [loading, setLoading] = useState(true)
  const [truncated, setTruncated] = useState(false)
  const pending = useRef(0)
  const bump = () => {
    // Coalesce bursts of live events into one render per frame.
    if (pending.current) return
    pending.current = requestAnimationFrame(() => {
      pending.current = 0
      setVersion((v) => v + 1)
    })
  }

  const fetchFrom = async (signal?: AbortSignal) => {
    let after = 0
    for (const c of store.current.keys()) after = Math.max(after, c)
    for (let page = 0; page < 200; page++) {
      const r = await api.get<Page<JobEvent>>(eventsPath(type, id), { order_by: 'counter', counter__gt: after, page_size: 500 }, signal)
      for (const e of r.results) store.current.set(e.counter, e)
      if (r.results.length) after = r.results[r.results.length - 1].counter
      if (store.current.size >= MAX_EVENTS) {
        setTruncated(true)
        break
      }
      if (!r.next) break
    }
    bump()
  }

  useEffect(() => {
    store.current = new Map()
    setLoading(true)
    setTruncated(false)
    const ctl = new AbortController()
    fetchFrom(ctl.signal)
      .catch(() => {})
      .finally(() => setLoading(false))
    return () => ctl.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [type, id])

  // When the job stops, pick up anything the socket missed.
  const wasRunning = useRef(running)
  useEffect(() => {
    if (wasRunning.current && !running) fetchFrom().catch(() => {})
    wasRunning.current = running
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running])

  useLive(running ? { [eventGroup(type)]: [id] } : null, (m) => {
    const e = m as unknown as JobEvent
    if (typeof e.counter !== 'number' || store.current.size >= MAX_EVENTS) return
    store.current.set(e.counter, e)
    bump()
  })

  const events = useMemo(
    () => [...store.current.values()].sort((a, b) => a.counter - b.counter),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [version],
  )
  return { events, loading, truncated }
}

export type LineKind = 'play' | 'task' | 'fatal' | 'ok' | 'changed' | 'skip' | 'recap' | 'plain'

export interface Line {
  n: number
  text: string
  plain: string
  kind: LineKind
  event: JobEvent
  /** Index of the task header this line belongs to (for collapsing). */
  task: number
  meta?: string
}

const failedEvent = (e: JobEvent) => (e.event === 'runner_on_failed' && !e.event_data?.ignore_errors) || e.event === 'runner_on_unreachable'

export function kindOf(e: JobEvent): LineKind {
  switch (e.event) {
    case 'playbook_on_play_start':
      return 'play'
    case 'playbook_on_task_start':
    case 'playbook_on_handler_task_start':
      return 'task'
    case 'runner_on_failed':
    case 'runner_on_unreachable':
    case 'runner_item_on_failed':
      return failedEvent(e) || e.event === 'runner_item_on_failed' ? 'fatal' : 'ok'
    case 'runner_on_ok':
    case 'runner_item_on_ok':
      return e.changed ? 'changed' : 'ok'
    case 'runner_on_skipped':
    case 'runner_item_on_skipped':
      return 'skip'
    case 'playbook_on_stats':
      return 'recap'
  }
  return 'plain'
}

/** Splits event stdout into numbered output lines; task headers carry a result summary. */
export function toLines(events: JobEvent[]): Line[] {
  const lines: Line[] = []
  let task = -1
  // Per task: counts of results and timing, for the header meta.
  const taskStats = new Map<string, { ok: number; changed: number; failed: number; skipped: number; start: number; end: number }>()
  for (const e of events) {
    const tu = (e.event_data?.task_uuid as string) ?? e.parent_uuid
    if (!tu || !e.event.startsWith('runner_on_')) continue
    const s = taskStats.get(tu) ?? { ok: 0, changed: 0, failed: 0, skipped: 0, start: Infinity, end: 0 }
    if (failedEvent(e)) s.failed++
    else if (e.event === 'runner_on_skipped') s.skipped++
    else if (e.changed) s.changed++
    else if (e.event === 'runner_on_ok') s.ok++
    const st = Date.parse((e.event_data?.start as string) ?? e.created)
    const en = Date.parse((e.event_data?.end as string) ?? e.created)
    if (!isNaN(st)) s.start = Math.min(s.start, st)
    if (!isNaN(en)) s.end = Math.max(s.end, en)
    taskStats.set(tu, s)
  }
  for (const e of events) {
    if (!e.stdout) continue
    const kind = kindOf(e)
    const parts = e.stdout.replace(/\r\n/g, '\n').split('\n')
    if (kind === 'task' || kind === 'play') task = lines.length
    parts.forEach((text, i) => {
      if (i === parts.length - 1 && text === '' && parts.length > 1) return
      const plain = stripAnsi(text)
      const header = (kind === 'task' || kind === 'play') && /\*{3,}\s*$/.test(plain)
      const line: Line = {
        n: (e.start_line ?? lines.length) + i + 1,
        text: header ? text.replace(/\s*\*{3,}(\x1b\[[0-9;]*m)*\s*$/, '$1') : text,
        plain: header ? plain.replace(/\s*\*{3,}\s*$/, '') : plain,
        kind: header ? kind : kind === 'task' || kind === 'play' ? 'plain' : kind,
        event: e,
        task: kind === 'task' || kind === 'play' ? lines.length : task,
      }
      if (header && kind === 'task') {
        const s = taskStats.get(e.uuid)
        if (s) {
          const bits = [s.ok && `${s.ok} ok`, s.changed && `${s.changed} changed`, s.failed && `${s.failed} failed`, s.skipped && `${s.skipped} skipped`].filter(Boolean)
          const secs = s.end > s.start ? (s.end - s.start) / 1000 : 0
          line.meta = bits.join(' · ') + (secs ? ` · ${secs.toFixed(1)}s` : '')
        }
      }
      lines.push(line)
    })
  }
  return lines
}

export type CellState = 'ok' | 'changed' | 'failed' | 'unreachable' | 'skipped' | 'none'

export interface Grid {
  tasks: { key: string; name: string; play: string }[]
  hosts: { name: string; cells: CellState[]; bad: boolean }[]
  counts: Record<CellState, number>
}

const rank: Record<CellState, number> = { none: 0, skipped: 1, ok: 2, changed: 3, failed: 4, unreachable: 5 }

/** Host × task matrix of results; tasks repeated per serial batch share a column. */
export function toGrid(events: JobEvent[], hostOrder: string[] = []): Grid {
  const taskIndex = new Map<string, number>()
  const tasks: Grid['tasks'] = []
  const byUuid = new Map<string, number>()
  for (const e of events) {
    if (e.event === 'playbook_on_task_start' || e.event === 'playbook_on_handler_task_start') {
      const key = `${e.play}\u0000${e.task}`
      let idx = taskIndex.get(key)
      if (idx === undefined) {
        idx = tasks.length
        taskIndex.set(key, idx)
        tasks.push({ key, name: e.task, play: e.play })
      }
      byUuid.set(e.uuid, idx)
    }
  }
  const results = new Map<string, CellState[]>()
  const hostList: string[] = [...hostOrder]
  const ensure = (h: string) => {
    let row = results.get(h)
    if (!row) {
      row = new Array(tasks.length).fill('none')
      results.set(h, row)
      if (!hostList.includes(h)) hostList.push(h)
    }
    return row
  }
  hostOrder.forEach(ensure)
  for (const e of events) {
    if (!e.event.startsWith('runner_on_') || !e.host_name) continue
    const idx = byUuid.get((e.event_data?.task_uuid as string) ?? e.parent_uuid)
    if (idx === undefined) continue
    let s: CellState
    if (e.event === 'runner_on_unreachable') s = 'unreachable'
    else if (e.event === 'runner_on_failed') s = e.event_data?.ignore_errors ? 'ok' : 'failed'
    else if (e.event === 'runner_on_skipped') s = 'skipped'
    else if (e.event === 'runner_on_ok') s = e.changed ? 'changed' : 'ok'
    else continue
    const row = ensure(e.host_name)
    if (rank[s] > rank[row[idx]]) row[idx] = s
  }
  const counts: Record<CellState, number> = { ok: 0, changed: 0, failed: 0, unreachable: 0, skipped: 0, none: 0 }
  const hosts = hostList.map((name) => {
    const cells: CellState[] = results.get(name) ?? new Array<CellState>(tasks.length).fill('none')
    cells.forEach((c) => counts[c]++)
    return { name, cells, bad: cells.some((c) => c === 'failed' || c === 'unreachable') }
  })
  return { tasks, hosts, counts }
}

export interface Failure {
  task: string
  play: string
  hosts: string[]
  message: string
  path: string
  event: JobEvent
  stopped: boolean
}

/** The first task that failed hosts, with its message and location. */
export function firstFailure(events: JobEvent[]): Failure | null {
  const failed = events.filter(failedEvent)
  if (!failed.length) return null
  const first = failed[0]
  const sameTask = failed.filter((e) => e.task === first.task && e.play === first.play)
  const res = (first.event_data?.res ?? {}) as Record<string, unknown>
  const msg = [res.msg, res.stderr, res.reason, res.module_stderr, res.stdout].find((x) => typeof x === 'string' && x.trim()) as string | undefined
  const path = String(first.event_data?.task_path ?? '')
  return {
    task: first.task,
    play: first.play,
    hosts: [...new Set(sameTask.map((e) => e.host_name))],
    message: (msg ?? stripAnsi(first.stdout).split('\n')[0]).slice(0, 600),
    path: path.includes('/project/') ? path.slice(path.indexOf('/project/') + 9) : path.split('/').slice(-3).join('/'),
    event: first,
    stopped: events.some((e) => /NO MORE HOSTS LEFT/.test(e.stdout ?? '')),
  }
}
