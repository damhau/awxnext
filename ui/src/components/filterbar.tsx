import { useEffect, useRef, useState, type ReactNode } from 'react'
import { tokenize, type KeyTable } from '../lib/query'
import { Icon } from './icons'
import { tr } from '../lib/i18n'

const structuredOf = (q: string, keys: KeyTable) => tokenize(q).filter((t) => t.key && keys[t.key])
const freeOf = (q: string, keys: KeyTable) =>
  tokenize(q)
    .filter((t) => !(t.key && keys[t.key]))
    .map((t) => t.raw)
    .join(' ')

/**
 * Filter input for the query language (see lib/query.ts). Applied key:value
 * terms show as removable chips; the input holds free text and terms being
 * typed. Free text applies after a pause, key:value terms on Enter or blur.
 */
export function FilterBar({
  value,
  onChange,
  keys,
  placeholder,
  chips = [],
  children,
  label = 'Filter',
}: {
  value: string
  onChange: (q: string) => void
  keys: KeyTable
  placeholder?: string
  chips?: { label: ReactNode; onRemove: () => void }[]
  children?: ReactNode
  label?: string
}) {
  const [draft, setDraft] = useState(() => freeOf(value, keys))
  const [focus, setFocus] = useState(false)
  const timer = useRef<number | undefined>(undefined)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => setDraft(freeOf(value, keys)), [value])
  const applied = structuredOf(value, keys)
  const apply = (d: string, base = applied) => {
    window.clearTimeout(timer.current)
    const next = [...base.map((t) => t.raw), d.trim()].filter(Boolean).join(' ')
    if (next !== value.trim()) onChange(next)
  }
  const remove = (raw: string) =>
    apply(
      draft,
      applied.filter((t) => t.raw !== raw),
    )
  const unknown = tokenize(draft)
    .filter((t) => t.key && !keys[t.key])
    .map((t) => t.key)
  return (
    <div className="stack" style={{ gap: 4 }}>
      <div className="filterbar">
        <div className="filter-input">
          <Icon name="search" size={14} style={{ color: 'var(--muted-2)' }} />
          {chips.map((c, i) => (
            <span key={i} className="chip">
              {c.label}
              <button type="button" aria-label={tr('Remove filter')} onClick={c.onRemove}>
                <Icon name="x" size={11} />
              </button>
            </span>
          ))}
          {applied.map((t) => (
            <span key={t.raw} className="chip">
              {t.negate && tr('not ')}
              {t.key}:<b>{t.value}</b>
              <button type="button" aria-label={tr('Remove {raw}', { raw: t.raw })} onClick={() => remove(t.raw)}>
                <Icon name="x" size={11} />
              </button>
            </span>
          ))}
          <label className="sr-only" htmlFor="filter-q">
            {label}
          </label>
          <input
            id="filter-q"
            data-filter
            autoComplete="off"
            spellCheck={false}
            value={draft}
            placeholder={applied.length || chips.length ? '' : placeholder}
            onChange={(e) => {
              const d = e.target.value
              setDraft(d)
              window.clearTimeout(timer.current)
              if (!tokenize(d).some((t) => t.key)) timer.current = window.setTimeout(() => apply(d), 400)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') apply(draft)
              if (e.key === 'Escape') (e.target as HTMLInputElement).blur()
              if (e.key === 'Backspace' && draft === '' && applied.length) remove(applied[applied.length - 1].raw)
            }}
            onFocus={() => setFocus(true)}
            onBlur={() => {
              setFocus(false)
              apply(draft)
            }}
          />
        </div>
        {children}
      </div>
      {unknown.length > 0 ? (
        <span className="small fail-text">
          {tr('Unknown filter')} {unknown.map((k) => `"${k}:"`).join(', ')}{tr('. Available:')} {Object.keys(keys).map((k) => k + ':').join(' ')}
        </span>
      ) : (
        focus && (
          <span className="hint">
            {Object.entries(keys)
              .map(([k, s]) => `${k}:${s.help ?? '…'}`)
              .join('  ')}
            {'  '}{tr('· -key:value excludes · * matches any text · Enter applies')}
          </span>
        )
      )}
    </div>
  )
}
