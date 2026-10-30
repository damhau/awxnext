// "Used by": what refers to an object, in a panel on its page and in its
// delete confirmation, with what deleting it does to each.
import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { api } from '../api/client'
import { countKinds, deleteEffects, distinct, howUsed, kindOf, linkTo, type Dependent, type UsedBy } from '../lib/usedBy'
import { Dialog } from './overlay'
import { Loading, Panel } from './ui'

export function useUsedBy(resource: string, id: number | undefined) {
  return useQuery({ queryKey: [`${resource}/`, id, 'used_by'], enabled: !!id, queryFn: () => api.get<UsedBy>(`${resource}/${id}/used_by/`) })
}

/** The used_by of an object, or null when it cannot be read. */
export async function fetchUsedBy(resource: string, id: number): Promise<UsedBy | null> {
  try {
    return await api.get<UsedBy>(`${resource}/${id}/used_by/`)
  } catch {
    return null
  }
}

function Item({ d }: { d: Dependent }) {
  const to = linkTo(d)
  return (
    <li style={{ display: 'block' }}>
      {to ? (
        <Link to={to} className="ellipsis">
          {d.name}
        </Link>
      ) : (
        <span>{d.name}</span>
      )}
      <div className="faint">
        {kindOf(d)} · {d.effect === 'deleted' ? 'deleted with it' : howUsed(d)}
      </div>
    </li>
  )
}

export function UsedByList({ u, max = 12, empty }: { u: UsedBy; max?: number; empty: ReactNode }) {
  const all = distinct(u.results)
  if (!all.length && !u.hidden) return <span className="faint small">{empty}</span>
  return (
    <ul className="list-plain small" style={{ gap: 10 }}>
      {all.slice(0, max).map((d) => (
        <Item key={`${d.type}/${d.id ?? d.name}/${d.field}`} d={d} />
      ))}
      {all.length > max && <li className="faint">and {countKinds(all.slice(max))} more</li>}
      {u.hidden > 0 && (
        <li className="faint">
          {all.length ? 'and ' : ''}
          {u.hidden} you cannot see
        </li>
      )}
    </ul>
  )
}

export function UsedByPanel({ resource, id, empty, children }: { resource: string; id: number; empty: ReactNode; children?: ReactNode }) {
  const u = useUsedBy(resource, id)
  return (
    <Panel title="Used by">
      {u.isPending ? <Loading rows={2} /> : u.data ? <UsedByList u={u.data} empty={empty} /> : <span className="faint small">Could not be read.</span>}
      {children}
    </Panel>
  )
}

export function UsedByDialog({ resource, id, name, empty, onClose }: { resource: string; id: number; name: string; empty: ReactNode; onClose: () => void }) {
  const u = useUsedBy(resource, id)
  return (
    <Dialog
      title={`What uses ${name}`}
      onClose={onClose}
      footer={
        <button className="btn" onClick={onClose}>
          Done
        </button>
      }
    >
      {u.isPending ? <Loading rows={3} /> : u.data ? <UsedByList u={u.data} max={50} empty={empty} /> : <span className="faint small">Could not be read.</span>}
    </Dialog>
  )
}

function Names({ ds, max = 6 }: { ds: Dependent[]; max?: number }) {
  return (
    <>
      {ds.slice(0, max).map((d, i) => (
        <span key={`${d.type}/${d.id ?? d.name}`}>
          {i > 0 && (i === ds.length - 1 && ds.length <= max ? ' and ' : ', ')}
          <b>{d.name}</b> <span className="faint">({kindOf(d)})</span>
        </span>
      ))}
      {ds.length > max && <> and {countKinds(ds.slice(max))} more</>}
    </>
  )
}

/** In a delete confirmation: what goes with the object and what loses it. */
export function DeleteEffects({ u }: { u: UsedBy | null }) {
  if (!u) return null
  const e = deleteEffects(u)
  if (!e.deleted.length && !e.cleared.length && !e.hidden) return null
  return (
    <div className="notice warn small stack" role="note" style={{ gap: 6, marginTop: 10 }}>
      {e.deleted.length > 0 && (
        <div>
          Deleted with it: <Names ds={e.deleted} />.
        </div>
      )}
      {e.cleared.length > 0 && (
        <div>
          In use by <Names ds={e.cleared} />, which will lose it.
        </div>
      )}
      {e.hidden > 0 && <div>{e.hidden === 1 ? 'One more object you cannot see uses it.' : `${e.hidden} more objects you cannot see use it.`}</div>}
    </div>
  )
}
