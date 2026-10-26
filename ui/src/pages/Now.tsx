import { useMemo } from 'react'
import { Link } from 'react-router'
import { useList } from '../api/hooks'
import type { Base, Job } from '../api/types'
import { useOrg } from '../app/context'
import { Ago, Elapsed, Empty, Panel, Status, StatusDot, Upcoming } from '../components/ui'
import { jobPath, jobsNewestFirst, statusKind, templatePath, typeLabel } from '../lib/format'

export function Now() {
  const { scope } = useOrg()
  const since = useMemo(() => new Date(Date.now() - 24 * 3600_000).toISOString(), [])
  const active = useList<Job>('unified_jobs/', { status__in: 'pending,waiting,running', order_by: jobsNewestFirst, page_size: 8, not__type: 'workflow_approval', ...scope })
  const groups = useList<Base>('instance_groups/', { order_by: 'name', page_size: 20 })
  const day = useList<Job>('unified_jobs/', { finished__gt: since, order_by: '-finished', page_size: 200, type__in: 'job,workflow_job', ...scope })
  const recent = useList<Job>('unified_jobs/', { order_by: jobsNewestFirst, page_size: 12, not__type: 'workflow_approval', ...scope })
  const approvals = useList<Base>('workflow_approvals/', { status: 'pending', order_by: '-id', page_size: 5 })
  const upcoming = useList<Base>('schedules/', { enabled: true, next_run__isnull: false, order_by: 'next_run', page_size: 6 })

  const failing = useMemo(() => {
    const by = new Map<string, { id: number; type: string; name: string; total: number; failed: number; lastFailed?: Job }>()
    for (const j of day.data?.results ?? []) {
      if (!j.unified_job_template) continue
      const key = String(j.unified_job_template)
      const t = j.summary_fields.unified_job_template
      const e = by.get(key) ?? { id: j.unified_job_template, type: t?.unified_job_type === 'workflow_job' ? 'workflow_job_template' : 'job_template', name: j.name, total: 0, failed: 0 }
      e.total++
      const k = statusKind(j.status)
      if (k === 'fail' || k === 'error') {
        e.failed++
        e.lastFailed ??= j
      }
      by.set(key, e)
    }
    return [...by.values()].filter((e) => e.failed > 0).sort((a, b) => b.failed / b.total - a.failed / a.total)
  }, [day.data])

  return (
    <div className="page ground">
      <header className="page-head" style={{ paddingBottom: 0, background: 'transparent' }}>
        <h1>Now</h1>
      </header>
      <div className="page-body">
        <div className="cards">
          <Panel title="Running and queued" actions={<Link to="/jobs?q=status:running" className="small">All</Link>}>
            {active.data?.results.length ? (
              <ul className="list-plain small">
                {active.data.results.map((j) => (
                  <li key={j.id}>
                    <Link to={jobPath(j)} className="row ellipsis" style={{ gap: 6, color: 'var(--ink)' }}>
                      <StatusDot status={j.status} />
                      <span className="ellipsis">{j.name}</span>
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
          <Panel title="Capacity" actions={<Link to="/instances" className="small">Instances</Link>}>
            <div className="stack small" style={{ gap: 10 }}>
              {(groups.data?.results ?? []).map((g) => {
                const used = g.capacity ? Math.round((100 * (g.consumed_capacity ?? 0)) / g.capacity) : 0
                return (
                  <div key={g.id}>
                    <div className="row">
                      <span className="grow ellipsis">
                        {g.name} <span className="faint">{g.is_container_group ? '(container group)' : `(${g.instances} node${g.instances === 1 ? '' : 's'})`}</span>
                      </span>
                      <span className="mono">{used}%</span>
                    </div>
                    <div className={`meter ${used > 80 ? 'hot' : ''}`} role="meter" aria-valuenow={used} aria-valuemin={0} aria-valuemax={100} aria-label={`${g.name} capacity used`}>
                      <div style={{ width: `${used}%` }} />
                    </div>
                  </div>
                )
              })}
            </div>
          </Panel>
          <Panel title="Failing in the last 24 hours" actions={<Link to="/jobs?q=status:failed" className="small">Failed jobs</Link>}>
            {failing.length ? (
              <ul className="list-plain small">
                {failing.slice(0, 6).map((f) => (
                  <li key={f.id}>
                    <Link to={f.lastFailed ? jobPath(f.lastFailed) : templatePath(f)} className="ellipsis" style={{ color: 'var(--ink)' }}>
                      {f.name}
                    </Link>
                    <span className="nowrap fail-text">
                      {f.failed} of {f.total}
                    </span>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="muted small" style={{ margin: 0 }}>
                {day.isPending ? 'Loading…' : 'No failures in the last 24 hours.'}
              </p>
            )}
          </Panel>
        </div>
        <div className="split">
          <div className="wide">
            <Panel title="Recent activity" pad={false} actions={<Link to="/jobs" className="small">All jobs</Link>}>
              {recent.data?.results.length ? (
                <table className="data">
                  <tbody>
                    {recent.data.results.map((j) => (
                      <tr key={j.id}>
                        <td style={{ width: 120 }}>
                          <Status status={j.status} />
                        </td>
                        <td>
                          <Link to={jobPath(j)} className="cell-title">
                            {j.name}
                          </Link>
                          <span className="faint small"> · {typeLabel(j.type)}</span>
                        </td>
                        <td className="mono small faint">#{j.id}</td>
                        <td className="small nowrap">{j.finished ? <Ago at={j.finished} /> : j.started ? <Elapsed job={j} /> : <Ago at={j.created} />}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <Empty title="No jobs yet">Launch a template to see it here.</Empty>
              )}
            </Panel>
          </div>
          <div className="narrow stack" style={{ gap: 16 }}>
            <Panel title="Waiting on approval" actions={<Link to="/approvals" className="small">Approvals</Link>}>
              {approvals.data?.results.length ? (
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
              ) : (
                <p className="muted small" style={{ margin: 0 }}>
                  Nothing waits for a decision.
                </p>
              )}
            </Panel>
            <Panel title="Coming up" actions={<Link to="/schedules" className="small">Schedules</Link>}>
              {upcoming.data?.results.length ? (
                <ul className="list-plain small">
                  {upcoming.data.results.map((s) => (
                    <li key={s.id}>
                      <span className="ellipsis">
                        {s.summary_fields.unified_job_template?.name ?? s.name}
                        <span className="faint"> · {s.name}</span>
                      </span>
                      <span className="nowrap">
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
    </div>
  )
}
