import { useQueryClient } from '@tanstack/react-query'
import { Suspense, useEffect, useRef, useState } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { useList } from '../api/hooks'
import { useLive } from '../api/live'
import type { RecentJob } from '../api/types'
import { ErrorBoundary } from '../components/ErrorBoundary'
import { Icon, type IconName } from '../components/icons'
import { Menu } from '../components/overlay'
import { Avatar, BrandMark, Loading, RunBars } from '../components/ui'
import { jobsNewestFirst, userName } from '../lib/format'
import { useHotkeys } from '../lib/hotkeys'
import { useStored } from '../lib/storage'
import { useOrg, useSession } from './context'
import { Palette } from './Palette'

/** Refreshes job-related queries when job statuses change anywhere. */
function LiveRefresh() {
  const qc = useQueryClient()
  const timer = useRef<number | undefined>(undefined)
  useLive({ jobs: ['status_changed'] }, () => {
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => {
      qc.invalidateQueries({
        predicate: (q) => {
          const k = q.queryKey[0]
          return (
            typeof k === 'string' &&
            /^(unified_jobs|jobs|workflow_jobs|project_updates|inventory_updates|ad_hoc_commands|system_jobs|workflow_approvals|unified_job_templates|job_templates|workflow_job_templates|projects|inventory_sources|live-)/.test(k)
          )
        },
      })
    }, 400)
  })
  return null
}

function LiveCount() {
  const running = useList('unified_jobs/', { status: 'running', page_size: 1 }, { queryKey: ['live-running'], refetchInterval: 60_000 })
  const queued = useList('unified_jobs/', { status__in: 'pending,waiting', not__type: 'workflow_approval', page_size: 1 }, { queryKey: ['live-queued'], refetchInterval: 60_000 })
  const r = running.data?.count ?? 0
  const q = queued.data?.count ?? 0
  return (
    <Link to="/jobs?q=status:running" className="live-count" title="Jobs running and waiting for capacity">
      <span aria-hidden="true" className={`live-dot ${r ? '' : 'idle'}`} />
      {r} running · {q} queued
    </Link>
  )
}

/** The last 12 jobs as cells, newest on the right. */
function RecentStrip() {
  const { scope } = useOrg()
  const recent = useList<RecentJob>('unified_jobs/', { order_by: jobsNewestFirst, page_size: 12, not__type: 'workflow_approval', ...scope })
  return <RunBars jobs={recent.data?.results} slots={12} />
}

type NavItem = { to: string; label: string; icon: IconName; end?: boolean; count?: number; superuser?: boolean }

function useNav(): { label: string; items: NavItem[] }[] {
  const approvals = useList('workflow_approvals/', { status: 'pending', page_size: 1 }, { queryKey: ['workflow_approvals', 'pending-count'], refetchInterval: 60_000 })
  return [
    {
      label: 'Automation',
      items: [
        { to: '/', label: 'Now', icon: 'activity', end: true },
        { to: '/templates', label: 'Templates', icon: 'templates' },
        { to: '/jobs', label: 'Jobs', icon: 'jobs' },
        { to: '/schedules', label: 'Schedules', icon: 'calendar' },
        { to: '/approvals', label: 'Approvals', icon: 'approve', count: approvals.data?.count || undefined },
      ],
    },
    {
      label: 'Resources',
      items: [
        { to: '/inventories', label: 'Inventories', icon: 'inventory' },
        { to: '/hosts', label: 'Hosts', icon: 'host' },
        { to: '/projects', label: 'Projects', icon: 'project' },
        { to: '/credentials', label: 'Credentials', icon: 'key' },
        { to: '/execution-environments', label: 'Execution environments', icon: 'box' },
      ],
    },
    {
      label: 'Access',
      items: [
        { to: '/organizations', label: 'Organizations', icon: 'org' },
        { to: '/folders', label: 'Folders', icon: 'folder' },
        { to: '/teams', label: 'Teams', icon: 'team' },
        { to: '/users', label: 'Users', icon: 'users' },
      ],
    },
    {
      label: 'Admin',
      items: [
        { to: '/instances', label: 'Instances', icon: 'server' },
        { to: '/notifications', label: 'Notifications', icon: 'bell' },
        { to: '/gitops', label: 'GitOps', icon: 'git' },
        { to: '/management-jobs', label: 'Management jobs', icon: 'terminal', superuser: true },
        { to: '/activity', label: 'Activity', icon: 'history' },
        { to: '/settings', label: 'Settings', icon: 'settings', superuser: true },
      ],
    },
  ]
}

function Nav() {
  const [collapsed, setCollapsed] = useStored('nav-collapsed', false)
  const { me } = useSession()
  const groups = useNav()
  return (
    <nav aria-label="Main" className={`nav ${collapsed ? 'collapsed' : ''}`}>
      {groups.map((g) => (
        <div key={g.label} className="nav-group">
          {!collapsed && <div className="nav-label">{g.label}</div>}
          {g.items
            .filter((i) => !i.superuser || me.is_superuser || me.is_system_auditor)
            .map((i) => (
              <NavLink key={i.to} to={i.to} end={i.end} className={({ isActive }) => (isActive ? 'active' : '')} title={collapsed ? i.label : undefined} aria-label={collapsed ? i.label : undefined}>
                {collapsed ? <Icon name={i.icon} /> : <span className="mark" aria-hidden="true" />}
                {!collapsed && <span>{i.label}</span>}
                {!collapsed && i.count ? <span className="count">{i.count}</span> : null}
              </NavLink>
            ))}
        </div>
      ))}
      <button className="nav-toggle" onClick={() => setCollapsed(!collapsed)} aria-label={collapsed ? 'Expand navigation' : 'Collapse navigation'}>
        {collapsed ? '»' : '« Collapse'}
      </button>
    </nav>
  )
}

function OrgSwitcher() {
  const { orgs, org, setOrg } = useOrg()
  if (orgs.length <= 1 && !org) return null
  return (
    <div className="org">
      <Menu
        label="Organization"
        align="left"
        className="bar-button"
        trigger={
          <>
            {org ? org.name : 'All organizations'}
            <Icon name="chevron" size={12} />
          </>
        }
      >
        <div className="head">Show content of</div>
        <button onClick={() => setOrg(null)}>{!org && <Icon name="check" size={14} />}All organizations</button>
        {orgs.map((o) => (
          <button key={o.id} onClick={() => setOrg(o.id)}>
            {org?.id === o.id ? <Icon name="check" size={14} /> : <span style={{ width: 14 }} />}
            {o.name}
          </button>
        ))}
      </Menu>
    </div>
  )
}

function Account() {
  const { me, logout } = useSession()
  const name = userName(me)
  return (
    <Menu label={`Account: ${name}`} className="avatar" trigger={<Avatar name={name} />}>
      <div className="head">{name}</div>
      <Link to={`/users/${me.id}`}>Profile and tokens</Link>
      <Link to="/run">Operator view</Link>
      <a href="/api/v2/" target="_blank" rel="noreferrer">
        API browser <Icon name="external" size={12} />
      </a>
      <hr />
      <button onClick={logout}>
        <Icon name="logout" size={14} />
        Sign out
      </button>
    </Menu>
  )
}

export function Shell() {
  const [palette, setPalette] = useState(false)
  const nav = useNavigate()
  const loc = useLocation()
  useHotkeys({
    'mod+k': () => setPalette((p) => !p),
    '/': () => {
      const f = document.querySelector<HTMLInputElement>('[data-filter]')
      if (f) f.focus()
      else setPalette(true)
    },
  })
  // "g then t/j/i/p" style jumps.
  useEffect(() => {
    let armed = 0
    const on = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || e.metaKey || e.ctrlKey) return
      if (e.key === 'g') {
        armed = Date.now()
        return
      }
      if (Date.now() - armed > 1000) return
      const to: Record<string, string> = { n: '/', t: '/templates', j: '/jobs', i: '/inventories', p: '/projects', s: '/schedules', a: '/approvals', c: '/credentials' }
      if (to[e.key]) {
        armed = 0
        nav(to[e.key])
      }
    }
    window.addEventListener('keydown', on)
    return () => window.removeEventListener('keydown', on)
  }, [nav])
  useEffect(() => setPalette(false), [loc.pathname])
  return (
    <div className="shell">
      <LiveRefresh />
      <header className="topbar">
        <Link to="/" className="brand" aria-label="awxnext home">
          <BrandMark />
          <span>awxnext</span>
        </Link>
        <OrgSwitcher />
        <button className="bar-button jump" onClick={() => setPalette(true)}>
          <Icon name="search" size={14} />
          <span className="grow">Jump to template, host, job…</span>
          <span className="kbd">{navigator.platform.includes('Mac') ? '⌘K' : 'Ctrl K'}</span>
        </button>
        <div className="topbar-right">
          {loc.pathname === '/' && <RecentStrip />}
          <LiveCount />
          <Account />
        </div>
      </header>
      <div className="body">
        <Nav />
        <main className="main" id="main">
          <ErrorBoundary resetKey={loc.pathname}>
            <Suspense fallback={<div className="page"><Loading /></div>}>
              <Outlet />
            </Suspense>
          </ErrorBoundary>
        </main>
      </div>
      {palette && <Palette onClose={() => setPalette(false)} />}
    </div>
  )
}
