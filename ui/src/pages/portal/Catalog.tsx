// The portal's home: what needs the user, then the catalog by category.
import { useState } from 'react'
import { Link } from 'react-router'
import { useList, usePortal } from '../../api/hooks'
import { useSession } from '../../app/context'
import { Icon } from '../../components/icons'
import { ago, dateOnly, dateTimeText } from '../../lib/format'
import { tr, trn } from '../../lib/i18n'
import { approvalHint, categories, lifetimeHint, matches, requestState, type NeedsYou, type PortalItem } from '../../lib/portal'
import { Cell, ItemIcon, Pill } from './parts'

function NeedsYouCards({ needs, requestOnly }: { needs: NeedsYou; requestOnly: boolean }) {
  const cards = []
  for (const d of needs.deployments) {
    cards.push(
      <div key={`d${d.id}`} className="portal-card">
        <div className="portal-card-head">
          <span className="mono portal-strong">{d.name}</span>
          <Pill tone="waiting">{tr('Expires {when}', { when: ago(d.expires_at) })}</Pill>
        </div>
        <span className="portal-muted">{tr('Extend it, or let it go on {date}.', { date: dateTimeText(d.expires_at) })}</span>
      </div>,
    )
  }
  for (const r of needs.requests) {
    const st = requestState(r.status)
    cards.push(
      <Link key={`r${r.id}`} to={`/portal/requests/${r.id}`} className="portal-card">
        <div className="portal-card-head">
          <span className="portal-strong">{r.item_name}</span>
          <span className="mono portal-muted">#{r.id}</span>
        </div>
        <div className="row" style={{ gap: 8 }}>
          <Cell state={r.status === 'awaiting_approval' ? 'waiting' : 'running'} />
          <span>{st.label}</span>
        </div>
        <span className="portal-muted">{tr('Sent {when}.', { when: ago(r.created) })}</span>
      </Link>,
    )
  }
  if (needs.approvals > 0) {
    const body = (
      <>
        <div className="portal-card-head">
          <span className="portal-strong">{trn(needs.approvals, '{n} request waits for you', '{n} requests wait for you')}</span>
        </div>
        <div className="row" style={{ gap: 4 }}>
          {Array.from({ length: Math.min(needs.approvals, 8) }, (_, i) => (
            <Cell key={i} state="waiting" />
          ))}
        </div>
        <span className="portal-muted">{tr('Approve or deny them before they time out.')}</span>
      </>
    )
    cards.push(
      requestOnly ? (
        <div key="a" className="portal-card">
          {body}
        </div>
      ) : (
        <Link key="a" to="/approvals" className="portal-card">
          {body}
        </Link>
      ),
    )
  }
  if (!cards.length) return null
  return (
    <section aria-labelledby="needs" className="portal-section">
      <h2 id="needs" className="portal-eyebrow">
        {tr('Needs you')}
      </h2>
      <div className="portal-grid wide">{cards}</div>
    </section>
  )
}

function ItemCard({ item }: { item: PortalItem }) {
  const approval = approvalHint(item.approval_rules)
  const freeze = item.summary_fields.freeze
  return (
    <Link to={`/portal/items/${item.id}`} className="portal-card portal-item">
      <div className="portal-card-head">
        <ItemIcon name={item.icon} />
        {item.category && <span className="portal-eyebrow">{item.category}</span>}
      </div>
      <div className="stack" style={{ gap: 4 }}>
        <span className="portal-item-name">{item.name}</span>
        {item.description && <span className="portal-muted portal-clamp">{item.description}</span>}
      </div>
      <div className="portal-chips">
        <Pill tone={approval.tone === 'none' ? 'ok' : 'waiting'}>{approval.text}</Pill>
        <Pill tone="neutral">{lifetimeHint(item)}</Pill>
      </div>
      {freeze && (
        <div className="portal-card-foot">
          <Cell state="skipped" size="sm" />
          <span>{tr('Paused for {name} until {date}', { name: freeze.name, date: dateOnly(freeze.until) })}</span>
        </div>
      )}
    </Link>
  )
}

export function Catalog() {
  const { me } = useSession()
  const portal = usePortal()
  const items = useList<PortalItem>('catalog_items/', { order_by: 'name', page_size: 200 })
  const [query, setQuery] = useState('')
  const [category, setCategory] = useState<string | null>(null)
  const all = items.data?.results ?? []
  const found = all.filter((i) => matches(i, query))
  const shown = found.filter((i) => category === null || i.category === category)
  const cats = categories(found)
  const first = me.first_name || me.username
  return (
    <div className="portal-page">
      <section className="portal-hero">
        <h1>{tr('What do you need, {name}?', { name: first })}</h1>
        <p className="portal-lead">{tr('Ask for a server, a database or access. You follow each request here.')}</p>
        <div className="portal-search">
          <label htmlFor="portal-q">{tr('Search the catalog')}</label>
          <div className="portal-search-box">
            <Icon name="search" size={18} />
            <input id="portal-q" type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder={tr('VM, database, firewall rule…')} />
          </div>
        </div>
      </section>

      {portal.data && <NeedsYouCards needs={portal.data.needs_you} requestOnly={portal.data.request_only} />}

      <section aria-labelledby="catalog" className="portal-section">
        <div className="portal-section-head">
          <h2 id="catalog">{tr('Catalog')}</h2>
          {cats.length > 1 && (
            <div role="group" aria-label={tr('Category')} className="portal-chips">
              <button type="button" className="portal-chip" aria-pressed={category === null} onClick={() => setCategory(null)}>
                {tr('All')} <span className="mono">{found.length}</span>
              </button>
              {cats.map((c) => (
                <button key={c.name} type="button" className="portal-chip" aria-pressed={category === c.name} onClick={() => setCategory(c.name)}>
                  {c.name || tr('Other')} <span className="mono">{c.n}</span>
                </button>
              ))}
            </div>
          )}
        </div>
        {items.isPending ? (
          <div className="portal-grid" aria-busy="true" />
        ) : items.error ? (
          <p className="portal-error">{items.error.message}</p>
        ) : all.length === 0 ? (
          <div className="portal-empty">
            <h3>{tr('Nothing to request yet')}</h3>
            <p>{tr('Your organization hasn’t given you any catalog item. Ask its admins for the requester role.')}</p>
          </div>
        ) : shown.length === 0 ? (
          <div className="portal-empty">
            <h3>{tr('Nothing matches “{query}”', { query })}</h3>
            <p>{tr('Try another word, or another category.')}</p>
          </div>
        ) : (
          <div className="portal-grid">
            {shown.map((i) => (
              <ItemCard key={i.id} item={i} />
            ))}
          </div>
        )}
      </section>
    </div>
  )
}
