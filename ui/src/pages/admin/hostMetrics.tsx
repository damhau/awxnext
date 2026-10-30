// Host metrics: the hosts jobs have automated, with their first and last
// automation and counts, soft deleted by superusers, and the monthly summary
// (system administrators and auditors, as in AWX).
import { useState } from 'react'
import { api } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Base } from '../../api/types'
import { useSession } from '../../app/context'
import { Check } from '../../components/form'
import { Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, PageHead, Panel } from '../../components/ui'
import { plural } from '../../lib/format'
import { metricsQuery, monthLabel } from '../../lib/hostMetrics'
import { FilterBox, KindTag } from '../resources/common'

interface HostMetric extends Omit<Base, 'name'> {
  hostname: string
  first_automation: string
  last_automation: string
  last_deleted: string | null
  automated_counter: number
  deleted_counter: number
  deleted: boolean
  used_in_inventories: number | null
}

interface MonthSummary {
  id: number
  date: string
  license_consumed: number
  hosts_added: number
  hosts_deleted: number
  indirectly_managed_hosts: number
}

export function HostMetrics() {
  const ls = useListState({ pageSize: 25 })
  const [showDeleted, setShowDeleted] = useState(false)
  const list = useList<HostMetric>('host_metrics/', metricsQuery(ls.q, showDeleted, ls.page, ls.pageSize))
  const summary = useList<MonthSummary>('host_metric_summary_monthly/', { order_by: '-date', page_size: 12 })
  const { me } = useSession()
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const remove = async (m: HostMetric) => {
    const ok = await confirm({
      title: `Delete ${m.hostname} from host metrics?`,
      body: <p style={{ margin: 0 }}>It stops counting as automated until a job automates it again. Its history stays.</p>,
      confirm: 'Delete',
      danger: true,
    })
    if (!ok) return
    try {
      await api.del(`host_metrics/${m.id}/`)
      invalidate('host_metrics')
      toast(`Deleted ${m.hostname} from host metrics`)
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Delete failed', 'error')
    }
  }
  const cols: Column<HostMetric>[] = [
    {
      key: 'host',
      header: 'Host',
      render: (m) => (
        <span className="row" style={{ gap: 6 }}>
          <span className="cell-title mono">{m.hostname}</span>
          {m.deleted && <KindTag>deleted</KindTag>}
        </span>
      ),
    },
    { key: 'first', header: 'First automated', render: (m) => <span className="small nowrap"><Ago at={m.first_automation} /></span> },
    { key: 'last', header: 'Last automated', render: (m) => <span className="small nowrap"><Ago at={m.last_automation} /></span> },
    { key: 'count', header: 'Times automated', className: 'num', render: (m) => <span className="small">{m.automated_counter}</span> },
    { key: 'inv', header: 'Inventories', className: 'num', render: (m) => <span className="small muted">{m.used_in_inventories ?? '—'}</span> },
    {
      key: 'deleted',
      header: 'Deleted',
      render: (m) =>
        m.deleted_counter ? (
          <span className="small muted">
            {plural(m.deleted_counter, 'time', 'times')}, last <Ago at={m.last_deleted} />
          </span>
        ) : (
          <span className="faint small">—</span>
        ),
    },
    {
      key: 'menu',
      header: <span className="sr-only">Actions</span>,
      className: 'end',
      render: (m) =>
        me?.is_superuser && !m.deleted ? (
          <Menu label={`Actions for ${m.hostname}`} className="btn sm icon ghost">
            <button className="danger" onClick={() => remove(m)}>
              Delete…
            </button>
          </Menu>
        ) : null,
    },
  ]
  return (
    <div className="page">
      <PageHead
        title="Host metrics"
        subtitle="Every host jobs have automated, when first and last, and how often: the count of unique managed hosts."
        bordered={false}
      />
      <div className="page-body stack" style={{ gap: 20 }}>
        <div className="row wrap" style={{ gap: 12 }}>
          <FilterBox value={ls.q} onApply={ls.setQ} placeholder="host name…" />
          <Check label="Show deleted hosts" checked={showDeleted} onChange={setShowDeleted} />
        </div>
        <DataTable
          label="Host metrics"
          rows={list.data?.results}
          columns={cols}
          loading={list.isPending}
          error={list.error}
          empty={<Empty title={ls.q ? 'No host matches' : 'No host automated yet'}>Jobs add the hosts they run on.</Empty>}
          footer={
            <div className="table-foot">
              <span>{list.data ? plural(list.data.count, 'host', 'hosts') : ''}</span>
              <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
        <Panel title="Monthly summary" pad={false}>
          <DataTable
            label="Monthly summary"
            rows={summary.data?.results}
            columns={[
              { key: 'month', header: 'Month', render: (s) => <span className="small">{monthLabel(s.date)}</span> },
              { key: 'consumed', header: 'Hosts automated', className: 'num', render: (s) => <span className="small">{s.license_consumed}</span> },
              { key: 'added', header: 'Added', className: 'num', render: (s) => <span className="small">{s.hosts_added}</span> },
              { key: 'deleted', header: 'Deleted', className: 'num', render: (s) => <span className="small">{s.hosts_deleted}</span> },
              { key: 'indirect', header: 'Indirectly managed', className: 'num', render: (s) => <span className="small muted">{s.indirectly_managed_hosts}</span> },
            ]}
            loading={summary.isPending}
            error={summary.error}
            empty={<Empty title="No summary yet">The weekly management job “Host metric summary monthly” fills it.</Empty>}
          />
        </Panel>
      </div>
    </div>
  )
}
