import { useEffect, useState, type ReactNode } from 'react'
import { Link, NavLink } from 'react-router'
import type { RecentJob } from '../api/types'
import { absolute, ago, avatarColor, duration, elapsed, initials, statusKind, statusLabel, upcoming } from '../lib/format'

export function StatusDot({ status }: { status: string | null | undefined }) {
  return <span aria-hidden="true" className={`sdot ${statusKind(status)}`} />
}

export function Status({ status, children }: { status: string | null | undefined; children?: ReactNode }) {
  return (
    <span className="status">
      <StatusDot status={status} />
      <span>{children ?? statusLabel(status)}</span>
    </span>
  )
}

export function StatusBadge({ status }: { status: string | null | undefined }) {
  return (
    <span className={`badge ${statusKind(status)}`}>
      <StatusDot status={status} />
      {statusLabel(status)}
    </span>
  )
}

/** Bars for the most recent runs, oldest first. Height and colour both encode status. */
export function RunBars({ jobs, slots = 12 }: { jobs: RecentJob[] | undefined; slots?: number }) {
  const list = [...(jobs ?? [])].slice(0, slots).reverse()
  const failed = list.filter((j) => statusKind(j.status) === 'fail' || statusKind(j.status) === 'error').length
  const label = list.length ? `Last ${list.length} runs: ${list.length - failed} not failed, ${failed} failed` : 'No runs yet'
  const pad = Math.max(0, Math.min(slots, 12) - list.length)
  return (
    <div className="runbars" role="img" aria-label={label} title={label}>
      {Array.from({ length: pad }, (_, i) => (
        <span key={'p' + i} className="slot" />
      ))}
      {list.map((j) => (
        <Link key={j.id} to={`/jobs/${j.type}/${j.id}`} title={`#${j.id} ${statusLabel(j.status)} · ${ago(j.finished)}`}>
          <span className={statusKind(j.status)} />
        </Link>
      ))}
    </div>
  )
}

/** Relative time that refreshes itself, with the absolute time as tooltip. */
export function Ago({ at, prefix }: { at: string | null | undefined; prefix?: string }) {
  useTick(30_000)
  if (!at) return <span className="faint">—</span>
  return (
    <time dateTime={at} title={absolute(at)}>
      {prefix}
      {ago(at)}
    </time>
  )
}

export function Upcoming({ at }: { at: string | null | undefined }) {
  if (!at) return <span className="faint">—</span>
  return (
    <time dateTime={at} title={absolute(at)}>
      {upcoming(at)}
    </time>
  )
}

/** Duration that keeps counting while the job runs. */
export function Elapsed({ job }: { job: { started?: string | null; finished?: string | null; elapsed?: number; status?: string } }) {
  const running = !job.finished && !!job.started
  useTick(running ? 1000 : 0)
  return <span className="mono">{duration(elapsed(job))}</span>
}

export function useTick(ms: number) {
  const [, set] = useState(0)
  useEffect(() => {
    if (!ms) return
    const t = window.setInterval(() => set((x) => x + 1), ms)
    return () => window.clearInterval(t)
  }, [ms])
}

/** The logo is a run: three hosts by three tasks, one change, two live cells. */
export function BrandMark() {
  const cells = ['live', '', '', '', 'changed', '', '', '', 'live']
  return (
    <span className="brand-mark" aria-hidden="true">
      {cells.map((c, i) => (
        <i key={i} className={c} />
      ))}
    </span>
  )
}

export function Avatar({ name, size }: { name: string; size?: 'sm' }) {
  return (
    <span className={`avatar ${size ?? ''}`} style={{ background: avatarColor(name) }} aria-hidden="true">
      {initials(name)}
    </span>
  )
}

export function Crumbs({ items }: { items: { label: string; to?: string }[] }) {
  return (
    <nav className="crumbs" aria-label="Breadcrumb">
      {items.map((it, i) => (
        <span key={i} className="row" style={{ gap: 6 }}>
          {i > 0 && <span aria-hidden="true">/</span>}
          {it.to ? <Link to={it.to}>{it.label}</Link> : <span className="here">{it.label}</span>}
        </span>
      ))}
    </nav>
  )
}

export function PageHead({
  crumbs,
  title,
  badge,
  subtitle,
  actions,
  tabs,
  children,
  bordered = true,
}: {
  crumbs?: { label: string; to?: string }[]
  title: ReactNode
  badge?: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  tabs?: ReactNode
  children?: ReactNode
  bordered?: boolean
}) {
  return (
    <header className={`page-head ${bordered ? 'bordered' : ''} ${tabs ? 'with-tabs' : ''}`} style={tabs ? undefined : { paddingBottom: bordered ? 14 : 0 }}>
      {crumbs && <Crumbs items={crumbs} />}
      <div className="title-row center">
        <div className="grow" style={{ flex: '1 1 320px' }}>
          <div className="row wrap" style={{ gap: 12 }}>
            <h1>{title}</h1>
            {badge}
          </div>
          {subtitle && <p className="subtitle">{subtitle}</p>}
        </div>
        {actions && <div className="actions">{actions}</div>}
      </div>
      {children}
      {tabs && (
        <nav className="tabs" role="tablist">
          {tabs}
        </nav>
      )}
    </header>
  )
}

export function Tab({ to, children, end, n }: { to: string; children: ReactNode; end?: boolean; n?: number | string }) {
  return (
    <NavLink to={to} end={end} role="tab" className={({ isActive }) => `tab ${isActive ? 'active' : ''}`}>
      {children}
      {n !== undefined && <span className="n">{n}</span>}
    </NavLink>
  )
}

export function Empty({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      <h3>{title}</h3>
      {children && <div>{children}</div>}
      {action && <div style={{ marginTop: 12 }}>{action}</div>}
    </div>
  )
}

export function Loading({ rows = 6 }: { rows?: number }) {
  return (
    <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 12 }} aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="skeleton" style={{ width: `${60 + ((i * 37) % 35)}%` }} />
      ))}
    </div>
  )
}

export function ErrorNote({ error }: { error: unknown }) {
  if (!error) return null
  return <div className="form-error">{error instanceof Error ? error.message : String(error)}</div>
}

export function KV({ items }: { items: [ReactNode, ReactNode][] }) {
  return (
    <dl className="kv">
      {items
        .filter(([, v]) => v !== undefined && v !== null && v !== false)
        .map(([k, v], i) => (
          <div key={i} style={{ display: 'contents' }}>
            <dt>{k}</dt>
            <dd>{v === '' ? <span className="faint">—</span> : v}</dd>
          </div>
        ))}
    </dl>
  )
}

export function Panel({ title, actions, children, pad = true, className }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; pad?: boolean; className?: string }) {
  return (
    <section className={`panel ${className ?? ''}`}>
      {(title || actions) && (
        <div className="panel-head">
          {typeof title === 'string' ? <h2>{title}</h2> : title}
          {actions && <div style={{ marginLeft: 'auto' }} className="row">{actions}</div>}
        </div>
      )}
      {pad ? <div className="panel-body">{children}</div> : children}
    </section>
  )
}

/** Shows text, falling back to a muted dash. */
export function Maybe({ v, mono }: { v: ReactNode; mono?: boolean }) {
  if (v === null || v === undefined || v === '') return <span className="faint">—</span>
  return mono ? <span className="mono">{v}</span> : <>{v}</>
}
