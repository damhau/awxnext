import { useQueries } from '@tanstack/react-query'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, ApiError, request } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Any, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { Check, Field, FormError, Lookup, MultiLookup, Segmented, SelectField, TextArea, TextInput } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog } from '../../components/overlay'
import { Ago, Cell, Panel } from '../../components/ui'
import { checkBody, conditionLabel, conditionsFor, draftOf, fieldErrors, hasConditions, mergeSchemas, PATTERNS, ruleBody, splitSeconds, UNITS, type MissDetails, type RuleDraft, type StreamSchema } from '../../lib/eventRules'
import { asIs, tr, trn } from '../../lib/i18n'
import { Backtest } from './backtest'
import { CelInput } from './CelInput'
import { MissView } from './decisions'

interface CheckedEvent {
  stream: number
  sequence: number
  event_id: string
  event_type: string
  source: string
  subject: string
  received: string
  test_mode: boolean
  matched?: boolean
  reason?: string
  filter?: MissDetails
  /** The pattern's conditions the event matches, from 0. */
  conditions?: number[]
  key?: string
  key_error?: string
  action?: Record<string, unknown>
  action_errors?: Record<string, string>
  /** The event matches the draft's check condition. */
  resolves?: boolean
}

interface CheckResult {
  schema_events: number
  errors: Record<string, string>
  checked: number
  matched: number
  truncated: boolean
  events: CheckedEvent[]
}

type RefKey = 'organization' | 'unified_job_template' | 'notification_template' | 'target_stream' | 'artifacts_stream' | 'breaker_notification_template' | 'check_template' | 'check_notification_template'

/** A number of seconds, as a number of a unit. */
function DurationInput({ label, seconds, onChange, help, error, optional }: { label: ReactNode; seconds: number; onChange: (s: number) => void; help?: ReactNode; error?: string; optional?: boolean }) {
  const [unit, setUnit] = useState(() => splitSeconds(seconds).unit)
  const n = seconds / unit
  return (
    <Field label={label} help={help} error={error} optional={optional}>
      <div className="row" style={{ gap: 6 }}>
        <input className="input" style={{ flex: '1 1 90px' }} type="number" min={0} aria-label={tr('Number')} value={Number.isInteger(n) ? n : Number(n.toFixed(2))} onChange={(e) => onChange(Math.round(Number(e.target.value) * unit))} />
        <select
          className="select"
          style={{ flex: '1 1 110px' }}
          aria-label={tr('Unit')}
          value={unit}
          onChange={(e) => {
            const u = Number(e.target.value)
            setUnit(u)
            onChange(Math.round(n * u))
          }}
        >
          {UNITS.map(([u, l]) => (
            <option key={u} value={u}>
              {l}
            </option>
          ))}
        </select>
      </div>
    </Field>
  )
}

const pretty = (v: unknown) => JSON.stringify(v, null, 2)

/** The editor of a rule: its expressions checked as they are written, and
 * run on the latest events of its streams. */
export function RuleEditor({ rule }: { rule?: Any }) {
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const { org } = useOrg()
  const [params] = useSearchParams()
  const presetId = !rule ? Number(params.get('stream')) || null : null
  const preset = useObject<Any>(presetId ? `event_streams/${presetId}/` : null)
  const [d, setD] = useState<RuleDraft>(() => draftOf(rule))
  const sf = rule?.summary_fields ?? {}
  const asRef = (id: number | null | undefined, ref?: Ref): Ref | null => (id ? (ref ?? { id, name: `#${id}` }) : null)
  const [refs, setRefs] = useState<Record<RefKey, Ref | null>>(() => ({
    organization: asRef(rule?.organization, sf.organization) ?? org,
    unified_job_template: asRef(rule?.unified_job_template, sf.unified_job_template),
    notification_template: asRef(rule?.notification_template, sf.notification_template),
    target_stream: asRef(rule?.target_stream, sf.target_stream),
    artifacts_stream: asRef(rule?.artifacts_stream, sf.artifacts_stream),
    breaker_notification_template: asRef(rule?.breaker_notification_template, sf.breaker_notification_template),
    check_template: asRef(rule?.check_template, sf.check_template),
    check_notification_template: asRef(rule?.check_notification_template, sf.check_notification_template),
  }))
  const [streams, setStreams] = useState<Ref[]>(() => (rule?.streams ?? []).map((id: number) => (sf.streams as Ref[] | undefined)?.find((s) => s.id === id) ?? { id, name: `#${id}` }))
  const [saveErrors, setSaveErrors] = useState<Record<string, string>>({})
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [backtest, setBacktest] = useState(false)

  const set = <K extends keyof RuleDraft>(k: K, v: RuleDraft[K]) => {
    setD((prev) => ({ ...prev, [k]: v }))
    setSaveErrors((e) => (e[k as string] ? { ...e, [k as string]: '' } : e))
  }
  const setRef = (k: RefKey, v: Ref | null) => {
    setRefs((r) => ({ ...r, [k]: v }))
    set(k, v?.id ?? null)
  }
  const setStreamRefs = (list: Ref[]) => {
    setStreams(list)
    set(
      'streams',
      list.map((s) => s.id),
    )
  }
  usePreselectOrg(refs.organization, (o) => setRef('organization', o), !rule)
  useEffect(() => {
    if (!preset.data || d.streams.length) return
    setStreamRefs([{ id: preset.data.id, name: preset.data.name }])
    if (preset.data.summary_fields?.organization) setRef('organization', preset.data.summary_fields.organization)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preset.data])
  useEffect(() => {
    if (refs.organization && d.organization !== refs.organization.id) set('organization', refs.organization.id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refs.organization])

  const schemas = useQueries({
    queries: d.streams.map((id) => ({ queryKey: [`event_streams/${id}/schema/`], queryFn: () => api.get<StreamSchema>(`event_streams/${id}/schema/`), staleTime: 30_000 })),
  })
  const schemaKey = schemas.map((q) => q.dataUpdatedAt).join(',')
  const schema = useMemo(
    () => mergeSchemas(schemas.map((q) => q.data).filter((x): x is StreamSchema => !!x)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [schemaKey],
  )

  const body = JSON.stringify(checkBody(d))
  const [check, setCheck] = useState<CheckResult | null>(null)
  const [checkError, setCheckError] = useState<unknown>(null)
  const [checking, setChecking] = useState(false)
  useEffect(() => {
    if (!d.streams.length) {
      setCheck(null)
      return
    }
    const ctl = new AbortController()
    const t = window.setTimeout(async () => {
      setChecking(true)
      try {
        setCheck(await request<CheckResult>('POST', 'event_rules/check/', JSON.parse(body), { signal: ctl.signal }))
        setCheckError(null)
      } catch (e) {
        if (!ctl.signal.aborted) setCheckError(e)
      } finally {
        if (!ctl.signal.aborted) setChecking(false)
      }
    }, 400)
    return () => {
      window.clearTimeout(t)
      ctl.abort()
    }
  }, [body, d.streams.length])

  const errOf = (field: string) => saveErrors[field] || check?.errors[field] || undefined
  const firstMatch = check?.events.find((e) => e.matched)
  const launch = d.action === 'launch'
  // An imported rule's conditions and action are its rulebook's: the API
  // refuses to change them, so the editor doesn't offer to.
  const imported = !!rule?.rulebook
  const approvalAsked = d.mode === 'approval' || d.window_action === 'approval'

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const b = ruleBody(d)
      const saved = rule ? await api.patch<Any>(`event_rules/${rule.id}/`, b) : await api.post<Any>('event_rules/', b)
      invalidate('event_rules')
      nav(`/event-rules/${saved.id}`)
    } catch (e) {
      setError(e)
      if (e instanceof ApiError && e.body && typeof e.body === 'object') setSaveErrors(fieldErrors(e.body as Record<string, unknown>))
    } finally {
      setBusy(false)
    }
  }
  const setPattern = (p: string) => {
    set('pattern', p)
    if (hasConditions(p)) set('conditions', conditionsFor(p, d.conditions))
  }
  const setCondition = (i: number, v: string) =>
    set(
      'conditions',
      d.conditions.map((x, j) => (j === i ? v : x)),
    )
  const setVar = (i: number, k: string, v: string) =>
    set(
      'vars',
      d.vars.map((x, j) => (j === i ? [k, v] : x)),
    )

  return (
    <div className="rule-editor">
      <form
        className="form"
        onSubmit={(ev) => {
          ev.preventDefault()
          save()
        }}
      >
        {imported && (
          <div className="notice">
            {tr('This rule comes from a rulebook: change its streams, conditions and action there, and import it again. Its description, mode and guardrails are yours.')}{' '}
            <Link to={`/rulebooks/${rule.rulebook}`}>{sf.rulebook?.name ?? asIs(`#${rule.rulebook}`)}</Link>
          </div>
        )}
        <fieldset disabled={imported} style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        <div className="form-grid">
          <TextInput label={tr('Name')} value={d.name} onChange={(v) => set('name', v)} error={saveErrors.name} autoFocus={!rule} />
          <Lookup label={tr('Organization')} resource="organizations/" value={refs.organization} onChange={(v) => setRef('organization', v)} error={saveErrors.organization} />
          <MultiLookup
            label={tr('Streams')}
            resource="event_streams/"
            values={streams}
            onChange={setStreamRefs}
            error={saveErrors.streams}
            full
            meta={(r) => (r.test_mode ? <span className="tag">{tr('test mode')}</span> : <span className="faint small">{r.stream_type}</span>)}
            help={schema ? trn(schema.events, 'Completion and types come from the streams’ latest event.', 'Completion and types come from the streams’ {n} latest events.') : tr('The events the rule reads. Completion and types come from their latest events.')}
          />
          {!imported && <TextArea label={tr('Description')} optional rows={2} value={d.description} onChange={(v) => set('description', v)} />}
        </div>

        <section className="form-section">
          <h2>{tr('When')}</h2>
          <CelInput
            label={tr('Filter')}
            value={d.filter}
            onChange={(v) => set('filter', v)}
            schema={schema}
            rows={3}
            error={errOf('filter')}
            placeholder={tr('event.type == "alert.firing" && event.data.labels.severity == "critical"')}
            help={tr('A CEL condition on event. Type a dot after event for its fields; Ctrl+Space completes.')}
          />
          <div className="form-grid">
            <CelInput
              label={tr('Key')}
              optional
              full={false}
              value={d.key}
              onChange={(v) => set('key', v)}
              schema={schema}
              error={errOf('key')}
              placeholder={tr('event.data.labels.instance')}
              help={d.pattern ? tr('A string the pattern’s windows and the circuit breaker count events by. Empty: one window for all.') : tr('A string the throttle and the circuit breaker count events by. Empty: one key for all.')}
            />
            <SelectField label={tr('Pattern')} value={d.pattern} onChange={setPattern} error={saveErrors.pattern} options={PATTERNS} help={tr('What the rule waits for, per key, before it acts once with all the events.')} />
            {d.pattern === 'count' && (
              <TextInput label={tr('Events')} type="number" value={d.pattern_count} onChange={(v) => set('pattern_count', Number(v))} error={saveErrors.pattern_count} help={tr('The rule fires on the last one, and counts again from 0.')} />
            )}
            {d.pattern && (
              <DurationInput
                label={tr('Within')}
                seconds={d.pattern_window}
                onChange={(v) => set('pattern_window', v)}
                error={saveErrors.pattern_window}
                help={d.pattern === 'absence' ? tr('From the first event: the rule fires at the end if nothing followed it.') : tr('Measured by when awxnext received each event.')}
              />
            )}
            {!d.pattern && (
              <SelectField
                label={tr('Throttle')}
                value={d.throttle}
                onChange={(v) => set('throttle', v)}
                error={saveErrors.throttle}
                options={[
                  ['', tr('None: act on every event')],
                  ['once_within', tr('Once within: the first event of a key, then none for the window')],
                  ['once_after', tr('Once after: once at the end of the window, for its first event')],
                ]}
              />
            )}
            {!d.pattern && d.throttle && <DurationInput label={tr('Throttle window')} seconds={d.throttle_window} onChange={(v) => set('throttle_window', v)} error={saveErrors.throttle_window} />}
          </div>
          {hasConditions(d.pattern) && (
            <Field
              label={d.pattern === 'sequence' ? tr('Steps') : tr('Conditions')}
              full
              error={saveErrors.conditions || check?.errors.conditions}
              help={
                d.pattern === 'all'
                  ? tr('CEL conditions on event, after the filter. The rule fires once an event of each came, in any order.')
                  : d.pattern === 'sequence'
                    ? tr('CEL conditions on event, after the filter. The rule fires when an event of each came, in this order.')
                    : tr('CEL conditions on event, after the filter. The rule fires at the end of the window if no event of the second came after the first.')
              }
            >
              <div className="stack" style={{ gap: 8 }}>
                {d.conditions.map((c, i) => (
                  <div key={i} className="cond-row">
                    <span className="cond-label small muted">{conditionLabel(d.pattern, i)}</span>
                    <CelInput ariaLabel={conditionLabel(d.pattern, i)} value={c} onChange={(v) => setCondition(i, v)} schema={schema} error={errOf(`conditions.${i}`)} placeholder={tr('event.type == "alert.firing"')} />
                    {d.pattern !== 'absence' && d.conditions.length > 2 ? (
                      <button type="button" className="btn ghost icon" aria-label={tr('Remove {name}', { name: conditionLabel(d.pattern, i) })} onClick={() => set('conditions', d.conditions.filter((_, j) => j !== i))}>
                        <Icon name="x" size={14} />
                      </button>
                    ) : (
                      <span />
                    )}
                  </div>
                ))}
                {d.pattern !== 'absence' && d.conditions.length < 10 && (
                  <div>
                    <button type="button" className="btn sm" onClick={() => set('conditions', [...d.conditions, ''])}>
                      <Icon name="plus" size={12} />
                      {d.pattern === 'sequence' ? tr('Add step') : tr('Add condition')}
                    </button>
                  </div>
                )}
              </div>
            </Field>
          )}
          {d.pattern && <p className="small muted" style={{ margin: 0 }}>{tr('A window keeps its events in the database: it survives a restart. An event received after its window ended counts for nothing. The action gets the first event as event, and all of them as events.')}</p>}
        </section>

        <section className="form-section">
          <h2>{tr('Action')}</h2>
          <Segmented
            legend={tr('The rule')}
            value={d.action}
            onChange={(v) => set('action', v)}
            options={[
              ['launch', tr('Launches a template')],
              ['notify', tr('Notifies')],
              ['post_event', tr('Posts an event')],
              ['log', tr('Logs a message')],
            ]}
          />
          {d.action === 'launch' && (
            <>
              <div className="form-grid">
                <Lookup
                  label={tr('Template')}
                  resource="unified_job_templates/"
                  params={{ type__in: 'job_template,workflow_job_template' }}
                  value={refs.unified_job_template}
                  onChange={(v) => setRef('unified_job_template', v)}
                  error={saveErrors.unified_job_template}
                  meta={(r) => <span className="faint small">{r.type === 'workflow_job_template' ? tr('workflow') : tr('job')}</span>}
                  help={tr('It must prompt for the variables and the limit the rule gives.')}
                />
                <CelInput label={tr('Limit')} optional full={false} value={d.limit} onChange={(v) => set('limit', v)} schema={schema} error={errOf('limit')} placeholder={tr('event.data.labels.instance')} help={tr('A CEL expression giving a host pattern.')} />
              </div>
              <Field label={tr('Variables')} optional full help={tr('Each variable’s value is a CEL expression on the event.')} error={saveErrors.extra_vars}>
                <div className="stack" style={{ gap: 8 }}>
                  {d.vars.map(([k, v], i) => (
                    <div key={i} className="var-row">
                      <input className="input mono" aria-label={tr('Variable name')} placeholder={tr('name')} value={k} onChange={(e) => setVar(i, e.target.value, v)} />
                      <CelInput ariaLabel={tr('Value of {name}', { name: k || tr('the variable') })} value={v} onChange={(x) => setVar(i, k, x)} schema={schema} error={errOf(`extra_vars.${k.trim()}`)} placeholder={tr('event.data.labels.instance')} />
                      <button type="button" className="btn ghost icon" aria-label={tr('Remove variable')} onClick={() => set('vars', d.vars.filter((_, j) => j !== i))}>
                        <Icon name="x" size={14} />
                      </button>
                    </div>
                  ))}
                  <div>
                    <button type="button" className="btn sm" onClick={() => set('vars', [...d.vars, ['', '']])}>
                      <Icon name="plus" size={12} />
                      {tr('Add variable')}
                    </button>
                  </div>
                </div>
              </Field>
              <div className="form-grid">
                <TextInput label={tr('Retries')} type="number" value={d.retries} onChange={(v) => set('retries', Number(v))} error={saveErrors.retries} help={tr('Launch a job that failed again, up to this many times. 0: never.')} />
                {d.retries > 0 && <DurationInput label={tr('After')} seconds={d.retry_delay} onChange={(v) => set('retry_delay', v)} error={saveErrors.retry_delay} help={tr('From when the failed job ended.')} />}
                <SelectField
                  label={tr('Post the job’s artifacts')}
                  value={d.post_artifacts}
                  onChange={(v) => set('post_artifacts', v)}
                  error={saveErrors.post_artifacts}
                  options={[
                    ['', tr('No')],
                    ['event', tr('As an event (post_event)')],
                    ['fact', tr('As a fact (set_fact)')],
                  ]}
                  help={tr('Once the job ends, what it set with set_stats, for other rules to read.')}
                />
                {d.post_artifacts && <Lookup label={tr('To the stream')} optional resource="event_streams/" value={refs.artifacts_stream} onChange={(v) => setRef('artifacts_stream', v)} error={saveErrors.artifacts_stream} help={tr('Empty: the rule’s first stream.')} />}
              </div>
            </>
          )}
          {d.action === 'notify' && (
            <div className="form-grid">
              <Lookup label={tr('Notification')} resource="notification_templates/" value={refs.notification_template} onChange={(v) => setRef('notification_template', v)} error={saveErrors.notification_template} />
              <CelInput label={tr('Message')} optional full={false} value={d.message} onChange={(v) => set('message', v)} schema={schema} error={errOf('message')} placeholder={tr('"Disk full on " + event.data.labels.instance')} />
            </div>
          )}
          {d.action === 'log' && (
            <div className="form-grid">
              <CelInput label={tr('Message')} full={false} value={d.message} onChange={(v) => set('message', v)} schema={schema} error={errOf('message')} placeholder={tr('"Disk full on " + event.data.labels.instance')} help={tr('A CEL expression; each decision records its value.')} />
            </div>
          )}
          {d.action === 'post_event' && (
            <div className="form-grid">
              <Lookup label={tr('To the stream')} resource="event_streams/" value={refs.target_stream} onChange={(v) => setRef('target_stream', v)} error={saveErrors.target_stream} help={tr('Not one the rule reads.')} />
              <CelInput label={tr('Event type')} full={false} value={d.event_type} onChange={(v) => set('event_type', v)} schema={schema} error={errOf('event_type')} placeholder={tr('"disk.full"')} />
              <CelInput label={tr('Event data')} optional value={d.event_data} onChange={(v) => set('event_data', v)} schema={schema} error={errOf('event_data')} help={tr('Empty: the event’s own data.')} />
            </div>
          )}
          <ActionPreview event={firstMatch} action={d.action} />
        </section>
        </fieldset>
        {imported && (
          <div className="form-grid">
            <TextArea label={tr('Description')} optional rows={2} value={d.description} onChange={(v) => set('description', v)} />
          </div>
        )}

        <section className="form-section">
          <h2>{tr('Mode')}</h2>
          <Segmented
            legend={<span className="sr-only">{tr('Mode')}</span>}
            value={d.mode}
            onChange={(v) => set('mode', v)}
            options={[
              ['shadow', tr('Shadow')],
              ['approval', tr('Approval')],
              ['auto', tr('Auto')],
            ]}
          />
          <p className="small muted" style={{ margin: 0 }}>
            {d.mode === 'shadow'
              ? tr('The rule records what it would do, and does nothing. Its decisions show whether it can be trusted.')
              : d.mode === 'approval'
                ? tr('Each action waits for someone with the rule’s Approve role, on the Approvals page.')
                : tr('The rule acts by itself. Putting it in auto mode takes the rule’s Automate role.')}
          </p>
          {approvalAsked && <DurationInput label={tr('An approval waits')} seconds={d.approval_timeout} onChange={(v) => set('approval_timeout', v)} error={saveErrors.approval_timeout} help={tr('0: until someone decides.')} />}
          {saveErrors.mode && <div className="form-error">{saveErrors.mode}</div>}
        </section>

        <section className="form-section">
          <h2>{tr('Check the fix')}</h2>
          <Segmented
            legend={<span className="sr-only">{tr('Check the fix')}</span>}
            value={d.check}
            onChange={(v) => set('check', v)}
            options={[
              ['', tr('No check')],
              ['event', tr('Wait for a resolving event')],
              ['template', tr('Run a check template')],
            ]}
          />
          <p className="small muted" style={{ margin: 0 }}>
            {d.check === 'event'
              ? tr('Once the action finished, the rule waits for an event that resolves the same key, such as the alert resolved. None within the time: not fixed. In shadow mode, it only records whether the key resolved by itself.')
              : d.check === 'template'
                ? tr('Once the action finished, the rule launches the check template, and its job decides: successful is fixed, failed is not fixed. Shadow mode launches nothing.')
                : tr('The rule acts and records no outcome. A check tells whether the action fixed the problem, and gives the rule a success rate.')}
          </p>
          {d.check === 'event' && (
            <div className="form-grid">
              <CelInput
                label={tr('Resolving event')}
                value={d.check_condition}
                onChange={(v) => set('check_condition', v)}
                schema={schema}
                error={errOf('check_condition')}
                placeholder={tr('event.type == "alert.resolved"')}
                help={tr('A CEL condition on event. The rule’s key, evaluated on it, names the key it resolves. The filter doesn’t apply to it.')}
              />
              <DurationInput label={tr('Wait for it')} seconds={d.check_window} onChange={(v) => set('check_window', v)} error={saveErrors.check_window} help={tr('From when the action finished.')} />
            </div>
          )}
          {d.check === 'template' && (
            <div className="form-grid">
              <Lookup
                label={tr('Check template')}
                resource="unified_job_templates/"
                params={{ type__in: 'job_template,workflow_job_template' }}
                value={refs.check_template}
                onChange={(v) => setRef('check_template', v)}
                error={saveErrors.check_template}
                meta={(r) => <span className="faint small">{r.type === 'workflow_job_template' ? tr('workflow') : tr('job')}</span>}
                help={tr('Launched with the action’s variables and limit. A job that sets the fixed artifact, with set_stats, decides by it instead.')}
              />
              <DurationInput label={tr('Launch it after')} seconds={d.check_window} onChange={(v) => set('check_window', v)} error={saveErrors.check_window} help={tr('From when the action finished. 0: at once.')} />
            </div>
          )}
          {d.check && (
            <>
              <div className="form-grid">
                <Lookup
                  label={tr('Tell when a fix doesn’t work')}
                  optional
                  resource="notification_templates/"
                  value={refs.check_notification_template}
                  onChange={(v) => setRef('check_notification_template', v)}
                  error={saveErrors.check_notification_template}
                  help={tr('When a check finds the key not fixed, or the action failed.')}
                />
              </div>
              <Check
                label={tr('Ask for approval before acting again on a key a fix didn’t fix')}
                checked={d.check_approval}
                onChange={(v) => set('check_approval', v)}
                help={d.mode === 'auto' ? tr('Until a check finds the key fixed. Each failed fix also counts as a firing for the circuit breaker.') : tr('In auto mode only. Each failed fix also counts as a firing for the circuit breaker.')}
              />
              {saveErrors.check_approval && <div className="form-error">{saveErrors.check_approval}</div>}
            </>
          )}
          {saveErrors.check && <div className="form-error">{saveErrors.check}</div>}
        </section>

        <section className="form-section">
          <h2>{tr('Guardrails')}</h2>
          <div className="form-grid">
            <TextInput
              label={tr('Circuit breaker: firings')}
              type="number"
              value={d.breaker_firings}
              onChange={(v) => set('breaker_firings', Number(v))}
              error={saveErrors.breaker_firings}
              help={tr('Stop acting on a key after this many firings within the window, until someone resets it. 0: no breaker.')}
            />
            {d.breaker_firings > 0 && <DurationInput label={tr('Within')} seconds={d.breaker_window} onChange={(v) => set('breaker_window', v)} error={saveErrors.breaker_window} />}
            {d.breaker_firings > 0 && (
              <Lookup label={tr('Tell when it trips')} optional resource="notification_templates/" value={refs.breaker_notification_template} onChange={(v) => setRef('breaker_notification_template', v)} error={saveErrors.breaker_notification_template} />
            )}
            <SelectField
              label={tr('In a maintenance window or change freeze')}
              value={d.window_action}
              onChange={(v) => set('window_action', v)}
              error={saveErrors.window_action}
              options={[
                ['hold', tr('Hold the action back')],
                ['approval', tr('Ask for approval')],
              ]}
              help={
                <>
                  {tr('The windows are on their own page.')} <Link to="/maintenance-windows">{tr('Maintenance windows')}</Link>
                </>
              }
            />
            {launch && (
              <TextInput
                label={tr('Concurrent runs at most')}
                type="number"
                value={d.max_concurrent_runs}
                onChange={(v) => set('max_concurrent_runs', Number(v))}
                error={saveErrors.max_concurrent_runs}
                help={tr('Of the rule’s launches at once. 0: no limit.')}
              />
            )}
            {launch && (
              <TextInput
                label={tr('Share of a group at most (%)')}
                type="number"
                value={d.max_group_share}
                onChange={(v) => set('max_group_share', Number(v))}
                error={saveErrors.max_group_share}
                help={tr('Of the hosts of any inventory group that the rule’s running jobs target. 0: no limit.')}
              />
            )}
          </div>
          {launch && <Check label={tr('Hold back a launch on a host that a running job uses')} checked={d.host_lock} onChange={(v) => set('host_lock', v)} />}
          <Check label={tr('Enabled')} checked={d.enabled} onChange={(v) => set('enabled', v)} help={tr('A disabled rule reads nothing. Saving an enabled rule checks that its owner, you, may run its action.')} />
        </section>

        <FormError error={error} />
        <div className="form-actions">
          <button className="btn primary" disabled={busy || !d.name.trim() || !d.organization || !d.streams.length}>
            {busy ? tr('Saving…') : rule ? tr('Save changes') : tr('Create rule')}
          </button>
          <button type="button" className="btn" disabled={!d.streams.length} onClick={() => setBacktest(true)}>
            {tr('Backtest the draft…')}
          </button>
          <button type="button" className="btn ghost" onClick={() => nav(rule ? `/event-rules/${rule.id}` : '/event-rules')}>
            {tr('Cancel')}
          </button>
        </div>
      </form>
      <aside className="draft" aria-label={tr('The draft on the latest events')}>
        <DraftPane d={d} check={check} checking={checking} error={checkError} />
      </aside>
      {backtest && (
        <Dialog title={tr('Backtest of the draft')} wide onClose={() => setBacktest(false)}>
          <Backtest path="event_rules/backtest/" draft={() => ruleBody(d)} filter={d.filter} conditions={d.conditions} />
        </Dialog>
      )}
    </div>
  )
}

/** What the action would get from the latest matching event. */
function ActionPreview({ event, action }: { event?: CheckedEvent; action: string }) {
  if (!event?.action) return null
  const a = event.action
  const errs = Object.entries(event.action_errors ?? {})
  const shown: [string, unknown][] =
    action === 'launch'
      ? [
          [tr('Variables'), a.extra_vars ?? {}],
          [tr('Limit'), a.limit],
        ]
      : action === 'notify' || action === 'log'
        ? [[tr('Message'), a.message]]
        : [
            [tr('Event type'), a.event_type],
            [tr('Event data'), a.event_data],
          ]
  return (
    <div className="action-preview">
      <div className="small muted">
        {tr('What the action gets from the latest matching event, #{seq}, received', { seq: event.sequence })} <Ago at={event.received} />
      </div>
      <dl className="kv">
        {shown
          .filter(([, v]) => v !== undefined && v !== '')
          .map(([k, v]) => (
            <div key={k} style={{ display: 'contents' }}>
              <dt>{k}</dt>
              <dd>
                <pre className="code">{typeof v === 'string' ? v : pretty(v)}</pre>
              </dd>
            </div>
          ))}
      </dl>
      {errs.map(([k, m]) => (
        <div key={k} className="small fail-text">
          <span className="mono">{k}</span>: {m}
        </div>
      ))}
    </div>
  )
}

/** The draft run on its streams' latest events, test mode's included. */
function DraftPane({ d, check, checking, error }: { d: RuleDraft; check: CheckResult | null; checking: boolean; error: unknown }) {
  const filterError = !!check?.errors.filter
  return (
    <Panel
      title={tr('On the latest events')}
      actions={checking ? <span className="small faint">{tr('Checking…')}</span> : check ? <span className="small mono">{tr('{matched} of {checked} match', { matched: check.matched, checked: check.checked })}</span> : null}
    >
      {!d.streams.length ? (
        <p className="small muted" style={{ margin: 0 }}>
          {tr('Choose a stream: the draft runs on its latest events as you write it, test mode’s included.')}
        </p>
      ) : error ? (
        <div className="form-error">{(error as Error).message}</div>
      ) : !check ? (
        <p className="small faint" style={{ margin: 0 }}>
          {tr('Checking…')}
        </p>
      ) : !check.events.length ? (
        <p className="small muted" style={{ margin: 0 }}>
          {tr('The streams have no events yet. Send one, in test mode if the rules must not see it, and it appears here.')}
        </p>
      ) : (
        <div className="stack" style={{ gap: 8 }}>
          {filterError && <div className="notice warn small">{tr('Fix the filter to see which events match.')}</div>}
          {!d.filter.trim() && <div className="notice small">{tr('Write a filter to see which events match.')}</div>}
          {check.truncated && <div className="notice warn small">{tr('The check stopped early: the draft took too long.')}</div>}
          <div className="draft-events" role="list">
            {check.events.map((e) => (
              <details key={`${e.stream}/${e.sequence}`} className="draft-event" role="listitem">
                <summary>
                  <Cell state={e.matched === undefined ? 'empty' : e.matched ? 'changed' : 'none'} label={e.matched === undefined ? tr('Not checked') : e.matched ? tr('Matches') : tr('No match')} />
                  <span className="mono faint small">#{e.sequence}</span>
                  <span className="mono small ellipsis">{e.event_type}</span>
                  {(e.matched || e.resolves) && e.key !== undefined && <span className="mono small faint ellipsis">{e.key || tr('one key')}</span>}
                  {e.conditions?.map((c) => (
                    <span key={c} className="tag" title={conditionLabel(d.pattern, c)}>
                      {d.pattern === 'sequence' ? tr('step {n}', { n: c + 1 }) : asIs(String(c + 1))}
                    </span>
                  ))}
                  {e.resolves && <span className="tag">{tr('resolves')}</span>}
                  {e.test_mode && <span className="tag">{tr('test')}</span>}
                </summary>
                <div className="stack" style={{ gap: 6 }}>
                  <div className="small faint">
                    {e.subject || e.source} · <Ago at={e.received} /> ·{' '}
                    <Link to={`/event-streams/${e.stream}/events/${e.sequence}`} target="_blank" rel="noreferrer">
                      {tr('Open')}
                    </Link>
                  </div>
                  {e.resolves && <div className="small">{e.key ? tr('It resolves the key {key}: a check of that key would find it fixed.', { key: e.key }) : tr('It resolves the rule’s one key: a check would find it fixed.')}</div>}
                  {e.matched === false && (
                    <>
                      {e.reason && <div className="small">{e.reason}</div>}
                      <MissView miss={e.filter} filter={d.filter} />
                    </>
                  )}
                  {e.matched && (
                    <>
                      {e.key_error && <div className="small fail-text">{tr('Key: {error}', { error: e.key_error })}</div>}
                      {e.action && Object.keys(e.action).length > 0 && <pre className="code small">{pretty(e.action)}</pre>}
                      {Object.entries(e.action_errors ?? {}).map(([k, m]) => (
                        <div key={k} className="small fail-text">
                          <span className="mono">{k}</span>: {m}
                        </div>
                      ))}
                    </>
                  )}
                </div>
              </details>
            ))}
          </div>
        </div>
      )}
    </Panel>
  )
}
