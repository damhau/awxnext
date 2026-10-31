import { useQuery } from '@tanstack/react-query'
import { isValidElement, useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Job, Ref, Template } from '../../api/types'
import { useOrg } from '../../app/context'
import { Check, FormError, Lookup, MultiLookup, parseVars, VarsEditor, varsError, varsText } from '../../components/form'
import { Icon } from '../../components/icons'
import { Empty, Loading, Panel, Status } from '../../components/ui'
import { ago, duration, jobPath, plural, shortSha, statusKind } from '../../lib/format'
import { usePref } from '../../lib/prefs'
import { SurveyFields, useSurveyChoices, validateSurvey, type Survey } from './SurveyEditor'
import { visibleQuestions } from './surveyLogic'
import { resOf, type Kind } from './TemplateDetail'
import { passwordLabel } from '../../lib/jobs'
import { asIs, tr } from '../../lib/i18n'

/* eslint-disable @typescript-eslint/no-explicit-any */
interface LaunchInfo {
  can_start_without_user_input: boolean
  defaults: Record<string, any>
  survey_enabled: boolean
  variables_needed_to_start: string[]
  passwords_needed_to_start?: string[]
  credential_needed_to_start?: boolean
  inventory_needed_to_start?: boolean
  [k: string]: any
}


export function LaunchPage({ operator = false }: { operator?: boolean }) {
  const { kind: k, id } = useParams()
  const kind: Kind = k === 'workflow' ? 'workflow' : 'job'
  const res = resOf(kind)
  const tpl = useObject<Template>(`${res}/${id}/`)
  const info = useObject<LaunchInfo>(`${res}/${id}/launch/`)
  const survey = useObject<Survey>(info.data?.survey_enabled ? `${res}/${id}/survey_spec/` : null)
  const body =
    tpl.isPending || info.isPending || (info.data?.survey_enabled && survey.isPending) ? (
      <Loading />
    ) : tpl.error || info.error ? (
      <div className="page-body">
        <FormError error={tpl.error ?? info.error} />
      </div>
    ) : (
      <LaunchForm key={`${kind}-${id}`} kind={kind} tpl={tpl.data!} info={info.data!} survey={survey.data ?? null} operator={operator} />
    )
  if (!operator) return <div className="page ground">{body}</div>
  return (
    <div className="body" style={{ minHeight: '100%' }}>
      <Catalog current={`${kind}:${id}`} />
      <div className="page ground" style={{ minWidth: 0 }}>
        {body}
      </div>
    </div>
  )
}

/** Templates the user may launch, for the operator view. */
function Catalog({ current }: { current?: string }) {
  const [q, setQ] = useState('')
  const { scope } = useOrg()
  const list = useList<Template>('unified_job_templates/', { type__in: 'job_template,workflow_job_template', search: q || undefined, order_by: 'name', page_size: 50, ...scope })
  const items = (list.data?.results ?? []).filter((t) => t.summary_fields.user_capabilities?.start !== false)
  return (
    <aside aria-label={tr('Catalog')} className="sidelist" style={{ flexBasis: 280, background: 'var(--surface-2)', gap: 12 }}>
      <label htmlFor="cat-search" className="nav-label" style={{ padding: 0 }}>
        {tr('What do you want to do?')}
      </label>
      <input id="cat-search" className="input" placeholder={tr('deploy, restart, certificate…')} value={q} onChange={(e) => setQ(e.target.value)} />
      <div className="stack" style={{ gap: 2 }}>
        {items.map((t) => {
          const key = `${t.type === 'workflow_job_template' ? 'workflow' : 'job'}:${t.id}`
          const on = key === current
          return (
            <Link
              key={t.id}
              to={`/run/${t.type === 'workflow_job_template' ? 'workflow' : 'job'}/${t.id}`}
              style={{ display: 'flex', flexDirection: 'column', gap: 3, padding: 10, borderRadius: 3, border: `1px solid ${on ? 'var(--ink)' : 'transparent'}`, background: on ? 'var(--surface)' : undefined, color: 'inherit', textDecoration: 'none' }}
            >
              <span className="row" style={{ justifyContent: 'space-between' }}>
                <span style={{ fontWeight: 500, color: 'var(--ink)' }}>{t.name}</span>
                <span className="small faint nowrap">{t.summary_fields.folder?.name}</span>
              </span>
              {t.description && <span className="small muted">{t.description}</span>}
            </Link>
          )
        })}
        {list.data && items.length === 0 && <span className="small muted">{tr('Nothing you can run matches.')}</span>}
      </div>
    </aside>
  )
}

/** Operator home: tiles of everything the user may run, starred ones first, then by folder. */
export function RunHome() {
  const [q, setQ] = useState('')
  const { scope } = useOrg()
  const [starred] = usePref<string[]>('starred-templates', [])
  const list = useList<Template>('unified_job_templates/', { type__in: 'job_template,workflow_job_template', search: q || undefined, order_by: 'name', page_size: 200, ...scope })
  const items = (list.data?.results ?? []).filter((t) => t.summary_fields.user_capabilities?.start !== false)
  const groups = new Map<string, Template[]>()
  const add = (name: string, t: Template) => groups.set(name, [...(groups.get(name) ?? []), t])
  for (const t of items) if (starred.includes(`${t.type}:${t.id}`)) add('Starred', t)
  for (const t of items) if (!starred.includes(`${t.type}:${t.id}`)) add(t.summary_fields.folder?.name ?? 'Other', t)
  const names = [...groups.keys()].sort((a, b) => (a === 'Starred' ? -1 : b === 'Starred' ? 1 : a === 'Other' ? 1 : b === 'Other' ? -1 : a.localeCompare(b)))
  return (
    <div className="page ground">
      <div className="page-body" style={{ padding: '20px 28px 32px', maxWidth: 1180 }}>
        <h1 style={{ fontSize: 20, margin: '0 0 4px' }}>{tr('What do you want to do?')}</h1>
        <p className="muted" style={{ margin: '0 0 14px' }}>
          {tr('Everything you are allowed to run. Each one asks only what it needs; the rest is set by its owner.')}
        </p>
        <input className="input" style={{ maxWidth: 440, marginBottom: 20 }} aria-label={tr('Search')} placeholder={tr('deploy, restart, certificate…')} value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
        {list.isPending && <Loading rows={3} />}
        {list.data && items.length === 0 && (
          <Empty title={q ? tr('Nothing you can run matches') : tr('Nothing to run yet')}>{q ? tr('Try another word.') : tr('Ask a template owner to give you the Execute role on what you need.')}</Empty>
        )}
        {names.map((name) => (
          <section key={name} style={{ marginBottom: 22 }} aria-label={name}>
            <h2 className="nav-label" style={{ padding: '0 0 8px' }}>
              {name}
            </h2>
            <div className="tiles">
              {groups.get(name)!.map((t) => {
                const kind = t.type === 'workflow_job_template' ? 'workflow' : 'job'
                const last = t.summary_fields.recent_jobs?.[0]
                return (
                  <Link key={`${t.type}:${t.id}`} className="tile" to={`/run/${kind}/${t.id}`}>
                    <span className="tile-title">{t.name}</span>
                    <span className="small muted tile-desc">{t.description || (kind === 'workflow' ? tr('Workflow') : tr('Playbook'))}</span>
                    <span className="small faint row" style={{ gap: 6, marginTop: 'auto' }}>
                      {last ? (
                        <>
                          <Status status={last.status} /> {last.finished ? ago(last.finished) : tr('running')}
                        </>
                      ) : (
                        tr('Never run')
                      )}
                    </span>
                  </Link>
                )
              })}
            </div>
          </section>
        ))}
      </div>
    </div>
  )
}

function LaunchForm({ kind, tpl, info, survey, operator }: { kind: Kind; tpl: Template; info: LaunchInfo; survey: Survey | null; operator: boolean }) {
  const res = resOf(kind)
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const d = info.defaults ?? {}
  const asks = (f: string) => !!info[`ask_${f}_on_launch`]
  const [answers, setAnswers] = useState<Record<string, unknown>>({})
  const [inventory, setInventory] = useState<Ref | null>(d.inventory?.id ? d.inventory : null)
  const [credentials, setCredentials] = useState<Ref[]>(d.credentials ?? [])
  const [ee, setEe] = useState<Ref | null>(d.execution_environment?.id ? d.execution_environment : null)
  const [labels, setLabels] = useState<Ref[]>(d.labels ?? [])
  const [igs, setIgs] = useState<Ref[]>(d.instance_groups ?? [])
  const [fields, setFields] = useState<Record<string, any>>({
    limit: d.limit ?? '',
    scm_branch: d.scm_branch ?? '',
    job_type: d.job_type ?? 'run',
    verbosity: d.verbosity ?? 0,
    forks: d.forks ?? 0,
    timeout: d.timeout ?? 0,
    job_slice_count: d.job_slice_count ?? 1,
    job_tags: d.job_tags ?? '',
    skip_tags: d.skip_tags ?? '',
    start_at_task: d.start_at_task ?? '',
    diff_mode: d.diff_mode ?? false,
    extra_vars: varsText(d.extra_vars ?? ''),
  })
  const [passwords, setPasswords] = useState<Record<string, string>>({})
  // Operators see what a run needs; the template's owners also get the
  // advanced settings, which others can open.
  const [showAll, setShowAll] = useState(!operator || !!tpl.summary_fields.user_capabilities?.edit)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const set = (k: string, v: unknown) => setFields((p) => ({ ...p, [k]: v }))

  const questions = survey?.spec ?? []
  const dynamic = useSurveyChoices(res, tpl.id, questions, asks('inventory') ? inventory?.id : null)
  const surveyErrors = validateSurvey(questions, answers, dynamic)
  const shownQuestions = visibleQuestions(questions, answers)
  const [touched, setTouched] = useState(false)
  const varsErr = asks('variables') ? varsError(fields.extra_vars) : null
  const missing: string[] = []
  if (asks('inventory') && !inventory && info.inventory_needed_to_start) missing.push('an inventory')
  for (const p of info.passwords_needed_to_start ?? []) if (!passwords[p]) missing.push(passwordLabel(p))
  const blocking = Object.keys(surveyErrors).length > 0 || !!varsErr || missing.length > 0

  // What the run will see.
  const inv = useObject<Base>(inventory ? `inventories/${inventory.id}/` : null)
  const invVars = useQuery({ queryKey: ['inventories/', inventory?.id, 'vars'], enabled: !!inventory, queryFn: () => api.get<Record<string, unknown>>(`inventories/${inventory!.id}/variable_data/`) })
  const project = useObject<Base>(kind === 'job' && tpl.project ? `projects/${tpl.project}/` : null)
  const recent = useList<Job>('unified_jobs/', { unified_job_template: tpl.id, status__in: 'successful,failed', order_by: '-id', page_size: 10 })
  const nodes = useList<Base>(kind === 'workflow' ? `workflow_job_templates/${tpl.id}/workflow_nodes/` : null, { page_size: 200 })
  const durations = (recent.data?.results ?? []).map((j) => j.elapsed).filter(Boolean).sort((a, b) => a - b)
  const median = durations.length ? durations[Math.floor(durations.length / 2)] : null
  const last = recent.data?.results[0]

  const varRows = useMemo(() => {
    type Row = { name: string; value: string; source: string; note?: string; secret?: boolean }
    const rows = new Map<string, Row>()
    const add = (vars: Record<string, unknown>, source: string, secret = new Set<string>()) => {
      for (const [name, value] of Object.entries(vars)) {
        const prev = rows.get(name)
        // An unchanged prefilled value still comes from where it was defined.
        if (prev && prev.value === (typeof value === 'string' ? value : JSON.stringify(value))) continue
        rows.set(name, { name, value: secret.has(name) ? '••••••••' : typeof value === 'string' ? value : JSON.stringify(value), source, secret: secret.has(name), note: prev && prev.value !== String(value) ? tr('overrides {slice} from {source}', { slice: JSON.stringify(prev.value).slice(0, 40), source: prev.source }) : prev?.note })
      }
    }
    add(invVars.data ?? {}, inventory ? tr('inventory {name}', { name: inventory.name }) : tr('inventory'))
    add(parseVars(varsText(tpl.extra_vars)), tr('template'))
    if (asks('variables')) add(parseVars(fields.extra_vars), tr('launch variables'))
    const secret = new Set(questions.filter((q) => q.type === 'password').map((q) => q.variable))
    const surveyVals: Record<string, unknown> = {}
    for (const q of visibleQuestions(questions, answers)) {
      const v = answers[q.variable] ?? q.default
      if (v !== undefined && v !== '') surveyVals[q.variable] = v
    }
    add(surveyVals, tr('your answer'), secret)
    const order = [tr('your answer'), tr('launch variables'), tr('template')]
    return [...rows.values()].sort((a, b) => {
      const ia = order.indexOf(a.source)
      const ib = order.indexOf(b.source)
      return (ia < 0 ? 9 : ia) - (ib < 0 ? 9 : ib) || a.name.localeCompare(b.name)
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [invVars.data, fields.extra_vars, answers, questions, tpl.extra_vars, inventory])

  const launch = async () => {
    setTouched(true)
    if (blocking) return
    setBusy(true)
    setError(null)
    const payload: Record<string, any> = {}
    const extra: Record<string, unknown> = asks('variables') ? parseVars(fields.extra_vars) : {}
    for (const q of shownQuestions) {
      const v = answers[q.variable] ?? q.default
      if (v !== undefined && v !== '') extra[q.variable] = q.type === 'multiselect' && typeof v === 'string' ? v.split('\n').filter(Boolean) : v
    }
    if (Object.keys(extra).length) payload.extra_vars = extra
    if (asks('inventory') && inventory) payload.inventory = inventory.id
    if (asks('credential')) payload.credentials = credentials.map((c) => c.id)
    if (asks('execution_environment') && ee) payload.execution_environment = ee.id
    if (asks('labels')) payload.labels = labels.map((l) => l.id)
    if (asks('instance_groups')) payload.instance_groups = igs.map((g) => g.id)
    for (const f of ['limit', 'scm_branch', 'job_type', 'verbosity', 'forks', 'timeout', 'job_slice_count', 'diff_mode']) if (asks(f)) payload[f] = fields[f]
    if (asks('tags')) payload.job_tags = fields.job_tags
    if (asks('skip_tags')) payload.skip_tags = fields.skip_tags
    if (asks('start_at_task')) payload.start_at_task = fields.start_at_task
    Object.assign(payload, passwords)
    try {
      const r = await api.post<{ id: number; type?: string; job?: number; workflow_job?: number }>(`${res}/${tpl.id}/launch/`, payload)
      invalidate('unified_jobs', 'unified_job_templates')
      nav(jobPath({ id: r.workflow_job ?? r.job ?? r.id, type: r.type ?? (kind === 'workflow' ? 'workflow_job' : 'job') }))
    } catch (e) {
      setError(e)
      setBusy(false)
    }
  }

  const prompts: ReactNode[] = []
  if (asks('inventory'))
    prompts.push(<Lookup key="inv" label={tr('Inventory')} resource="inventories/" value={inventory} onChange={setInventory} meta={(i) => `${i.total_hosts ?? 0} hosts`} />)
  if (asks('limit'))
    prompts.push(
      <div key="limit" className="field">
        <label htmlFor="lim">{tr('Limit')}</label>
        <input id="lim" className="input mono" value={fields.limit} onChange={(e) => set('limit', e.target.value)} placeholder={tr('all hosts')} style={{ maxWidth: 440 }} />
        <span className="help">{tr('Host pattern, e.g. web01,web02 or web:&eu')}</span>
      </div>,
    )
  if (asks('credential')) prompts.push(<MultiLookup key="cred" label={tr('Credentials')} resource="credentials/" values={credentials} onChange={setCredentials} meta={(c) => c.kind ?? ''} />)
  if (asks('start_at_task'))
    prompts.push(
      <div key="start" className="field">
        <label htmlFor="start-at">{tr('Start at task')}</label>
        <input id="start-at" className="input mono" value={fields.start_at_task} onChange={(e) => set('start_at_task', e.target.value)} placeholder={tr('the first task')} style={{ maxWidth: 440 }} />
        <span className="help">{tr('Name of the task to start from; empty runs the whole playbook.')}</span>
      </div>,
    )
  if (asks('scm_branch'))
    prompts.push(
      <div key="branch" className="field">
        <label htmlFor="br">{tr('Branch, tag or commit')}</label>
        <input id="br" className="input mono" value={fields.scm_branch} onChange={(e) => set('scm_branch', e.target.value)} style={{ maxWidth: 440 }} />
      </div>,
    )
  if (asks('job_type'))
    prompts.push(
      <fieldset key="jt">
        <legend>{tr('Mode')}</legend>
        <div className="segmented">
          {[
            ['run', tr('Run')],
            ['check', tr('Check only (dry run)')],
          ].map(([v, l]) => (
            <label key={v} className={fields.job_type === v ? 'on' : ''}>
              <input type="radio" name="jt" checked={fields.job_type === v} onChange={() => set('job_type', v)} />
              {l}
            </label>
          ))}
        </div>
      </fieldset>,
    )
  if (asks('execution_environment')) prompts.push(<Lookup key="ee" label={tr('Execution environment')} resource="execution_environments/" value={ee} onChange={setEe} placeholder={tr('Default')} />)
  if (asks('instance_groups')) prompts.push(<MultiLookup key="ig" label={tr('Instance groups')} resource="instance_groups/" values={igs} onChange={setIgs} ordered />)
  if (asks('labels')) prompts.push(<MultiLookup key="lab" label={tr('Labels')} resource="labels/" values={labels} onChange={setLabels} />)
  for (const [f, label] of [
    ['tags', tr('Tags')],
    ['skip_tags', tr('Skip tags')],
  ] as const)
    if (asks(f))
      prompts.push(
        <div key={f} className="field">
          <label htmlFor={f}>{label}</label>
          <input id={f} className="input mono" value={fields[f === 'tags' ? 'job_tags' : 'skip_tags']} onChange={(e) => set(f === 'tags' ? 'job_tags' : 'skip_tags', e.target.value)} style={{ maxWidth: 440 }} />
        </div>,
      )
  for (const [f, label, help] of [
    ['verbosity', tr('Verbosity'), '0 normal … 5 WinRM debug'],
    ['forks', tr('Forks'), '0 uses the default'],
    ['timeout', tr('Timeout (seconds)'), '0 means none'],
    ['job_slice_count', tr('Job slices'), 'parallel jobs over the inventory'],
  ] as const)
    if (asks(f))
      prompts.push(
        <div key={f} className="field">
          <label htmlFor={f}>{label}</label>
          <input id={f} className="input" type="number" min={0} max={f === 'verbosity' ? 5 : undefined} value={fields[f]} onChange={(e) => set(f, Number(e.target.value))} style={{ maxWidth: 160 }} />
          <span className="help">{help}</span>
        </div>,
      )
  if (asks('diff_mode')) prompts.push(<Check key="diff" label={tr('Show changes (--diff)')} checked={fields.diff_mode} onChange={(v) => set('diff_mode', v)} />)

  const hidden = operator && !showAll
  const essentialKeys = new Set(['inv', 'limit', 'branch', 'jt', ...(info.credential_needed_to_start ? ['cred'] : [])])
  const isEssential = (p: ReactNode) => isValidElement(p) && essentialKeys.has(String(p.key))
  const advanced = prompts.filter((p) => !isEssential(p))
  const hostCount = inv.data?.total_hosts as number | undefined
  const launchLabel = kind === 'workflow' ? tr('Launch workflow') : hostCount !== undefined ? tr('Launch on {hosts}', { hosts: fields.limit && asks('limit') ? tr('limited hosts') : plural(hostCount, 'host') }) : tr('Launch')
  const approvals = (nodes.data?.results ?? []).filter((n) => n.summary_fields?.unified_job_template?.unified_job_type === 'workflow_approval').length

  return (
    <main className="page-body" style={{ padding: '20px 28px 32px' }}>
      {!operator && (
        <div className="crumbs">
          <Link to="/templates">{tr('Templates')}</Link>
          <span aria-hidden="true">/</span>
          <Link to={`/templates/${kind}/${tpl.id}`}>{tpl.name}</Link>
          <span aria-hidden="true">/</span>
          <span className="here">{tr('Launch')}</span>
        </div>
      )}
      <div className="split" style={{ gap: 24 }}>
        <form
          className="panel wide"
          aria-labelledby="launch-title"
          style={{ flex: '3 1 460px' }}
          onSubmit={(e) => {
            e.preventDefault()
            launch()
          }}
        >
          <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-soft)' }}>
            <h1 id="launch-title" style={{ fontSize: 18 }}>
              {tpl.name}
            </h1>
            <p className="subtitle">{tpl.description || (kind === 'workflow' ? tr('Workflow') : tr('Runs {playbook}', { playbook: tpl.playbook }))}</p>
          </div>
          <div style={{ padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 20 }}>
            <FormError error={error} />
            {questions.length > 0 && (
              <SurveyFields questions={questions} values={answers} onChange={(v, x) => setAnswers((a) => ({ ...a, [v]: x }))} errors={touched ? surveyErrors : {}} dynamic={dynamic} />
            )}
            {(info.passwords_needed_to_start ?? []).map((p) => (
              <div key={p} className="field">
                <label htmlFor={p}>{passwordLabel(p)}</label>
                <input id={p} className="input" type="password" autoComplete="off" value={passwords[p] ?? ''} onChange={(e) => setPasswords((x) => ({ ...x, [p]: e.target.value }))} style={{ maxWidth: 320 }} />
                <span className="help">{tr('A credential of this template asks for it at launch; it is not stored.')}</span>
              </div>
            ))}
            {prompts.filter(isEssential)}
            {!hidden && advanced}
            {!hidden && asks('variables') && <VarsEditor label={tr('Extra variables')} value={fields.extra_vars} onChange={(v) => set('extra_vars', v)} rows={6} />}
            {questions.length === 0 && prompts.length === 0 && !asks('variables') && !(info.passwords_needed_to_start ?? []).length && (
              <p className="muted" style={{ margin: 0 }}>
                {tr('Nothing to answer: this template runs with its saved settings.')}
              </p>
            )}
            {operator && (advanced.length > 0 || asks('variables')) && (
              <p className="small muted" style={{ margin: 0, borderTop: '1px solid var(--line-soft)', paddingTop: 12 }}>
                {hidden ? tr('Credentials, environment and other settings are preset by the template owner. ') : ''}
                <button type="button" className="link-btn" onClick={() => setShowAll((s) => !s)}>
                  {hidden ? tr('Show advanced settings') : tr('Hide advanced settings')}
                </button>
              </p>
            )}
          </div>
        </form>

        <aside aria-label={tr('What will happen')} className="narrow stack" style={{ flex: '2 1 380px', gap: 16 }}>
          <Panel title={tr('What will happen')}>
            <ol style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 8, lineHeight: 1.45 }}>
              {kind === 'job' ? (
                <>
                  <li>
                    {fields.job_type === 'check' && asks('job_type') ? tr('Check (no changes) ') : tr('Run ')}
                    <span className="mono">{tpl.playbook}</span> {tr('from')}{' '}
                    <span className="mono">
                      {project.data?.name ?? '…'}
                      {(asks('scm_branch') && fields.scm_branch) || project.data?.scm_branch ? asIs('@' + ((asks('scm_branch') && fields.scm_branch) || project.data?.scm_branch)) : ''}
                      {project.data?.scm_revision ? tr(' ({shortSha})', { shortSha: shortSha(project.data.scm_revision as string) }) : ''}
                    </span>
                  </li>
                  <li>
                    {tr('On')} {inventory ? <span className="mono">{inventory.name}</span> : <b>{tr('no inventory yet')}</b>}
                    {hostCount !== undefined && tr(' · {count}', { count: plural(hostCount, 'host') })}
                    {asks('limit') && fields.limit ? <> {tr('limited to')} <span className="mono">{fields.limit}</span></> : tpl.limit ? <> {tr('limited to')} <span className="mono">{tpl.limit}</span></> : ''}
                  </li>
                  {credentials.length > 0 && <li>{tr('Connect with')} {credentials.map((c) => c.name).join(', ')}</li>}
                </>
              ) : (
                <>
                  <li>{tr('Run')} {plural(nodes.data?.count ?? 0, 'step')} {tr('in order, following success and failure paths')}</li>
                  {approvals > 0 && <li>{tr('Pause for')} {plural(approvals, 'approval')} {tr('before continuing')}</li>}
                  {inventory && <li>{tr('Use inventory')} <span className="mono">{inventory.name}</span> {tr('for every step that asks')}</li>}
                </>
              )}
            </ol>
            <dl className="kv small" style={{ marginTop: 14 }}>
              <dt>{tr('Takes')}</dt>
              <dd>{median ? tr('about {duration} (median of the last {count})', { duration: duration(median), count: durations.length }) : tr('no finished runs to estimate from')}</dd>
              <dt>{tr('Last run')}</dt>
              <dd>
                {last ? (
                  <Link to={jobPath(last)} className="row" style={{ gap: 6, display: 'inline-flex' }}>
                    <Status status={last.status} /> {ago(last.finished)}
                  </Link>
                ) : (
                  tr('never')
                )}
              </dd>
              {inv.data && (inv.data.hosts_with_active_failures as number) > 0 && (
                <>
                  <dt>{tr('Watch out')}</dt>
                  <dd className="fail-text">{plural(inv.data.hosts_with_active_failures as number, 'host')} {tr('failed in their last run')}</dd>
                </>
              )}
            </dl>
          </Panel>

          {kind === 'job' && (
            <section className="panel">
              <div className="panel-head">
                <h2>{tr('Variables this run will see')}</h2>
                <span className="small muted" style={{ marginLeft: 'auto' }} title={tr('Survey answers override launch variables, which override template extra variables, which override inventory variables.')}>
                  {tr('highest first')}
                </span>
              </div>
              {varRows.length ? (
                <table className="data small vars-table">
                  <thead>
                    <tr>
                      <th>{tr('name')}</th>
                      <th>{tr('value')}</th>
                      <th>{tr('set by')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {varRows.slice(0, 40).map((r) => (
                      <tr key={r.name}>
                        <td className="mono">{r.name}</td>
                        <td className="mono" style={{ maxWidth: 200, overflowWrap: 'anywhere', color: touched && surveyErrors[r.name] ? 'var(--fail-ink)' : undefined }}>
                          {r.value}
                        </td>
                        <td>
                          {r.source}
                          {r.note && <span className="faint" style={{ display: 'block', fontSize: 11 }}>{r.note}</span>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <p className="small muted" style={{ margin: 0, padding: 14 }}>
                  {tr('No variables beyond the playbook’s own defaults.')}
                </p>
              )}
            </section>
          )}

          <div className="stack" style={{ gap: 6 }}>
            <button className="btn primary lg block" disabled={busy || (touched && blocking)} onClick={launch}>
              <Icon name="play" size={12} />
              {busy ? tr('Launching…') : launchLabel}
            </button>
            {touched && blocking && (
              <span className="small fail-text" style={{ textAlign: 'center' }}>
                {missing.length ? tr('Provide {list} to launch.', { list: missing.join(', ') }) : varsErr ? tr('Fix the extra variables to launch.') : tr('Answer {count} to launch.', { count: plural(Object.keys(surveyErrors).length, 'question') })}
              </span>
            )}
            {statusKind(last?.status) === 'run' && !tpl.allow_simultaneous && <span className="small muted" style={{ textAlign: 'center' }}>{tr('A run is in progress; this one waits until it finishes.')}</span>}
          </div>
        </aside>
      </div>
    </main>
  )
}

