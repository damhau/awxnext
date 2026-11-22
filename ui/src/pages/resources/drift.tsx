import { useState } from 'react'
import { Link } from 'react-router'
import { useList } from '../../api/hooks'
import type { Ref } from '../../api/types'
import { useOrg } from '../../app/context'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, PageHead } from '../../components/ui'
import { absolute, plural } from '../../lib/format'
import { taskName } from '../../lib/hostHistory'
import { compile, type KeyTable } from '../../lib/query'
import { asIs, tr } from '../../lib/i18n'
import { FilterBox } from './common'
import { DriftDiff } from './hostHistory'

export interface Drift {
  id: number
  host: number
  inventory: number
  job_template: number
  play: string
  role: string
  task: string
  since: string
  job: number | null
  last_job: number | null
  last_seen: string
  diff: unknown
  summary_fields: { host?: Ref; inventory?: Ref; job_template?: Ref; job?: Ref & { status?: string }; last_job?: Ref & { status?: string } }
}

const keys: KeyTable = {
  host: { field: 'host__name', lookup: 'icontains' },
  inventory: { field: 'inventory__name', lookup: 'iexact' },
  template: { field: 'job_template__name', lookup: 'icontains' },
  task: { field: 'task', lookup: 'icontains' },
}

/** Hosts a check-mode run found would change, across the inventories one can read. */
export function DriftList() {
  const ls = useListState({ order: '-since', pageSize: 50 })
  const { scope } = useOrg()
  const q = compile(ls.q, keys)
  const params = { ...q.params, ...(scope.organization ? { inventory__organization: scope.organization } : {}), order_by: ls.order, page: ls.page, page_size: ls.pageSize }
  const drift = useList<Drift>('host_drift/', params)
  const [open, setOpen] = useState<Set<number>>(new Set())
  const toggle = (id: number) =>
    setOpen((s) => {
      const n = new Set(s)
      if (!n.delete(id)) n.add(id)
      return n
    })
  const cols: Column<Drift>[] = [
    {
      key: 'host',
      header: tr('Host'),
      sort: 'host__name',
      render: (d) => (
        <Link to={`/hosts/${d.host}`} className="cell-title mono" style={{ fontSize: 13 }}>
          {d.summary_fields.host?.name ?? asIs(`#${d.host}`)}
        </Link>
      ),
    },
    {
      key: 'inventory',
      header: tr('Inventory'),
      sort: 'inventory__name',
      render: (d) => (
        <Link to={`/inventories/${d.inventory}/hosts`} className="small" style={{ color: 'var(--ink-2)' }}>
          {d.summary_fields.inventory?.name}
        </Link>
      ),
    },
    {
      key: 'task',
      header: tr('Task that would change it'),
      sort: 'task',
      render: (d) => (
        <div style={{ minWidth: 0 }}>
          <div className="row" style={{ gap: 8 }}>
            <span className="mono small">{taskName(d)}</span>
            <button className="link-btn small" aria-expanded={open.has(d.id)} onClick={() => toggle(d.id)}>
              {open.has(d.id) ? tr('Hide the diff') : tr('Show the diff')}
            </button>
          </div>
          <div className="cell-sub">
            <Link to={`/templates/job_template/${d.job_template}`}>{d.summary_fields.job_template?.name ?? asIs(`#${d.job_template}`)}</Link>
            {d.play && <span> · {d.play}</span>}
          </div>
          {open.has(d.id) && <DriftDiff diff={d.diff} />}
        </div>
      ),
    },
    {
      key: 'since',
      header: tr('Drifted since'),
      sort: 'since',
      render: (d) => (
        <span className="small nowrap" title={absolute(d.since)}>
          <Ago at={d.since} />
        </span>
      ),
    },
    {
      key: 'found',
      header: tr('Found by'),
      render: (d) => {
        const first = d.job ?? d.last_job
        return (
          <span className="small mono nowrap">
            {first ? <Link to={`/jobs/job/${first}`}>#{first}</Link> : '—'}
            {d.job && d.last_job && d.last_job !== d.job && (
              <span className="faint">
                {' '}
                {tr('· still in')} <Link to={`/jobs/job/${d.last_job}`}>#{d.last_job}</Link>
              </span>
            )}
          </span>
        )
      },
    },
  ]
  return (
    <div className="page">
      <PageHead
        title={tr('Drift')}
        subtitle={tr('Hosts that differ from what their templates set: a check-mode run found a task would change them. A host stays here until a later run of the template finds nothing to change.')}
        bordered={false}
      />
      <div className="page-body">
        <FilterBox value={ls.q} onApply={ls.setQ} placeholder={tr('task…  try  host:web-01  inventory:prod  template:baseline')} />
        <DataTable
          label={tr('Drifted hosts')}
          rows={drift.data?.results}
          columns={cols}
          loading={drift.isPending}
          error={drift.error}
          order={ls.order}
          onOrder={ls.setOrder}
          keyboard={false}
          empty={
            <Empty title={ls.q ? tr('No drift matches') : tr('No drift found')}>
              {ls.q
                ? tr('Try a shorter filter.')
                : tr('Drift is found by templates run in check mode with diff mode. Schedule one, and its runs become a drift report.')}
            </Empty>
          }
          footer={
            <div className="table-foot">
              <span>{drift.data ? plural(drift.data.count, 'drifted task', 'drifted tasks') : ''}</span>
              <Pager count={drift.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
    </div>
  )
}
