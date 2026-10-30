import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { api, ApiError, getAll, onUnauthorized, type Page } from '../api/client'
import type { Me, Ref } from '../api/types'
import { live, useLive } from '../api/live'
import { useStored } from '../lib/storage'
import { Login } from '../pages/Login'

interface Session {
  me: Me
  logout: () => Promise<void>
}

const SessionCtx = createContext<Session | null>(null)

export function useSession(): Session {
  const s = useContext(SessionCtx)
  if (!s) throw new Error('no session')
  return s
}

export function useMe(): Me {
  return useSession().me
}

/** Renders the login page until /api/v2/me/ succeeds, and again when the session expires. */
export function SessionGate({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const [expired, setExpired] = useState(false)
  const [replaced, setReplaced] = useState(false)
  const me = useQuery({
    queryKey: ['me'],
    queryFn: () => api.get<Page<Me>>('me/').then((r) => r.results[0]),
    retry: (n, e) => !(e instanceof ApiError && (e.status === 401 || e.status === 403)) && n < 2,
    staleTime: 5 * 60_000,
  })
  useEffect(() => onUnauthorized(() => setExpired(true)), [])
  // SESSIONS_PER_USER: a login elsewhere ended the user's oldest sessions;
  // tabs of a session that still works stay.
  useLive(me.data ? { control: [`limit_reached_${me.data.id}`] } : null, (m) => {
    if (m.reason !== 'limit_reached') return
    api.get('me/').catch((e) => {
      if (e instanceof ApiError && e.status === 401) {
        setReplaced(true)
        setExpired(true)
      }
    })
  })
  const logout = async () => {
    await api.post('/api/logout/').catch(() => {})
    live.stop()
    qc.clear()
    window.location.assign('/')
  }
  if (me.isPending) return <div className="login" aria-busy="true" />
  const unauth = expired || (me.error instanceof ApiError && (me.error.status === 401 || me.error.status === 403)) || !me.data
  if (unauth) {
    return (
      <Login
        expired={expired && !!me.data}
        replaced={replaced}
        onDone={async () => {
          setExpired(false)
          setReplaced(false)
          await qc.invalidateQueries()
        }}
      />
    )
  }
  return <SessionCtx.Provider value={{ me: me.data, logout }}>{children}</SessionCtx.Provider>
}

interface OrgState {
  orgs: Ref[]
  org: Ref | null
  setOrg: (id: number | null) => void
  /** Params narrowing a list to the current organization. */
  scope: Record<string, number>
}

const OrgCtx = createContext<OrgState>({ orgs: [], org: null, setOrg: () => {}, scope: {} })

export function OrgProvider({ children }: { children: ReactNode }) {
  const [orgId, setOrgId] = useStored<number | null>('org', null)
  const orgs = useQuery({ queryKey: ['organizations', 'all'], queryFn: () => getAll<Ref>('organizations/', { order_by: 'name' }), staleTime: 60_000 })
  const list = orgs.data ?? []
  const org = list.find((o) => o.id === orgId) ?? null
  useEffect(() => {
    // A single organization is selected implicitly.
    if (!orgs.data) return
    if (orgId !== null && !orgs.data.some((o) => o.id === orgId)) setOrgId(null)
  }, [orgs.data, orgId, setOrgId])
  return <OrgCtx.Provider value={{ orgs: list, org, setOrg: setOrgId, scope: org ? { organization: org.id } : {} }}>{children}</OrgCtx.Provider>
}

export const useOrg = () => useContext(OrgCtx)

/**
 * Fills the organization field of a create form: the organization chosen in
 * the top bar, else the first one. Waits for the list to load, and runs only
 * once, so clearing the field afterwards sticks.
 */
export function usePreselectOrg(value: Ref | null | undefined, set: (o: Ref) => void, enabled = true) {
  const { org, orgs } = useOrg()
  const done = useRef(!enabled || !!value)
  useEffect(() => {
    if (done.current) return
    const pick = org ?? orgs[0]
    if (!pick) return
    done.current = true
    if (!value) set(pick)
  }, [org, orgs, value, set])
}
