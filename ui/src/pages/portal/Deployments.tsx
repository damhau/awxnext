// My deployments: each one's lifetime as day cells and its extensions, how
// to connect, its day-2 actions, its history and who can use it, with
// hand-over and deletion, and a teardown that failed with what it left.
import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import { useSession } from '../../app/context'
import { Icon } from '../../components/icons'
import { useToast } from '../../components/overlay'
import {
  addedDays,
  daysUntil,
  deploymentHistory,
  deploymentState,
  expiryText,
  extensionNote,
  leftBehind,
  lifetimeCells,
  usersText,
  type ConnectionEntry,
  type DayCell,
  type Deployment,
  type DeploymentAction,
  type ExtensionOptions,
  type ExtensionStep,
} from '../../lib/deployments'
import { dateOnly, dateTimeText } from '../../lib/format'
import { tr, trn } from '../../lib/i18n'
import { namesText, type CatalogRequest, type GatePreview, type PortalItem } from '../../lib/portal'
import { SurveyFields, validateSurvey, type Question } from '../templates/SurveyEditor'
import { ItemIcon, Pill } from './parts'

type Request = CatalogRequest & { summary_fields: CatalogRequest['summary_fields'] & { created_by?: { id: number; username: string; first_name?: string; last_name?: string } } }

const live = (d?: Deployment) => (d?.status === 'deleting' ? 4000 : false)
const gateNames = (gs: GatePreview[]) => namesText(gs.flatMap((g) => g.approvers.map((p) => p.name)))

function Lifetime({ d, requests }: { d: Deployment; requests: Request[] }) {
  const invalidate = useInvalidate()
  const opts = useObject<ExtensionOptions>(d.summary_fields.user_capabilities?.extend ? `deployments/${d.id}/extend/` : null)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  if (!d.expires_at) return null
  const cells = lifetimeCells(d.created, d.expires_at, addedDays(requests))
  const left = Math.max(0, daysUntil(d.expires_at))
  const o = opts.data
  const extend = async (s: ExtensionStep) => {
    setBusy(true)
    try {
      const r = await api.post<CatalogRequest>(`deployments/${d.id}/extend/`, { days: s.days })
      setNote(
        r.status === 'awaiting_approval'
          ? trn(s.days, 'Asked {names} for {n} more day. Until they answer, it still expires {date}.', 'Asked {names} for {n} more days. Until they answer, it still expires {date}.', {
              names: gateNames(s.approvals),
              date: dateTimeText(d.expires_at),
            })
          : trn(s.days, 'Extended by {n} day.', 'Extended by {n} days.') + (s.expires_at ? ' ' + tr('It now expires {date}.', { date: dateTimeText(s.expires_at) }) : ''),
      )
      invalidate('deployments', 'catalog_requests', 'portal/')
    } catch (e) {
      setNote((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const counts: Record<DayCell, number> = { gone: 0, today: 0, left: 0, added: 0 }
  for (const c of cells ?? []) counts[c]++
  return (
    <section aria-labelledby="dep-life" className="portal-card portal-block">
      <div className="portal-block-head">
        <h3 id="dep-life">{tr('{when}: {date}', { when: expiryText(left), date: dateTimeText(d.expires_at) })}</h3>
        {o && (
          <span className="portal-muted small">
            {o.max_extensions != null ? tr('Extended {n} of {max} times', { n: o.extensions, max: o.max_extensions }) : trn(o.extensions, 'Extended {n} time', 'Extended {n} times')}
            {o.until && ' · ' + tr('until {date} at most', { date: dateOnly(o.until) })}
          </span>
        )}
      </div>
      {cells && (
        <>
          <div
            role="img"
            className="portal-days"
            aria-label={tr('{gone} days gone, today, {left} days left', { gone: counts.gone, left: counts.left + counts.added })}
          >
            {cells.map((c, i) => (
              <span key={i} data-day={c} />
            ))}
          </div>
          <div aria-hidden="true" className="portal-legend">
            {(['gone', 'today', 'left', 'added'] as DayCell[]).map((c) => (
              <span key={c}>
                <span data-day={c} />
                {{ gone: tr('Gone'), today: tr('Today'), left: tr('Left'), added: tr('Added') }[c]}
              </span>
            ))}
          </div>
        </>
      )}
      {o && o.steps.length > 0 && !(o.max_extensions != null && o.extensions >= o.max_extensions) && (
        <div className="portal-ext">
          {o.steps.map((s) => (
            <button
              key={s.days}
              type="button"
              className={`portal-btn ${s.approvals.length ? 'gated' : ''}`}
              disabled={busy || !!s.refused}
              title={s.refused || undefined}
              onClick={() => extend(s)}
            >
              {s.approvals.length
                ? trn(s.days, '+{n} day · {names} approves', '+{n} days · {names} approves', { names: gateNames(s.approvals) })
                : trn(s.days, '+{n} day', '+{n} days')}
            </button>
          ))}
        </div>
      )}
      <p aria-live="polite" className="portal-muted small portal-ext-note">
        {note || (o ? extensionNote(o, gateNames) : '')}
      </p>
    </section>
  )
}

function Secret({ d, e }: { d: Deployment; e: ConnectionEntry }) {
  const [value, setValue] = useState<string | null>(null)
  const [error, setError] = useState('')
  const toggle = async () => {
    if (value != null) return setValue(null)
    try {
      setValue((await api.post<{ value: string }>(`deployments/${d.id}/reveal/`, { label: e.label })).value)
      setError('')
    } catch (x) {
      setError((x as Error).message)
    }
  }
  return (
    <>
      <span className="mono">{value ?? '••••••••••••'}</span>
      {d.summary_fields.user_capabilities?.reveal && (
        <button type="button" className="portal-btn small" onClick={toggle} aria-label={value != null ? tr('Hide {label}', { label: e.label }) : tr('Show {label}', { label: e.label })}>
          {value != null ? tr('Hide') : tr('Show')}
        </button>
      )}
      {value != null && e.copy && <CopyButton text={value} label={e.label} />}
      {error && <span className="portal-error small">{error}</span>}
    </>
  )
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const toast = useToast()
  return (
    <button
      type="button"
      className="portal-icon-btn"
      aria-label={tr('Copy {label}', { label })}
      onClick={() => navigator.clipboard?.writeText(text).then(() => toast(tr('Copied {key}', { key: label })), () => {})}
    >
      <Icon name="copy" size={16} />
    </button>
  )
}

function Connect({ d }: { d: Deployment }) {
  if (!d.connection?.length || d.status === 'deleted') return null
  return (
    <section aria-labelledby="dep-conn" className="portal-card portal-block">
      <h3 id="dep-conn">{tr('How to connect')}</h3>
      <dl className="portal-dl portal-conn">
        {d.connection.map((e) => (
          <div key={e.label}>
            <dt>{e.label}</dt>
            <dd>
              {e.error ? (
                <span className="portal-error small">{e.error}</span>
              ) : e.secret ? (
                <Secret d={d} e={e} />
              ) : (
                <>
                  <span className="mono">{e.value}</span>
                  {e.copy && e.value && <CopyButton text={e.value} label={e.label} />}
                </>
              )}
            </dd>
          </div>
        ))}
      </dl>
      {d.connection.some((e) => e.secret) && <p className="portal-muted small">{tr('Showing a secret is logged.')}</p>}
    </section>
  )
}

function ActionForm({ d, a, onDone }: { d: Deployment; a: DeploymentAction; onDone: (r: CatalogRequest) => void }) {
  const questions = a.questions as unknown as Question[]
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [reason, setReason] = useState('')
  const [touched, setTouched] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const errors = validateSurvey(questions, values, {})
  const send = async () => {
    setTouched(true)
    if (Object.keys(errors).length || busy) return
    setBusy(true)
    try {
      const answers: Record<string, unknown> = {}
      for (const q of questions) {
        const v = values[q.variable] ?? q.default
        if (v !== undefined && v !== '' && v !== null) answers[q.variable] = (q.type === 'integer' || q.type === 'float') && typeof v === 'string' ? Number(v) : v
      }
      onDone(await api.post<CatalogRequest>(`deployments/${d.id}/actions/`, { action: a.id, answers, ...(reason.trim() ? { reason: reason.trim() } : {}) }))
    } catch (e) {
      setError((e as Error).message)
      setBusy(false)
    }
  }
  return (
    <form
      className="portal-form portal-inline-form"
      aria-label={a.name}
      onSubmit={(e) => {
        e.preventDefault()
        send()
      }}
    >
      <h4>{a.name}</h4>
      {questions.length > 0 && <SurveyFields questions={questions} values={values} onChange={(v, x) => setValues((s) => ({ ...s, [v]: x }))} errors={touched ? errors : {}} dynamic={{}} />}
      <div className="field">
        <label className="label" htmlFor={`act-${a.id}-reason`}>
          {tr('Why?')} <span className="opt">{tr('· optional')}</span>
        </label>
        <textarea id={`act-${a.id}-reason`} className="textarea" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} />
      </div>
      {error && (
        <p className="portal-note fail" role="alert">
          {error}
        </p>
      )}
      <div className="portal-actions">
        <button type="submit" className="portal-btn primary" disabled={busy}>
          {busy ? tr('Sending…') : tr('Run it')}
        </button>
      </div>
    </form>
  )
}

function HandOver({ d, onDone }: { d: Deployment; onDone: () => void }) {
  const choices = useObject<{ users: { id: number; name: string; username: string }[]; teams: { id: number; name: string }[] }>(`deployments/${d.id}/hand_over/`)
  const [to, setTo] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const send = async () => {
    const [kind, id] = to.split(':')
    if (!id) return
    setBusy(true)
    try {
      await api.post(`deployments/${d.id}/hand_over/`, kind === 'team' ? { team: Number(id), user: null } : { user: Number(id) })
      onDone()
    } catch (e) {
      setError((e as Error).message)
      setBusy(false)
    }
  }
  const c = choices.data
  return (
    <form
      className="portal-form portal-inline-form"
      aria-label={tr('Hand it over')}
      onSubmit={(e) => {
        e.preventDefault()
        send()
      }}
    >
      <h4>{tr('Hand it over')}</h4>
      {c && c.users.length + c.teams.length === 0 ? (
        <p className="portal-muted">{tr('Nobody else can request this item, so nobody can take it.')}</p>
      ) : (
        <div className="field">
          <label className="label" htmlFor="dep-hand">
            {tr('To whom?')}
          </label>
          <select id="dep-hand" className="select" value={to} onChange={(e) => setTo(e.target.value)} aria-busy={!c}>
            <option value="">{tr('Choose a colleague or a team')}</option>
            {c && c.teams.length > 0 && (
              <optgroup label={tr('Teams')}>
                {c.teams.map((t) => (
                  <option key={t.id} value={`team:${t.id}`}>
                    {t.name}
                  </option>
                ))}
              </optgroup>
            )}
            {c && c.users.length > 0 && (
              <optgroup label={tr('People')}>
                {c.users.map((u) => (
                  <option key={u.id} value={`user:${u.id}`}>
                    {u.name === u.username ? u.name : tr('{name} ({username})', { name: u.name, username: u.username })}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
          <span className="portal-muted small">{tr('They get an email, and you may lose access to it.')}</span>
        </div>
      )}
      {error && (
        <p className="portal-note fail" role="alert">
          {error}
        </p>
      )}
      <div className="portal-actions">
        <button type="submit" className="portal-btn primary" disabled={busy || !to}>
          {tr('Hand it over')}
        </button>
      </div>
    </form>
  )
}

function Actions({ d }: { d: Deployment }) {
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const caps = d.summary_fields.user_capabilities ?? {}
  const list = useObject<{ actions: DeploymentAction[] }>(caps.run ? `deployments/${d.id}/actions/` : null)
  const [open, setOpen] = useState<string | null>(null)
  const [sent, setSent] = useState<CatalogRequest | null>(null)
  const actions = list.data?.actions ?? []
  if (!actions.length && !caps.hand_over) return null
  const current = actions.find((a) => a.id === open)
  return (
    <section aria-labelledby="dep-acts" className="portal-card portal-block">
      <h3 id="dep-acts">{tr('Actions')}</h3>
      <div className="portal-acts">
        {actions.map((a) => (
          <button key={a.id} type="button" className="portal-act" aria-expanded={open === a.id} onClick={() => setOpen(open === a.id ? null : a.id)}>
            <span className="portal-strong">{a.name}</span>
            {a.description && <span className="portal-muted small">{a.description}</span>}
          </button>
        ))}
        {caps.hand_over && (
          <button type="button" className="portal-act" aria-expanded={open === '$hand_over'} onClick={() => setOpen(open === '$hand_over' ? null : '$hand_over')}>
            <span className="portal-strong">{tr('Hand it over')}</span>
            <span className="portal-muted small">{d.summary_fields.team ? tr('To a colleague or to {team}', { team: d.summary_fields.team.name }) : tr('To a colleague or a team')}</span>
          </button>
        )}
      </div>
      {current && (
        <ActionForm
          key={current.id}
          d={d}
          a={current}
          onDone={(r) => {
            setOpen(null)
            setSent(r)
            invalidate('deployments', 'catalog_requests', 'portal/')
          }}
        />
      )}
      {open === '$hand_over' && (
        <HandOver
          d={d}
          onDone={() => {
            invalidate('deployments', 'portal/')
            nav('/portal/deployments')
          }}
        />
      )}
      {sent && (
        <p className="portal-note ok" role="status">
          {sent.status === 'awaiting_approval' ? tr('Sent for approval.') : tr('Sent.')} <Link to={`/portal/requests/${sent.id}`}>{tr('Follow it under My requests')}</Link>
        </p>
      )}
    </section>
  )
}

function Users({ d }: { d: Deployment }) {
  const { me } = useSession()
  const invalidate = useInvalidate()
  const [confirm, setConfirm] = useState(false)
  const [error, setError] = useState('')
  const can = d.summary_fields.user_capabilities?.delete && d.status === 'active'
  const remove = async () => {
    try {
      await api.del(`deployments/${d.id}/`)
      setConfirm(false)
      invalidate('deployments', 'catalog_requests', 'portal/')
    } catch (e) {
      setError((e as Error).message)
    }
  }
  return (
    <section aria-labelledby="dep-who" className="portal-card portal-block portal-who">
      <h3 id="dep-who">{tr('Who can use it')}</h3>
      <p>{usersText(d, me.id)}</p>
      {can && !confirm && (
        <button type="button" className="portal-btn danger" onClick={() => setConfirm(true)}>
          {tr('Delete it now')}
        </button>
      )}
      {can && confirm && (
        <div className="portal-confirm" role="alertdialog" aria-labelledby="dep-del">
          <span id="dep-del">{tr('Delete {name}? You can’t undo this.', { name: d.name })}</span>
          <div className="portal-actions">
            <button type="button" className="portal-btn danger solid" onClick={remove}>
              {tr('Delete')}
            </button>
            <button type="button" className="portal-btn" onClick={() => setConfirm(false)}>
              {tr('Keep it')}
            </button>
          </div>
        </div>
      )}
      {error && (
        <p className="portal-note fail" role="alert">
          {error}
        </p>
      )}
    </section>
  )
}

function History({ requests, actions }: { requests: Request[]; actions: PortalItem['actions'] }) {
  const { me } = useSession()
  const names = new Map((actions ?? []).map((a) => [a.id, a.name]))
  const rows = deploymentHistory(
    requests.map((r) => {
      const u = r.summary_fields.created_by
      return { ...r, created_by: u?.id ?? null, who: u ? [u.first_name, u.last_name].filter(Boolean).join(' ') || u.username : '' }
    }),
    names,
    me.id,
  )
  if (!rows.length) return null
  return (
    <section aria-labelledby="dep-hist" className="portal-card portal-block portal-history">
      <h3 id="dep-hist">{tr('What happened')}</h3>
      <ol>
        {rows.map((e, i) => (
          <li key={i}>
            <time dateTime={e.at} className="mono portal-muted">
              {dateTimeText(e.at)}
            </time>
            <span>{e.text}</span>
          </li>
        ))}
      </ol>
    </section>
  )
}

function Failed({ d }: { d: Deployment }) {
  const invalidate = useInvalidate()
  const [error, setError] = useState('')
  const left = leftBehind(d.left_behind)
  const expired = d.expires_at && new Date(d.expires_at).getTime() < Date.now()
  const retry = async () => {
    try {
      await api.del(`deployments/${d.id}/`)
      invalidate('deployments', 'portal/')
    } catch (e) {
      setError((e as Error).message)
    }
  }
  return (
    <>
      <div className="portal-next-box fail" role="alert">
        <span className="portal-strong">
          {expired ? tr('It expired on {date}, but deleting it failed.', { date: dateTimeText(d.expires_at) }) : tr('Deleting it failed.')}
        </span>
        {d.explanation && <span>{d.explanation}</span>}
      </div>
      <section aria-labelledby="dep-left" className="portal-card portal-block">
        <h3 id="dep-left">{tr('What’s left')}</h3>
        {left.length ? (
          <ul className="portal-left">
            {left.map((x, i) => (
              <li key={i}>
                <span className="portal-cell" data-state="failed" aria-hidden="true" />
                <span className="mono">{x.name}</span>
                {x.why && <span className="portal-muted small">{x.why}</span>}
              </li>
            ))}
          </ul>
        ) : (
          <p className="portal-muted">{tr('Its teardown didn’t say. The item’s owners can see what went wrong in its job.')}</p>
        )}
        {d.summary_fields.user_capabilities?.delete && (
          <button type="button" className="portal-btn" onClick={retry}>
            {tr('Try deleting it again')}
          </button>
        )}
        {error && (
          <p className="portal-note fail" role="alert">
            {error}
          </p>
        )}
      </section>
    </>
  )
}

function Detail({ id }: { id: number }) {
  const { me } = useSession()
  const q = useObject<Deployment>(`deployments/${id}/`, { refetchInterval: (query) => live(query.state.data) })
  const d = q.data
  const item = useObject<PortalItem>(d?.catalog_item ? `catalog_items/${d.catalog_item}/` : null, { retry: false })
  const reqs = useList<Request>(d ? `deployments/${id}/requests/` : null, { order_by: '-id', page_size: 50 })
  const invalidate = useInvalidate()
  const status = d?.status
  useEffect(() => {
    if (status) invalidate(`deployments/${id}/requests`)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, id])
  if (q.error) return <p className="portal-error">{q.error.message}</p>
  if (!d) return <section className="portal-detail plain" aria-busy="true" />
  const st = deploymentState(d)
  const requests = reqs.data?.results ?? []
  const owner = d.owner === me.id ? tr('owner: you') : d.summary_fields.owner ? tr('owner: {name}', { name: d.summary_fields.owner.username }) : d.summary_fields.team ? tr('owner: {name}', { name: d.summary_fields.team.name }) : ''
  return (
    <section aria-labelledby="dep-name" className="portal-detail plain">
      <Link to="/portal/deployments" className="portal-back">
        {tr('All deployments')}
      </Link>
      <div className="portal-detail-head">
        <div className="row" style={{ gap: 14, alignItems: 'flex-start' }}>
          <ItemIcon name={item.data?.icon ?? ''} />
          <div className="stack" style={{ gap: 2 }}>
            <h2 id="dep-name" className="mono">
              {d.name}
            </h2>
            <span className="portal-muted">
              {[
                d.item_name,
                d.request ? (
                  <span key="r">
                    {tr('created {date} from', { date: dateOnly(d.created) })} <Link to={`/portal/requests/${d.request}`}>{tr('request {id}', { id: d.request })}</Link>
                  </span>
                ) : (
                  tr('created {date}', { date: dateOnly(d.created) })
                ),
                owner,
              ]
                .filter(Boolean)
                .flatMap((x, i) => (i ? [' · ', x] : [x]))}
            </span>
          </div>
        </div>
        {d.status === 'active' ? <Pill tone="ok">{tr('Ready')}</Pill> : <Pill tone={st.tone}>{st.label}</Pill>}
      </div>
      {d.status === 'deleting' && (
        <div role="status" className="portal-next-box denied">
          {tr('Being deleted. It leaves this list once it’s gone.')}
        </div>
      )}
      {d.status === 'delete_failed' && <Failed d={d} />}
      {d.status === 'active' && <Lifetime d={d} requests={requests} />}
      <Connect d={d} />
      {d.status === 'active' && <Actions d={d} />}
      <div className="portal-pair">
        <History requests={requests} actions={item.data?.actions} />
        {d.status !== 'deleted' && <Users d={d} />}
      </div>
    </section>
  )
}

export function Deployments() {
  const { me } = useSession()
  const { id } = useParams()
  const teams = useList<{ id: number }>(`users/${me.id}/teams/`, { page_size: 200 })
  const teamIds = (teams.data?.results ?? []).map((t) => t.id)
  const list = useList<Deployment>(
    teams.data ? 'deployments/' : null,
    { or__owner: me.id, ...(teamIds.length ? { or__team__in: teamIds.join(',') } : {}), not__status: 'deleted', order_by: '-id', page_size: 100 },
    { refetchInterval: (q) => (q.state.data?.results.some((d) => d.status === 'deleting') ? 5000 : false) },
  )
  const rows = list.data?.results ?? []
  const selected = id ? Number(id) : rows[0]?.id
  return (
    <div className="portal-page" data-detail={id ? 'yes' : 'no'}>
      <div className="portal-section-head">
        <h1>{tr('My deployments')}</h1>
      </div>
      {list.isPending ? (
        <div aria-busy="true" />
      ) : rows.length === 0 && !id ? (
        <div className="portal-empty">
          <h3>{tr('No deployment yet')}</h3>
          <p>{tr('What you ask for in the catalog and keep, a VM or a database, shows here until it expires or you delete it.')}</p>
          <Link to="/portal" className="portal-btn">
            {tr('Open the catalog')}
          </Link>
        </div>
      ) : (
        <div className="portal-requests" data-detail={id ? 'yes' : 'no'}>
          <section aria-label={tr('Deployments')} className="portal-request-list">
            <ul>
              {rows.map((d) => {
                const st = deploymentState(d)
                return (
                  <li key={d.id}>
                    <Link to={`/portal/deployments/${d.id}`} aria-current={d.id === selected ? 'true' : undefined} className="portal-request-row">
                      <span className="portal-cell" data-state={st.cell} aria-hidden="true" />
                      <span className="stack" style={{ gap: 2, minWidth: 0 }}>
                        <span className="mono portal-strong portal-ellipsis">{d.name}</span>
                        <span className="portal-muted small portal-ellipsis">{d.item_name}</span>
                        <span className={`small portal-tone-${st.tone}`}>{st.label}</span>
                      </span>
                    </Link>
                  </li>
                )
              })}
            </ul>
          </section>
          {selected ? <Detail key={selected} id={selected} /> : null}
        </div>
      )}
    </div>
  )
}
