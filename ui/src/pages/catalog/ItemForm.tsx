// The form that publishes a template as a catalog item, or changes an item.
import { useEffect, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Ref } from '../../api/types'
import { Check, Field, FormError, Lookup, Segmented, SelectField, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import {
  actionID,
  errorSection,
  hasFixedChoices,
  itemBody,
  lifetimeError,
  newAction,
  newRule,
  parseDays,
  questionEdits,
  surveyChoices,
  surveyPath,
  type ActionEdit,
  type CatalogItem,
  type ItemForm as Values,
  type QuestionEdit,
  type RuleEdit,
  type SurveyQuestion,
} from '../../lib/catalogItems'
import { asIs, tr } from '../../lib/i18n'
import { itemIcons } from '../portal/parts'

const TEMPLATES = { type__in: 'job_template,workflow_job_template' }
type Named = NonNullable<Values['template']>

const templateMeta = (r: Ref) => <span className="faint small">{r.type === 'workflow_job_template' ? tr('workflow') : tr('job')}</span>

/** The template's survey: its questions, none when it is off, or null while unknown. */
export function useSurvey(template: Named | null): { spec: SurveyQuestion[] | null; error: unknown } {
  const path = template ? surveyPath(template) : null
  const tpl = useObject<{ survey_enabled: boolean; organization: number | null }>(path)
  const survey = useObject<{ spec?: SurveyQuestion[] }>(path ? `${path}survey_spec/` : null)
  if (!template) return { spec: [], error: null }
  if (!tpl.data || !survey.data) return { spec: null, error: tpl.error ?? survey.error }
  return { spec: tpl.data.survey_enabled ? (survey.data.spec ?? []) : [], error: null }
}

function Section({ id, title, intro, error, children }: { id: string; title: string; intro?: ReactNode; error?: string; children: ReactNode }) {
  return (
    <section className="form-section" id={`item-${id}`}>
      <h2>{title}</h2>
      {intro && (
        <p className="small muted" style={{ margin: 0 }}>
          {intro}
        </p>
      )}
      {error && (
        <div className="form-error" role="alert">
          {error}
        </div>
      )}
      {children}
    </section>
  )
}

function Remove({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button type="button" className="btn ghost icon" aria-label={label} title={label} onClick={onClick}>
      <Icon name="x" size={14} />
    </button>
  )
}

function Add({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <div>
      <button type="button" className="btn sm" onClick={onClick}>
        <Icon name="plus" size={12} />
        {label}
      </button>
    </div>
  )
}

const replace = <T,>(list: T[], i: number, x: T) => list.map((y, j) => (j === i ? x : y))

/** An answer's input, by the question's type. */
function AnswerInput({ q, label, value, onChange, help, placeholder }: { q: SurveyQuestion; label: string; value: string; onChange: (v: string) => void; help?: ReactNode; placeholder?: string }) {
  if (q.type === 'multiplechoice' && !q.choices_from) {
    return <SelectField label={label} value={value} onChange={onChange} help={help} options={[['', placeholder ?? tr('None')], ...surveyChoices(q).map((c): [string, string] => [c, c])]} />
  }
  if (q.type === 'multiselect' || q.type === 'textarea') return <TextArea label={label} rows={2} full={false} value={value} onChange={onChange} help={help ?? (q.type === 'multiselect' ? tr('One answer per line.') : undefined)} />
  const type = q.type === 'password' ? 'password' : q.type === 'integer' || q.type === 'float' ? 'number' : 'text'
  return <TextInput label={label} type={type} value={value} onChange={onChange} help={help} placeholder={placeholder} />
}

function QuestionBlock({ q, r, set }: { q: SurveyQuestion; r: QuestionEdit; set: (r: QuestionEdit) => void }) {
  const all = surveyChoices(q)
  const picked = r.choices ?? all
  const toggle = (c: string, on: boolean) => {
    const next = all.filter((x) => (x === c ? on : picked.includes(x)))
    set({ ...r, choices: next.length === all.length ? null : next })
  }
  const surveyDefault = q.default == null || q.default === '' ? tr('none') : Array.isArray(q.default) ? q.default.join(', ') : String(q.default)
  const reword = r.mode === 'asked' || (r.mode === 'fixed' && r.visible)
  return (
    <div className="ci-block">
      <div className="row wrap" style={{ gap: 8 }}>
        <strong>{q.question_name}</strong>
        <span className="mono small muted">{q.variable}</span>
        <span className="tag">{q.type}</span>
        {q.required && <span className="tag">{tr('required')}</span>}
      </div>
      <Segmented<QuestionEdit['mode']>
        legend={tr('Requesters')}
        value={r.mode}
        onChange={(mode) => set({ ...r, mode })}
        options={[
          ['asked', tr('Answer it')],
          ['hidden', tr('Don’t see it')],
          ['fixed', tr('Get a fixed answer')],
        ]}
      />
      <div className="form-grid">
        {reword && <TextInput label={tr('Question')} optional value={r.question_name} placeholder={q.question_name} onChange={(x) => set({ ...r, question_name: x })} />}
        {reword && <TextInput label={tr('Description')} optional value={r.question_description} placeholder={q.question_description} onChange={(x) => set({ ...r, question_description: x })} />}
        {r.mode === 'fixed' ? (
          <>
            <AnswerInput
              q={q}
              label={tr('Fixed answer')}
              value={r.value}
              onChange={(x) => set({ ...r, value: x })}
              help={q.type === 'password' ? (r.value === '$encrypted$' ? tr('Stored encrypted; leave as is to keep it.') : tr('Stored encrypted, and never shown.')) : undefined}
            />
            {q.type !== 'password' && <Check label={tr('Show the answer to requesters')} checked={r.visible} onChange={(x) => set({ ...r, visible: x })} help={tr('Shown on the form, and not to be changed.')} />}
          </>
        ) : (
          q.type !== 'password' && (
            <AnswerInput
              q={q}
              label={tr('Default')}
              value={r.default}
              onChange={(x) => set({ ...r, default: x })}
              placeholder={tr('The survey’s: {value}', { value: surveyDefault })}
              help={r.mode === 'hidden' ? tr('The answer it gets. The survey’s: {value}.', { value: surveyDefault }) : undefined}
            />
          )
        )}
        {r.mode === 'asked' && hasFixedChoices(q) && (
          <Field label={tr('Choices requesters may pick')} full>
            <div className="row wrap" style={{ gap: 14 }}>
              {all.map((c) => (
                <label key={c} className="check">
                  <input type="checkbox" checked={picked.includes(c)} onChange={(e) => toggle(c, e.target.checked)} />
                  <span>{c}</span>
                </label>
              ))}
            </div>
          </Field>
        )}
        {r.mode === 'asked' && !!q.choices_from && (
          <TextInput label={tr('Filter')} optional mono full value={r.filter} onChange={(x) => set({ ...r, filter: x })} placeholder={asIs('item.site == "gva"')} help={tr('Lists a choice when true. It reads value, answers and user.')} />
        )}
      </div>
    </div>
  )
}

function RuleBlock({ r, i, set, remove, org }: { r: RuleEdit; i: number; set: (r: RuleEdit) => void; remove: () => void; org?: number | null }) {
  return (
    <div className="ci-block">
      <div className="row">
        <strong className="grow">{r.name || tr('Rule {n}', { n: i + 1 })}</strong>
        <Remove label={tr('Remove {name}', { name: r.name || tr('Rule {n}', { n: i + 1 }) })} onClick={remove} />
      </div>
      <div className="form-grid">
        <TextInput label={tr('Name')} value={r.name} onChange={(x) => set({ ...r, name: x })} placeholder={tr('Large databases')} />
        <TextInput label={tr('Condition')} optional mono value={r.condition} onChange={(x) => set({ ...r, condition: x })} placeholder={asIs('size == "large"')} help={tr('An expression on the answers, kind and request. Empty: every request.')} />
        <Segmented<RuleEdit['approvers']>
          legend={tr('Approvers')}
          value={r.approvers}
          onChange={(approvers) => set({ ...r, approvers })}
          options={[
            ['team_admins', tr('The team’s admins')],
            ['team', tr('A team')],
            ['users', tr('Users from the answers')],
          ]}
        />
        {r.approvers === 'team' && <Lookup label={tr('Team')} resource="teams/" params={org ? { organization: org } : undefined} value={r.team} onChange={(x) => set({ ...r, team: x })} />}
        {r.approvers === 'users' && <TextInput label={tr('Users')} mono value={r.users_from} onChange={(x) => set({ ...r, users_from: x })} placeholder={asIs('answers.owner')} help={tr('An expression giving a username, or a list of them.')} />}
        <TextInput label={tr('Approvals needed')} type="number" value={r.required} onChange={(x) => set({ ...r, required: x })} />
        <TextInput label={tr('Timeout, in hours')} type="number" value={r.timeout_hours} onChange={(x) => set({ ...r, timeout_hours: x })} help={tr('A request not decided by then is denied.')} />
      </div>
    </div>
  )
}

function ActionBlock({ a, i, set, remove }: { a: ActionEdit; i: number; set: (a: ActionEdit) => void; remove: () => void }) {
  const name = a.name || tr('Action {n}', { n: i + 1 })
  return (
    <div className="ci-block">
      <div className="row">
        <strong className="grow">{name}</strong>
        <Remove label={tr('Remove {name}', { name })} onClick={remove} />
      </div>
      <div className="form-grid">
        <TextInput label={tr('Name')} value={a.name} onChange={(x) => set({ ...a, name: x, id: a.auto ? actionID(x) : a.id })} placeholder={tr('Restart')} />
        <TextInput label={tr('Id')} mono value={a.id} onChange={(x) => set({ ...a, id: x, auto: false })} help={tr('Its requests’ kind, for approval rules: lowercase letters, digits and underscores.')} />
        <TextInput label={tr('Description')} optional value={a.description} onChange={(x) => set({ ...a, description: x })} placeholder={tr('About a minute')} />
        <Lookup label={tr('Template')} resource="unified_job_templates/" params={TEMPLATES} meta={templateMeta} value={a.template} onChange={(x) => set({ ...a, template: x })} help={tr('It gets the deployment’s outputs.')} />
      </div>
    </div>
  )
}

/** The item's form; template preselects the template of a new item. */
export function ItemForm({ item, initial, onDone }: { item?: CatalogItem; initial: Values; onDone: (id?: number) => void }) {
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const f = useForm<Values>(initial)
  const v = f.values
  const { spec, error: surveyError } = useSurvey(v.template)
  const org = (v.template as { organization?: number | null } | null)?.organization
  const specKey = spec?.map((q) => q.variable).join('\n')
  useEffect(() => {
    if (!spec) return
    f.set('questions', questionEdits(spec, v.template?.id === item?.unified_job_template ? item?.questions : {}))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [specKey, v.template?.id])
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const days = parseDays(v.extension_days)
  const lifetime = lifetimeError(v)
  const dep = v.kind === 'deployment'
  const byVariable = new Map((spec ?? []).map((q) => [q.variable, q]))

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const body = itemBody(v, spec)
      const saved = item ? await api.patch<CatalogItem>(`catalog_items/${item.id}/`, body) : await api.post<CatalogItem>('catalog_items/', body)
      invalidate('catalog_items')
      onDone(saved.id)
    } catch (e) {
      setError(e)
      if (e instanceof ApiError) {
        f.setErrors(e.fields)
        const first = Object.keys(e.fields)[0]
        if (first) document.getElementById(`item-${errorSection(first)}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
      }
    } finally {
      setBusy(false)
    }
  }
  const ready = v.name.trim() && v.template && !days.error && !lifetime
  return (
    <form
      className="form"
      style={{ maxWidth: 980 }}
      onSubmit={(ev) => {
        ev.preventDefault()
        save()
      }}
    >
      <Section id="item" title={tr('The item')}>
        <div className="form-grid">
          <TextInput label={tr('Name')} value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus={!item} placeholder={tr('PostgreSQL database')} />
          <Lookup
            label={tr('Template')}
            resource="unified_job_templates/"
            params={TEMPLATES}
            meta={templateMeta}
            value={v.template}
            onChange={(x) => f.set('template', x)}
            error={f.errors.unified_job_template}
            help={tr('The job or workflow template a request runs. Publishing it takes Admin on it.')}
          />
          <TextArea label={tr('Description')} optional rows={2} value={v.description} onChange={(x) => f.set('description', x)} error={f.errors.description} />
          <Segmented<Values['kind']>
            legend={tr('A request')}
            value={v.kind}
            onChange={(x) => f.set('kind', x)}
            options={[
              ['run_once', tr('Runs once')],
              ['deployment', tr('Creates a deployment')],
            ]}
          />
          <TextInput label={tr('Category')} optional value={v.category} onChange={(x) => f.set('category', x)} error={f.errors.category} placeholder={tr('Databases')} help={tr('The portal lists items by category.')} />
          <SelectField label={tr('Icon')} value={v.icon} onChange={(x) => f.set('icon', x)} error={f.errors.icon} options={[['', tr('Box (the default)')], ...itemIcons.map((i): [string, string] => [i, i])]} />
          <Lookup label={tr('Owner team')} optional resource="teams/" params={org ? { organization: org } : undefined} value={v.owner_team} onChange={(x) => f.set('owner_team', x)} error={f.errors.owner_team} help={tr('It approves requests over quota, when the item lets them wait.')} />
        </div>
      </Section>

      <Section id="questions" title={tr('Questions')} intro={tr('From the template’s survey. What you leave as it is, requesters see as the survey asks it.')} error={f.errors.questions}>
        {!v.template ? (
          <p className="faint small" style={{ margin: 0 }}>
            {tr('Pick a template first.')}
          </p>
        ) : spec === null ? (
          surveyError ? <FormError error={surveyError} /> : <p className="faint small" style={{ margin: 0 }}>{tr('Reading the survey…')}</p>
        ) : spec.length === 0 ? (
          <p className="faint small" style={{ margin: 0 }}>
            {tr('The template has no survey turned on: requesters answer nothing.')}
          </p>
        ) : (
          <div className="stack" style={{ gap: 10 }}>
            {v.questions.map((r, i) => {
              const q = byVariable.get(r.variable)
              return q ? <QuestionBlock key={r.variable} q={q} r={r} set={(x) => f.set('questions', replace(v.questions, i, x))} /> : null
            })}
          </div>
        )}
      </Section>

      <Section id="approvals" title={tr('Approvals')} intro={tr('A request that meets a rule’s condition waits for its approvers. Without rules, requests run at once.')} error={f.errors.approval_rules}>
        <div className="stack" style={{ gap: 10 }}>
          {v.rules.map((r, i) => (
            <RuleBlock key={i} r={r} i={i} org={org} set={(x) => f.set('rules', replace(v.rules, i, x))} remove={() => f.set('rules', v.rules.filter((_, j) => j !== i))} />
          ))}
          <Add label={tr('Add a rule')} onClick={() => f.set('rules', [...v.rules, newRule()])} />
        </div>
        <div className="form-grid">
          <Lookup
            label={tr('Freeze exception team')}
            optional
            resource="teams/"
            params={org ? { organization: org } : undefined}
            value={v.freeze_exception_team}
            onChange={(x) => f.set('freeze_exception_team', x)}
            error={f.errors.freeze_exception_team}
            help={tr('It approves a request during a change freeze that lists the item. Empty: such a request is refused.')}
          />
          <Lookup
            label={tr('Email notification')}
            optional
            resource="notification_templates/"
            params={{ notification_type: 'email', ...(org ? { organization: org } : {}) }}
            value={v.notification_template}
            onChange={(x) => f.set('notification_template', x)}
            error={f.errors.notification_template}
            help={tr('Mails the approvers when a request waits for them, and the requester when it is decided.')}
          />
        </div>
      </Section>

      {dep && (
        <Section id="deployment" title={tr('The deployment')} intro={tr('What a request creates, how long its owners keep it, and what they can do with it.')}>
          <div className="form-grid">
            <TextInput
              label={tr('Name')}
              optional
              mono
              value={v.deployment_name}
              onChange={(x) => f.set('deployment_name', x)}
              error={f.errors.deployment_name}
              placeholder={asIs('answers.name')}
              help={tr('An expression over answers, outputs and request. Empty: the item’s name and the request’s number.')}
            />
            <Lookup
              label={tr('Teardown')}
              optional
              resource="unified_job_templates/"
              params={TEMPLATES}
              meta={templateMeta}
              value={v.teardown_template}
              onChange={(x) => f.set('teardown_template', x)}
              error={f.errors.teardown_template}
              help={tr('Runs with the outputs when the deployment is deleted. Empty: it is deleted at once.')}
            />
          </div>
          <Segmented<Values['lifetime']>
            legend={tr('Lifetime')}
            value={v.lifetime}
            onChange={(x) => f.set('lifetime', x)}
            options={[
              ['forever', tr('Kept until deleted')],
              ['limited', tr('Expires')],
            ]}
          />
          {v.lifetime === 'forever' ? (
            <p className="small muted" style={{ margin: 0 }}>
              {tr('It stays until its owners delete it.')}
            </p>
          ) : (
            <>
              <div className="form-grid">
                <TextInput label={tr('Default lifetime, in days')} optional type="number" value={v.default_lifetime_days} onChange={(x) => f.set('default_lifetime_days', x)} error={f.errors.default_lifetime_days ?? lifetime ?? undefined} help={tr('What the form proposes.')} />
                <TextInput label={tr('Longest lifetime, in days')} optional type="number" value={v.max_lifetime_days} onChange={(x) => f.set('max_lifetime_days', x)} error={f.errors.max_lifetime_days} help={tr('Empty: requesters pick any number of days.')} />
                <TextInput label={tr('Extensions, in days')} optional value={v.extension_days} onChange={(x) => f.set('extension_days', x)} error={f.errors.extension_days ?? days.error ?? undefined} placeholder={asIs('1, 3, 7')} help={tr('What owners may ask for. Approval rules see them as kind == "extend".')} />
                <TextInput label={tr('Most extensions')} optional type="number" value={v.max_extensions} onChange={(x) => f.set('max_extensions', x)} error={f.errors.max_extensions} help={tr('Empty: as often as the longest lifetime allows.')} />
              </div>
              <Check
                label={tr('Expiry waits for the end of a change freeze')}
                checked={v.expiry_waits_for_freeze}
                onChange={(x) => f.set('expiry_waits_for_freeze', x)}
                help={tr('A deployment that expires in a window that lists the item is deleted when the window ends.')}
              />
            </>
          )}
          <Field label={tr('How to connect')} optional full error={f.errors.connection} help={tr('Each line a label and an expression over the outputs, in order. Empty: every output under its name.')}>
            <div className="stack" style={{ gap: 6 }}>
              {v.connection.map((c, i) => (
                <div key={i} className="row">
                  <input className="input" style={{ flex: '0 1 220px' }} aria-label={tr('Label')} placeholder={tr('Host')} value={c.label} onChange={(e) => f.set('connection', replace(v.connection, i, { ...c, label: e.target.value }))} />
                  <input className="input mono grow" aria-label={tr('Value')} placeholder={tr('host')} value={c.value} onChange={(e) => f.set('connection', replace(v.connection, i, { ...c, value: e.target.value }))} />
                  <label className="check small">
                    <input type="checkbox" checked={c.copy !== false} onChange={(e) => f.set('connection', replace(v.connection, i, { ...c, copy: e.target.checked }))} />
                    <span>{tr('Copy button')}</span>
                  </label>
                  <Remove label={tr('Remove {name}', { name: c.label || tr('line {n}', { n: i + 1 }) })} onClick={() => f.set('connection', v.connection.filter((_, j) => j !== i))} />
                </div>
              ))}
              <Add label={tr('Add a line')} onClick={() => f.set('connection', [...v.connection, { label: '', value: '', copy: true }])} />
            </div>
          </Field>
          <Field label={tr('Actions')} optional full error={f.errors.actions} help={tr('What owners can run on their deployment, such as a restart.')}>
            <div className="stack" style={{ gap: 10 }}>
              {v.actions.map((a, i) => (
                <ActionBlock key={i} a={a} i={i} set={(x) => f.set('actions', replace(v.actions, i, x))} remove={() => f.set('actions', v.actions.filter((_, j) => j !== i))} />
              ))}
              <Add label={tr('Add an action')} onClick={() => f.set('actions', [...v.actions, newAction()])} />
            </div>
          </Field>
        </Section>
      )}

      <Section id="quotas" title={tr('Quotas')} intro={tr('Per team, or per user requesting for no team. Empty: no limit.')}>
        <div className="form-grid">
          {dep && <TextInput label={tr('Deployments at once')} optional type="number" value={v.max_deployments} onChange={(x) => f.set('max_deployments', x)} error={f.errors.max_deployments} />}
          <TextInput label={tr('Requests a day')} optional type="number" value={v.max_requests_per_day} onChange={(x) => f.set('max_requests_per_day', x)} error={f.errors.max_requests_per_day} />
          <Segmented<Values['over_quota']>
            legend={tr('A request over quota')}
            value={v.over_quota}
            onChange={(x) => f.set('over_quota', x)}
            options={[
              ['refuse', tr('Is refused')],
              ['approve', tr('Waits for the owner team')],
            ]}
          />
        </div>
      </Section>

      <Section id="steps" title={tr('Step labels')} intro={tr('How the portal names a request’s steps: each play of the playbook, or node of the workflow, by its name. A step without a label shows under its own name.')} error={f.errors.step_labels}>
        <div className="stack" style={{ gap: 6 }}>
          {v.steps.map((s, i) => (
            <div key={i} className="row">
              <input className="input grow" aria-label={tr('Play or node')} placeholder={tr('Create the database')} value={s.step} onChange={(e) => f.set('steps', replace(v.steps, i, { ...s, step: e.target.value }))} />
              <input className="input grow" aria-label={tr('Label')} placeholder={tr('Database')} value={s.label} onChange={(e) => f.set('steps', replace(v.steps, i, { ...s, label: e.target.value }))} />
              <Remove label={tr('Remove {name}', { name: s.step || tr('line {n}', { n: i + 1 }) })} onClick={() => f.set('steps', v.steps.filter((_, j) => j !== i))} />
            </div>
          ))}
          <Add label={tr('Add a label')} onClick={() => f.set('steps', [...v.steps, { step: '', label: '' }])} />
        </div>
      </Section>

      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !ready}>
          {busy ? tr('Saving…') : item ? tr('Save changes') : tr('Publish')}
        </button>
        <button type="button" className="btn ghost" onClick={() => (item ? onDone() : nav(-1))}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}
