import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { parse as parseYaml, stringify as stringifyYaml } from 'yaml'
import { api, type Page } from '../api/client'
import type { Ref } from '../api/types'
import { moveEarlier } from '../lib/relations'
import { Icon } from './icons'
import { tr } from '../lib/i18n'

export function Field({
  label,
  help,
  error,
  optional,
  children,
  full,
  htmlFor,
}: {
  label: ReactNode
  help?: ReactNode
  error?: string
  optional?: boolean
  children: ReactNode
  full?: boolean
  htmlFor?: string
}) {
  return (
    <div className={`field ${full ? 'full' : ''}`}>
      <label htmlFor={htmlFor}>
        {label} {optional && <span className="opt">{tr('· optional')}</span>}
      </label>
      {children}
      {error ? <span className="err">{error}</span> : help ? <span className="help">{help}</span> : null}
    </div>
  )
}

export function TextInput({
  label,
  value,
  onChange,
  error,
  help,
  optional,
  mono,
  type = 'text',
  placeholder,
  full,
  disabled,
  autoFocus,
}: {
  label: ReactNode
  value: string | number | null | undefined
  onChange: (v: string) => void
  error?: string
  help?: ReactNode
  optional?: boolean
  mono?: boolean
  type?: string
  placeholder?: string
  full?: boolean
  disabled?: boolean
  autoFocus?: boolean
}) {
  const id = useId()
  return (
    <Field label={label} help={help} error={error} optional={optional} full={full} htmlFor={id}>
      <input
        id={id}
        className={`input ${mono ? 'mono' : ''}`}
        type={type}
        value={value ?? ''}
        placeholder={placeholder}
        aria-invalid={!!error}
        disabled={disabled}
        autoFocus={autoFocus}
        onChange={(e) => onChange(e.target.value)}
      />
    </Field>
  )
}

export function TextArea({
  label,
  value,
  onChange,
  error,
  help,
  optional,
  mono,
  rows = 4,
  full = true,
}: {
  label: ReactNode
  value: string | null | undefined
  onChange: (v: string) => void
  error?: string
  help?: ReactNode
  optional?: boolean
  mono?: boolean
  rows?: number
  full?: boolean
}) {
  const id = useId()
  return (
    <Field label={label} help={help} error={error} optional={optional} full={full} htmlFor={id}>
      <textarea id={id} className={`textarea ${mono ? 'mono' : ''}`} rows={rows} value={value ?? ''} aria-invalid={!!error} onChange={(e) => onChange(e.target.value)} />
    </Field>
  )
}

export function SelectField({
  label,
  value,
  onChange,
  options,
  error,
  help,
  optional,
  full,
  disabled,
}: {
  label: ReactNode
  value: string | number | null | undefined
  onChange: (v: string) => void
  options: [string | number, string][]
  error?: string
  help?: ReactNode
  optional?: boolean
  full?: boolean
  disabled?: boolean
}) {
  const id = useId()
  return (
    <Field label={label} help={help} error={error} optional={optional} full={full} htmlFor={id}>
      <select id={id} className="select" value={value ?? ''} onChange={(e) => onChange(e.target.value)} aria-invalid={!!error} disabled={disabled}>
        {options.map(([v, l]) => (
          <option key={String(v)} value={v}>
            {l}
          </option>
        ))}
      </select>
    </Field>
  )
}

export function Check({ label, checked, onChange, help }: { label: ReactNode; checked: boolean; onChange: (v: boolean) => void; help?: ReactNode }) {
  return (
    <div className="field">
      <label className="check">
        <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
        <span>{label}</span>
      </label>
      {help && <span className="help" style={{ marginLeft: 22 }}>{help}</span>}
    </div>
  )
}

export function Segmented<T extends string | number>({
  legend,
  value,
  onChange,
  options,
  name,
}: {
  legend: ReactNode
  value: T
  onChange: (v: T) => void
  options: [T, ReactNode][]
  name?: string
}) {
  const auto = useId()
  return (
    <fieldset>
      <legend>{legend}</legend>
      <div className="segmented">
        {options.map(([v, l]) => (
          <label key={String(v)} className={v === value ? 'on' : ''}>
            <input type="radio" name={name ?? auto} checked={v === value} onChange={() => onChange(v)} />
            {l}
          </label>
        ))}
      </div>
    </fieldset>
  )
}

/** Variables editor: accepts YAML or JSON, validates as you type. */
export function VarsEditor({
  label = 'Variables',
  value,
  onChange,
  error,
  help,
  rows = 6,
  optional = true,
}: {
  label?: ReactNode
  value: string | null | undefined
  onChange: (v: string) => void
  error?: string
  help?: ReactNode
  rows?: number
  optional?: boolean
}) {
  const id = useId()
  const parseError = useMemo(() => varsError(value ?? ''), [value])
  return (
    <Field
      label={label}
      optional={optional}
      full
      htmlFor={id}
      error={error ?? parseError ?? undefined}
      help={help ?? tr('YAML or JSON. A mapping of variable names to values.')}
    >
      <textarea
        id={id}
        className="textarea mono"
        rows={rows}
        spellCheck={false}
        value={value ?? ''}
        aria-invalid={!!(error || parseError)}
        onChange={(e) => onChange(e.target.value)}
      />
    </Field>
  )
}

export function varsError(text: string): string | null {
  if (!text.trim()) return null
  try {
    const v = parseYaml(text)
    if (v !== null && (typeof v !== 'object' || Array.isArray(v))) return tr('Variables must be a mapping (key: value).')
    return null
  } catch (e) {
    return (e as Error).message.split('\n')[0]
  }
}

export function parseVars(text: string | null | undefined): Record<string, unknown> {
  if (!text || !text.trim()) return {}
  try {
    const v = parseYaml(text)
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

export function varsText(v: Record<string, unknown> | string | null | undefined): string {
  if (!v) return ''
  if (typeof v === 'string') {
    try {
      const parsed = JSON.parse(v)
      if (parsed && typeof parsed === 'object' && Object.keys(parsed).length === 0) return ''
      return stringifyYaml(parsed)
    } catch {
      return v
    }
  }
  return Object.keys(v).length ? stringifyYaml(v) : ''
}

export function FormError({ error }: { error: unknown }) {
  if (!error) return null
  return (
    <div className="form-error" role="alert">
      {error instanceof Error ? error.message : String(error)}
    </div>
  )
}

/** Async single-object picker over an API list. */
export function Lookup({
  label,
  resource,
  value,
  onChange,
  params,
  error,
  help,
  optional,
  placeholder = 'Search…',
  display = (r: Ref) => r.name,
  meta,
  full,
  order,
}: {
  label: ReactNode
  resource: string
  value: Ref | null
  onChange: (v: Ref | null) => void
  params?: Record<string, string | number>
  error?: string
  help?: ReactNode
  optional?: boolean
  placeholder?: string
  display?: (r: Ref) => string
  meta?: (r: Ref) => ReactNode
  full?: boolean
  /** order_by for the options (default name). */
  order?: string
}) {
  const id = useId()
  return (
    <Field label={label} error={error} help={help} optional={optional} htmlFor={id} full={full}>
      <Picker id={id} resource={resource} params={params} placeholder={placeholder} display={display} meta={meta} order={order} values={value ? [value] : []} single onChange={(v) => onChange(v[0] ?? null)} />
    </Field>
  )
}

export function MultiLookup({
  label,
  resource,
  values,
  onChange,
  params,
  error,
  help,
  optional,
  display = (r: Ref) => r.name,
  meta,
  full,
  order,
  ordered,
}: {
  label: ReactNode
  resource: string
  values: Ref[]
  onChange: (v: Ref[]) => void
  params?: Record<string, string | number>
  error?: string
  help?: ReactNode
  optional?: boolean
  display?: (r: Ref) => string
  meta?: (r: Ref) => ReactNode
  full?: boolean
  order?: string
  /** The values' order matters (instance groups): numbered, with a move
   * earlier button. */
  ordered?: boolean
}) {
  const id = useId()
  return (
    <Field label={label} error={error} help={help} optional={optional} htmlFor={id} full={full}>
      <Picker id={id} resource={resource} params={params} placeholder={tr('Add…')} display={display} meta={meta} order={order} values={values} onChange={onChange} ordered={ordered} />
    </Field>
  )
}

function Picker({
  id,
  resource,
  params,
  placeholder,
  display,
  meta,
  values,
  single,
  onChange,
  order = 'name',
  ordered,
}: {
  id: string
  resource: string
  params?: Record<string, string | number>
  placeholder: string
  display: (r: Ref) => string
  meta?: (r: Ref) => ReactNode
  values: Ref[]
  single?: boolean
  onChange: (v: Ref[]) => void
  order?: string
  ordered?: boolean
}) {
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const [hi, setHi] = useState(0)
  const [items, setItems] = useState<Ref[]>([])
  const [loading, setLoading] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const key = JSON.stringify(params ?? {})
  useEffect(() => {
    if (!open) return
    const ctl = new AbortController()
    setLoading(true)
    const t = window.setTimeout(() => {
      api
        .get<Page<Ref>>(resource, { ...(params ?? {}), search: q || undefined, page_size: 20, order_by: order }, ctl.signal)
        .then((r) => {
          setItems(r.results)
          setHi(0)
        })
        .catch(() => {})
        .finally(() => setLoading(false))
    }, 150)
    return () => {
      ctl.abort()
      window.clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, open, resource, key])
  useEffect(() => {
    if (!open) return
    const on = (e: MouseEvent) => !box.current?.contains(e.target as Node) && setOpen(false)
    document.addEventListener('mousedown', on)
    return () => document.removeEventListener('mousedown', on)
  }, [open])
  const pick = (r: Ref) => {
    if (single) {
      onChange([r])
      setOpen(false)
      setQ('')
      return
    }
    if (!values.some((v) => v.id === r.id)) onChange([...values, r])
    setQ('')
  }
  const visible = items.filter((r) => single || !values.some((v) => v.id === r.id))
  return (
    <div className="lookup" ref={box}>
      <div className="lookup-box" onClick={() => box.current?.querySelector('input')?.focus()}>
        {!single &&
          values.map((v, i) => (
            <span key={v.id} className="chip">
              {ordered && <span className="faint">{i + 1}.</span>}
              {display(v)}
              {ordered && i > 0 && (
                <button type="button" aria-label={tr('Move {display} earlier', { display: display(v) })} title={tr('Move earlier')} onClick={() => onChange(moveEarlier(values, i))}>
                  <Icon name="arrowLeft" size={12} />
                </button>
              )}
              <button type="button" aria-label={tr('Remove {display}', { display: display(v) })} onClick={() => onChange(values.filter((x) => x.id !== v.id))}>
                <Icon name="x" size={12} />
              </button>
            </span>
          ))}
        <input
          id={id}
          value={open || !single || !values[0] ? q : display(values[0])}
          placeholder={single && values[0] ? display(values[0]) : placeholder}
          onFocus={() => setOpen(true)}
          onChange={(e) => {
            setQ(e.target.value)
            setOpen(true)
          }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              setHi((h) => Math.min(visible.length - 1, h + 1))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setHi((h) => Math.max(0, h - 1))
            } else if (e.key === 'Enter') {
              if (open && visible[hi]) {
                e.preventDefault()
                pick(visible[hi])
              }
            } else if (e.key === 'Escape') {
              setOpen(false)
            } else if (e.key === 'Backspace' && !q && !single && values.length) {
              onChange(values.slice(0, -1))
            }
          }}
          role="combobox"
          aria-expanded={open}
          aria-autocomplete="list"
        />
        {single && values[0] && (
          <button type="button" className="btn ghost sm icon" aria-label={tr('Clear')} onClick={() => onChange([])}>
            <Icon name="x" size={12} />
          </button>
        )}
      </div>
      {open && (
        <div className="popover" role="listbox">
          {loading && visible.length === 0 && <div className="opt faint">{tr('Searching…')}</div>}
          {!loading && visible.length === 0 && <div className="opt faint">{tr('No matches')}</div>}
          {visible.map((r, i) => (
            <div
              key={r.id}
              role="option"
              aria-selected={i === hi}
              className={`opt ${i === hi ? 'hi' : ''}`}
              onMouseEnter={() => setHi(i)}
              onMouseDown={(e) => {
                e.preventDefault()
                pick(r)
              }}
            >
              <span>{display(r)}</span>
              {meta && <span className="meta">{meta(r)}</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

/** Keeps form state and server field errors together. */
export function useForm<T extends Record<string, unknown>>(initial: T) {
  const [values, setValues] = useState<T>(initial)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const set = <K extends keyof T>(k: K, v: T[K]) => {
    setValues((prev) => ({ ...prev, [k]: v }))
    if (errors[k as string]) setErrors((e) => ({ ...e, [k as string]: '' }))
  }
  return { values, setValues, set, errors, setErrors }
}
