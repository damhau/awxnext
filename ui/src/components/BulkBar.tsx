// The bulk action bar shared by the lists: a selection kept across pages,
// "Select all N" when the page is selected and more match, and a runner that
// applies an action to each selected row and reports in a toast.
import { useMemo, useState, type ReactNode } from 'react'
import { api, getAll, type Params } from '../api/client'
import { useInvalidate } from '../api/hooks'
import { bulkMessage, bulkOutcome, MAX_SELECT_ALL, nameList, nextSelection, offerSelectAll, type BulkOutcome } from '../lib/bulk'
import { HAS_USED_BY, inUse, summarize } from '../lib/usedBy'
import { useConfirm, useToast } from './overlay'
import { fetchUsedBy } from './UsedBy'
import { plural } from '../lib/format'
import { tr } from '../lib/i18n'

type Key = number | string

export interface Selection<T> {
  selected: Set<Key>
  onSelect: (next: Set<Key>) => void
  /** The selected rows, from every page seen. */
  rows: T[]
  size: number
  clear: () => void
  set: (rows: T[]) => void
}

export function useSelection<T>(page: T[] | undefined, key: (r: T) => Key = (r) => (r as { id: number }).id): Selection<T> {
  const [picked, setPicked] = useState<Map<Key, T>>(new Map())
  const selected = useMemo(() => new Set(picked.keys()), [picked])
  return {
    selected,
    onSelect: (next) => setPicked((p) => nextSelection(p, next, page ?? [], key)),
    rows: [...picked.values()],
    size: picked.size,
    clear: () => setPicked(new Map()),
    set: (rows) => setPicked(new Map(rows.map((r) => [key(r), r]))),
  }
}

/** Runs an action on each selected row, then toasts and refreshes the
 * lists. Returns the outcome and the rows it failed on. */
export function useBulk<T>(noun: [string, string], ...lists: string[]) {
  const toast = useToast()
  const invalidate = useInvalidate()
  return async (label: string, rows: T[], fn: (r: T) => Promise<unknown>): Promise<BulkOutcome & { failedRows: T[] }> => {
    const results = await Promise.allSettled(rows.map(fn))
    const o = bulkOutcome(results)
    toast(bulkMessage(label, o, noun), o.failed ? 'error' : undefined)
    if (lists.length) invalidate(...lists)
    return { ...o, failedRows: rows.filter((_, i) => results[i].status === 'rejected') }
  }
}

/** Deletes the selected rows after confirmation; those that could not be
 * deleted stay selected. */
export function BulkDelete<T>({
  sel,
  resource,
  noun,
  name,
  note,
}: {
  sel: Selection<T>
  resource: string
  noun: [string, string]
  name: (r: T) => string
  note?: ReactNode
}) {
  const confirm = useConfirm()
  const run = useBulk<T>(noun, resource)
  return (
    <button
      className="btn warn"
      onClick={async () => {
        const rows = sel.rows
        // What uses them, for up to 50 of them.
        const check = HAS_USED_BY.has(resource) && rows.length <= 50
        const used = check ? await Promise.all(rows.map((r) => fetchUsedBy(resource, (r as { id: number }).id))) : []
        const busy = rows.map((r, i) => ({ name: name(r), u: used[i] ?? null })).filter((x) => inUse(x.u))
        const ok = await confirm({
          title: tr('Delete {what}?', { what: plural(rows.length, noun[0], noun[1]) }),
          body: (
            <>
              <p style={{ margin: 0 }}>
                {nameList(rows.map(name))}. {note ?? tr('This cannot be undone.')}
              </p>
              {busy.length > 0 && (
                <div className="notice warn small stack" role="note" style={{ gap: 4, marginTop: 10 }}>
                  <div>{busy.length === 1 ? tr('One of them is in use:') : tr('{count} of them are in use:', { count: busy.length })}</div>
                  {busy.slice(0, 8).map((x) => (
                    <div key={x.name}>
                      <b>{x.name}</b>{tr(': used by')} {summarize(x.u!)}
                    </div>
                  ))}
                  {busy.length > 8 && <div>{tr('and')} {busy.length - 8} {tr('more.')}</div>}
                </div>
              )}
              {HAS_USED_BY.has(resource) && !check && <p className="small faint">{tr('Not checked for what uses them: more than 50 are selected.')}</p>}
            </>
          ),
          confirm: tr('Delete'),
          danger: true,
        })
        if (!ok) return
        const o = await run('Delete', rows, (r) => api.del(`${resource}/${(r as { id: number }).id}/`))
        sel.set(o.failedRows)
      }}
    >
      {tr('Delete')}
    </button>
  )
}

export function BulkBar<T>({
  sel,
  page,
  total,
  path,
  params,
  rowKey = (r) => (r as { id: number }).id,
  children,
  style,
}: {
  sel: Selection<T>
  /** The page shown, and how many rows match in all. */
  page?: T[]
  total?: number
  /** The list's path and filter, to select every matching row. */
  path?: string
  params?: Params
  rowKey?: (r: T) => Key
  children: ReactNode
  style?: React.CSSProperties
}) {
  const toast = useToast()
  const [loading, setLoading] = useState(false)
  if (sel.size === 0) return null
  const keys = (page ?? []).map(rowKey)
  const more = path && total !== undefined && offerSelectAll(sel.size, keys, (k) => sel.selected.has(k as Key), total)
  const selectAll = async () => {
    setLoading(true)
    try {
      const { page: _p, page_size: _s, ...rest } = params ?? {}
      const rows = await getAll<T>(path!, rest, Math.ceil(MAX_SELECT_ALL / 200))
      sel.set(rows.slice(0, MAX_SELECT_ALL))
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Could not select them all'), 'error')
    } finally {
      setLoading(false)
    }
  }
  return (
    <div className="bulkbar" role="region" aria-label={tr('Bulk actions')} style={style}>
      <span className="n">{sel.size} {tr('selected')}</span>
      {more && (
        <button className="btn" onClick={selectAll} disabled={loading}>
          {total! > MAX_SELECT_ALL ? tr('Select the first {MAX_SELECT_ALL}', { MAX_SELECT_ALL }) : tr('Select all {total}', { total })}
        </button>
      )}
      {children}
      <button className="btn clear" onClick={sel.clear}>
        {tr('Clear')}
      </button>
    </div>
  )
}
