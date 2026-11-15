import { useId, useRef, useState, type ReactNode } from 'react'
import { Field } from '../../components/form'
import { completionsAt, offsetAt, parseCelErrors, type Completion, type StreamSchema } from '../../lib/eventRules'
import { tr } from '../../lib/i18n'

/** A CEL expression: completion from the rule's streams' schema, and the
 * check's errors under it, each pointing at its place in the text. */
export function CelInput({
  label,
  value,
  onChange,
  schema,
  error,
  help,
  optional,
  rows = 1,
  placeholder,
  full = true,
  ariaLabel,
}: {
  label?: ReactNode
  value: string
  onChange: (v: string) => void
  schema: StreamSchema | null
  error?: string
  help?: ReactNode
  optional?: boolean
  rows?: number
  placeholder?: string
  full?: boolean
  /** For an input labelled elsewhere, as an extra variable's value. */
  ariaLabel?: string
}) {
  const id = useId()
  const ref = useRef<HTMLTextAreaElement>(null)
  const [items, setItems] = useState<Completion[]>([])
  const [hi, setHi] = useState(0)
  const refresh = (el: HTMLTextAreaElement) => {
    const next = el.selectionStart === el.selectionEnd ? completionsAt(schema, el.value, el.selectionStart) : []
    setItems(next.slice(0, 50))
    setHi(0)
  }
  const accept = (c: Completion) => {
    const el = ref.current
    if (!el) return
    const cursor = el.selectionStart
    const next = value.slice(0, c.from) + c.insert + value.slice(cursor)
    onChange(next)
    setItems([])
    const at = c.from + c.insert.length
    window.requestAnimationFrame(() => {
      el.focus()
      el.setSelectionRange(at, at)
    })
  }
  const errors = parseCelErrors(error)
  const goTo = (line?: number, column?: number) => {
    const el = ref.current
    if (!el || !line) return
    const at = offsetAt(value, line, column ?? 1)
    el.focus()
    el.setSelectionRange(at, at)
  }
  const lines = value.split('\n')
  const input = (
    <div className="cel">
      <textarea
        id={id}
        ref={ref}
        className="textarea mono cel-text"
        rows={Math.max(rows, Math.min(8, lines.length))}
        value={value}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        aria-invalid={errors.length > 0}
        aria-label={ariaLabel}
        aria-autocomplete="list"
        aria-expanded={items.length > 0}
        aria-controls={items.length ? `${id}-list` : undefined}
        aria-activedescendant={items.length ? `${id}-opt-${hi}` : undefined}
        onChange={(e) => {
          onChange(e.target.value)
          refresh(e.target)
        }}
        onClick={(e) => refresh(e.currentTarget)}
        onBlur={() => window.setTimeout(() => setItems([]), 120)}
        onKeyDown={(e) => {
          if (e.key === ' ' && e.ctrlKey) {
            e.preventDefault()
            refresh(e.currentTarget)
            return
          }
          if (!items.length) return
          if (e.key === 'ArrowDown') setHi((h) => (h + 1) % items.length)
          else if (e.key === 'ArrowUp') setHi((h) => (h - 1 + items.length) % items.length)
          else if (e.key === 'Enter' || e.key === 'Tab') accept(items[hi])
          else if (e.key === 'Escape') setItems([])
          else return
          e.preventDefault()
        }}
      />
      {items.length > 0 && (
        <div className="popover cel-pop" role="listbox" id={`${id}-list`} aria-label={tr('Completions')}>
          {items.map((c, i) => (
            <div
              key={c.label + i}
              id={`${id}-opt-${i}`}
              role="option"
              aria-selected={i === hi}
              className={`opt mono ${i === hi ? 'hi' : ''}`}
              onMouseDown={(e) => {
                e.preventDefault()
                accept(c)
              }}
              onMouseEnter={() => setHi(i)}
            >
              <span>{c.label}</span>
              <span className="meta">{c.detail}</span>
            </div>
          ))}
        </div>
      )}
      {errors.length > 0 && (
        <ul className="cel-errors" aria-live="polite">
          {errors.map((er, i) => (
            <li key={i}>
              {er.line ? (
                <>
                  <button type="button" className="link-btn mono small" onClick={() => goTo(er.line, er.column)}>
                    {tr('{line}:{column}', { line: er.line, column: er.column })}
                  </button>{' '}
                  <span>{er.message}</span>
                  <pre className="cel-caret" aria-hidden="true">
                    {lines[er.line - 1] ?? ''}
                    {'\n'}
                    {' '.repeat(Math.max(0, (er.column ?? 1) - 1))}^
                  </pre>
                </>
              ) : (
                <span>{er.message}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
  if (!label) return input
  return (
    <Field label={label} help={errors.length ? undefined : help} optional={optional} full={full} htmlFor={id}>
      {input}
    </Field>
  )
}
