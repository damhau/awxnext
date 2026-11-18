// Catalog quotas: what each team, or each user requesting for no team, uses
// of the quotas of the catalog items the viewer administers.
import { useList, useObject } from '../../api/hooks'
import type { Base } from '../../api/types'
import { DataTable } from '../../components/table'
import { Empty, PageHead, Panel } from '../../components/ui'
import { tr } from '../../lib/i18n'
import { hasQuota, holderName, quotaSlots, type ItemQuotaUsage, type QuotaUsage } from '../../lib/quotas'
import { KindTag } from '../resources/common'

interface CatalogItem extends Base {
  kind: string
  max_deployments: number | null
  max_requests_per_day: number | null
  over_quota: 'refuse' | 'approve'
}

function QuotaMeter({ who, used, max }: { who: string; used: number; max: number | null }) {
  if (max == null) return <span className="small muted">{tr('{used}, no limit', { used })}</span>
  const label = tr('{used} of {max}', { used, max })
  const slots = quotaSlots(used, max)
  return (
    <span className="row nowrap" style={{ gap: 8 }}>
      {slots && (
        <span className="quota-meter" role="img" aria-label={tr('{who}: {used} of {max}', { who, used, max })}>
          {slots.map((s, i) => (
            <span key={i} data-slot={s} />
          ))}
        </span>
      )}
      <span className={`small${used > max ? ' over-quota' : ''}`}>{label}</span>
    </span>
  )
}

function ItemUsage({ item }: { item: CatalogItem }) {
  const usage = useObject<ItemQuotaUsage>(`catalog_items/${item.id}/quota_usage/`)
  const deployments = item.kind === 'deployment' && item.max_deployments != null
  const limits = [
    deployments && tr('{n} deployments each', { n: item.max_deployments ?? 0 }),
    item.max_requests_per_day != null && tr('{n} requests in 24 hours', { n: item.max_requests_per_day }),
    item.over_quota === 'approve' ? tr('over quota, the owner team approves') : tr('over quota, refused'),
  ].filter(Boolean)
  return (
    <Panel
      title={
        <span className="row wrap" style={{ gap: 8 }}>
          <span>{item.name}</span>
          <span className="small muted">{limits.join(' · ')}</span>
        </span>
      }
      pad={false}
    >
      <DataTable<QuotaUsage>
        label={tr('Quota usage of {name}', { name: item.name })}
        stack
        rows={usage.data?.results}
        rowKey={(u) => `${u.team?.id ?? 0}-${u.user?.id ?? 0}`}
        loading={usage.isPending}
        error={usage.error}
        columns={[
          {
            key: 'who',
            header: tr('Team or user'),
            render: (u) => (
              <span className="row" style={{ gap: 6 }}>
                <span className="cell-title">{holderName(u)}</span>
                {!u.team && <KindTag>{tr('user')}</KindTag>}
              </span>
            ),
          },
          ...(deployments
            ? [{ key: 'deployments', header: tr('Deployments'), render: (u: QuotaUsage) => <QuotaMeter who={holderName(u)} used={u.deployments} max={u.max_deployments} /> }]
            : []),
          { key: 'requests', header: tr('Requests in 24 hours'), render: (u) => <QuotaMeter who={holderName(u)} used={u.requests} max={u.max_requests_per_day} /> },
        ]}
        empty={<Empty title={tr('Nothing used yet')}>{tr('Its requests and deployments count here.')}</Empty>}
      />
    </Panel>
  )
}

export function CatalogQuotas() {
  const items = useList<CatalogItem>('catalog_items/', { order_by: 'name', page_size: 200 })
  const limited = (items.data?.results ?? []).filter((i) => hasQuota(i) && i.summary_fields.user_capabilities?.edit)
  return (
    <div className="page">
      <PageHead
        title={tr('Catalog quotas')}
        subtitle={tr('What each team, or each user requesting for no team, uses of the quotas of the catalog items you administer: the deployments it keeps and its requests of the last 24 hours.')}
        bordered={false}
      />
      <div className="page-body stack" style={{ gap: 20 }}>
        {items.isPending ? null : limited.length === 0 ? (
          <Empty title={tr('No item with a quota')}>{tr('An item’s max_deployments and max_requests_per_day set its quotas.')}</Empty>
        ) : (
          limited.map((i) => <ItemUsage key={i.id} item={i} />)
        )}
      </div>
    </div>
  )
}
