import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { choosable, columnLabel, shownColumns, toggleColumn } from '../lib/columns'
import { useHotkeys } from '../lib/hotkeys'
import { usePref } from '../lib/prefs'
import { Icon } from './icons'
import { Menu } from './overlay'
import { Empty, ErrorNote, Loading } from './ui'

export interface Column<T> {
  key: string
  header: ReactNode
  render: (row: T) => ReactNode
  /** order_by field enabling sorting on this column. */
  sort?: string
  className?: string
  width?: number | string
  /** Always shown (the first column always is). */
  fixed?: boolean
  /** The column's name in the column chooser, when its header is not text. */
  label?: string
}

interface Props<T> {
  rows: T[] | undefined
  columns: Column<T>[]
  rowKey?: (r: T) => number | string
  loading?: boolean
  error?: unknown
  empty?: ReactNode
  selected?: Set<number | string>
  onSelect?: (next: Set<number | string>) => void
  onOpen?: (r: T) => void
  order?: string
  onOrder?: (o: string) => void
  /** Extra single-key shortcuts acting on the row under the cursor. */
  keys?: Record<string, (r: T) => void>
  keyboard?: boolean
  footer?: ReactNode
  minWidth?: number
  label?: string
  /** Rows shown above the data rows (folder rows); the caller spans the
   * columns, given how many data columns are shown. */
  lead?: ReactNode | ((span: number) => ReactNode)
  /** Offers a column chooser; the hidden columns are kept with the user's
   * preferences as columns.<columnsKey>. */
  columnsKey?: string
}

export function DataTable<T>({
  rows,
  columns,
  rowKey = (r) => (r as { id: number }).id,
  loading,
  error,
  empty,
  selected,
  onSelect,
  onOpen,
  order,
  onOrder,
  keys,
  keyboard = true,
  footer,
  minWidth,
  label,
  lead,
  columnsKey,
}: Props<T>) {
  const [cursor, setCursor] = useState(-1)
  const [hidden, setHidden] = usePref<string[]>(`columns.${columnsKey ?? 'none'}`, [])
  const all = columns
  columns = columnsKey ? shownColumns(all, hidden) : all
  const span = columns.length + (columnsKey ? 1 : 0)
  const list = rows ?? []
  useEffect(() => setCursor((c) => Math.min(c, list.length - 1)), [list.length])
  const toggle = (k: number | string) => {
    if (!selected || !onSelect) return
    const next = new Set(selected)
    if (next.has(k)) next.delete(k)
    else next.add(k)
    onSelect(next)
  }
  const bindings = useMemo(() => {
    const b: Record<string, () => void> = {
      j: () => setCursor((c) => Math.min(list.length - 1, c + 1)),
      k: () => setCursor((c) => Math.max(0, c - 1)),
      arrowdown: () => setCursor((c) => Math.min(list.length - 1, c + 1)),
      arrowup: () => setCursor((c) => Math.max(0, c - 1)),
    }
    if (onSelect) b.x = () => cursor >= 0 && toggle(rowKey(list[cursor]))
    if (onOpen) b.enter = () => cursor >= 0 && onOpen(list[cursor])
    for (const [k, fn] of Object.entries(keys ?? {})) b[k] = () => cursor >= 0 && fn(list[cursor])
    return b
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [list, cursor, selected, keys])
  useHotkeys(bindings, keyboard && list.length > 0)
  useEffect(() => {
    if (cursor < 0) return
    document.querySelector('tr.cursor')?.scrollIntoView({ block: 'nearest' })
  }, [cursor])

  const allKeys = list.map(rowKey)
  const allSelected = !!selected && allKeys.length > 0 && allKeys.every((k) => selected.has(k))
  // A multi-field order ("-started,-id") is shown on its first field's column.
  const primary = order?.split(',')[0]
  const sortState = (f?: string) => (!f || !primary ? null : primary === f ? 'ascending' : primary === '-' + f ? 'descending' : null)

  if (error) return <ErrorNote error={error} />
  return (
    <div className="table-wrap">
      <table className="data" style={minWidth ? { minWidth } : undefined} aria-label={label}>
        <thead>
          <tr>
            {onSelect && (
              <th className="sel" scope="col">
                <input
                  type="checkbox"
                  aria-label="Select all"
                  checked={allSelected}
                  onChange={() => onSelect(allSelected ? new Set([...selected!].filter((k) => !allKeys.includes(k))) : new Set([...(selected ?? []), ...allKeys]))}
                />
              </th>
            )}
            {columns.map((c) => {
              const s = sortState(c.sort)
              return (
                <th
                  key={c.key}
                  scope="col"
                  className={`${c.className ?? ''} ${c.sort && onOrder ? 'sortable' : ''}`}
                  style={c.width ? { width: c.width } : undefined}
                  aria-sort={s ?? undefined}
                  onClick={c.sort && onOrder ? () => onOrder(primary === c.sort ? '-' + c.sort : c.sort!) : undefined}
                >
                  {c.header}
                  {s && <span aria-hidden="true"> {s === 'ascending' ? '↑' : '↓'}</span>}
                </th>
              )
            })}
            {columnsKey && (
              <th className="cols-pick" scope="col">
                <ColumnChooser columns={all} hidden={hidden} onToggle={(k) => setHidden((h) => toggleColumn(h, k))} onReset={() => setHidden([])} />
              </th>
            )}
          </tr>
        </thead>
        <tbody>
          {typeof lead === 'function' ? lead(span) : lead}
          {list.map((r, i) => {
            const k = rowKey(r)
            const sel = selected?.has(k)
            return (
              <tr
                key={k}
                className={`${sel ? 'selected' : ''} ${i === cursor ? 'cursor' : ''} ${onOpen ? 'clickable' : ''}`}
                onClick={(e) => {
                  setCursor(i)
                  if (!onOpen) return
                  const t = e.target as HTMLElement
                  if (t.closest('a,button,input,label,select,textarea')) return
                  if (window.getSelection()?.toString()) return
                  onOpen(r)
                }}
              >
                {onSelect && (
                  <td className="sel">
                    <input type="checkbox" aria-label="Select row" checked={!!sel} onChange={() => toggle(k)} />
                  </td>
                )}
                {columns.map((c) => (
                  <td key={c.key} className={c.className}>
                    {c.render(r)}
                  </td>
                ))}
                {columnsKey && <td className="cols-pick" />}
              </tr>
            )
          })}
        </tbody>
      </table>
      {loading && list.length === 0 && <Loading />}
      {!loading && list.length === 0 && !lead && (empty ?? <Empty title="Nothing here yet" />)}
      {footer}
    </div>
  )
}

/** Shows or hides a list's columns; kept with the user's preferences. */
function ColumnChooser<T>({ columns, hidden, onToggle, onReset }: { columns: Column<T>[]; hidden: string[]; onToggle: (key: string) => void; onReset: () => void }) {
  const can = choosable(columns)
  if (!can.length) return null
  const n = can.filter((c) => hidden.includes(c.key)).length
  return (
    <Menu label={n ? `Columns, ${n} hidden` : 'Columns'} className={`btn sm icon ghost${n ? ' on' : ''}`} trigger={<Icon name="columns" size={14} />}>
      <div className="head">Columns</div>
      {can.map((c) => (
        <label key={c.key} className="menu-check">
          <input type="checkbox" checked={!hidden.includes(c.key)} onChange={() => onToggle(c.key)} />
          {columnLabel(c)}
        </label>
      ))}
      {n > 0 && (
        <>
          <hr />
          <button onClick={onReset}>Show all columns</button>
        </>
      )}
    </Menu>
  )
}

export function Pager({ count, page, pageSize, onPage }: { count: number; page: number; pageSize: number; onPage: (p: number) => void }) {
  const pages = Math.max(1, Math.ceil(count / pageSize))
  if (pages <= 1) return null
  return (
    <div className="pager">
      <button className="btn sm" disabled={page <= 1} onClick={() => onPage(page - 1)}>
        Previous
      </button>
      <span className="mono">
        {page} / {pages}
      </span>
      <button className="btn sm" disabled={page >= pages} onClick={() => onPage(page + 1)}>
        Next
      </button>
    </div>
  )
}

/** List state (query, page, order) kept in the URL so views are linkable. */
export function useListState(defaults: { order?: string; pageSize?: number } = {}) {
  const [sp, setSp] = useSearchParams()
  const set = (patch: Record<string, string | null>) =>
    setSp(
      (prev) => {
        const n = new URLSearchParams(prev)
        for (const [k, v] of Object.entries(patch)) {
          if (v === null || v === '') n.delete(k)
          else n.set(k, v)
        }
        return n
      },
      { replace: true },
    )
  return {
    q: sp.get('q') ?? '',
    page: Number(sp.get('page') ?? 1) || 1,
    order: sp.get('order') ?? defaults.order ?? '',
    pageSize: defaults.pageSize ?? 25,
    get: (k: string) => sp.get(k),
    setQ: (q: string) => set({ q, page: null }),
    setPage: (p: number) => set({ page: p > 1 ? String(p) : null }),
    setOrder: (o: string) => set({ order: o, page: null }),
    set,
  }
}
