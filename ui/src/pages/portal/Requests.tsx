// My requests: each request's steps, the playbook's plays as progress, and
// what happens next while it waits, once denied, or when it failed.
import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { useList, useObject } from '../../api/hooks'
import { useSession } from '../../app/context'
import { ago, dateTimeText, jobPath } from '../../lib/format'
import { tr } from '../../lib/i18n'
import { denial, namesText, requestState, requestSteps, unfinished, waitingApprovals, type CatalogRequest, type Progress, type StepState } from '../../lib/portal'
import { Cell, Pill } from './parts'

const live = (status?: string) => (status && unfinished.includes(status) ? 4000 : false)

function listCell(r: CatalogRequest): StepState {
  switch (r.status) {
    case 'awaiting_approval':
      return 'waiting'
    case 'pending':
    case 'running':
      return 'running'
    case 'successful':
      return 'done'
    case 'denied':
      return 'denied'
    case 'canceled':
      return 'canceled'
    default:
      return 'failed'
  }
}

function stepText(key: string, state: StepState, r: CatalogRequest, percent: number | null | undefined): { label: string; sub: string } {
  switch (key) {
    case 'sent':
      return { label: tr('Sent'), sub: dateTimeText(r.created) }
    case 'approval':
      return {
        label: tr('Approval'),
        sub: state === 'skipped' ? tr('Not needed') : state === 'waiting' ? r.approvals.map((a) => a.rule).join(', ') : state === 'denied' ? tr('Denied') : tr('Approved'),
      }
    case 'run':
      return {
        label: tr('Running'),
        sub: state === 'running' ? (percent != null ? tr('About {n}%', { n: percent }) : tr('Under way')) : state === 'failed' ? tr('Failed') : state === 'done' ? tr('Finished') : '',
      }
    default:
      return { label: tr('Done'), sub: state === 'done' ? dateTimeText(r.modified) : '' }
  }
}

function Answers({ r }: { r: CatalogRequest }) {
  const form = useObject<{ questions: { variable: string; question_name: string }[]; fixed_answers: { variable: string; question_name: string }[] }>(
    r.catalog_item ? `catalog_items/${r.catalog_item}/request/` : null,
    { retry: false },
  )
  const names = new Map<string, string>()
  for (const q of [...(form.data?.questions ?? []), ...(form.data?.fixed_answers ?? [])]) names.set(q.variable, q.question_name)
  const rows = Object.entries(r.answers ?? {})
  if (!rows.length && !r.reason) return null
  const shown = (k: string, v: unknown) => {
    const label = r.answer_labels?.[k]
    const x = label ?? v
    if (x === '$encrypted$') return '••••••'
    return Array.isArray(x) ? x.join(', ') : String(x ?? '')
  }
  return (
    <div className="portal-sub">
      <h3>{tr('Your answers')}</h3>
      <dl className="portal-dl">
        {rows.map(([k, v]) => (
          <div key={k}>
            <dt>{names.get(k) || k}</dt>
            <dd>{shown(k, v)}</dd>
          </div>
        ))}
        {r.lifetime_days != null && (
          <div>
            <dt>{tr('Keep it for')}</dt>
            <dd>{tr('{n} days', { n: r.lifetime_days })}</dd>
          </div>
        )}
        {r.reason && (
          <div>
            <dt>{tr('Why')}</dt>
            <dd>{r.reason}</dd>
          </div>
        )}
      </dl>
    </div>
  )
}

function Next({ r }: { r: CatalogRequest }) {
  const again = r.catalog_item ? (
    <Link to={`/portal/items/${r.catalog_item}?from=${r.id}`} className="portal-btn">
      {tr('Ask again, with these answers')}
    </Link>
  ) : null
  if (r.status === 'awaiting_approval') {
    const waiting = waitingApprovals(r)
    return (
      <div className="portal-next-box waiting">
        {waiting.map((a) => (
          <span key={a.id}>
            {tr('Waiting for {names} since {when}.', { names: namesText(a.approvers.map((p) => p.name)), when: dateTimeText(r.created) })}{' '}
            {a.required > 1 && tr('{n} of them must approve.', { n: a.required })}{' '}
            {a.expires && tr('If nobody answers by {date}, the request is denied.', { date: dateTimeText(a.expires) })}
          </span>
        ))}
      </div>
    )
  }
  if (r.status === 'denied') {
    const d = denial(r)
    return (
      <div className="portal-next-box denied">
        {d?.timedOut ? (
          <span className="portal-strong">{tr('Nobody answered in time, so it was denied.')}</span>
        ) : d ? (
          <>
            <span className="portal-strong">{tr('Denied by {who} on {date}', { who: d.by, date: dateTimeText(d.at) })}</span>
            {d.comment && <blockquote>{d.comment}</blockquote>}
          </>
        ) : (
          <span className="portal-strong">{tr('Denied')}</span>
        )}
        {r.explanation && !d?.comment && <span>{r.explanation}</span>}
        {again}
      </div>
    )
  }
  if (r.status === 'failed' || r.status === 'error') {
    return (
      <div className="portal-next-box fail">
        <span className="portal-strong">{tr('It couldn’t finish')}</span>
        <span>{r.explanation || tr('The item’s owners can see what went wrong in its job. Ask them, or try again.')}</span>
        {again}
      </div>
    )
  }
  if (r.status === 'canceled') {
    return (
      <div className="portal-next-box denied">
        <span className="portal-strong">{tr('It was canceled before it finished.')}</span>
        {again}
      </div>
    )
  }
  if (r.status === 'successful') {
    return (
      <div className="portal-next-box ok">
        <span className="portal-strong">{tr('Done on {date}.', { date: dateTimeText(r.modified) })}</span>
        {r.summary_fields.deployment && <span>{tr('It’s yours as {name}.', { name: r.summary_fields.deployment.name })}</span>}
      </div>
    )
  }
  return null
}

function ProgressView({ id, status }: { id: number; status: string }) {
  const p = useObject<Progress>(`catalog_requests/${id}/progress/`, { refetchInterval: live(status) })
  if (!p.data || (!p.data.steps.length && !p.data.job)) return null
  const { steps, percent, job } = p.data
  return (
    <div className="portal-sub">
      <h3>{tr('Progress')}</h3>
      {percent != null && (
        <div className="portal-bar-meter" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} aria-label={tr('Progress')}>
          <span style={{ width: `${percent}%` }} />
        </div>
      )}
      {steps.length > 0 && (
        <ol className="portal-plays">
          {steps.map((s, i) => (
            <li key={i}>
              <Cell state={s.state} size="sm" />
              <span>{s.label}</span>
            </li>
          ))}
        </ol>
      )}
      <p className="portal-muted small">
        {percent != null && status !== 'successful' ? tr('About {n}% done, going by the last successful run.', { n: percent }) + ' ' : ''}
        {job?.can_see && (
          <Link to={jobPath({ id: job.id, type: job.type })}>{tr('Technical details: job {id}', { id: job.id })}</Link>
        )}
      </p>
    </div>
  )
}

function Detail({ id }: { id: number }) {
  const q = useObject<CatalogRequest>(`catalog_requests/${id}/`, { refetchInterval: (query) => live(query.state.data?.status) })
  const r = q.data
  const progress = useObject<Progress>(r?.job ? `catalog_requests/${id}/progress/` : null, { refetchInterval: live(r?.status) })
  if (q.error) return <p className="portal-error">{q.error.message}</p>
  if (!r) return <section className="portal-detail" aria-busy="true" />
  const st = requestState(r.status)
  return (
    <section aria-labelledby="portal-rq" className="portal-detail">
      <Link to="/portal/requests" className="portal-back">
        {tr('All requests')}
      </Link>
      <div className="portal-detail-head">
        <div className="stack" style={{ gap: 4 }}>
          <span className="mono portal-muted">#{r.id}</span>
          <h2 id="portal-rq">{r.item_name}</h2>
          <span className="portal-muted">
            {r.summary_fields.team ? tr('For {team}, sent {when}', { team: r.summary_fields.team.name, when: ago(r.created) }) : tr('Sent {when}', { when: ago(r.created) })}
          </span>
        </div>
        <Pill tone={st.tone}>{st.label}</Pill>
      </div>
      <ol aria-label={tr('Steps')} className="portal-steps">
        {requestSteps(r).map((s) => {
          const t = stepText(s.key, s.state, r, progress.data?.percent)
          return (
            <li key={s.key}>
              <Cell state={s.state} size="lg" />
              <span className="portal-strong">{t.label}</span>
              <span className="portal-muted small">{t.sub}</span>
            </li>
          )
        })}
      </ol>
      <Next r={r} />
      {r.job && <ProgressView id={r.id} status={r.status} />}
      <Answers r={r} />
    </section>
  )
}

export function Requests() {
  const { me } = useSession()
  const { id } = useParams()
  const [open, setOpen] = useState(false)
  const list = useList<CatalogRequest>(
    'catalog_requests/',
    { created_by: me.id, order_by: '-id', page_size: 50 },
    { refetchInterval: (q) => (q.state.data?.results.some((r) => unfinished.includes(r.status)) ? 5000 : false) },
  )
  const all = list.data?.results ?? []
  const rows = open ? all.filter((r) => unfinished.includes(r.status)) : all
  const selected = id ? Number(id) : rows[0]?.id
  return (
    <div className="portal-page">
      <div className="portal-section-head">
        <h1>{tr('My requests')}</h1>
        <Link to="/portal" className="portal-btn">
          {tr('New request')}
        </Link>
      </div>
      {list.isPending ? (
        <div aria-busy="true" />
      ) : all.length === 0 ? (
        <div className="portal-empty">
          <h3>{tr('No request yet')}</h3>
          <p>{tr('What you ask for in the catalog shows here, from the approval to the end.')}</p>
        </div>
      ) : (
        <div className="portal-requests" data-detail={id ? 'yes' : 'no'}>
          <section aria-label={tr('Requests')} className="portal-request-list">
            <div role="group" aria-label={tr('Show')} className="portal-chips">
              <button type="button" className="portal-chip" aria-pressed={!open} onClick={() => setOpen(false)}>
                {tr('All')} <span className="mono">{all.length}</span>
              </button>
              <button type="button" className="portal-chip" aria-pressed={open} onClick={() => setOpen(true)}>
                {tr('Open')} <span className="mono">{all.filter((r) => unfinished.includes(r.status)).length}</span>
              </button>
            </div>
            <ul>
              {rows.map((r) => (
                <li key={r.id}>
                  <Link to={`/portal/requests/${r.id}`} aria-current={r.id === selected ? 'true' : undefined} className="portal-request-row">
                    <Cell state={listCell(r)} />
                    <span className="stack" style={{ gap: 2, minWidth: 0 }}>
                      <span className="row" style={{ gap: 8, justifyContent: 'space-between' }}>
                        <span className="portal-strong portal-ellipsis">{r.item_name}</span>
                        <span className="portal-muted small nowrap">{ago(r.created)}</span>
                      </span>
                      <span className="portal-muted small portal-ellipsis">{r.summary_fields.deployment?.name ?? tr('Request {id}', { id: r.id })}</span>
                      <span className="small">{requestState(r.status).label}</span>
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          </section>
          {selected ? <Detail key={selected} id={selected} /> : null}
        </div>
      )}
    </div>
  )
}
