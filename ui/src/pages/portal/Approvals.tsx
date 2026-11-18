// Approvals: the requests waiting for the user, each with the rule that
// applies, the requester's reason, the answers the rule reads, the quota and
// the timeout. Approve, or deny with a comment; on a phone, from a bar at
// the bottom of the screen.
import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import { useSession } from '../../app/context'
import { Avatar } from '../../components/ui'
import { ago, dateTimeText } from '../../lib/format'
import { tr } from '../../lib/i18n'
import { namesText } from '../../lib/portal'
import { afterAnswer, answerText, approvalShort, approvalTitle, decision, firstName, ruleReads, timeoutNote, type PortalApproval } from '../../lib/portalApprovals'
import { holderName, quotaSlots } from '../../lib/quotas'
import { Cell } from './parts'

type Queue = { count: number; results: PortalApproval[] }

function Why({ a }: { a: PortalApproval }) {
  const names = namesText(a.approvers.map((p) => p.name))
  return (
    <div className="portal-next-box waiting">
      <span className="portal-strong">
        {a.kind === 'quota' ? tr('Over quota: it needs approval') : a.kind === 'freeze' ? tr('Asked during {rule}: it needs approval', { rule: a.rule }) : tr('{rule}: it needs approval', { rule: a.rule })}
      </span>
      {a.condition && (
        <span className="small">
          {tr('Rule on {item}:', { item: a.request.item.name })} <code className="portal-code">{a.condition}</code>
        </span>
      )}
      <span className="small">
        {a.required > 1 ? tr('{names} approve it: {n} of them must.', { names, n: a.required }) : tr('{names} approves it.', { names })}
        {a.fallback && ' ' + tr('Nobody else could, so the organization’s admins do.')}
      </span>
    </div>
  )
}

function Asked({ a }: { a: PortalApproval }) {
  const r = a.request
  const first = firstName(r.requester?.name ?? '')
  const reads = ruleReads(a.condition)
  const rows: { k: string; v: string; hot?: boolean }[] = r.answers.map((x) => ({ k: x.question, v: answerText(x.value), hot: reads.has(x.variable) }))
  const dep = r.deployment
  if (r.kind === 'new' && r.lifetime_days != null) rows.push({ k: tr('Keep it for'), v: tr('{n} days', { n: r.lifetime_days }), hot: reads.has('lifetime_days') })
  if (dep) {
    rows.unshift({ k: tr('Deployment'), v: dep.name })
    if (dep.expires_at) rows.push({ k: tr('Expires now'), v: dateTimeText(dep.expires_at) })
    if (r.kind === 'extend' && dep.expires_at && r.lifetime_days != null) {
      rows.push({ k: tr('Would expire'), v: dateTimeText(new Date(new Date(dep.expires_at).getTime() + r.lifetime_days * 86_400_000)), hot: true })
    }
    if (r.kind === 'extend') {
      rows.push({ k: tr('Extended'), v: dep.max_extensions != null ? tr('{n} of {max} times', { n: dep.extensions, max: dep.max_extensions }) : String(dep.extensions) })
      if (dep.until) rows.push({ k: tr('Kept at most until'), v: dateTimeText(dep.until) })
    }
  }
  if (!rows.length) return null
  return (
    <div className="stack" style={{ gap: 8 }}>
      <h3 className="portal-eyebrow">
        {r.kind === 'extend' ? tr('The extension') : first ? tr('What {who} asked for', { who: first }) : tr('What was asked for')}
      </h3>
      <dl className="portal-dl portal-answers">
        {rows.map((x, i) => (
          <div key={i} data-hot={x.hot ? 'yes' : undefined}>
            <dt>{x.k}</dt>
            <dd>{x.v}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

function Quota({ a }: { a: PortalApproval }) {
  const q = a.quota
  if (!q || a.request.kind !== 'new' || a.status !== 'pending') return null
  const who = holderName(q)
  const slots = q.max_deployments != null ? quotaSlots(Math.max(0, q.deployments - 1), q.max_deployments, 1) : null
  return (
    <div className="stack" style={{ gap: 8 }}>
      <h3 className="portal-eyebrow">{tr('Quota of {who}', { who })}</h3>
      {slots && (
        <span className="row" role="img" style={{ gap: 3 }} aria-label={tr('{who}: {used} used, this request, {max} in all', { who, used: q.deployments - 1, max: q.max_deployments })}>
          {slots.map((s, i) => (
            <Cell key={i} state={s} />
          ))}
        </span>
      )}
      {q.max_deployments != null && <span className="small">{tr('This one makes {n} of the {max} deployments {who} can keep.', { n: q.deployments, max: q.max_deployments, who })}</span>}
      {q.max_requests_per_day != null && <span className="small">{tr('This one is {n} of the {max} requests {who} can make in 24 hours.', { n: q.requests, max: q.max_requests_per_day, who })}</span>}
    </div>
  )
}

function Detail({ id, queue }: { id: number; queue: PortalApproval[] }) {
  const { me } = useSession()
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const q = useObject<PortalApproval>(`portal/approvals/${id}/`)
  const [comment, setComment] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const a = q.data
  if (q.error) return <p className="portal-error">{q.error.message}</p>
  if (!a) return <section className="portal-detail" aria-busy="true" />
  const r = a.request
  const first = firstName(r.requester?.name ?? '')
  const done = decision(a, me.id)
  const at = queue.findIndex((x) => x.id === id)
  const next = queue.find((x) => x.id !== id)
  const answer = async (approve: boolean) => {
    if (busy || (!approve && !comment.trim())) return
    setBusy(true)
    setError('')
    try {
      await api.post(`workflow_approvals/${id}/${approve ? 'approve' : 'deny'}/`, comment.trim() ? { comment: comment.trim() } : {})
      nav(`/portal/approvals/${id}`, { replace: true })
      await invalidate('portal/', 'catalog_requests', 'workflow_approvals')
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <section aria-labelledby="ap-title" className="portal-detail" data-deciding={a.can_answer ? 'yes' : undefined}>
      <div className="portal-back-row">
        <Link to="/portal/approvals" className="portal-back">
          {tr('All approvals')}
        </Link>
        {at >= 0 && <span className="portal-muted small">{tr('{n} of {total} waiting', { n: at + 1, total: queue.length })}</span>}
      </div>
      <div className="row" style={{ gap: 14, alignItems: 'flex-start' }}>
        <Avatar name={r.requester?.name ?? '?'} />
        <div className="stack" style={{ gap: 2, minWidth: 0 }}>
          <span className="mono portal-muted">{tr('Request {id}', { id: r.id })}</span>
          <h2 id="ap-title" className="portal-detail-title">{approvalTitle(a)}</h2>
          <span className="portal-muted small">
            {[r.team?.name, tr('sent {date}, {ago}', { date: dateTimeText(r.created), ago: ago(r.created) })].filter(Boolean).join(' · ')}
          </span>
        </div>
      </div>
      <Why a={a} />
      {r.reason && (
        <figure className="portal-reason">
          <figcaption className="portal-eyebrow">{first ? tr('{who} wrote', { who: first }) : tr('The reason')}</figcaption>
          <blockquote>{r.reason}</blockquote>
        </figure>
      )}
      <Asked a={a} />
      <Quota a={a} />
      {a.own && a.status === 'pending' && (
        <div className="portal-next-box denied">
          <span className="portal-strong">{tr('This is your own request')}</span>
          <span>{tr('{names} answers it: nobody approves their own requests.', { names: namesText(a.approvers.map((p) => p.name)) })}</span>
          <Link to={`/portal/requests/${r.id}`}>{tr('Follow it under My requests')}</Link>
        </div>
      )}
      {!a.own && !a.can_answer && !done && a.cannot_answer && (
        <div className="portal-next-box denied">
          <span>{a.cannot_answer}</span>
        </div>
      )}
      {a.can_answer && (
        <div className="portal-decide">
          <p className="portal-muted small">{timeoutNote(a, dateTimeText)}</p>
          <div className="field">
            <label className="label" htmlFor="ap-comment">
              {first ? tr('Comment for {who}', { who: first }) : tr('Comment')}
            </label>
            <textarea
              id="ap-comment"
              className="textarea"
              rows={3}
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder={tr('Needed to deny, optional to approve')}
            />
          </div>
          {error && (
            <p className="portal-note fail" role="alert">
              {error}
            </p>
          )}
          <div className="portal-decide-bar">
            <button type="button" className="portal-btn" disabled={busy || !comment.trim()} onClick={() => answer(false)} title={!comment.trim() ? tr('Say why in the comment to deny it.') : undefined}>
              {tr('Deny')}
            </button>
            <button type="button" className="portal-btn approve" disabled={busy} onClick={() => answer(true)}>
              {tr('Approve')}
            </button>
          </div>
        </div>
      )}
      {done && (
        <div role="status" className={`portal-next-box ${done.tone}`}>
          <span className="portal-strong">
            {done.title}
            {done.at && ' · ' + dateTimeText(done.at)}
          </span>
          <span>{done.mine ? afterAnswer(a, done.tone === 'ok') : done.comment}</span>
          {next && (
            <Link to={`/portal/approvals/${next.id}`} className="portal-btn">
              {tr('Next: {what}', { what: approvalShort(next) })}
            </Link>
          )}
        </div>
      )}
    </section>
  )
}

export function Approvals() {
  const { id } = useParams()
  const queue = useObject<Queue>('portal/approvals/', { refetchInterval: 30_000 })
  const rows = queue.data?.results ?? []
  const selected = id ? Number(id) : rows[0]?.id
  return (
    <div className="portal-page" data-detail={id ? 'yes' : 'no'}>
      <div className="stack portal-intro" style={{ gap: 4 }}>
        <h1>{tr('Approvals')}</h1>
        <p className="portal-muted">{tr('The requests waiting for your answer, oldest first. The first answer counts unless a rule needs more.')}</p>
      </div>
      {queue.isPending ? (
        <div aria-busy="true" />
      ) : queue.error ? (
        <p className="portal-error">{queue.error.message}</p>
      ) : rows.length === 0 && !id ? (
        <div className="portal-empty">
          <h3>{tr('Nothing waits for you')}</h3>
          <p>{tr('When a request needs your approval, it shows here.')}</p>
        </div>
      ) : (
        <div className="portal-requests" data-detail={id ? 'yes' : 'no'}>
          <section aria-label={tr('Waiting for you')} className="portal-request-list">
            <ul>
              {rows.map((a) => (
                <li key={a.id}>
                  <Link to={`/portal/approvals/${a.id}`} aria-current={a.id === selected ? 'true' : undefined} className="portal-request-row">
                    <Avatar name={a.request.requester?.name ?? '?'} />
                    <span className="stack" style={{ gap: 2, minWidth: 0, flex: 1 }}>
                      <span className="row" style={{ gap: 8, justifyContent: 'space-between' }}>
                        <span className="portal-strong portal-ellipsis">{a.request.requester?.name ?? tr('Someone')}</span>
                        <span className="portal-muted small nowrap">{ago(a.created)}</span>
                      </span>
                      <span className="small portal-ellipsis">{approvalShort(a)}</span>
                      <span className="small portal-tone-waiting">{tr('Waiting for you')}</span>
                    </span>
                  </Link>
                </li>
              ))}
              {rows.length === 0 && <li className="portal-muted small portal-none">{tr('Nothing else waits for you.')}</li>}
            </ul>
          </section>
          {selected ? <Detail key={selected} id={selected} queue={rows} /> : null}
        </div>
      )}
    </div>
  )
}
