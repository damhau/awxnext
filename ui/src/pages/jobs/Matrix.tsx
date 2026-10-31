import { Fragment, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { Link } from 'react-router'
import { useList } from '../../api/hooks'
import type { Job } from '../../api/types'
import { Ago, Cell } from '../../components/ui'
import { stripAnsi } from '../../lib/ansi'
import { diffLines, hunks, taskDiffs, tooLargeToCompare, unifiedKind, type TaskDiff } from '../../lib/diff'
import type { CellSize } from '../../lib/cells'
import { active, jobPath, plural } from '../../lib/format'
import { isBad, peers, projectPath, type firstFailure, type Grid, type TaskState } from './events'

const word: Record<TaskState, string> = { ok: 'ok', changed: 'changed', failed: 'failed', unreachable: 'unreachable', skipped: 'skipped', none: 'not run' }
const HOSTS = 24
const TASK_LIST = 40

type Pick = { host: string; task: number }

/** The cell a job page opens on: the first failure, else the first change. */
function firstPick(grid: Grid): Pick | null {
  for (const want of [isBad, (s: TaskState) => s === 'changed']) {
    for (const h of grid.hosts) {
      const t = h.cells.findIndex(want)
      if (t >= 0) return { host: h.name, task: t }
    }
  }
  return null
}

/**
 * Hosts x tasks: columns are task numbers, the numbered list below names
 * them. Selecting a cell (click, or arrows once one has focus) shows its
 * result beside the matrix.
 */
export function Matrix({ grid, job, failure, onHost, onJump }: { grid: Grid; job: Job; failure: ReturnType<typeof firstFailure>; onHost: (h: string) => void; onJump: (line: number) => void }) {
  const [allHosts, setAllHosts] = useState(false)
  const [allTasks, setAllTasks] = useState(false)
  const [picked, setPicked] = useState<Pick | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const ordered = useMemo(() => [...grid.hosts.filter((h) => h.bad), ...grid.hosts.filter((h) => !h.bad)], [grid])
  const badHosts = grid.hosts.filter((h) => h.bad).length
  const shown = allHosts ? ordered : ordered.slice(0, Math.max(HOSTS, badHosts))
  const cur = picked ?? firstPick(grid)
  const curRow = cur ? shown.findIndex((h) => h.name === cur.host) : -1
  const n = grid.tasks.length
  const size: CellSize = n <= 16 ? 'lg' : n <= 40 ? 'md' : 'sm'
  const every = size === 'sm' ? 5 : 1
  const c = grid.counts

  const select = (row: number, task: number) => {
    const r = Math.max(0, Math.min(shown.length - 1, row))
    const t = Math.max(0, Math.min(n - 1, task))
    setPicked({ host: shown[r].name, task: t })
    box.current?.querySelector<HTMLButtonElement>(`[data-cell="${r}:${t}"]`)?.focus()
  }
  const onKey = (e: KeyboardEvent) => {
    const move: Record<string, [number, number]> = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1], Home: [0, -n], End: [0, n] }
    const d = move[e.key]
    if (!d || !cur) return
    e.preventDefault()
    select(Math.max(curRow, 0) + d[0], cur.task + d[1])
  }

  return (
    <section className="panel" aria-labelledby="mx-h">
      <div className="panel-head">
        <h2 id="mx-h">Hosts × tasks</h2>
        <span className="mono small muted matrix-counts">
          {plural(grid.hosts.length, 'host')} · {plural(n, 'task')}
          {([['ok', c.ok], ['changed', c.changed], ['failed', c.failed + c.unreachable], ['skipped', c.skipped], ['none', c.none]] as const).map(([s, v]) => (
            <Fragment key={s}>
              {' · '}
              <span>
                <Cell state={s} size="xs" /> {word[s]} {v}
              </span>
            </Fragment>
          ))}
        </span>
        <span className="small muted hint" style={{ marginLeft: 'auto' }}>
          Click a cell, then arrows to move
        </span>
      </div>
      <div className="panel-body matrix-body">
        <div className="matrix-main">
          <div className="matrix-scroll">
            <div ref={box} className="matrix" data-size={size} style={{ gridTemplateColumns: `max-content repeat(${n}, auto)` }} role="group" aria-label={`Results of ${plural(grid.hosts.length, 'host')} by ${plural(n, 'task')}`} onKeyDown={onKey}>
              <span />
              {grid.tasks.map((t, i) => (
                <span key={t.key} className={`matrix-no${t.bad ? ' bad' : ''}${cur?.task === i ? ' sel' : ''}`} title={t.name} aria-hidden="true">
                  {(i + 1) % every === 0 || i === 0 ? i + 1 : ''}
                </span>
              ))}
              {shown.map((h, row) => (
                <div key={h.name} className="matrix-row">
                  <span className={`matrix-host mono${h.bad ? ' bad' : ''}${row === curRow ? ' sel' : ''}`}>{h.id ? <Link to={`/hosts/${h.id}`}>{h.name}</Link> : h.name}</span>
                  {h.cells.map((s, t) => {
                    const on = row === curRow && cur?.task === t
                    return (
                      <Cell
                        key={t}
                        state={s}
                        size={size}
                        selected={on}
                        tabIndex={on || (!cur && row === 0 && t === 0) ? 0 : -1}
                        data-cell={`${row}:${t}`}
                        label={`${h.name}, task ${t + 1} ${grid.tasks[t].name}: ${word[s]}`}
                        onClick={() => select(row, t)}
                      />
                    )
                  })}
                </div>
              ))}
            </div>
          </div>
          {shown.length < ordered.length && (
            <button className="link-btn small" style={{ marginTop: 10 }} onClick={() => setAllHosts(true)}>
              Show {plural(ordered.length - shown.length, 'more host')} (no failures)
            </button>
          )}
          <ol className="matrix-tasks mono">
            {(allTasks ? grid.tasks : grid.tasks.slice(0, TASK_LIST)).map((t, i) => (
              <li key={t.key} className={`${t.bad ? 'bad' : ''}${cur?.task === i ? ' sel' : ''}`}>
                <button className="link-btn" onClick={() => select(Math.max(curRow, 0), i)} title={t.play}>
                  {t.name}
                </button>
              </li>
            ))}
          </ol>
          {!allTasks && n > TASK_LIST && (
            <button className="link-btn small" onClick={() => setAllTasks(true)}>
              Show all {n} tasks
            </button>
          )}
        </div>
        {cur && curRow >= 0 ? (
          <CellDetail grid={grid} host={grid.hosts.findIndex((h) => h.name === cur.host)} task={cur.task} job={job} failure={failure} onHost={onHost} onJump={onJump} />
        ) : (
          <aside className="cell-detail empty-detail small muted">Select a cell to see what that task did on that host.</aside>
        )}
      </div>
    </section>
  )
}

function CellDetail({ grid, host, task, job, failure, onHost, onJump }: { grid: Grid; host: number; task: number; job: Job; failure: ReturnType<typeof firstFailure>; onHost: (h: string) => void; onJump: (line: number) => void }) {
  const h = grid.hosts[host]
  const s = h.cells[task]
  const e = h.events[task]
  const t = grid.tasks[task]
  const bad = isBad(s)
  const prev = useList<Job>(bad && job.unified_job_template ? 'unified_jobs/' : null, { unified_job_template: job.unified_job_template!, status: 'successful', id__lt: job.id, order_by: '-id', page_size: 1 })
  const p = prev.data?.results[0]
  const secs = Number(e?.event_data?.duration)
  const path = e?.event_data?.task_path ? projectPath(String(e.event_data.task_path)) : ''
  const out = e
    ? stripAnsi(e.stdout ?? '').trim() || `${word[s]}: [${h.name}]`
    : h.bad && h.cells.slice(0, task).some(isBad)
      ? `Not run: ${h.name} failed an earlier task.`
      : active(job.status)
        ? 'Not run yet.'
        : 'Did not run on this host: not targeted, skipped by the play, or the play ended first.'
  return (
    <aside className="cell-detail" aria-label="Selected cell" aria-live="polite">
      <div className="cd-head">
        <Cell state={s} size="lg" />
        <div className="grow">
          <div className="mono small muted">
            {h.name} · TASK {task + 1}
          </div>
          <div className="cd-task">{t.name}</div>
          {path && <div className="mono small faint ellipsis">{path}</div>}
        </div>
        {secs > 0 && <span className="mono small">{secs < 10 ? secs.toFixed(1) : Math.round(secs)} s</span>}
      </div>
      <div className="cd-peers small">
        <b className={bad ? 'fail-text' : undefined}>{word[s]}</b> {peers(grid, host, task)}
      </div>
      <pre className="cd-out">{out}</pre>
      {e && <Diffs diffs={taskDiffs(e.event_data?.res)} onJump={() => onJump(e.start_line + 1)} />}
      {bad && (
        <div className="cd-note small muted">
          {failure?.stopped && failure.task === t.name && <p>The play stopped here: no hosts were left to continue (max_fail_percentage or any_errors_fatal).</p>}
          <p>
            Last success of this template:{' '}
            {p ? (
              <>
                <Link to={jobPath(p)}>#{p.id}</Link> <Ago at={p.finished} />
              </>
            ) : (
              'none before this run'
            )}
          </p>
        </div>
      )}
      <div className="cd-links small">
        {e && (
          <button className="link-btn" onClick={() => onJump(e.start_line + 1)}>
            Jump to line in output
          </button>
        )}
        <button className="link-btn" onClick={() => onHost(h.name)}>
          Only this host in output
        </button>
        {h.id && <Link to={`/hosts/${h.id}`}>Open host →</Link>}
      </div>
    </aside>
  )
}

/** Diff mode: each file the task changed on this host, before and after. */
function Diffs({ diffs, onJump }: { diffs: TaskDiff[]; onJump: () => void }) {
  if (!diffs.length) return null
  return (
    <div className="cd-diffs">
      {diffs.map((d, i) => (
        <div key={i}>
          <div className="mono small cd-diff-path" title={d.path}>
            {d.item !== undefined && <span className="faint">{d.item} · </span>}
            {d.path || 'Changes'}
          </div>
          {d.prepared ? (
            <div className="diff" role="table" aria-label={`Changes to ${d.path || 'the host'}`}>
              {d.prepared.replace(/\n$/, '').split('\n').map((l, j) => {
                const k = unifiedKind(l)
                return (
                  <div key={j} role="row" className={k === 'add' || k === 'del' ? k : ''}>
                    <span>{l}</span>
                  </div>
                )
              })}
            </div>
          ) : tooLargeToCompare(d.before, d.after) ? (
            <p className="small muted" style={{ margin: '6px 0 0' }}>
              Too large to compare here.{' '}
              <button className="link-btn" onClick={onJump}>
                See the diff in the output
              </button>
            </p>
          ) : (
            <div className="diff" role="table" aria-label={`Diff of ${d.path || 'the change'}`}>
              {hunks(diffLines(d.before, d.after)).map((l, j) =>
                l.kind === 'skip' ? (
                  <div key={j} role="row" className="skip">
                    <span>⋯ {plural(l.n, 'unchanged line')}</span>
                  </div>
                ) : (
                  <div key={j} role="row" className={l.kind === 'ctx' ? '' : l.kind}>
                    <span className="n">{l.a ?? ''}</span>
                    <span className="n">{l.b ?? ''}</span>
                    <span className="m">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span>
                    <span>{l.text}</span>
                  </div>
                ),
              )}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
