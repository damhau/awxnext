import { Fragment, useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { useList } from '../../api/hooks'
import type { Any } from '../../api/types'
import { objectPath } from '../../components/access/Access'
import { Icon } from '../../components/icons'
import { Pager, useListState } from '../../components/table'
import { Ago, Avatar, Empty, ErrorNote, Loading, PageHead } from '../../components/ui'
import { absolute, userName } from '../../lib/format'
import { compile, getTerm, setTerm, type KeyTable } from '../../lib/query'
import { FilterBox } from './common'

interface Entry {
  id: number
  timestamp: string
  operation: string
  object1: string
  object2: string
  object_association: string
  changes: Record<string, unknown>
  summary_fields: Any
}

const keys: KeyTable = {
  type: { field: 'object1', help: 'object type, e.g. job_template' },
  op: { field: 'operation', help: 'create, update, delete, associate, disassociate' },
  actor: { field: 'actor__username', help: 'username' },
}

const verbs: Record<string, string> = { create: 'created', update: 'changed', delete: 'deleted', associate: 'linked', disassociate: 'unlinked' }

const types: [string, string][] = [
  ['', 'All objects'],
  ['job_template', 'Job templates'],
  ['workflow_job_template', 'Workflow templates'],
  ['inventory', 'Inventories'],
  ['host', 'Hosts'],
  ['project', 'Projects'],
  ['credential', 'Credentials'],
  ['schedule', 'Schedules'],
  ['user', 'Users'],
  ['team', 'Teams'],
  ['organization', 'Organizations'],
  ['role', 'Roles'],
  ['setting', 'Settings'],
  ['notification_template', 'Notifications'],
]

const ctFor: Record<string, string> = {
  job_template: 'jobtemplate',
  workflow_job_template: 'workflowjobtemplate',
  notification_template: 'notificationtemplate',
  execution_environment: 'executionenvironment',
  instance_group: 'instancegroup',
}

function objLink(type: string, o: Any | undefined): ReactNode {
  if (!o) return null
  const name = o.name ?? o.username ?? o.hostname ?? `#${o.id}`
  let path = objectPath(ctFor[type] ?? type, o.id)
  if (type === 'user') path = `/users/${o.id}`
  if (type === 'host') path = `/hosts/${o.id}`
  if (type === 'schedule') path = `/schedules?q=id:${o.id}`
  if (type === 'job') path = `/jobs/job/${o.id}`
  if (type === 'gitops_source') path = `/gitops/${o.id}`
  if (type === 'instance') path = `/instances/${o.id}`
  return path ? <Link to={path}>{name}</Link> : <span>{name}</span>
}

function describe(e: Entry): ReactNode {
  const first = (e.summary_fields[e.object1] as Any[] | undefined)?.[0]
  const second = e.object2 ? (e.summary_fields[e.object2] as Any[] | undefined)?.[e.object1 === e.object2 ? 1 : 0] : undefined
  const t1 = e.object1.replace(/_/g, ' ')
  if (e.object1 === 'setting') return <>changed settings {Object.keys(e.changes ?? {}).map((k) => <span key={k} className="mono small"> {k}</span>)}</>
  if (e.operation === 'associate' || e.operation === 'disassociate') {
    return (
      <>
        {e.operation === 'associate' ? 'linked' : 'unlinked'} {e.object2.replace(/_/g, ' ')} {objLink(e.object2, second)} {e.operation === 'associate' ? 'to' : 'from'} {t1} {objLink(e.object1, first)}
        {e.object_association && <span className="faint small"> ({e.object_association.split('.').pop()?.replace(/_/g, ' ')})</span>}
      </>
    )
  }
  return (
    <>
      {verbs[e.operation] ?? e.operation} {t1} {e.operation === 'delete' ? <span>{first?.name ?? (e.changes?.name as string) ?? ''}</span> : objLink(e.object1, first)}
    </>
  )
}

function fmt(v: unknown): string {
  if (v === null || v === undefined) return '—'
  if (typeof v === 'string') return v === '' ? '""' : v
  return JSON.stringify(v)
}

function Changes({ e }: { e: Entry }) {
  const entries = Object.entries(e.changes ?? {})
  if (!entries.length) return <p className="muted small" style={{ margin: 0 }}>No field changes recorded.</p>
  const isDiff = e.operation === 'update' && entries.every(([, v]) => Array.isArray(v) && v.length === 2)
  return (
    <div className="changes" style={{ gridTemplateColumns: isDiff ? '200px 1fr 1fr' : '200px 1fr' }}>
      <div className="head">field</div>
      {isDiff ? (
        <>
          <div className="head">before</div>
          <div className="head">after</div>
        </>
      ) : (
        <div className="head">value</div>
      )}
      {entries.map(([k, v]) =>
        isDiff ? (
          <Fragment key={k}>
            <div className="mono">{k}</div>
            <div className="old strike mono small">{fmt((v as unknown[])[0])}</div>
            <div className="new mono small">{fmt((v as unknown[])[1])}</div>
          </Fragment>
        ) : (
          <Fragment key={k}>
            <div className="mono">{k}</div>
            <div className="mono small" style={{ whiteSpace: 'pre-wrap' }}>
              {fmt(v)}
            </div>
          </Fragment>
        ),
      )}
    </div>
  )
}

export function Activity() {
  const ls = useListState({ order: '-timestamp', pageSize: 50 })
  const compiled = compile(ls.q, keys)
  const list = useList<Entry>('activity_stream/', { ...compiled.params, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const [open, setOpen] = useState<Set<number>>(new Set())
  const type = getTerm(ls.q, 'type') ?? ''
  const op = getTerm(ls.q, 'op') ?? ''
  const toggle = (id: number) => {
    const n = new Set(open)
    if (n.has(id)) n.delete(id)
    else n.add(id)
    setOpen(n)
  }
  let lastDay = ''
  return (
    <div className="page">
      <PageHead title="Activity" subtitle="Every change to configuration, who made it and what changed." bordered={false}>
        <div className="row wrap">
          <select className="select" style={{ width: 'auto' }} aria-label="Object type" value={type} onChange={(e) => ls.setQ(setTerm(ls.q, 'type', e.target.value || null))}>
            {types.map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
          <div className="segmented" role="radiogroup" aria-label="Operation">
            {(
              [
                ['', 'All'],
                ['create', 'Created'],
                ['update', 'Changed'],
                ['delete', 'Deleted'],
                ['associate', 'Linked'],
              ] as const
            ).map(([v, l]) => (
              <label key={v} className={op === v ? 'on' : ''}>
                <input type="radio" name="op" checked={op === v} onChange={() => ls.setQ(setTerm(ls.q, 'op', v || null))} />
                {l}
              </label>
            ))}
          </div>
        </div>
        <FilterBox value={ls.q} onChange={ls.setQ} keys={keys} placeholder="Search changes…  try  actor:priya type:job_template" />
      </PageHead>
      <div className="page-body">
        {list.error && <ErrorNote error={list.error} />}
        <div className="table-wrap">
          {list.isPending && <Loading />}
          {list.data && list.data.results.length === 0 && <Empty title="No activity matches" />}
          <ol className="activity">
            {(list.data?.results ?? []).map((e) => {
              const day = new Date(e.timestamp).toDateString()
              const head = day !== lastDay ? <li className="activity-day">{new Date(e.timestamp).toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })}</li> : null
              lastDay = day
              const actor = e.summary_fields.actor
              const expanded = open.has(e.id)
              return (
                <Fragment key={e.id}>
                  {head}
                  <li className={`activity-row ${expanded ? 'open' : ''}`}>
                    <button className="activity-main" onClick={() => toggle(e.id)} aria-expanded={expanded}>
                      {actor ? <Avatar name={userName(actor)} size="sm" /> : <span className="avatar sm" style={{ background: 'var(--line)' }} aria-hidden="true">·</span>}
                      <span className="grow" style={{ textAlign: 'left' }}>
                        <b style={{ fontWeight: 500 }}>{actor ? userName(actor) : 'System'}</b> {describe(e)}
                      </span>
                      <span className="small faint nowrap" title={absolute(e.timestamp)}>
                        <Ago at={e.timestamp} />
                      </span>
                      <Icon name={expanded ? 'chevron' : 'chevronRight'} size={14} />
                    </button>
                    {expanded && (
                      <div className="activity-detail">
                        <Changes e={e} />
                      </div>
                    )}
                  </li>
                </Fragment>
              )
            })}
          </ol>
          {list.data && list.data.count > ls.pageSize && (
            <div className="table-foot">
              <span>{list.data.count} entries</span>
              <Pager count={list.data.count} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
