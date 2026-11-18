// The admin lists of catalog requests and deployments: those of the items
// the user administers, and their own. Each links to its page in the portal.
import { Link } from 'react-router'
import { useList } from '../../api/hooks'
import type { Any } from '../../api/types'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Cell, Empty, Upcoming } from '../../components/ui'
import { deploymentStatus, requestKind, requestStatus, type StatusCell } from '../../lib/catalogItems'
import { jobPath } from '../../lib/format'
import { tr } from '../../lib/i18n'
import { compile, type KeyTable } from '../../lib/query'
import { FilterBox, ListPage } from '../admin/common'

type Request = Any & { id: number; item_name: string; kind: string; status: string; job: number | null; created: string }
type Deployment = Any & { id: number; name: string; item_name: string; status: string; expires_at: string | null; request: number | null; created: string }

const person = (u?: { username: string; first_name?: string; last_name?: string }) => (u ? [u.first_name, u.last_name].filter(Boolean).join(' ') || u.username : '—')

const requestKeys: KeyTable = {
  item: { field: 'item_name', lookup: 'icontains', help: tr('item name') },
  status: { field: 'status', help: tr('awaiting_approval, running, successful, failed, denied or canceled') },
  kind: { field: 'kind', help: tr('new, extend, delete or an action’s id') },
  team: { field: 'team__name', lookup: 'iexact', help: tr('team name') },
  requester: { field: 'created_by__username', lookup: 'iexact', help: tr('username') },
}

const { item: _item, ...itemRequestKeys } = requestKeys

const deploymentKeys: KeyTable = {
  item: { field: 'item_name', lookup: 'icontains', help: tr('item name') },
  status: { field: 'status', help: tr('active, deleting, delete_failed or deleted') },
  owner: { field: 'owner__username', lookup: 'iexact', help: tr('username') },
  team: { field: 'team__name', lookup: 'iexact', help: tr('team name') },
}

function StateText({ s }: { s: { cell: StatusCell; label: string } }) {
  return (
    <span className="status">
      <Cell state={s.cell} />
      <span>{s.label}</span>
    </span>
  )
}

function requestColumns(actions?: { id: string; name: string }[], withItem = true): Column<Request>[] {
  return [
    {
      key: 'id',
      header: withItem ? tr('Request and item') : tr('Request and deployment'),
      sort: 'id',
      render: (r) => (
        <div>
          <Link to={`/portal/requests/${r.id}`} className="cell-title">
            {tr('#{id}', { id: r.id })} {withItem ? r.item_name : ''}
          </Link>
          {r.summary_fields.deployment && <div className="cell-sub">{r.summary_fields.deployment.name}</div>}
        </div>
      ),
    },
    { key: 'kind', header: tr('Kind'), render: (r) => requestKind(r.kind, actions) },
    { key: 'status', header: tr('Status'), sort: 'status', render: (r) => <StateText s={requestStatus(r.status)} /> },
    { key: 'requester', header: tr('Requester'), render: (r) => person(r.summary_fields.created_by) },
    { key: 'team', header: tr('Team'), render: (r) => r.summary_fields.team?.name ?? <span className="faint">{tr('None')}</span> },
    { key: 'created', header: tr('Sent'), sort: 'created', render: (r) => <Ago at={r.created} /> },
    {
      key: 'job',
      header: tr('Job'),
      render: (r) => {
        const j = r.summary_fields.job as { id: number; type: string } | undefined
        return j ? <Link to={jobPath(j)}>{tr('#{id}', { id: j.id })}</Link> : <span className="faint">—</span>
      },
    },
  ]
}

const noRequests = <Empty title={tr('No requests yet')}>{tr('Requests made in the portal, or through the API by another catalog, show here.')}</Empty>

export function CatalogRequestList() {
  return <ListPage<Request> title={tr('Catalog requests')} subtitle={tr('The requests for the items you administer, and your own.')} path="catalog_requests/" order="-id" keys={requestKeys} placeholder={tr('Filter requests…')} columns={requestColumns()} empty={noRequests} />
}

/** An item's requests, filtered as the admin list is. */
export function RequestsTable({ path, actions }: { path: string; actions?: { id: string; name: string }[] }) {
  const ls = useListState({ order: '-id' })
  const compiled = compile(ls.q, itemRequestKeys)
  const list = useList<Request>(path, { ...compiled.params, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  return (
    <div className="stack" style={{ gap: 10 }}>
      <FilterBox value={ls.q} onChange={ls.setQ} placeholder={tr('Filter requests…')} keys={itemRequestKeys} />
      <DataTable
        label={tr('Requests')}
        rows={list.data?.results}
        columns={requestColumns(actions, false)}
        loading={list.isPending}
        error={list.error}
        order={ls.order}
        onOrder={ls.setOrder}
        empty={ls.q ? undefined : noRequests}
        footer={list.data && list.data.count > ls.pageSize ? <div className="table-foot"><span /><Pager count={list.data.count} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} /></div> : null}
      />
    </div>
  )
}

export function DeploymentList() {
  return (
    <ListPage<Deployment>
      title={tr('Deployments')}
      subtitle={tr('What requests created and their owners keep: those of the items you administer, and your own.')}
      path="deployments/"
      order="-id"
      keys={deploymentKeys}
      placeholder={tr('Filter deployments…')}
      columns={[
        {
          key: 'name',
          header: tr('Deployment'),
          sort: 'name',
          render: (d) => (
            <div>
              <Link to={`/portal/deployments/${d.id}`} className="cell-title">
                {d.name}
              </Link>
              <div className="cell-sub">{d.item_name}</div>
            </div>
          ),
        },
        { key: 'status', header: tr('Status'), sort: 'status', render: (d) => <StateText s={deploymentStatus(d.status)} /> },
        { key: 'owner', header: tr('Owner'), render: (d) => person(d.summary_fields.owner) },
        { key: 'team', header: tr('Team'), render: (d) => d.summary_fields.team?.name ?? <span className="faint">{tr('None')}</span> },
        { key: 'expires', header: tr('Expires'), sort: 'expires_at', render: (d) => (d.status !== 'active' ? <span className="faint">—</span> : d.expires_at ? <Upcoming at={d.expires_at} /> : <span className="faint">{tr('Never')}</span>) },
        { key: 'created', header: tr('Created'), sort: 'created', render: (d) => <Ago at={d.created} /> },
        { key: 'request', header: tr('Its request'), render: (d) => (d.request ? <Link to={`/portal/requests/${d.request}`}>{tr('#{id}', { id: d.request })}</Link> : <span className="faint">—</span>) },
      ]}
      empty={<Empty title={tr('No deployments yet')}>{tr('A request for an item that creates a deployment makes one, which its owners keep until it expires or they delete it.')}</Empty>}
    />
  )
}
