import { useEffect, useState } from 'react'
import { api } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Template } from '../../api/types'
import { Check, FormError, SelectField, TextArea, TextInput } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, useConfirm, useToast } from '../../components/overlay'
import { Empty, Loading, Panel } from '../../components/ui'
import { resOf, type Kind } from './TemplateDetail'

export interface Question {
  question_name: string
  question_description?: string
  variable: string
  type: 'text' | 'textarea' | 'password' | 'integer' | 'float' | 'multiplechoice' | 'multiselect'
  required: boolean
  default?: string | number | string[]
  choices?: string[] | string
  min?: number | null
  max?: number | null
  new_question?: boolean
}

export interface Survey {
  name: string
  description: string
  spec: Question[]
}

const typeLabels: Record<Question['type'], string> = {
  text: 'Text',
  textarea: 'Text, several lines',
  password: 'Password',
  integer: 'Whole number',
  float: 'Number',
  multiplechoice: 'One of a list',
  multiselect: 'Several of a list',
}

export const choicesOf = (q: Question): string[] => (Array.isArray(q.choices) ? q.choices : (q.choices ?? '').split('\n')).map((c) => c.trim()).filter(Boolean)

export function SurveyEditor({ kind, tpl }: { kind: Kind; tpl: Template }) {
  const res = resOf(kind)
  const remote = useObject<Survey | Record<string, never>>(`${res}/${tpl.id}/survey_spec/`)
  const [survey, setSurvey] = useState<Survey>({ name: '', description: '', spec: [] })
  const [dirty, setDirty] = useState(false)
  const [editing, setEditing] = useState<{ index: number; q: Question } | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [saving, setSaving] = useState(false)
  const invalidate = useInvalidate()
  const toast = useToast()
  const confirm = useConfirm()
  const readOnly = !!tpl.managed_by || tpl.summary_fields.user_capabilities?.edit === false

  useEffect(() => {
    if (!remote.data) return
    const d = remote.data as Partial<Survey>
    setSurvey({ name: d.name ?? '', description: d.description ?? '', spec: d.spec ?? [] })
    setDirty(false)
  }, [remote.data])

  if (remote.isPending) return <Loading />
  const update = (s: Survey) => {
    setSurvey(s)
    setDirty(true)
  }
  const move = (i: number, d: number) => {
    const spec = [...survey.spec]
    const [q] = spec.splice(i, 1)
    spec.splice(i + d, 0, q)
    update({ ...survey, spec })
  }
  const save = async () => {
    setSaving(true)
    setError(null)
    try {
      await api.post(`${res}/${tpl.id}/survey_spec/`, { ...survey, name: survey.name || tpl.name, spec: survey.spec.map(({ new_question, ...q }) => (void new_question, q)) })
      if (!tpl.survey_enabled && survey.spec.length) await api.patch(`${res}/${tpl.id}/`, { survey_enabled: true })
      invalidate(`${res}/${tpl.id}`, 'unified_job_templates')
      setDirty(false)
      toast('Survey saved')
    } catch (e) {
      setError(e)
    } finally {
      setSaving(false)
    }
  }
  const toggle = async (on: boolean) => {
    await api.patch(`${res}/${tpl.id}/`, { survey_enabled: on })
    invalidate(`${res}/${tpl.id}`, 'unified_job_templates')
  }

  return (
    <div className="split">
      <div className="wide stack" style={{ gap: 12 }}>
        <div className="row wrap">
          <label className="check">
            <input type="checkbox" checked={!!tpl.survey_enabled} disabled={readOnly || !survey.spec.length} onChange={(e) => toggle(e.target.checked)} />
            Ask these questions when someone launches
          </label>
          <div style={{ marginLeft: 'auto' }} className="row">
            {!readOnly && (
              <button className="btn" onClick={() => setEditing({ index: -1, q: { question_name: '', variable: '', type: 'text', required: true, new_question: true } })}>
                <Icon name="plus" size={14} />
                Add question
              </button>
            )}
            {!readOnly && survey.spec.length > 0 && (
              <button
                className="btn danger"
                onClick={async () => {
                  if (!(await confirm({ title: 'Delete the survey?', body: 'Launches stop asking these questions.', confirm: 'Delete survey', danger: true }))) return
                  await api.del(`${res}/${tpl.id}/survey_spec/`)
                  await api.patch(`${res}/${tpl.id}/`, { survey_enabled: false })
                  invalidate(`${res}/${tpl.id}`)
                }}
              >
                Delete survey
              </button>
            )}
            {dirty && (
              <button className="btn primary" disabled={saving} onClick={save}>
                {saving ? 'Saving…' : 'Save survey'}
              </button>
            )}
          </div>
        </div>
        {readOnly && tpl.managed_by && <div className="notice">This template is managed by {tpl.managed_by}; change the survey in its repository.</div>}
        <FormError error={error} />
        {survey.spec.length === 0 ? (
          <Panel>
            <Empty title="No questions" action={!readOnly && <button className="btn primary" onClick={() => setEditing({ index: -1, q: { question_name: '', variable: '', type: 'text', required: true, new_question: true } })}>Add the first question</button>}>
              A survey asks the person launching for values, validates them, and passes them as extra variables.
            </Empty>
          </Panel>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th style={{ width: 56 }}>
                    <span className="sr-only">Order</span>
                  </th>
                  <th>Question</th>
                  <th>Variable</th>
                  <th>Type</th>
                  <th>Default</th>
                  <th className="end">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {survey.spec.map((q, i) => (
                  <tr key={q.variable + i}>
                    <td className="nowrap">
                      {!readOnly && (
                        <>
                          <button className="btn ghost sm icon" disabled={i === 0} onClick={() => move(i, -1)} aria-label={`Move ${q.question_name} up`}>
                            ↑
                          </button>
                          <button className="btn ghost sm icon" disabled={i === survey.spec.length - 1} onClick={() => move(i, 1)} aria-label={`Move ${q.question_name} down`}>
                            ↓
                          </button>
                        </>
                      )}
                    </td>
                    <td>
                      <span className="cell-title">{q.question_name}</span>
                      {q.required && <span className="faint small"> · required</span>}
                      {q.question_description && <div className="small muted">{q.question_description}</div>}
                    </td>
                    <td className="mono">{q.variable}</td>
                    <td className="small">
                      {typeLabels[q.type]}
                      {(q.type === 'multiplechoice' || q.type === 'multiselect') && <span className="faint"> · {choicesOf(q).length} choices</span>}
                    </td>
                    <td className="mono small">{q.type === 'password' ? (q.default ? '••••••' : '') : Array.isArray(q.default) ? q.default.join(', ') : String(q.default ?? '')}</td>
                    <td className="end nowrap">
                      {!readOnly && (
                        <>
                          <button className="btn sm" onClick={() => setEditing({ index: i, q })}>
                            Edit
                          </button>{' '}
                          <button className="btn sm ghost" aria-label={`Remove ${q.question_name}`} onClick={() => update({ ...survey, spec: survey.spec.filter((_, j) => j !== i) })}>
                            <Icon name="trash" size={14} />
                          </button>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <aside className="narrow">
        <Panel title="Preview">
          {survey.spec.length ? <SurveyFields questions={survey.spec} values={{}} onChange={() => {}} preview /> : <p className="small muted" style={{ margin: 0 }}>Questions appear here as people will see them.</p>}
        </Panel>
      </aside>
      {editing && (
        <QuestionDialog
          initial={editing.q}
          taken={survey.spec.filter((_, i) => i !== editing.index).map((q) => q.variable)}
          onClose={() => setEditing(null)}
          onSave={(q) => {
            const spec = [...survey.spec]
            if (editing.index < 0) spec.push(q)
            else spec[editing.index] = q
            update({ ...survey, spec })
            setEditing(null)
          }}
        />
      )}
    </div>
  )
}

function QuestionDialog({ initial, taken, onClose, onSave }: { initial: Question; taken: string[]; onClose: () => void; onSave: (q: Question) => void }) {
  const [q, setQ] = useState<Question>({ ...initial, choices: choicesOf(initial).join('\n') })
  const set = <K extends keyof Question>(k: K, v: Question[K]) => setQ((p) => ({ ...p, [k]: v }))
  const isChoice = q.type === 'multiplechoice' || q.type === 'multiselect'
  const isNumber = q.type === 'integer' || q.type === 'float'
  const isText = q.type === 'text' || q.type === 'textarea' || q.type === 'password'
  const varError = !q.variable ? '' : !/^[A-Za-z_][A-Za-z0-9_]*$/.test(q.variable) ? 'Letters, digits and _ only; must not start with a digit.' : taken.includes(q.variable) ? 'Another question already sets this variable.' : ''
  const valid = q.question_name.trim() && q.variable && !varError && (!isChoice || choicesOf(q).length > 0)
  const finish = () => {
    const out: Question = { ...q }
    if (isChoice) {
      out.choices = choicesOf(q)
      if (q.type === 'multiselect' && typeof q.default === 'string') out.default = q.default.split('\n').map((s) => s.trim()).filter(Boolean).join('\n')
    } else delete out.choices
    if (isNumber) {
      out.default = q.default === '' || q.default === undefined ? '' : Number(q.default)
    }
    if (!isNumber && !isText) {
      delete out.min
      delete out.max
    }
    onSave(out)
  }
  return (
    <Dialog
      title={initial.question_name ? `Edit "${initial.question_name}"` : 'New question'}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!valid} onClick={finish}>
            {initial.new_question && !initial.question_name ? 'Add question' : 'Done'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <TextInput label="Question" value={q.question_name} onChange={(v) => set('question_name', v)} autoFocus />
        <TextInput label="Answer variable" value={q.variable} onChange={(v) => set('variable', v)} mono error={varError || undefined} help="Passed to the playbook as this extra variable." />
        <TextInput label="Help text" value={q.question_description ?? ''} onChange={(v) => set('question_description', v)} optional full />
        <SelectField label="Answer type" value={q.type} onChange={(v) => set('type', v as Question['type'])} options={Object.entries(typeLabels) as [string, string][]} />
        <div className="field" style={{ justifyContent: 'flex-end' }}>
          <Check label="An answer is required" checked={q.required} onChange={(v) => set('required', v)} />
        </div>
        {isChoice && <TextArea label="Choices" value={(q.choices as string) ?? ''} onChange={(v) => set('choices', v)} help="One per line." rows={5} />}
        {(isNumber || isText) && (
          <>
            <TextInput label={isNumber ? 'Minimum' : 'Minimum length'} type="number" value={q.min ?? ''} onChange={(v) => set('min', v === '' ? null : Number(v))} optional />
            <TextInput label={isNumber ? 'Maximum' : 'Maximum length'} type="number" value={q.max ?? ''} onChange={(v) => set('max', v === '' ? null : Number(v))} optional />
          </>
        )}
        {q.type === 'textarea' || q.type === 'multiselect' ? (
          <TextArea label="Default" value={Array.isArray(q.default) ? q.default.join('\n') : String(q.default ?? '')} onChange={(v) => set('default', v)} optional rows={3} help={q.type === 'multiselect' ? 'One choice per line.' : undefined} />
        ) : q.type === 'multiplechoice' ? (
          <SelectField label="Default" value={String(q.default ?? '')} onChange={(v) => set('default', v)} options={[['', 'No default'], ...choicesOf({ ...q }).map((c): [string, string] => [c, c])]} optional />
        ) : (
          <TextInput label="Default" type={q.type === 'password' ? 'password' : isNumber ? 'number' : 'text'} value={q.default as string} onChange={(v) => set('default', v)} optional mono={q.type !== 'text'} help={q.type === 'password' ? 'Stored encrypted. Leave $encrypted$ to keep the current default.' : undefined} />
        )}
      </div>
    </Dialog>
  )
}

/** Renders survey questions as form fields (used by the launch page and the preview). */
export function SurveyFields({
  questions,
  values,
  onChange,
  errors = {},
  preview,
}: {
  questions: Question[]
  values: Record<string, unknown>
  onChange: (variable: string, v: unknown) => void
  errors?: Record<string, string>
  preview?: boolean
}) {
  return (
    <div className="form" style={{ gap: 16 }}>
      {questions.map((q) => {
        const id = `sq-${q.variable}`
        const v = values[q.variable] ?? (q.type === 'password' && q.default ? '$encrypted$' : q.default ?? (q.type === 'multiselect' ? [] : ''))
        const choices = choicesOf(q)
        const label = (
          <>
            {q.question_name} {!q.required && <span className="opt">· optional</span>}
          </>
        )
        const help = errors[q.variable] ? <span className="err">{errors[q.variable]}</span> : q.question_description ? <span className="help">{q.question_description}</span> : null
        if (q.type === 'multiplechoice' && choices.length <= 4) {
          return (
            <fieldset key={q.variable} className="field">
              <legend style={{ marginBottom: 6 }}>{label}</legend>
              <div className="segmented">
                {choices.map((c) => (
                  <label key={c} className={v === c ? 'on' : ''}>
                    <input type="radio" name={id} checked={v === c} disabled={preview} onChange={() => onChange(q.variable, c)} />
                    {c}
                  </label>
                ))}
              </div>
              {help}
            </fieldset>
          )
        }
        if (q.type === 'multiselect') {
          const sel = Array.isArray(v) ? (v as string[]) : String(v).split('\n').filter(Boolean)
          return (
            <fieldset key={q.variable} className="field">
              <legend style={{ marginBottom: 6 }}>{label}</legend>
              <div className="stack" style={{ gap: 4 }}>
                {choices.map((c) => (
                  <label key={c} className="check">
                    <input type="checkbox" disabled={preview} checked={sel.includes(c)} onChange={(e) => onChange(q.variable, e.target.checked ? [...sel, c] : sel.filter((x) => x !== c))} />
                    {c}
                  </label>
                ))}
              </div>
              {help}
            </fieldset>
          )
        }
        return (
          <div key={q.variable} className="field">
            <label htmlFor={id}>{label}</label>
            {q.type === 'multiplechoice' ? (
              <select id={id} className="select" disabled={preview} value={String(v)} onChange={(e) => onChange(q.variable, e.target.value)} style={{ maxWidth: 440 }}>
                {!q.required && <option value="">—</option>}
                {choices.map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </select>
            ) : q.type === 'textarea' ? (
              <textarea id={id} className="textarea" disabled={preview} value={String(v)} onChange={(e) => onChange(q.variable, e.target.value)} aria-invalid={!!errors[q.variable]} />
            ) : (
              <input
                id={id}
                className={`input ${q.type !== 'text' ? 'mono' : ''}`}
                style={{ maxWidth: 440 }}
                disabled={preview}
                type={q.type === 'password' ? 'password' : q.type === 'integer' || q.type === 'float' ? 'number' : 'text'}
                step={q.type === 'float' ? 'any' : undefined}
                value={String(v)}
                aria-invalid={!!errors[q.variable]}
                onChange={(e) => onChange(q.variable, q.type === 'integer' || q.type === 'float' ? (e.target.value === '' ? '' : Number(e.target.value)) : e.target.value)}
              />
            )}
            {help}
          </div>
        )
      })}
    </div>
  )
}

/** Client-side validation mirroring the server's survey rules. */
export function validateSurvey(questions: Question[], values: Record<string, unknown>): Record<string, string> {
  const errs: Record<string, string> = {}
  for (const q of questions) {
    const v = values[q.variable] ?? q.default
    const empty = v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)
    if (empty) {
      if (q.required) errs[q.variable] = 'An answer is required.'
      continue
    }
    if (q.type === 'integer' || q.type === 'float') {
      const n = Number(v)
      if (isNaN(n) || (q.type === 'integer' && !Number.isInteger(n))) errs[q.variable] = q.type === 'integer' ? 'Enter a whole number.' : 'Enter a number.'
      else if (q.min !== null && q.min !== undefined && n < q.min) errs[q.variable] = `At least ${q.min}.`
      else if (q.max !== null && q.max !== undefined && n > q.max) errs[q.variable] = `At most ${q.max}.`
    } else if (typeof v === 'string' && v !== '$encrypted$') {
      if (q.min && v.length < q.min) errs[q.variable] = `At least ${q.min} characters.`
      else if (q.max && v.length > q.max) errs[q.variable] = `At most ${q.max} characters.`
    }
    if (q.type === 'multiplechoice' && !choicesOf(q).includes(String(v))) errs[q.variable] = 'Pick one of the choices.'
  }
  return errs
}
