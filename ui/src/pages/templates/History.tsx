import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router'
import { api, type Page } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Template } from '../../api/types'
import { varsText } from '../../components/form'
import { useConfirm, useToast } from '../../components/overlay'
import { Ago, Avatar, Empty, Loading, Panel } from '../../components/ui'
import { absolute, ago, userName } from '../../lib/format'
import { diffLines } from '../../lib/diff'
import { resOf, type Kind } from './TemplateDetail'

interface Version extends Base {
  version: number
  operation: string
  comment: string
  changes: Record<string, unknown>
  data?: Record<string, unknown>
  actor: number | null
}

// Bookkeeping fields that change without anyone editing the template.
const noise = new Set(['created', 'modified', 'created_by', 'modified_by', 'status', 'last_job_run', 'last_job_failed', 'next_job_run', 'current_job', 'last_job', 'id', 'type', 'url', 'related', 'summary_fields'])

function describeChange(v: Version): string {
  if (v.comment) return v.comment
  if (v.operation === 'create') return 'Created'
  const keys = Object.keys(v.changes ?? {}).filter((k) => !noise.has(k))
  if (!keys.length) return 'No visible change'
  if (keys.length === 1) {
    const k = keys[0]
    const c = v.changes[k]
    if (Array.isArray(c) && c.length === 2 && typeof c[0] !== 'object' && typeof c[1] !== 'object' && String(c[0]).length < 24 && String(c[1]).length < 24) return `${k} ${c[0] === '' ? '∅' : String(c[0])} → ${c[1] === '' ? '∅' : String(c[1])}`
    if (c && typeof c === 'object' && !Array.isArray(c)) {
      const o = c as Record<string, unknown>
      if ('associate' in o) return `${k}: added`
      if ('disassociate' in o) return `${k}: removed`
    }
  }
  return keys.slice(0, 4).join(', ') + (keys.length > 4 ? ` +${keys.length - 4}` : '')
}

function show(v: unknown): string {
  if (v === null || v === undefined || v === '') return '∅'
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

export function History({ kind, tpl }: { kind: Kind; tpl: Template }) {
  const res = resOf(kind)
  const [sp, setSp] = useSearchParams()
  const versions = useList<Version>(`${res}/${tpl.id}/history/`, { page_size: 100 })
  const list = versions.data?.results ?? []
  const latest = list[0]?.version
  const to = Number(sp.get('to')) || latest
  const from = Number(sp.get('from')) || (to ? to - 1 : undefined)
  const toV = useObject<Version>(to ? `${res}/${tpl.id}/history/${to}/` : null)
  const fromV = useObject<Version>(from && from >= 1 ? `${res}/${tpl.id}/history/${from}/` : null)
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const canRevert = !!tpl.summary_fields.user_capabilities?.edit && !tpl.managed_by

  if (versions.isPending) return <Loading />
  if (!list.length) return <div className="page-body"><Empty title="No history yet">Every change to this template is recorded here.</Empty></div>

  const select = (v: number) => setSp({ to: String(v), from: String(v - 1) }, { replace: true })
  const a = (fromV.data?.data ?? {}) as Record<string, unknown>
  const b = (toV.data?.data ?? {}) as Record<string, unknown>
  const keys = [...new Set([...Object.keys(a), ...Object.keys(b)])].filter((k) => !noise.has(k)).sort()
  const changed = keys.filter((k) => JSON.stringify(a[k] ?? null) !== JSON.stringify(b[k] ?? null))
  const showAll = sp.get('all') === '1'
  const rows = (showAll ? keys : changed).filter((k) => k !== 'extra_vars' && k !== 'survey_spec')
  const extraBefore = varsText(a.extra_vars as string)
  const extraAfter = varsText(b.extra_vars as string)
  const target = toV.data

  const revert = async (v: number) => {
    if (!(await confirm({ title: `Revert to version ${v}?`, body: <p style={{ margin: 0 }}>The template takes the settings it had in v{v}. This is recorded as a new version, so you can undo it.</p>, confirm: `Revert to v${v}` }))) return
    try {
      await api.post(`${res}/${tpl.id}/history/${v}/revert/`, {})
      invalidate(`${res}/${tpl.id}`, 'unified_job_templates')
      setSp({}, { replace: true })
      toast(`Reverted to version ${v}`)
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }

  return (
    <div className="body" style={{ alignItems: 'stretch' }}>
      <nav aria-label="Versions" style={{ flex: '0 0 300px', background: 'var(--surface-2)', borderRight: '1px solid var(--line)' }}>
        <ol style={{ listStyle: 'none', margin: 0, padding: 0 }}>
          {list.map((v) => {
            const cur = v.version === to
            const cmp = v.version === from
            return (
              <li key={v.version} style={{ borderBottom: '1px solid var(--line-soft)', background: cur ? 'var(--accent-soft)' : cmp ? '#f2f3f0' : undefined }}>
                <button className="link-btn" style={{ display: 'block', width: '100%', textAlign: 'left', padding: '11px 16px', color: 'var(--ink)' }} onClick={() => select(v.version)} aria-current={cur ? 'true' : undefined}>
                  <div className="row" style={{ alignItems: 'baseline' }}>
                    <span className="mono" style={{ fontWeight: 500, width: 34 }}>
                      v{v.version}
                    </span>
                    <span className="grow ellipsis">{describeChange(v)}</span>
                  </div>
                  <div className="small faint" style={{ marginLeft: 42, marginTop: 3 }}>
                    {userName(v.summary_fields.actor)} · {ago(v.created)}
                    {v.version === latest && ' · current'}
                  </div>
                </button>
              </li>
            )
          })}
        </ol>
      </nav>
      <div className="page-body grow">
        <div className="row wrap" style={{ gap: 10 }}>
          <label htmlFor="from" className="muted">
            Compare
          </label>
          <select id="from" className="select mono" style={{ width: 'auto', height: 32 }} value={from ?? ''} onChange={(e) => setSp({ to: String(to), from: e.target.value }, { replace: true })}>
            {list
              .filter((v) => v.version < (to ?? 0))
              .map((v) => (
                <option key={v.version} value={v.version}>
                  v{v.version} · {ago(v.created)}
                </option>
              ))}
          </select>
          <span aria-hidden="true" className="muted">
            →
          </span>
          <label htmlFor="to" className="sr-only">
            to version
          </label>
          <select id="to" className="select mono" style={{ width: 'auto', height: 32 }} value={to} onChange={(e) => select(Number(e.target.value))}>
            {list.map((v) => (
              <option key={v.version} value={v.version}>
                v{v.version}
                {v.version === latest ? ' · current' : ` · ${ago(v.created)}`}
              </option>
            ))}
          </select>
          <label className="check" style={{ marginLeft: 8 }}>
            <input type="checkbox" checked={showAll} onChange={(e) => setSp({ to: String(to), from: String(from), ...(e.target.checked ? { all: '1' } : {}) }, { replace: true })} />
            Show unchanged fields
          </label>
          {canRevert && from && from >= 1 && (
            <button className="btn" style={{ marginLeft: 'auto' }} onClick={() => revert(to === latest ? from : to)}>
              Revert to v{to === latest ? from : to}…
            </button>
          )}
        </div>

        {target && (
          <div className="panel" style={{ padding: '14px 16px', display: 'flex', gap: 12, alignItems: 'flex-start' }}>
            <Avatar name={userName(target.summary_fields.actor)} />
            <div className="grow">
              <div>
                <b style={{ fontWeight: 600 }}>{userName(target.summary_fields.actor)}</b> {target.operation === 'create' ? 'created the template' : `changed ${changed.length} field${changed.length === 1 ? '' : 's'}`}{' '}
                <span className="faint" title={absolute(target.created)}>
                  · {ago(target.created)}
                </span>
              </div>
              {target.comment ? <p style={{ margin: '4px 0 0' }}>“{target.comment}”</p> : <p className="small faint" style={{ margin: '4px 0 0' }}>No comment. Describe changes in the edit form to explain them here.</p>}
            </div>
          </div>
        )}

        {from && from >= 1 ? (
          <section aria-label="Changed fields" className="panel">
            <div className="changes">
              <div className="head">field</div>
              <div className="head">v{from}</div>
              <div className="head">v{to}</div>
              {rows.map((k) => {
                const same = JSON.stringify(a[k] ?? null) === JSON.stringify(b[k] ?? null)
                return (
                  <Row key={k} name={k} before={<Ref field={k} v={a[k]} />} after={<Ref field={k} v={b[k]} />} same={same} />
                )
              })}
              {rows.length === 0 && extraBefore === extraAfter && (
                <div style={{ gridColumn: '1 / -1' }} className="muted">
                  No field changed between these versions.
                </div>
              )}
            </div>
            {extraBefore !== extraAfter && (
              <div style={{ padding: '10px 16px 14px' }}>
                <div className="mono" style={{ marginBottom: 8 }}>
                  extra_vars
                </div>
                <div className="diff" role="table" aria-label="extra_vars diff">
                  {diffLines(extraBefore, extraAfter).map((l, i) => (
                    <div key={i} role="row" className={l.kind === 'ctx' ? '' : l.kind}>
                      <span className="n">{l.a ?? ''}</span>
                      <span className="n">{l.b ?? ''}</span>
                      <span className="m">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span>
                      <span>{l.text}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </section>
        ) : (
          <Panel title="First version">
            <p className="small muted" style={{ margin: 0 }}>
              This is how the template was created.
            </p>
          </Panel>
        )}
      </div>
      {target && <Around tpl={tpl} version={target} next={list.find((v) => v.version === (to ?? 0) + 1)} />}
    </div>
  )
}

function Row({ name, before, after, same }: { name: string; before: ReactNode; after: ReactNode; same: boolean }) {
  return (
    <>
      <div className="mono">{name}</div>
      <div className={same ? 'muted' : 'old strike'}>{before}</div>
      <div className={same ? 'muted' : 'new'}>{after}</div>
    </>
  )
}

const refRes: Record<string, string> = {
  project: 'projects',
  inventory: 'inventories',
  execution_environment: 'execution_environments',
  folder: 'folders',
  organization: 'organizations',
  webhook_credential: 'credentials',
}

/** Shows ids of related objects by name. */
function Ref({ field, v }: { field: string; v: unknown }) {
  const many = field === 'credentials' || field === 'labels' || field === 'instance_groups'
  const res = many ? field : refRes[field]
  const ids = res ? (many ? ((v as number[]) ?? []) : v ? [v as number] : []) : []
  const names = useQuery({
    queryKey: [`${res}/`, 'names', ids],
    enabled: !!res && ids.length > 0,
    queryFn: () => api.get<Page<Base>>(`${res}/`, { id__in: ids.join(','), page_size: 50 }),
    staleTime: 60_000,
  })
  if (!res) return <span className="mono">{show(v)}</span>
  if (!ids.length) return <span className="faint">none</span>
  const byId = new Map((names.data?.results ?? []).map((o) => [o.id, o.name]))
  return <span>{ids.map((id) => byId.get(id) ?? `#${id}`).join(', ')}</span>
}

function Around({ tpl, version, next }: { tpl: Template; version: Version; next?: Version }) {
  const q = (extra: Record<string, string | number>) => ({ unified_job_template: tpl.id, page_size: 1, ...extra })
  const before = useList<Base>('unified_jobs/', q({ created__lt: version.created! }))
  const beforeFailed = useList<Base>('unified_jobs/', q({ created__lt: version.created!, status__in: 'failed,error' }))
  const since = { created__gte: version.created!, ...(next ? { created__lt: next.created! } : {}) }
  const after = useList<Base>('unified_jobs/', q(since))
  const afterFailed = useList<Base>('unified_jobs/', { ...q(since), status__in: 'failed,error', order_by: '-id' })
  const af = afterFailed.data
  return (
    <aside aria-label="Context" style={{ flex: '0 1 320px', minWidth: 260, borderLeft: '1px solid var(--line)', background: 'var(--surface)', padding: '20px 18px', display: 'flex', flexDirection: 'column', gap: 22 }}>
      <section>
        <h2 style={{ marginBottom: 10 }}>Runs around this change</h2>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
          <div className="panel" style={{ padding: 10, borderColor: 'var(--line-soft)' }}>
            <div className="small faint">before (v{version.version - 1})</div>
            <div style={{ fontSize: 18, fontWeight: 500, marginTop: 2 }}>
              {before.data?.count ?? '…'} <span className="small muted" style={{ fontWeight: 400 }}>runs</span>
            </div>
            <div className={`small ${beforeFailed.data?.count ? 'fail-text' : 'ok-text'}`}>{beforeFailed.data?.count ?? 0} failed</div>
          </div>
          <div className="panel" style={{ padding: 10, ...(af?.count ? { borderColor: 'var(--fail-line)', background: '#fff7f2' } : { borderColor: 'var(--line-soft)' }) }}>
            <div className="small faint">{next ? `with v${version.version}` : `since v${version.version}`}</div>
            <div style={{ fontSize: 18, fontWeight: 500, marginTop: 2 }}>
              {after.data?.count ?? '…'} <span className="small muted" style={{ fontWeight: 400 }}>runs</span>
            </div>
            <div className={`small ${af?.count ? 'fail-text' : 'ok-text'}`}>
              {af?.count ?? 0} failed
              {af?.results[0] && (
                <>
                  {' · '}
                  <Link to={`/jobs/${af.results[0].type}/${af.results[0].id}`}>#{af.results[0].id}</Link>
                </>
              )}
            </div>
          </div>
        </div>
      </section>
      <section>
        <h2 style={{ marginBottom: 8 }}>Version</h2>
        <p className="small" style={{ margin: 0 }}>
          v{version.version} · {version.operation} · <Ago at={version.created} />
        </p>
        <p className="small muted" style={{ margin: '6px 0 0' }}>
          Reverting creates a new version with the old settings; nothing is lost.
        </p>
      </section>
    </aside>
  )
}
