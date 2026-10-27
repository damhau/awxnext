import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { api, type Page } from '../api/client'
import { useList } from '../api/hooks'
import type { Base, Job, JobEvent } from '../api/types'
import { useOrg } from '../app/context'
import { Ago, Elapsed, Panel, StatusDot, Upcoming, useTick } from '../components/ui'
import { fleetRows, hourBuckets, type FleetCell, type FleetRow } from '../lib/fleet'
import { jobPath, jobsNewestFirst, plural, templatePath, typeLabel } from '../lib/format'
import { eventsPath, firstFailure, toGrid } from './jobs/events'

const ROWS = 12
const dayFmt = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: '2-digit', month: '2-digit', year: 'numeric' })
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', timeZoneName: 'short' })
const hourLabel = (t: number) => `${String(new Date(t).getHours()).padStart(2, '0')}h`
const hourRange = (t: number) => `${String(new Date(t).getHours()).padStart(2, '0')}:00–${String(new Date(t + 3600_000).getHours()).padStart(2, '0')}:00`

export function Now() {
  const { scope } = useOrg()
  useTick(30_000)
  const now = Date.now()
  const buckets = useMemo(() => hourBuckets(now, 24), [Math.floor(now / 3600_000)]) // eslint-disable-line react-hooks/exhaustive-deps
  const since = new Date(buckets[0]).toISOString()
  const last24h = { created__gt: since, order_by: '-id', page_size: 200, ...scope }
  const jobs = useList<Job>('jobs/', last24h)
  const workflows = useList<Job>('workflow_jobs/', last24h)
  const failedRuns = useList<Job>('unified_jobs/', { created__gt: since, status__in: 'failed,error', type__in: 'job,workflow_job', page_size: 1, ...scope })
  const failures = useList<Job>('unified_jobs/', { created__gt: since, status__in: 'failed,error', not__type: 'workflow_approval', order_by: '-id', page_size: 5, ...scope })
  const active = useList<Job>('unified_jobs/', { status__in: 'pending,waiting,running', order_by: jobsNewestFirst, page_size: 8, not__type: 'workflow_approval', ...scope })
  const approvals = useList<Base>('workflow_approvals/', { status: 'pending', order_by: '-id', page_size: 5 })
  const upcoming = useList<Base>('schedules/', { enabled: true, next_run__isnull: false, order_by: 'next_run', page_size: 6 })

  const rows = useMemo(() => fleetRows([...(jobs.data?.results ?? []), ...(workflows.data?.results ?? [])], buckets), [jobs.data, workflows.data, buckets])
  const runs = (jobs.data?.count ?? 0) + (workflows.data?.count ?? 0)
  const failed = failedRuns.data?.count ?? 0
  const running = (active.data?.results ?? []).filter((j) => j.status === 'running').length
  const truncated = !!(jobs.data?.next || workflows.data?.next)

  return (
    <div className="page ground now">
      <header className="page-head">
        <h1>Now</h1>
        <p className="now-line mono">
          {dayFmt.format(now)} · {timeFmt.format(now)} · <strong>{plural(runs, 'run')}</strong> in 24 h ·{' '}
          <strong className={failed ? 'fail-text' : undefined}>{failed} failed</strong> · {running} running
        </p>
      </header>
      <div className="page-body">
        <Fleet rows={rows} buckets={buckets} loading={jobs.isPending || workflows.isPending} truncated={truncated} />
        <div className="now-grid">
          <Panel
            title={
              <h2>
                Running now <span className="mono small muted">{plural(active.data?.count ?? 0, 'job')}</span>
              </h2>
            }
            actions={<Link to="/jobs?q=status:running" className="small">All</Link>}
          >
            {active.data?.results.length ? (
              <ul className="list-plain small">
                {active.data.results.map((j) => (
                  <li key={j.id}>
                    <Link to={jobPath(j)} className="row ellipsis" style={{ gap: 8, color: 'var(--ink)' }}>
                      <StatusDot status={j.status} />
                      <span className="ellipsis">{j.name}</span>
                      <span className="mono faint">#{j.id}</span>
                    </Link>
                    {j.status === 'running' ? <Elapsed job={j} /> : <span className="faint">{j.status}</span>}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="muted small" style={{ margin: 0 }}>
                Nothing is running.
              </p>
            )}
          </Panel>
          <Panel
            title={
              <h2>
                Needs attention{' '}
                {!!failures.data?.count && <span className="mono small muted">{plural(failures.data.count, 'failure')} in 24 h</span>}
              </h2>
            }
            actions={<Link to="/jobs?q=status:failed" className="small">Failed jobs</Link>}
          >
            {failures.data?.results.length ? (
              <div className="stack" style={{ gap: 10 }}>
                {failures.data.results.map((j) => (
                  <Attention key={`${j.type}:${j.id}`} job={j} />
                ))}
              </div>
            ) : (
              <p className="muted small" style={{ margin: 0 }}>
                {failures.isPending ? 'Loading…' : 'No failures in the last 24 hours.'}
              </p>
            )}
          </Panel>
        </div>
        <div className="now-grid">
          {!!approvals.data?.results.length && (
            <Panel title="Waiting on approval" actions={<Link to="/approvals" className="small">Approvals</Link>}>
              <ul className="list-plain small">
                {approvals.data.results.map((a) => (
                  <li key={a.id}>
                    <Link to="/approvals" className="ellipsis">
                      {a.name}
                    </Link>
                    <Ago at={a.created} />
                  </li>
                ))}
              </ul>
            </Panel>
          )}
          <Panel title="Coming up" actions={<Link to="/schedules" className="small">Schedules</Link>}>
            {upcoming.data?.results.length ? (
              <ul className="list-plain small">
                {upcoming.data.results.map((s) => (
                  <li key={s.id}>
                    <span className="ellipsis">
                      {s.summary_fields.unified_job_template?.name ?? s.name}
                      <span className="faint"> · {s.name}</span>
                    </span>
                    <span className="nowrap mono">
                      <Upcoming at={s.next_run} />
                    </span>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="muted small" style={{ margin: 0 }}>
                No scheduled runs.
              </p>
            )}
          </Panel>
        </div>
      </div>
    </div>
  )
}

function Fleet({ rows, buckets, loading, truncated }: { rows: FleetRow[]; buckets: number[]; loading: boolean; truncated: boolean }) {
  const [all, setAll] = useState(false)
  const shown = all ? rows : rows.slice(0, ROWS)
  return (
    <section className="panel" aria-labelledby="fleet-h">
      <div className="panel-head">
        <h2 id="fleet-h">Fleet · templates × hours</h2>
        <span className="small muted">Each cell is the worst result in that hour. Click a cell to open that run.</span>
        <div className="legend" style={{ marginLeft: 'auto' }}>
          <span>
            <i className="fcell ok" />
            ok
          </span>
          <span>
            <i className="fcell fail" />
            failed
          </span>
          <span>
            <i className="fcell run" />
            running
          </span>
          <span>
            <i className="fcell cancel" />
            canceled
          </span>
          <span>
            <i className="fcell idle" />
            no run
          </span>
        </div>
      </div>
      <div className="panel-body">
        {!rows.length ? (
          <p className="muted small" style={{ margin: 0 }}>
            {loading ? 'Loading…' : 'No template ran in the last 24 hours.'}
          </p>
        ) : (
          <div className="fleet">
            <div className="fleet-row fleet-hours mono" aria-hidden="true">
              <span />
              {buckets.map((b, i) => (
                <span key={b}>{i % 3 === 0 ? hourLabel(b) : ''}</span>
              ))}
              <span className="end">runs · fail</span>
            </div>
            {shown.map((r) => (
              <div key={r.key} className="fleet-row">
                {r.id ? (
                  <Link to={templatePath({ id: r.id, type: r.type })} className="fleet-name">
                    <b>{r.name}</b> <span className="mono">{r.inventory}</span>
                  </Link>
                ) : (
                  <span className="fleet-name">
                    <b>{r.name}</b> <span className="mono">{r.inventory}</span>
                  </span>
                )}
                {r.cells.map((c) => (
                  <FleetSquare key={c.start} row={r} cell={c} />
                ))}
                <span className="end mono">
                  {r.runs} · <span className={r.fails ? 'fail-text' : 'faint'}>{r.fails}</span>
                </span>
              </div>
            ))}
          </div>
        )}
        {(rows.length > ROWS || truncated) && (
          <div className="row small muted" style={{ marginTop: 12 }}>
            {rows.length > ROWS && (
              <button className="link-btn" onClick={() => setAll(!all)}>
                {all ? `Show the ${ROWS} most relevant` : `Show all ${rows.length} templates`}
              </button>
            )}
            {truncated && <span>Only the latest 200 runs are shown; older hours may be incomplete.</span>}
          </div>
        )}
      </div>
    </section>
  )
}

function FleetSquare({ row, cell }: { row: FleetRow; cell: FleetCell }) {
  if (!cell.job) return <span className="fcell idle" aria-hidden="true" />
  const what = cell.fails ? `${plural(cell.runs, 'run')}, ${cell.fails} failed` : plural(cell.runs, 'run')
  const label = `${row.name} · ${hourRange(cell.start)} · ${what}`
  return <Link to={jobPath(cell.job)} className={`fcell ${cell.state}`} aria-label={label} title={label} />
}

const GRID_EVENTS = 'playbook_on_task_start,playbook_on_handler_task_start,runner_on_ok,runner_on_failed,runner_on_unreachable,runner_on_skipped'
const MINI_HOSTS = 6
const MINI_TASKS = 24

/** A failed run: where it failed, why, and its hosts x tasks matrix when the job is small. */
function Attention({ job }: { job: Job }) {
  const playbook = job.type === 'job'
  const grid = useQuery({
    queryKey: ['job_events', job.id, 'grid'],
    queryFn: ({ signal }) => api.get<Page<JobEvent>>(eventsPath('job', job.id), { order_by: 'counter', page_size: 500, event__in: GRID_EVENTS }, signal),
    enabled: playbook,
    staleTime: Infinity,
  })
  const complete = !!grid.data && !grid.data.next
  // Too many events for one page: ask for the failures alone.
  const failed = useQuery({
    queryKey: ['job_events', job.id, 'failed'],
    queryFn: ({ signal }) => api.get<Page<JobEvent>>(eventsPath('job', job.id), { order_by: 'counter', page_size: 50, event__in: 'runner_on_failed,runner_on_unreachable' }, signal),
    enabled: playbook && !!grid.data?.next,
    staleTime: Infinity,
  })
  const events = complete ? grid.data!.results : (failed.data?.results ?? [])
  const g = useMemo(() => (complete ? toGrid(events) : null), [complete, events])
  const f = useMemo(() => firstFailure(events), [events])
  const taskNo = g && f ? g.tasks.findIndex((t) => t.name === f.task && t.play === f.play) + 1 : 0
  const others = g && f ? g.hosts.filter((h) => !h.bad).length : 0
  const mini = g && g.tasks.length <= MINI_TASKS ? [...g.hosts].sort((a, b) => Number(b.bad) - Number(a.bad)).slice(0, MINI_HOSTS) : null
  const reason = f ? f.message.split('\n')[0] : job.job_explanation || `${typeLabel(job.type)} failed`

  return (
    <Link to={jobPath(job)} className="attention">
      {mini && (
        <span className="mini" role="img" aria-label={`${plural(g!.hosts.length, 'host')} by ${plural(g!.tasks.length, 'task')}, failed on ${plural(g!.hosts.length - others, 'host')}`}>
          {mini.map((h) => (
            <span key={h.name} className="mini-row">
              {h.cells.map((c, i) => (
                <i key={i} className={`cell ${c}`} />
              ))}
            </span>
          ))}
        </span>
      )}
      <span className="attention-text">
        <span>
          <b>{job.name}</b>{' '}
          <span className="mono small muted">
            #{job.id} · <Ago at={job.finished ?? job.created} />
          </span>
        </span>
        {f && (
          <span className="mono small fail-text ellipsis">
            {f.hosts[0]}
            {f.hosts.length > 1 ? ` +${f.hosts.length - 1}` : ''}
            {taskNo ? ` · task ${taskNo}` : ''} · {f.task}
          </span>
        )}
        <span className="small muted ellipsis">
          {reason}
          {others > 0 && ` — ${plural(others, 'other host')} fine`}
        </span>
      </span>
    </Link>
  )
}
