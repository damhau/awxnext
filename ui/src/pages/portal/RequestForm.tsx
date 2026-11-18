// An item's request form: its questions, the fixed answers its owner shows,
// the team and lifetime, and a summary of who approves, why, and the quota
// left, worked out by the server before the request is sent.
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import { dateOnly, dateTimeText } from '../../lib/format'
import { tr, trn } from '../../lib/i18n'
import {
  answersOf,
  expiresOn,
  lifetimeChoices,
  lifetimeLabel,
  namesText,
  prefill,
  requestBody,
  surveyQuestions,
  timeoutText,
  type CatalogRequest,
  type GatePreview,
  type PortalItem,
  type Preview,
} from '../../lib/portal'
import { holderName, quotaSlots } from '../../lib/quotas'
import { SurveyFields, useSettled, useSurveyChoices, validateSurvey, type Question } from '../templates/SurveyEditor'
import { visibleQuestions } from '../templates/surveyLogic'
import { Cell, ItemIcon } from './parts'

interface FormQuestion extends Question {
  dynamic: boolean
}

interface RequestFormData {
  catalog_item: { id: number; name: string; description: string; kind: string; icon: string; category: string }
  questions: FormQuestion[]
  fixed_answers: { variable: string; question_name: string; value: unknown }[]
  teams: { id: number; name: string }[]
  team_required: boolean
  lifetime: { max_days: number | null; default_days: number | null } | null
}

function GateText({ g }: { g: GatePreview }) {
  const names = namesText(g.approvers.map((p) => p.name))
  return (
    <div className="portal-sum-block waiting">
      <span className="portal-strong">{g.kind === 'quota' ? tr('Over quota: it needs approval') : tr('{rule}: it needs approval', { rule: g.rule })}</span>
      <span>
        {g.required > 1 ? tr('{names} approve it: {n} of them must.', { names, n: g.required }) : tr('{names} approves it.', { names })}{' '}
        {g.fallback && tr('Nobody else could, so the organization’s admins do.')}{' '}
        {g.timeout > 0 && tr('If nobody answers within {time}, the request is denied.', { time: timeoutText(g.timeout) })}
      </span>
      {g.why && <span className="portal-muted small">{g.why}</span>}
    </div>
  )
}

function QuotaText({ preview, kind }: { preview: Preview; kind: string }) {
  const q = preview.quota
  if (!q) return null
  const who = holderName(q)
  const parts = []
  if (kind === 'deployment' && q.max_deployments != null) {
    const slots = quotaSlots(q.deployments, q.max_deployments, 1)
    parts.push(
      <div key="d" className="stack" style={{ gap: 6 }}>
        {slots && (
          <span className="row" role="img" style={{ gap: 3 }} aria-label={tr('{who}: {used} used, this request, {max} in all', { who, used: q.deployments, max: q.max_deployments })}>
            {slots.map((s, i) => (
              <Cell key={i} state={s} />
            ))}
          </span>
        )}
        <span>{tr('This one makes {n} of the {max} deployments {who} can keep.', { n: q.deployments + 1, max: q.max_deployments, who })}</span>
      </div>,
    )
  }
  if (q.max_requests_per_day != null) {
    parts.push(<span key="r">{tr('This one is {n} of the {max} requests {who} can make in 24 hours.', { n: q.requests + 1, max: q.max_requests_per_day, who })}</span>)
  }
  if (!parts.length) return null
  return (
    <div className="portal-sum-block">
      <span className="portal-eyebrow">{tr('Quota')}</span>
      {parts}
    </div>
  )
}

export function RequestForm() {
  const { id } = useParams()
  const [params] = useSearchParams()
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const from = params.get('from')
  const item = useObject<PortalItem>(`catalog_items/${id}/`)
  const form = useObject<RequestFormData>(`catalog_items/${id}/request/`)
  const prior = useObject<CatalogRequest>(from ? `catalog_requests/${from}/` : null)
  const questions = useMemo(() => surveyQuestions(form.data?.questions ?? []), [form.data])
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [team, setTeam] = useState<number | null>(null)
  const [lifetime, setLifetime] = useState<number | null | undefined>(undefined)
  const [reason, setReason] = useState('')
  const [touched, setTouched] = useState(false)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!form.data) return
    if (form.data.teams.length === 1) setTeam(form.data.teams[0].id)
    const lt = form.data.lifetime
    if (lt) setLifetime(lt.default_days ?? (lt.max_days != null ? Math.min(7, lt.max_days) : null))
  }, [form.data])
  useEffect(() => {
    if (!prior.data || !form.data) return
    setValues(prefill(prior.data.answers, form.data.questions))
    if (prior.data.summary_fields.team) setTeam(prior.data.summary_fields.team.id)
    if (prior.data.lifetime_days != null && form.data.lifetime) setLifetime(prior.data.lifetime_days)
    setReason(prior.data.reason ?? '')
  }, [prior.data, form.data])

  const dynamic = useSurveyChoices('catalog_items', Number(id), questions, null, values)
  const errors = validateSurvey(questions, values, dynamic)
  const teamMissing = !!form.data?.team_required && team == null
  const valid = !!form.data && Object.keys(errors).length === 0 && !teamMissing
  const body = requestBody(answersOf(visibleQuestions(questions, values), values), team, form.data?.lifetime ? lifetime : undefined, reason)
  const settled = useSettled(body, 500)
  const preview = useQuery({
    queryKey: [`catalog_items/${id}/preview/`, settled],
    queryFn: () => api.post<Preview>(`catalog_items/${id}/preview/`, settled),
    enabled: valid && JSON.stringify(settled) === JSON.stringify(body),
    retry: false,
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  })

  if (form.error || item.error) {
    const e = (form.error ?? item.error) as Error
    return (
      <div className="portal-empty">
        <h1>{e instanceof ApiError && e.status === 404 ? tr('This item isn’t in your catalog') : tr('The item couldn’t be loaded')}</h1>
        <p>{e.message}</p>
        <Link to="/portal" className="portal-btn">
          {tr('Back to the catalog')}
        </Link>
      </div>
    )
  }
  if (!form.data || !item.data) return <div className="portal-page" aria-busy="true" />

  const f = form.data
  const it = item.data
  const p = preview.data
  const gated = !!p && p.approvals.length > 0
  const refused = p?.refused?.detail
  const freeze = it.summary_fields.freeze
  const owner = it.summary_fields.owner_team?.name ?? it.summary_fields.organization?.name ?? ''

  const send = async () => {
    setTouched(true)
    if (!valid || sending) return
    setSending(true)
    setError(null)
    try {
      const r = await api.post<CatalogRequest>(`catalog_items/${id}/request/`, body)
      invalidate('catalog_requests', 'portal/')
      nav(`/portal/requests/${r.id}`)
    } catch (e) {
      setError((e as Error).message)
      setSending(false)
    }
  }

  return (
    <div className="portal-page">
      <div className="stack" style={{ gap: 14 }}>
        <nav aria-label={tr('Breadcrumb')} className="portal-crumbs">
          <Link to="/portal">{tr('Catalog')}</Link>
          {f.catalog_item.category && (
            <>
              <span aria-hidden="true">/</span>
              <span>{f.catalog_item.category}</span>
            </>
          )}
        </nav>
        <div className="portal-item-title">
          <ItemIcon name={f.catalog_item.icon} size={28} />
          <div className="stack" style={{ gap: 6 }}>
            <h1>{f.catalog_item.name}</h1>
            {f.catalog_item.description && <p className="portal-lead">{f.catalog_item.description}</p>}
            {owner && <p className="portal-muted small">{tr('Owned by {owner}', { owner })}</p>}
          </div>
        </div>
        {freeze && (
          <p className="portal-note waiting">
            {tr('Paused for {name} until {date}.', { name: freeze.name, date: dateTimeText(freeze.until) })}
          </p>
        )}
      </div>

      <div className="portal-form-grid">
        <form
          className="portal-form"
          onSubmit={(e) => {
            e.preventDefault()
            send()
          }}
        >
          {questions.length > 0 && (
            <SurveyFields questions={questions} values={values} onChange={(v, x) => setValues((a) => ({ ...a, [v]: x }))} errors={touched ? errors : {}} dynamic={dynamic} />
          )}
          {f.teams.length > 1 && (
            <div className="field">
              <label className="label" htmlFor="portal-team">
                {tr('For which team?')}
              </label>
              <select id="portal-team" className="select" value={team ?? ''} onChange={(e) => setTeam(e.target.value ? Number(e.target.value) : null)}>
                <option value="">{tr('Choose a team')}</option>
                {f.teams.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </select>
              {touched && teamMissing && <span className="err">{tr('Choose the team it’s for.')}</span>}
            </div>
          )}
          {f.teams.length === 1 && <p className="portal-muted small">{tr('For {team}.', { team: f.teams[0].name })}</p>}
          {f.lifetime && (
            <fieldset className="portal-fieldset">
              <legend className="label">{tr('Keep it for')}</legend>
              <div className="portal-chips">
                {lifetimeChoices(f.lifetime.max_days, f.lifetime.default_days).map((d) => (
                  <button key={String(d)} type="button" className="portal-chip" aria-pressed={lifetime === d} onClick={() => setLifetime(d)}>
                    {lifetimeLabel(d)}
                  </button>
                ))}
              </div>
              {it.max_extensions ? (
                <p className="portal-muted small">{trn(it.max_extensions, 'You can extend it once before it expires.', 'You can extend it {n} times before it expires.')}</p>
              ) : null}
            </fieldset>
          )}
          <div className="field">
            <label className="label" htmlFor="portal-reason">
              {gated ? tr('Why do you need it?') : (
                <>
                  {tr('Why do you need it?')} <span className="opt">{tr('· optional')}</span>
                </>
              )}
            </label>
            <textarea id="portal-reason" className="textarea" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} placeholder={tr('For the approvers: what it’s for, and for how long')} />
          </div>
          {f.fixed_answers.length > 0 && (
            <section aria-labelledby="portal-fixed" className="portal-fixed">
              <h2 id="portal-fixed" className="portal-eyebrow">
                {owner ? tr('Set by {owner}', { owner }) : tr('Set for you')}
              </h2>
              <dl>
                {f.fixed_answers.map((a) => (
                  <div key={a.variable}>
                    <dt>{a.question_name || a.variable}</dt>
                    <dd>{Array.isArray(a.value) ? a.value.join(', ') : String(a.value ?? '')}</dd>
                  </div>
                ))}
              </dl>
            </section>
          )}
        </form>

        <aside aria-labelledby="portal-sum" className="portal-summary">
          <h2 id="portal-sum">{tr('Your request')}</h2>
          {!valid ? (
            <div className="portal-sum-block">
              <span className="portal-muted">{tr('Answer the questions to see who approves it.')}</span>
            </div>
          ) : preview.error && !p ? (
            <div className="portal-sum-block fail">
              <span>{preview.error.message}</span>
            </div>
          ) : !p ? (
            <div className="portal-sum-block" aria-busy="true">
              <span className="portal-muted">{tr('Checking…')}</span>
            </div>
          ) : refused ? (
            <div className="portal-sum-block fail" role="alert">
              <span className="portal-strong">{tr('It would be refused')}</span>
              <span>{refused}</span>
            </div>
          ) : p.approvals.length === 0 ? (
            <div className="portal-sum-block ok">
              <span className="portal-strong">{tr('No approval needed')}</span>
              <span>{tr('It starts as soon as you send it.')}</span>
            </div>
          ) : (
            p.approvals.map((g, i) => <GateText key={i} g={g} />)
          )}
          {p && <QuotaText preview={p} kind={f.catalog_item.kind} />}
          {f.lifetime && (
            <div className="portal-sum-block">
              <span className="portal-eyebrow">{tr('Lifetime')}</span>
              <span>{lifetime ? tr('Expires around {date}, counted from when it’s ready.', { date: dateOnly(expiresOn(lifetime)) }) : tr('Kept until you delete it.')}</span>
            </div>
          )}
          <div className="portal-sum-block">
            <span className="portal-eyebrow">{tr('What happens next')}</span>
            <ol className="portal-next">
              {gated && <li>{tr('The approvers approve or deny it.')}</li>}
              <li>{tr('It runs, and you follow it under My requests.')}</li>
              {f.catalog_item.kind === 'deployment' && <li>{tr('What it creates stays yours until it expires or you delete it.')}</li>}
            </ol>
          </div>
          {error && (
            <p className="portal-note fail" role="alert">
              {error}
            </p>
          )}
          <div className="portal-actions">
            <button type="button" className="portal-btn primary" onClick={send} disabled={sending || !!refused}>
              {sending ? tr('Sending…') : gated ? tr('Send for approval') : tr('Send the request')}
            </button>
            <Link to="/portal" className="portal-btn">
              {tr('Cancel')}
            </Link>
          </div>
        </aside>
      </div>
    </div>
  )
}
