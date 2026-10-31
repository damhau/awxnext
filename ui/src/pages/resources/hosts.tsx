import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, HostSummary, Ref } from '../../api/types'
import { useOrg } from '../../app/context'
import { Icon } from '../../components/icons'
import { Menu, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, ErrorNote, KV, Loading, PageHead, Panel, StatusDot } from '../../components/ui'
import { absolute, plural } from '../../lib/format'
import { compile, type KeyTable } from '../../lib/query'
import { FilterBox, Provenance, useDelete } from './common'
import { AdHocDialog, HostDialog, type Host } from './inventoryHosts'
import { tr } from '../../lib/i18n'

const keys: KeyTable = {
  inventory: { field: 'inventory__name', lookup: 'iexact' },
  group: { field: 'groups__name', lookup: 'iexact' },
  enabled: { field: 'enabled', map: (v) => (/^(y|yes|true|on|1)$/i.test(v) ? 'true' : 'false') },
  name: { field: 'name', lookup: 'icontains' },
}

export function HostList() {
  const ls = useListState({ order: 'name', pageSize: 50 })
  const { scope } = useOrg()
  const nav = useNavigate()
  const q = compile(ls.q, keys)
  const params = { ...q.params, ...(scope.organization ? { inventory__organization: scope.organization } : {}), order_by: ls.order, page: ls.page, page_size: ls.pageSize }
  const hosts = useList<Host>('hosts/', params)
  const cols: Column<Host>[] = [
    {
      key: 'name',
      header: tr('Host'),
      sort: 'name',
      render: (h) => (
        <div className="row" style={{ gap: 6 }}>
          <Link to={`/hosts/${h.id}`} className="cell-title mono" style={{ fontSize: 13, opacity: h.enabled ? 1 : 0.55 }}>
            {h.name}
          </Link>
          {!h.enabled && <span className="tag">{tr('disabled')}</span>}
        </div>
      ),
    },
    {
      key: 'inv',
      header: tr('Inventory'),
      sort: 'inventory__name',
      render: (h) => (
        <Link to={`/inventories/${h.inventory}/hosts`} className="small" style={{ color: 'var(--ink-2)' }}>
          {h.summary_fields.inventory?.name}
        </Link>
      ),
    },
    {
      key: 'groups',
      header: tr('Groups'),
      render: (h) => {
        const g = (h.summary_fields.groups?.results ?? []) as Ref[]
        return g.length ? (
          <span className="mono small ellipsis" style={{ display: 'inline-block', maxWidth: 240 }}>
            {g.map((x) => x.name).join(', ')}
          </span>
        ) : (
          <span className="faint small">—</span>
        )
      },
    },
    {
      key: 'last',
      header: tr('Last job'),
      render: (h) => {
        const j = (h.summary_fields.recent_jobs ?? [])[0] as (Base & { status: string; finished: string }) | undefined
        if (!j) return <span className="faint small">{tr('never targeted')}</span>
        return (
          <Link to={`/jobs/${j.type}/${j.id}`} className="row small" style={{ gap: 6, color: 'var(--ink-2)' }}>
            <StatusDot status={j.status} />
            <span className="ellipsis" style={{ maxWidth: 220 }}>
              {j.name}
            </span>
            <span className="faint nowrap">
              <Ago at={j.finished} />
            </span>
          </Link>
        )
      },
    },
  ]
  return (
    <div className="page">
      <PageHead title={tr('Hosts')} subtitle={tr('Every host across the inventories you can see.')} bordered={false} />
      <div className="page-body">
        <FilterBox value={ls.q} onApply={ls.setQ} placeholder={tr('name…  try  inventory:prod-eu-west  group:web  enabled:no')} />
        <DataTable
          label={tr('Hosts')}
          rows={hosts.data?.results}
          columns={cols}
          loading={hosts.isPending}
          error={hosts.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(h) => nav(`/hosts/${h.id}`)}
          empty={<Empty title={ls.q ? tr('No host matches') : tr('No hosts yet')}>{ls.q ? tr('Try a shorter filter.') : tr('Hosts live in inventories. Create an inventory to add some.')}</Empty>}
          footer={
            <div className="table-foot">
              <span>{hosts.data ? tr('{count}', { count: plural(hosts.data.count, 'host', 'hosts') }) : ''}</span>
              <span className="mono">{tr('j/k move · enter open · / filter')}</span>
              <Pager count={hosts.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
    </div>
  )
}

interface Summary extends HostSummary {
  job: number
  created: string
  summary_fields: { job?: Ref & { status?: string; failed?: boolean } }
}

export function HostDetail() {
  const { id } = useParams()
  const host = useObject<Host>(`hosts/${id}/`)
  const summaries = useList<Summary>(`hosts/${id}/job_host_summaries/`, { order_by: '-id', page_size: 15 })
  const facts = useQuery({ queryKey: [`hosts/${id}/ansible_facts/`], queryFn: () => api.get<Record<string, unknown>>(`hosts/${id}/ansible_facts/`) })
  const [dialog, setDialog] = useState<null | 'edit' | 'adhoc'>(null)
  const invalidate = useInvalidate()
  const toast = useToast()
  const h = host.data
  const del = useDelete('hosts', h ? `/inventories/${h.inventory}/hosts` : '/hosts')
  if (host.isPending) return <Loading />
  if (host.error || !h) return <div className="page-body"><ErrorNote error={host.error ?? tr('Not found')} /></div>
  const inv = h.summary_fields.inventory as Ref | undefined
  const caps = h.summary_fields.user_capabilities ?? {}
  const groups = (h.summary_fields.groups?.results ?? []) as Ref[]
  const toggle = async () => {
    try {
      await api.patch(`hosts/${h.id}/`, { enabled: !h.enabled })
      invalidate(`hosts/${h.id}`, 'hosts', `inventories/${h.inventory}`)
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Update failed'), 'error')
    }
  }
  const sumCols: Column<Summary>[] = [
    {
      key: 'job',
      header: tr('Job'),
      render: (s) => (
        <div className="row" style={{ gap: 6 }}>
          <StatusDot status={s.failed ? 'failed' : s.summary_fields.job?.status ?? 'successful'} />
          <Link to={`/jobs/job/${s.job}`} className="cell-title">
            {s.summary_fields.job?.name ?? tr('Job {job}', { job: s.job })}
          </Link>
          <span className="mono small faint">#{s.job}</span>
        </div>
      ),
    },
    {
      key: 'counts',
      header: tr('Result on this host'),
      render: (s) => (
        <span className="mono small">
          {tr('ok')} {s.ok} · <span style={{ color: s.changed ? 'var(--changed)' : undefined }}>{tr('changed')} {s.changed}</span>
          {s.failures ? <span className="fail-text"> {tr('· failed')} {s.failures}</span> : null}
          {s.dark ? <span className="fail-text"> {tr('· unreachable')} {s.dark}</span> : null}
          {s.skipped ? <span className="faint"> {tr('· skipped')} {s.skipped}</span> : null}
        </span>
      ),
    },
    { key: 'when', header: tr('When'), render: (s) => <span className="small nowrap"><Ago at={s.created} /></span> },
  ]
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Inventories'), to: '/inventories' }, ...(inv ? [{ label: inv.name, to: `/inventories/${inv.id}/hosts` }] : []), { label: h.name }]}
        title={<span className="mono" style={{ fontSize: 19 }}>{h.name}</span>}
        badge={h.enabled ? undefined : <span className="badge">{tr('Disabled')}</span>}
        subtitle={h.description || undefined}
        actions={
          <>
            <button className="btn" onClick={() => setDialog('adhoc')}>
              <Icon name="terminal" size={14} /> {tr('Run command…')}
            </button>
            {caps.edit && (
              <button className="btn" onClick={() => setDialog('edit')}>
                {tr('Edit')}
              </button>
            )}
            <Menu label={tr('More actions')}>
              {caps.edit && <button onClick={toggle}>{h.enabled ? tr('Disable host') : tr('Enable host')}</button>}
              <Link to={`/jobs?q=${encodeURIComponent(`host:${h.name}`)}`}>{tr('Jobs that targeted this host')}</Link>
              {caps.delete && (
                <>
                  <hr />
                  <button className="danger" onClick={() => del(h, 'host')}>
                    {tr('Delete…')}
                  </button>
                </>
              )}
            </Menu>
          </>
        }
      />
      <div className="page-body">
        <div className="split">
          <div className="wide stack" style={{ gap: 16 }}>
            <Panel title={tr('Recent jobs on this host')} pad={false} className="flush">
              <DataTable
                label={tr('Job results')}
                rows={summaries.data?.results}
                columns={sumCols}
                loading={summaries.isPending}
                error={summaries.error}
                keyboard={false}
                empty={<Empty title={tr('Never targeted')}>{tr('No job has run against this host yet.')}</Empty>}
              />
            </Panel>
            <Panel title={tr('Variables')}>{h.variables?.trim() ? <pre className="code">{h.variables}</pre> : <span className="faint small">{tr('No host variables.')}</span>}</Panel>
            <Facts facts={facts.data} gathered={h.ansible_facts_modified} loading={facts.isPending} />
          </div>
          <div className="narrow">
            <Panel title={tr('About')}>
              <KV
                items={[
                  [tr('Inventory'), inv ? <Link to={`/inventories/${inv.id}`}>{inv.name}</Link> : '—'],
                  [
                    tr('Groups'),
                    groups.length ? (
                      <span className="row wrap" style={{ gap: 4 }}>
                        {groups.map((g) => (
                          <Link key={g.id} to={`/inventories/${h.inventory}/groups?group=${g.id}`} className="chip">
                            {g.name}
                          </Link>
                        ))}
                      </span>
                    ) : (
                      <span className="faint">{tr('ungrouped')}</span>
                    ),
                  ],
                  [tr('Enabled'), h.enabled ? tr('Yes') : tr('No — jobs skip this host')],
                  [tr('Instance ID'), h.instance_id ? <code>{h.instance_id}</code> : ''],
                  [tr('Facts gathered'), h.ansible_facts_modified ? <span title={absolute(h.ansible_facts_modified)}><Ago at={h.ansible_facts_modified} /></span> : <span className="faint">{tr('never')}</span>],
                  [tr('History'), <Provenance obj={h} />],
                ]}
              />
            </Panel>
          </div>
        </div>
      </div>
      {dialog === 'edit' && <HostDialog inventory={h.inventory} host={h} onClose={() => setDialog(null)} onSaved={() => invalidate(`hosts/${h.id}`, 'hosts')} />}
      {dialog === 'adhoc' && inv && <AdHocDialog inventory={{ id: inv.id, name: inv.name }} limit={h.name} onClose={() => setDialog(null)} />}
    </div>
  )
}

const keyFacts = [
  ['OS', (f: Record<string, unknown>) => [f.ansible_distribution, f.ansible_distribution_version].filter(Boolean).join(' ')],
  [tr('Kernel'), (f: Record<string, unknown>) => f.ansible_kernel],
  [tr('Architecture'), (f: Record<string, unknown>) => f.ansible_architecture],
  ['IPv4', (f: Record<string, unknown>) => (f.ansible_default_ipv4 as Record<string, unknown> | undefined)?.address],
  [tr('Memory'), (f: Record<string, unknown>) => (f.ansible_memtotal_mb ? `${Math.round(Number(f.ansible_memtotal_mb) / 1024)} GiB` : undefined)],
  ['CPUs', (f: Record<string, unknown>) => f.ansible_processor_vcpus],
  [tr('Virtualization'), (f: Record<string, unknown>) => [f.ansible_virtualization_type, f.ansible_virtualization_role].filter(Boolean).join(' · ')],
] as const

function Facts({ facts, gathered, loading }: { facts?: Record<string, unknown>; gathered?: string | null; loading: boolean }) {
  const [filter, setFilter] = useState('')
  const [all, setAll] = useState(false)
  const entries = useMemo(() => Object.entries(facts ?? {}).sort(([a], [b]) => a.localeCompare(b)), [facts])
  if (loading) return <Panel title={tr('Facts')}><Loading rows={3} /></Panel>
  if (!entries.length)
    return (
      <Panel title={tr('Facts')}>
        <span className="faint small">
          {tr('No facts stored. Facts are kept when a job template enables fact storage and gathers facts.')}
        </span>
      </Panel>
    )
  const known = keyFacts.map(([k, fn]) => [k, fn(facts!)] as const).filter(([, v]) => v !== undefined && v !== '' && v !== null)
  const shown = entries.filter(([k, v]) => !filter || k.includes(filter) || JSON.stringify(v).toLowerCase().includes(filter.toLowerCase()))
  return (
    <Panel
      title={tr('Facts')}
      actions={
        <span className="small faint">
          {tr('gathered')} <Ago at={gathered} />
        </span>
      }
    >
      {known.length > 0 && <KV items={known.map(([k, v]) => [k, <span className="mono">{String(v)}</span>])} />}
      <div className="row" style={{ marginTop: known.length ? 14 : 0 }}>
        <input className="input mono" style={{ maxWidth: 280 }} placeholder={tr('filter facts…')} value={filter} onChange={(e) => setFilter(e.target.value)} aria-label={tr('Filter facts')} />
        <span className="small faint">
          {shown.length} {tr('of')} {entries.length}
        </span>
      </div>
      <div style={{ marginTop: 10, maxHeight: all ? undefined : 360, overflow: 'auto' }}>
        <table className="data">
          <tbody>
            {shown.slice(0, all ? undefined : 60).map(([k, v]) => (
              <tr key={k}>
                <td className="mono small" style={{ width: '35%', verticalAlign: 'top' }}>
                  {k}
                </td>
                <td className="mono small" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
                  {typeof v === 'string' ? v : JSON.stringify(v, null, 1)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {!all && shown.length > 60 && (
        <button className="link-btn small" style={{ marginTop: 8 }} onClick={() => setAll(true)}>
          {tr('Show all')} {shown.length}
        </button>
      )}
    </Panel>
  )
}
