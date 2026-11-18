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
  const tabs: { to: string; label: string; icon: IconName; end?: boolean }[] = [
    { to: '/portal', label: tr('Catalog'), icon: 'box', end: true },
    { to: '/portal/requests', label: tr('My requests'), icon: 'history' },
  ]
  return (
    <div className="portal">
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
            <Icon name={t.icon} size={20} />
            <span>{t.label}</span>
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
