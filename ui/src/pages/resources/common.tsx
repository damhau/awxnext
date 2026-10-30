// Helpers shared by the resource pages (inventories, hosts, projects,
// credentials, execution environments).
import { useEffect, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { useOrg } from '../../app/context'
import { Icon } from '../../components/icons'
import { useConfirm, useToast } from '../../components/overlay'
import { DeleteEffects, fetchUsedBy } from '../../components/UsedBy'
import { HAS_USED_BY } from '../../lib/usedBy'
import { userName } from '../../lib/format'
import type { KeySpec } from '../../lib/query'

/** Filter box bound to the list's ?q=; Enter applies, Escape clears. "/" focuses it. */
export function FilterBox({ value, onApply, placeholder, hint }: { value: string; onApply: (q: string) => void; placeholder: string; hint?: ReactNode }) {
  const [text, setText] = useState(value)
  useEffect(() => setText(value), [value])
  useEffect(() => {
    if (text === value) return
    const t = window.setTimeout(() => onApply(text), 350)
    return () => window.clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text])
  return (
    <div className="filterbar">
      <label className="filter-input">
        <span className="sr-only">Filter</span>
        <Icon name="search" size={14} style={{ color: 'var(--muted-2)' }} />
        <input
          data-filter
          value={text}
          placeholder={placeholder}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onApply(text)
            if (e.key === 'Escape') {
              setText('')
              onApply('')
              ;(e.target as HTMLInputElement).blur()
            }
          }}
        />
        {text && (
          <button
            type="button"
            className="btn ghost sm icon"
            aria-label="Clear filter"
            onClick={() => {
              setText('')
              onApply('')
            }}
          >
            <Icon name="x" size={12} />
          </button>
        )}
      </label>
      {hint}
    </div>
  )
}

/** Organization column, shown only when every organization is listed. */
export function useOrgColumn<T extends Base>() {
  const { org } = useOrg()
  if (org) return []
  return [
    {
      key: 'org',
      header: 'Organization',
      render: (r: T) => <span className="small">{r.summary_fields.organization?.name ?? <span className="faint">—</span>}</span>,
    },
  ]
}

/** Filter on the folder an object is in, by name: folder:checkout (or a path, folder:apps/checkout). */
export const folderKey: KeySpec = { field: 'folder__name', lookup: 'iexact', map: (v) => v.split('/').pop() ?? v, help: 'checkout' }

/** Folder column, shown only when some row of the page is in a folder. */
export function folderColumn<T extends Base>(rows: T[] | undefined) {
  if (!rows?.some((r) => r.summary_fields.folder)) return []
  return [
    {
      key: 'folder',
      header: 'Folder',
      className: 'small',
      render: (r: T) => {
        const f = r.summary_fields.folder
        return f ? <Link to={`/folders/${f.id}`}>{f.name}</Link> : <span className="faint">—</span>
      },
    },
  ]
}

/** Deletes an object after confirmation and returns to a list. */
export function useDelete(resource: string, after?: string) {
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const nav = useNavigate()
  return async (obj: { id: number; name: string }, what = 'object', body?: ReactNode) => {
    const used = HAS_USED_BY.has(resource) ? await fetchUsedBy(resource, obj.id) : null
    const ok = await confirm({
      title: `Delete ${obj.name}?`,
      body: (
        <>
          {body ?? <p style={{ margin: 0 }}>This permanently deletes the {what}. It cannot be undone.</p>}
          <DeleteEffects u={used} />
        </>
      ),
      confirm: 'Delete',
      danger: true,
    })
    if (!ok) return false
    try {
      await api.del(`${resource}/${obj.id}/`)
      toast(`Deleted ${obj.name}`)
      invalidate(resource)
      if (after) nav(after)
      return true
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Delete failed', 'error')
      return false
    }
  }
}

/** Notice for objects owned by a GitOps source or other external system. */
export function ManagedNotice({ managedBy }: { managedBy?: string }) {
  if (!managedBy) return null
  const m = managedBy.match(/^gitops:(\d+)$/)
  return (
    <div className="notice accent row" role="note">
      <Icon name="git" />
      <span>
        Managed by {m ? <Link to={`/gitops`}>GitOps source #{m[1]}</Link> : <span className="mono">{managedBy}</span>}. Change it in Git; edits here are rejected.
      </span>
    </div>
  )
}

/** "created 3 days ago by Priya" style line. */
export function Provenance({ obj }: { obj: Base }) {
  const sf = obj.summary_fields
  return (
    <span className="small faint">
      Created {obj.created ? new Date(obj.created).toLocaleDateString() : '—'}
      {sf.created_by ? ` by ${userName(sf.created_by)}` : ''}
      {obj.modified && obj.modified !== obj.created ? ` · edited ${new Date(obj.modified).toLocaleDateString()}${sf.modified_by ? ` by ${userName(sf.modified_by)}` : ''}` : ''}
    </span>
  )
}

/** Maps an ApiError onto form field errors; returns a message for anything else. */
export function applyErrors(e: unknown, setErrors: (f: Record<string, string>) => void): unknown {
  if (e instanceof ApiError && e.status === 400) {
    const f = e.fields
    setErrors(f)
    const known = Object.keys(f).length
    return known ? new Error('Fix the highlighted fields.') : e
  }
  return e
}

export const refOf = (sf: Ref | undefined | null, id?: number | null): Ref | null => (sf ? sf : id ? { id, name: `#${id}` } : null)

/** Placeholder kept for secret fields the API returns masked. */
export const SECRET = '$encrypted$'

export function Count({ n, bad }: { n: number | undefined; bad?: number }) {
  return (
    <span className="mono small">
      {n ?? 0}
      {bad ? <span className="fail-text"> · {bad} failing</span> : null}
    </span>
  )
}

/** Small "type" badge (mono) used in lists. */
export function KindTag({ children }: { children: ReactNode }) {
  return <span className="tag">{children}</span>
}

/** Title + subtitle cell used by most lists. */
export function NameCell({ to, name, sub, mono }: { to: string; name: string; sub?: ReactNode; mono?: boolean }) {
  return (
    <div style={{ minWidth: 0 }}>
      <Link to={to} className="cell-title">
        {name}
      </Link>
      {sub &&
        (mono ? (
          <div className="cell-sub ellipsis">{sub}</div>
        ) : (
          <div className="small faint ellipsis" style={{ marginTop: 2, maxWidth: 520 }}>
            {sub}
          </div>
        ))}
    </div>
  )
}
