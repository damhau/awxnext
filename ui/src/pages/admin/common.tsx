import { useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { api, type Params } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import { Icon } from '../../components/icons'
import { useConfirm, useToast } from '../../components/overlay'
import { BulkBar, useSelection, type Selection } from '../../components/BulkBar'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { PageHead } from '../../components/ui'
import { compile, type KeyTable } from '../../lib/query'
import { tr } from '../../lib/i18n'

/** The query box used by list pages: free text searches, key:value terms filter. */
export function FilterBox({ value, onChange, placeholder, keys }: { value: string; onChange: (v: string) => void; placeholder: string; keys?: KeyTable }) {
  const [draft, setDraft] = useState(value)
  const [last, setLast] = useState(value)
  if (value !== last) {
    setLast(value)
    setDraft(value)
  }
  const unknown = keys ? compile(draft, keys).unknown : []
  return (
    <div className="filterbar">
      <label className="filter-input" style={{ flex: '1 1 420px' }}>
        <Icon name="search" size={14} />
        <span className="sr-only">{tr('Filter')}</span>
        <input
          data-filter
          value={draft}
          placeholder={placeholder}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onChange(draft.trim())
            if (e.key === 'Escape') {
              setDraft('')
              onChange('')
              ;(e.target as HTMLInputElement).blur()
            }
          }}
          onBlur={() => draft.trim() !== value && onChange(draft.trim())}
        />
        {draft && (
          <button
            type="button"
            className="btn ghost sm icon"
            aria-label={tr('Clear filter')}
            onClick={() => {
              setDraft('')
              onChange('')
            }}
          >
            <Icon name="x" size={12} />
          </button>
        )}
      </label>
      {unknown.length > 0 && <span className="small fail-text">{tr('Unknown filter')} {unknown.map((u) => `${u}:`).join(', ')}</span>}
      {keys && (
        <span className="small faint" title={Object.entries(keys).map(([k, s]) => `${k}: ${s.help ?? s.field}`).join('\n')}>
          {tr('Filters')}{' '}
          {Object.keys(keys).map((k) => (
            <span key={k} className="mono">
              {k}:{' '}
            </span>
          ))}
        </span>
      )}
    </div>
  )
}

/** A complete list page: header, filter, table, pagination, URL-synced state. */
export function ListPage<T extends { id: number }>({
  title,
  subtitle,
  actions,
  path,
  params,
  columns,
  keys,
  placeholder = tr('Filter by name…'),
  order = 'name',
  empty,
  onOpen,
  rowKeys,
  bulk,
  children,
}: {
  title: string
  subtitle?: ReactNode
  actions?: ReactNode
  path: string
  params?: Params
  columns: Column<T>[]
  keys?: KeyTable
  placeholder?: string
  order?: string
  empty?: ReactNode
  onOpen?: (r: T) => void
  rowKeys?: Record<string, (r: T) => void>
  /** The bulk actions on the selected rows; the list is selectable when given. */
  bulk?: (sel: Selection<T>) => ReactNode
  children?: ReactNode
}) {
  const ls = useListState({ order })
  const compiled = compile(ls.q, keys ?? {})
  const listParams = { ...params, ...compiled.params, order_by: ls.order, page: ls.page, page_size: ls.pageSize }
  const list = useList<T>(path, listParams)
  const sel = useSelection<T>(list.data?.results)
  return (
    <div className="page">
      <PageHead title={title} subtitle={subtitle} actions={actions} bordered={false}>
        <FilterBox value={ls.q} onChange={ls.setQ} placeholder={placeholder} keys={keys} />
      </PageHead>
      {children}
      <div className="page-body">
        {bulk && (
          <BulkBar sel={sel} page={list.data?.results} total={list.data?.count} path={path} params={listParams} style={{ marginBottom: 12 }}>
            {bulk(sel)}
          </BulkBar>
        )}
        <DataTable
          label={title}
          rows={list.data?.results}
          columns={columns}
          selected={bulk ? sel.selected : undefined}
          onSelect={bulk ? sel.onSelect : undefined}
          columnsKey={path.replace(/\/$/, '').replace(/[^a-z0-9_.-]/g, '-')}
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={onOpen}
          keys={rowKeys}
          empty={ls.q ? undefined : empty}
          footer={
            list.data && list.data.count > 0 ? (
              <div className="table-foot">
                <span>
                  {list.data.count} {list.data.count === 1 ? tr('item') : tr('items')}
                  {ls.q ? tr(' match the filter') : ''}
                </span>
                <Pager count={list.data.count} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
              </div>
            ) : null
          }
        />
      </div>
    </div>
  )
}

/** Confirmed delete with toast and navigation. */
export function useDelete() {
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const nav = useNavigate()
  return async (opts: { path: string; name: string; what: string; body?: ReactNode; then?: string; invalidate?: string[] }) => {
    const ok = await confirm({
      title: tr('Delete {what} “{name}”?', { what: opts.what, name: opts.name }),
      body: opts.body ?? <p style={{ margin: 0 }}>{tr('This cannot be undone.')}</p>,
      confirm: tr('Delete {what}', { what: opts.what }),
      danger: true,
    })
    if (!ok) return false
    try {
      await api.del(opts.path)
      toast(tr('Deleted {name}', { name: opts.name }))
      invalidate(...(opts.invalidate ?? [opts.path.split('/')[0]]))
      if (opts.then) nav(opts.then)
      return true
    } catch (e) {
      toast((e as Error).message, 'error')
      return false
    }
  }
}

/** Pretty label for a settings or API key: "OIDC_GROUPS_CLAIM" -> "Groups claim". */
export function humanize(key: string, stripPrefix?: string): string {
  let k = key
  if (stripPrefix && k.startsWith(stripPrefix)) k = k.slice(stripPrefix.length)
  const words = k.toLowerCase().split('_').filter(Boolean)
  const keep = new Set(['oidc', 'url', 'ssl', 'api', 'ui', 'id', 'ip', 'k8s', 'awx', 'uuid', 'http', 'https', 'oauth2', 'ee'])
  const out = words.map((w) => (keep.has(w) ? w.toUpperCase() : w))
  if (out.length) out[0] = out[0].charAt(0).toUpperCase() + out[0].slice(1)
  return out.join(' ')
}

/** Copy button with feedback. */
export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      className="btn sm"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text)
        } catch {
          const ta = document.createElement('textarea')
          ta.value = text
          document.body.appendChild(ta)
          ta.select()
          document.execCommand('copy')
          ta.remove()
        }
        setDone(true)
        window.setTimeout(() => setDone(false), 1600)
      }}
    >
      <Icon name={done ? 'check' : 'copy'} size={14} />
      {done ? tr('Copied') : label}
    </button>
  )
}

/** A secret shown once, with copy. */
export function OneTimeSecret({ value, note }: { value: string; note: ReactNode }) {
  return (
    <div className="stack" style={{ gap: 8 }}>
      <div className="notice accent">{note}</div>
      <div className="row">
        <pre className="code grow" style={{ margin: 0 }}>
          {value}
        </pre>
        <CopyButton text={value} />
      </div>
    </div>
  )
}
