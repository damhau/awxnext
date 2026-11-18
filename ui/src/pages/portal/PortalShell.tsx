// The portal's shell (roadmap M15-08): a light top bar with the requesters'
// pages, and on phones a tab bar at the bottom. No admin navigation.
import { Suspense } from 'react'
import { Link, NavLink, Outlet, useLocation } from 'react-router'
import { usePortal } from '../../api/hooks'
import { useSession } from '../../app/context'
import { LanguageAndTheme } from '../../app/Shell'
import { ErrorBoundary } from '../../components/ErrorBoundary'
import { Icon, type IconName } from '../../components/icons'
import { Menu } from '../../components/overlay'
import { Avatar, BrandMark, Loading } from '../../components/ui'
import { userName } from '../../lib/format'
import { tr } from '../../lib/i18n'
import './portal.css'

function Account() {
  const { me, logout } = useSession()
  const name = userName(me)
  return (
    <Menu label={tr('Account: {name}', { name })} className="portal-avatar" trigger={<Avatar name={name} />}>
      <div className="head">{name}</div>
      <LanguageAndTheme />
      <hr />
      <button onClick={logout}>
        <Icon name="logout" size={14} />
        {tr('Sign out')}
      </button>
    </Menu>
  )
}

export function PortalShell() {
  const portal = usePortal()
  const loc = useLocation()
  const brand = portal.data?.brand
  const title = brand?.title || tr('Self-service')
  const waiting = portal.data?.needs_you.approvals ?? 0
  const tabs: { to: string; label: string; short: string; icon: IconName; end?: boolean; count?: number }[] = [
    { to: '/portal', label: tr('Catalog'), short: tr('Catalog'), icon: 'box', end: true },
    { to: '/portal/requests', label: tr('My requests'), short: tr('Requests'), icon: 'history' },
    { to: '/portal/deployments', label: tr('My deployments'), short: tr('Deployments'), icon: 'server' },
    { to: '/portal/approvals', label: tr('Approvals'), short: tr('Approvals'), icon: 'approve', count: waiting },
  ]
  const badge = (n?: number) =>
    n ? (
      <span className="portal-count" aria-label={tr('{n} waiting', { n })}>
        {n}
      </span>
    ) : null
  return (
    <div className="portal" data-focus={/^\/portal\/approvals\/\d+/.test(loc.pathname) ? 'decide' : undefined}>
      <header className="portal-bar">
        <div className="portal-wrap portal-bar-in">
          <Link to="/portal" className="portal-brand">
            {brand?.logo ? <img src={brand.logo} alt="" className="portal-logo" /> : <BrandMark />}
            <span>{title}</span>
          </Link>
          <nav aria-label={tr('Portal')} className="portal-nav">
            {tabs.map((t) => (
              <NavLink key={t.to} to={t.to} end={t.end}>
                {t.label}
                {badge(t.count)}
              </NavLink>
            ))}
          </nav>
          <div className="portal-bar-right">
            {portal.data && !portal.data.request_only && (
              <Link to="/" className="portal-admin">
                {tr('Admin view')}
              </Link>
            )}
            <Account />
          </div>
        </div>
      </header>
      <main className="portal-main portal-wrap" id="main">
        <ErrorBoundary resetKey={loc.pathname}>
          <Suspense fallback={<Loading />}>
            <Outlet />
          </Suspense>
        </ErrorBoundary>
      </main>
      <nav aria-label={tr('Portal')} className="portal-tabs">
        {tabs.map((t) => (
          <NavLink key={t.to} to={t.to} end={t.end}>
            <span className="portal-tab-icon">
              <Icon name={t.icon} size={20} />
              {badge(t.count)}
            </span>
            <span>{t.short}</span>
          </NavLink>
        ))}
      </nav>
    </div>
  )
}

export function PortalNotFound() {
  return (
    <div className="portal-empty">
      <h1>{tr('This page does not exist')}</h1>
      <p>{tr('The link may be outdated, or the object was deleted.')}</p>
      <Link to="/portal" className="portal-btn">
        {tr('Back to the catalog')}
      </Link>
    </div>
  )
}
