import { useQuery } from '@tanstack/react-query'
import { useEffect, useId, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError } from '../../api/client'
import type { Any, Ref } from '../../api/types'
import { Check, Field, FormError, Lookup, parseVars, SelectField, TextInput, VarsEditor, varsText } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, useToast } from '../../components/overlay'
import { useInvalidate } from '../../api/hooks'
import { typeLabel } from '../../lib/format'
import './automation.css'
import { browserZone, DAY_NAMES, DAYS, defaultModel, describe, MONTHS, parseRRule, toRRule, type Day, type Freq, type RuleModel } from './rrule'

const FREQS: [Freq, string, string][] = [
  ['once', 'Once', ''],
  ['MINUTELY', 'Minutes', 'minutes'],
  ['HOURLY', 'Hours', 'hours'],
  ['DAILY', 'Daily', 'days'],
  ['WEEKLY', 'Weekly', 'weeks'],
  ['MONTHLY', 'Monthly', 'months'],
  ['YEARLY', 'Yearly', 'years'],
]

function useZones() {
  return useQuery({
    queryKey: ['schedules/zoneinfo/'],
    queryFn: () => api.get<{ name: string; links?: string[] }[]>('schedules/zoneinfo/'),
    staleTime: Infinity,
  })
}

/** Next occurrences of a rule, computed by the server (same engine as the scheduler). */
function usePreview(rrule: string) {
  const [debounced, setDebounced] = useState(rrule)
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(rrule), 250)
    return () => window.clearTimeout(t)
  }, [rrule])
  return useQuery({
    queryKey: ['schedules/preview/', debounced],
    queryFn: () => api.post<{ local: string[]; utc: string[] }>('schedules/preview/', { rrule: debounced }),
    enabled: debounced.trim().length > 0,
    retry: false,
    staleTime: 60_000,
  })
}

const occurrence = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })

function formatIn(iso: string, tz: string): string {
  try {
    return new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', timeZone: tz }).format(new Date(iso))
  } catch {
    return occurrence.format(new Date(iso))
  }
}

/**
 * Edits schedule rrule text with a form for the common shapes (once,
 * every N minutes/hours/days/weeks/months/years, end conditions, skipped
 * dates) and falls back to raw text for anything else.
 */
export function RRuleEditor({ value, onChange, error }: { value: string; onChange: (v: string) => void; error?: string }) {
  const parsed = useMemo(() => (value ? parseRRule(value) : { model: defaultModel() }), [value])
  const [advanced, setAdvanced] = useState(!parsed.model)
  const [text, setText] = useState(value)
  const model = parsed.model
  const zones = useZones()
  const preview = usePreview(value)
  const ids = { every: useId(), start: useId(), time: useId(), tz: useId(), zones: useId(), count: useId(), until: useId(), ex: useId(), text: useId() }
  const [exDraft, setExDraft] = useState('')

  useEffect(() => {
    // Seed a new schedule with a rule so preview and save have something to work with.
    if (!value) onChange(toRRule(defaultModel()))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => setText(value), [value])

  const set = (patch: Partial<RuleModel>) => {
    if (!model) return
    onChange(toRRule({ ...model, ...patch }))
  }
  const unit = FREQS.find((f) => f[0] === model?.freq)?.[2] ?? ''
  const browserTz = browserZone()
  const previewError = preview.error instanceof ApiError ? preview.error.message : null

  return (
    <div className="rr">
      {!advanced && model ? (
        <>
          <fieldset>
            <legend>Repeats</legend>
            <div className="segmented">
              {FREQS.map(([f, label]) => (
                <label key={f} className={model.freq === f ? 'on' : ''}>
                  <input type="radio" name={ids.every + 'f'} checked={model.freq === f} onChange={() => set({ freq: f })} />
                  {label}
                </label>
              ))}
            </div>
          </fieldset>

          {model.freq !== 'once' && (
            <div className="rr-row">
              <label htmlFor={ids.every}>Every</label>
              <input id={ids.every} className="input rr-num" type="number" min={1} value={model.interval} onChange={(e) => set({ interval: Math.max(1, Number(e.target.value) || 1) })} />
              <span>{model.interval === 1 ? unit.replace(/s$/, '') : unit}</span>
            </div>
          )}

          {model.freq === 'WEEKLY' && (
            <fieldset>
              <legend>On</legend>
              <div className="segmented rr-days">
                {DAYS.map((d) => {
                  const on = model.byday.includes(d)
                  return (
                    <label key={d} className={on ? 'on' : ''} title={DAY_NAMES[d]}>
                      <input
                        type="checkbox"
                        checked={on}
                        aria-label={DAY_NAMES[d]}
                        onChange={() => {
                          const next = on ? model.byday.filter((x) => x !== d) : [...model.byday, d]
                          if (next.length) set({ byday: next })
                        }}
                      />
                      {DAY_NAMES[d].slice(0, 2)}
                    </label>
                  )
                })}
              </div>
            </fieldset>
          )}

          {model.freq === 'MONTHLY' && (
            <fieldset>
              <legend>On</legend>
              <div className="stack" style={{ gap: 6 }}>
                <label className="rr-row">
                  <input type="radio" name={ids.every + 'm'} checked={model.monthly === 'day'} onChange={() => set({ monthly: 'day' })} />
                  Day
                  <input
                    className="input rr-num"
                    type="number"
                    min={1}
                    max={31}
                    aria-label="Day of the month"
                    value={model.monthday}
                    onFocus={() => model.monthly !== 'day' && set({ monthly: 'day' })}
                    onChange={(e) => set({ monthly: 'day', monthday: Math.min(31, Math.max(1, Number(e.target.value) || 1)) })}
                  />
                  of the month
                </label>
                <label className="rr-row">
                  <input type="radio" name={ids.every + 'm'} checked={model.monthly === 'weekday'} onChange={() => set({ monthly: 'weekday' })} />
                  The
                  <select className="select rr-sel" aria-label="Which week" value={model.setpos} onChange={(e) => set({ monthly: 'weekday', setpos: Number(e.target.value) })}>
                    {[
                      [1, 'first'],
                      [2, 'second'],
                      [3, 'third'],
                      [4, 'fourth'],
                      [-1, 'last'],
                    ].map(([v, l]) => (
                      <option key={v} value={v}>
                        {l}
                      </option>
                    ))}
                  </select>
                  <select className="select rr-sel" aria-label="Weekday" value={model.setposDay} onChange={(e) => set({ monthly: 'weekday', setposDay: e.target.value as Day })}>
                    {DAYS.map((d) => (
                      <option key={d} value={d}>
                        {DAY_NAMES[d]}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
            </fieldset>
          )}

          {model.freq === 'YEARLY' && (
            <div className="rr-row">
              <span>On</span>
              <input className="input rr-num" type="number" min={1} max={31} aria-label="Day" value={model.monthday} onChange={(e) => set({ monthday: Math.min(31, Math.max(1, Number(e.target.value) || 1)) })} />
              <select className="select rr-sel" aria-label="Month" value={model.month} onChange={(e) => set({ month: Number(e.target.value) })}>
                {MONTHS.map((m, i) => (
                  <option key={m} value={i + 1}>
                    {m}
                  </option>
                ))}
              </select>
            </div>
          )}

          <div className="rr-row wrap">
            <div className="field">
              <label htmlFor={ids.start}>{model.freq === 'once' ? 'Date' : 'Starting'}</label>
              <input id={ids.start} className="input" type="date" value={model.start.slice(0, 10)} onChange={(e) => e.target.value && set({ start: `${e.target.value}T${model.start.slice(11)}` })} />
            </div>
            <div className="field">
              <label htmlFor={ids.time}>At</label>
              <input id={ids.time} className="input" type="time" value={model.start.slice(11, 16)} onChange={(e) => e.target.value && set({ start: `${model.start.slice(0, 10)}T${e.target.value}` })} />
            </div>
            <div className="field grow" style={{ minWidth: 220 }}>
              <label htmlFor={ids.tz}>Time zone</label>
              <ZoneInput id={ids.tz} listId={ids.zones} value={model.tz} zones={zones.data?.map((z) => z.name) ?? []} onChange={(tz) => set({ tz })} />
            </div>
          </div>

          {model.freq !== 'once' && (
            <fieldset>
              <legend>Ends</legend>
              <div className="stack" style={{ gap: 6 }}>
                <label className="rr-row">
                  <input type="radio" name={ids.every + 'e'} checked={model.end === 'never'} onChange={() => set({ end: 'never' })} />
                  Never
                </label>
                <label className="rr-row">
                  <input type="radio" name={ids.every + 'e'} checked={model.end === 'count'} onChange={() => set({ end: 'count' })} />
                  After
                  <input
                    id={ids.count}
                    className="input rr-num"
                    type="number"
                    min={1}
                    max={999}
                    aria-label="Number of runs"
                    value={model.count}
                    onFocus={() => model.end !== 'count' && set({ end: 'count' })}
                    onChange={(e) => set({ end: 'count', count: Math.min(999, Math.max(1, Number(e.target.value) || 1)) })}
                  />
                  runs
                </label>
                <label className="rr-row">
                  <input type="radio" name={ids.every + 'e'} checked={model.end === 'until'} onChange={() => set({ end: 'until', until: model.until || model.start.slice(0, 10) })} />
                  On
                  <input
                    id={ids.until}
                    className="input"
                    style={{ width: 'auto' }}
                    type="date"
                    aria-label="Last day"
                    value={model.until}
                    min={model.start.slice(0, 10)}
                    onChange={(e) => set({ end: 'until', until: e.target.value })}
                  />
                </label>
              </div>
            </fieldset>
          )}

          {model.freq !== 'once' && (
            <div className="field">
              <label htmlFor={ids.ex}>
                Skip these dates <span className="opt">· optional</span>
              </label>
              <div className="rr-row wrap">
                {model.exdates.map((d) => (
                  <span key={d} className="chip">
                    {d}
                    <button type="button" aria-label={`Do not skip ${d}`} onClick={() => set({ exdates: model.exdates.filter((x) => x !== d) })}>
                      <Icon name="x" size={12} />
                    </button>
                  </span>
                ))}
                <input id={ids.ex} className="input" style={{ width: 'auto' }} type="date" value={exDraft} onChange={(e) => setExDraft(e.target.value)} />
                <button
                  type="button"
                  className="btn sm"
                  disabled={!exDraft || model.exdates.includes(exDraft)}
                  onClick={() => {
                    set({ exdates: [...model.exdates, exDraft].sort() })
                    setExDraft('')
                  }}
                >
                  Skip date
                </button>
              </div>
              <span className="help">Runs that would fall on these days at {model.start.slice(11, 16)} are left out (holidays, freeze days).</span>
            </div>
          )}
        </>
      ) : (
        <div className="field">
          <label htmlFor={ids.text}>Rule</label>
          <textarea
            id={ids.text}
            className="textarea mono"
            rows={3}
            spellCheck={false}
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              onChange(e.target.value)
            }}
            aria-invalid={!!(error || previewError)}
          />
          <span className="help">
            iCalendar text: a DTSTART and an RRULE, optionally EXRULE, RDATE and EXDATE, separated by spaces.
            {!parsed.model && parsed.reason && value ? ` The form cannot show this rule: ${parsed.reason}` : ''}
          </span>
        </div>
      )}

      <div className="rr-summary">
        <div className="row wrap" style={{ gap: 10 }}>
          <strong className="grow">{model ? describe(model) : 'Custom rule'}</strong>
          {advanced ? (
            <button type="button" className="link-btn small" disabled={!model} title={model ? undefined : parsed.reason} onClick={() => model && setAdvanced(false)}>
              Use the form
            </button>
          ) : (
            <button type="button" className="link-btn small" onClick={() => setAdvanced(true)}>
              Edit as text
            </button>
          )}
        </div>
        {!advanced && <code className="rr-text">{value}</code>}
        {(error || previewError) && <div className="err small">{error || previewError}</div>}
        {preview.data && preview.data.utc.length > 0 && (
          <div className="rr-preview">
            <span className="faint small">Next runs</span>
            <ol>
              {preview.data.utc.slice(0, 5).map((u, i) => (
                <li key={u}>
                  <span className="mono">{model ? formatIn(u, model.tz) : preview.data!.local[i]}</span>
                  {model && model.tz !== browserTz && <span className="faint"> · {formatIn(u, browserTz)} your time</span>}
                </li>
              ))}
            </ol>
          </div>
        )}
        {preview.data && preview.data.utc.length === 0 && <div className="small faint">This rule has no future runs.</div>}
      </div>
    </div>
  )
}

function ZoneInput({ id, listId, value, zones, onChange }: { id: string; listId: string; value: string; zones: string[]; onChange: (v: string) => void }) {
  const [draft, setDraft] = useState(value)
  useEffect(() => setDraft(value), [value])
  const valid = (z: string) => z === 'UTC' || zones.includes(z)
  return (
    <>
      <input
        id={id}
        className="input"
        list={listId}
        value={draft}
        aria-invalid={!!draft && zones.length > 0 && !valid(draft)}
        onChange={(e) => {
          setDraft(e.target.value)
          if (valid(e.target.value)) onChange(e.target.value)
        }}
        onBlur={() => !valid(draft) && setDraft(value)}
      />
      <datalist id={listId}>
        <option value="UTC" />
        {zones.map((z) => (
          <option key={z} value={z} />
        ))}
      </datalist>
    </>
  )
}

// ---------- schedule dialog ----------

const TEMPLATE_RES: Record<string, string> = {
  job_template: 'job_templates',
  workflow_job_template: 'workflow_job_templates',
  project: 'projects',
  inventory_source: 'inventory_sources',
  system_job_template: 'system_job_templates',
}

export function templateResource(type: string): string {
  return TEMPLATE_RES[type] ?? 'unified_job_templates'
}

interface PromptSpec {
  field: string
  ask: string
}

export const PROMPTS: PromptSpec[] = [
  { field: 'inventory', ask: 'ask_inventory_on_launch' },
  { field: 'limit', ask: 'ask_limit_on_launch' },
  { field: 'scm_branch', ask: 'ask_scm_branch_on_launch' },
  { field: 'job_type', ask: 'ask_job_type_on_launch' },
  { field: 'job_tags', ask: 'ask_tags_on_launch' },
  { field: 'skip_tags', ask: 'ask_skip_tags_on_launch' },
  { field: 'verbosity', ask: 'ask_verbosity_on_launch' },
  { field: 'diff_mode', ask: 'ask_diff_mode_on_launch' },
  { field: 'execution_environment', ask: 'ask_execution_environment_on_launch' },
  { field: 'forks', ask: 'ask_forks_on_launch' },
  { field: 'job_slice_count', ask: 'ask_job_slice_count_on_launch' },
  { field: 'timeout', ask: 'ask_timeout_on_launch' },
]

const DAYS_JOBS = ['cleanup_jobs', 'cleanup_activitystream', 'cleanup_host_metrics']

export interface SurveyQuestion {
  question_name: string
  question_description?: string
  variable: string
  type: string
  required?: boolean
  default?: unknown
  choices?: string[] | string
  min?: number
  max?: number
}

function surveyChoices(q: SurveyQuestion): string[] {
  if (Array.isArray(q.choices)) return q.choices
  return (q.choices ?? '').split('\n').filter(Boolean)
}

/** What a template allows a schedule to fill in. */
export function useTemplatePrompts(type: string | undefined, id: number | undefined) {
  const tpl = useQuery({
    queryKey: [type ? `${templateResource(type)}/${id}/` : 'none'],
    queryFn: () => api.get<Any>(`${templateResource(type!)}/${id}/`),
    enabled: !!type && !!id,
  })
  const surveyOn = !!tpl.data?.survey_enabled
  const survey = useQuery({
    queryKey: [`${type ? templateResource(type) : ''}/${id}/survey_spec/`],
    queryFn: () => api.get<{ spec?: SurveyQuestion[] }>(`${templateResource(type!)}/${id}/survey_spec/`),
    enabled: surveyOn,
  })
  return { template: tpl.data, survey: surveyOn ? survey.data?.spec ?? [] : [], loading: tpl.isPending && !!type }
}

type PromptValues = Record<string, unknown>

export function ScheduleDialog({
  schedule,
  template: fixedTemplate,
  onClose,
}: {
  schedule?: Any
  template?: { id: number; type: string; name: string }
  onClose: () => void
}) {
  const toast = useToast()
  const invalidate = useInvalidate()
  const editing = !!schedule
  const [template, setTemplate] = useState<{ id: number; type: string; name: string } | null>(
    fixedTemplate ??
      (schedule
        ? {
            id: schedule.unified_job_template,
            type: ujtTypeOf(schedule.summary_fields?.unified_job_template?.unified_job_type),
            name: schedule.summary_fields?.unified_job_template?.name ?? '',
          }
        : null),
  )
  const [name, setName] = useState<string>(schedule?.name ?? '')
  const [description, setDescription] = useState<string>(schedule?.description ?? '')
  const [enabled, setEnabled] = useState<boolean>(schedule?.enabled ?? true)
  const [rrule, setRrule] = useState<string>(schedule?.rrule ?? '')
  const [prompts, setPrompts] = useState<PromptValues>(() => {
    const p: PromptValues = {}
    for (const s of PROMPTS) p[s.field] = schedule?.[s.field] ?? null
    return p
  })
  const [inventoryRef, setInventoryRef] = useState<Ref | null>(schedule?.summary_fields?.inventory ?? null)
  const [eeRef, setEeRef] = useState<Ref | null>(schedule?.summary_fields?.execution_environment ?? null)
  const extraInit = (schedule?.extra_data ?? {}) as Record<string, unknown>
  const [answers, setAnswers] = useState<Record<string, unknown>>(extraInit)
  const [extraText, setExtraText] = useState<string>('')
  const [days, setDays] = useState<string>(extraInit.days !== undefined ? String(extraInit.days) : '')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const { template: tpl, survey, loading } = useTemplatePrompts(template?.type, template?.id)
  const surveyVars = useMemo(() => new Set(survey.map((q) => q.variable)), [survey])

  // Extra vars that are not survey answers go to the free-form editor.
  useEffect(() => {
    const rest: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(extraInit)) if (!surveyVars.has(k) && k !== 'days') rest[k] = v
    setExtraText(varsText(rest))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [surveyVars])

  const allowed = PROMPTS.filter((p) => tpl?.[p.ask])
  const askVars = !!tpl?.ask_variables_on_launch
  const isSystem = template?.type === 'system_job_template'
  const wantsDays = isSystem && DAYS_JOBS.includes(tpl?.job_type)

  const save = async () => {
    setBusy(true)
    setErrors({})
    setFormError(null)
    const body: Any = { name, description, enabled, rrule }
    for (const p of PROMPTS) body[p.field] = allowed.includes(p) ? emptyToNull(prompts[p.field]) : null
    if (allowed.some((p) => p.field === 'inventory')) body.inventory = inventoryRef?.id ?? null
    if (allowed.some((p) => p.field === 'execution_environment')) body.execution_environment = eeRef?.id ?? null
    let extra: Record<string, unknown> = {}
    if (askVars) extra = { ...parseVars(extraText) }
    for (const q of survey) {
      const v = answers[q.variable]
      if (v !== undefined && v !== '' && v !== null) extra[q.variable] = v
    }
    if (wantsDays && days !== '') extra.days = Number(days)
    body.extra_data = extra
    try {
      if (editing) await api.patch(`schedules/${schedule!.id}/`, body)
      else if (template) await api.post(`${templateResource(template.type)}/${template.id}/schedules/`, body)
      else throw new Error('Choose what the schedule runs.')
      invalidate('schedules', templateResource(template?.type ?? ''), 'unified_job_templates')
      toast(editing ? `Schedule “${name}” saved` : `Schedule “${name}” created`)
      onClose()
    } catch (e) {
      if (e instanceof ApiError && Object.keys(e.fields).length) {
        setErrors(e.fields)
        const shown = new Set(['name', 'description', 'rrule', 'extra_data', 'extra_vars', ...PROMPTS.map((p) => p.field)])
        if (Object.keys(e.fields).some((k) => !shown.has(k))) setFormError(e)
      } else setFormError(e)
    } finally {
      setBusy(false)
    }
  }

  const setPrompt = (k: string, v: unknown) => setPrompts((p) => ({ ...p, [k]: v }))

  return (
    <Dialog
      wide
      title={editing ? `Edit schedule` : 'New schedule'}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !name.trim() || !template || !rrule} onClick={save}>
            {busy ? 'Saving…' : editing ? 'Save schedule' : 'Create schedule'}
          </button>
        </>
      }
    >
      <FormError error={formError} />
      {!fixedTemplate && !editing ? (
        <Lookup
          label="Runs"
          resource="unified_job_templates/"
          params={{ type__in: 'job_template,workflow_job_template,project,inventory_source,system_job_template' }}
          value={template ? ({ id: template.id, name: template.name } as Ref) : null}
          onChange={(r) => setTemplate(r ? { id: r.id, type: r.type, name: r.name } : null)}
          meta={(r) => typeLabel(r.type)}
          error={errors.unified_job_template}
        />
      ) : (
        template && (
          <div className="small muted">
            Runs <strong style={{ color: 'var(--ink)' }}>{template.name}</strong> · {typeLabel(template.type)}
          </div>
        )
      )}
      <div className="form-grid">
        <TextInput label="Name" value={name} onChange={setName} error={errors.name} autoFocus={!!fixedTemplate || editing} />
        <TextInput label="Description" value={description} onChange={setDescription} optional error={errors.description} />
      </div>
      <RRuleEditor value={rrule} onChange={setRrule} error={errors.rrule} />
      <Check label="Enabled" checked={enabled} onChange={setEnabled} help="Disabled schedules keep their rule but do not launch anything." />

      {template && (allowed.length > 0 || survey.length > 0 || askVars || wantsDays) && (
        <section className="form-section">
          <h2>Launch with</h2>
          <p className="small muted" style={{ margin: 0 }}>
            Only what the template lets you change at launch. Empty fields use the template's values.
          </p>
          <div className="form-grid">
            {allowed.map((p) => (
              <PromptField
                key={p.field}
                field={p.field}
                value={prompts[p.field]}
                onChange={(v) => setPrompt(p.field, v)}
                error={errors[p.field]}
                inventory={inventoryRef}
                setInventory={setInventoryRef}
                ee={eeRef}
                setEe={setEeRef}
                orgId={tpl?.organization}
              />
            ))}
            {wantsDays && (
              <TextInput label="Keep data for (days)" type="number" value={days} onChange={setDays} error={errors.extra_vars || errors.extra_data} help="Older records are removed." />
            )}
          </div>
          {survey.length > 0 && (
            <div className="form-grid">
              {survey.map((q) => (
                <SurveyField key={q.variable} q={q} value={answers[q.variable] ?? q.default} onChange={(v) => setAnswers((a) => ({ ...a, [q.variable]: v }))} />
              ))}
            </div>
          )}
          {askVars && <VarsEditor label="Extra variables" value={extraText} onChange={setExtraText} error={errors.extra_data} rows={4} />}
          {!askVars && errors.extra_data && <div className="err small">{errors.extra_data}</div>}
        </section>
      )}
      {loading && <div className="small faint">Loading template…</div>}
    </Dialog>
  )
}

function emptyToNull(v: unknown): unknown {
  return v === '' || v === undefined ? null : v
}

/** Template type of a schedule from the job type in its summary. */
export function ujtTypeOf(jobType: string | undefined): string {
  return (
    (
      {
        job: 'job_template',
        workflow_job: 'workflow_job_template',
        project_update: 'project',
        inventory_update: 'inventory_source',
        system_job: 'system_job_template',
      } as Record<string, string>
    )[jobType ?? ''] ?? 'job_template'
  )
}

export function PromptField({
  field,
  value,
  onChange,
  error,
  inventory,
  setInventory,
  ee,
  setEe,
  orgId,
}: {
  field: string
  value: unknown
  onChange: (v: unknown) => void
  error?: string
  inventory: Ref | null
  setInventory: (r: Ref | null) => void
  ee: Ref | null
  setEe: (r: Ref | null) => void
  orgId?: number | null
}) {
  const str = (v: unknown) => (v === null || v === undefined ? '' : String(v))
  switch (field) {
    case 'inventory':
      return <Lookup label="Inventory" resource="inventories/" value={inventory} onChange={setInventory} optional error={error} params={orgId ? { organization: orgId } : undefined} />
    case 'execution_environment':
      return <Lookup label="Execution environment" resource="execution_environments/" value={ee} onChange={setEe} optional error={error} />
    case 'job_type':
      return (
        <SelectField
          label="Job type"
          value={str(value)}
          onChange={(v) => onChange(v || null)}
          options={[
            ['', 'Template default'],
            ['run', 'Run'],
            ['check', 'Check'],
          ]}
          error={error}
        />
      )
    case 'verbosity':
      return (
        <SelectField
          label="Verbosity"
          value={str(value)}
          onChange={(v) => onChange(v === '' ? null : Number(v))}
          options={[
            ['', 'Template default'],
            [0, '0 · normal'],
            [1, '1 · verbose'],
            [2, '2 · more verbose'],
            [3, '3 · debug'],
            [4, '4 · connection debug'],
            [5, '5 · WinRM debug'],
          ]}
          error={error}
        />
      )
    case 'diff_mode':
      return (
        <SelectField
          label="Show changes"
          value={value === null || value === undefined ? '' : value ? 'on' : 'off'}
          onChange={(v) => onChange(v === '' ? null : v === 'on')}
          options={[
            ['', 'Template default'],
            ['on', 'On'],
            ['off', 'Off'],
          ]}
          error={error}
        />
      )
    case 'forks':
    case 'job_slice_count':
    case 'timeout':
      return (
        <TextInput
          label={{ forks: 'Forks', job_slice_count: 'Job slices', timeout: 'Timeout (seconds)' }[field]}
          type="number"
          value={str(value)}
          onChange={(v) => onChange(v === '' ? null : Number(v))}
          optional
          error={error}
        />
      )
    default:
      return (
        <TextInput
          label={{ limit: 'Limit', scm_branch: 'Source control branch', job_tags: 'Job tags', skip_tags: 'Skip tags' }[field] ?? field}
          value={str(value)}
          onChange={(v) => onChange(v)}
          optional
          mono={field !== 'scm_branch'}
          error={error}
        />
      )
  }
}

export function SurveyField({ q, value, onChange }: { q: SurveyQuestion; value: unknown; onChange: (v: unknown) => void }) {
  const id = useId()
  const label: ReactNode = q.question_name
  const help = q.question_description || undefined
  const str = value === undefined || value === null ? '' : String(value)
  switch (q.type) {
    case 'textarea':
      return (
        <Field label={label} help={help} optional={!q.required} full htmlFor={id}>
          <textarea id={id} className="textarea" rows={3} value={str} onChange={(e) => onChange(e.target.value)} />
        </Field>
      )
    case 'password':
      return (
        <Field label={label} help={help} optional={!q.required} htmlFor={id}>
          <input id={id} className="input" type="password" autoComplete="new-password" value={str} onChange={(e) => onChange(e.target.value)} />
        </Field>
      )
    case 'integer':
    case 'float':
      return (
        <Field label={label} help={help} optional={!q.required} htmlFor={id}>
          <input
            id={id}
            className="input"
            type="number"
            step={q.type === 'float' ? 'any' : 1}
            min={q.min}
            max={q.max}
            value={str}
            onChange={(e) => onChange(e.target.value === '' ? '' : Number(e.target.value))}
          />
        </Field>
      )
    case 'multiplechoice':
      return (
        <Field label={label} help={help} optional={!q.required} htmlFor={id}>
          <select id={id} className="select" value={str} onChange={(e) => onChange(e.target.value)}>
            {!q.required && <option value="">—</option>}
            {surveyChoices(q).map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </Field>
      )
    case 'multiselect': {
      const sel = Array.isArray(value) ? (value as string[]) : typeof value === 'string' && value ? value.split('\n') : []
      return (
        <fieldset>
          <legend>
            {label} {!q.required && <span className="opt faint">· optional</span>}
          </legend>
          <div className="row wrap" style={{ gap: 12 }}>
            {surveyChoices(q).map((c) => (
              <label key={c} className="check">
                <input type="checkbox" checked={sel.includes(c)} onChange={() => onChange(sel.includes(c) ? sel.filter((x) => x !== c) : [...sel, c])} />
                {c}
              </label>
            ))}
          </div>
        </fieldset>
      )
    }
  }
  return (
    <Field label={label} help={help} optional={!q.required} htmlFor={id}>
      <input id={id} className="input" value={str} maxLength={q.max} onChange={(e) => onChange(e.target.value)} />
    </Field>
  )
}
