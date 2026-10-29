import { useQueries } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router'
import { api, type Page } from '../api/client'
import { useInvalidate, useList } from '../api/hooks'
import type { Base } from '../api/types'
import { Dialog, useToast } from './overlay'
import { DataTable, type Column } from './table'
import { Empty } from './ui'

/** The events a notification can be attached for, as the API names its sub-lists. */
export const notificationEvents = [
  { key: 'started', label: 'Start' },
  { key: 'success', label: 'Success' },
  { key: 'error', label: 'Failure' },
  { key: 'approvals', label: 'Approval' },
] as const

type NT = Base & { notification_type: string }

/**
 * The notifications that fire for an object's jobs: every notification the
 * user can see, with a switch per event. `approvals` adds the approval event
 * (workflows and organizations). Attaching needs change on the object and on
 * the notification.
 */
export function NotificationsTab({ resource, id, approvals, editable }: { resource: string; id: number; approvals?: boolean; editable: boolean }) {
  const [q, setQ] = useState('')
  const [busy, setBusy] = useState<string>()
  const toast = useToast()
  const invalidate = useInvalidate()
  const all = useList<NT>('notification_templates/', { search: q || undefined, order_by: 'name', page_size: 100 })
  const events = notificationEvents.filter((e) => approvals || e.key !== 'approvals')
  const sub = (ev: string) => `${resource}/${id}/notification_templates_${ev}/`
  const attached = useQueries({
    queries: events.map((e) => ({ queryKey: [sub(e.key), 'list', { page_size: 200 }], queryFn: () => api.get<Page<Base>>(sub(e.key), { page_size: 200 }) })),
  })
  const on = (ev: string, nt: number) => !!attached[events.findIndex((e) => e.key === ev)]?.data?.results.some((r) => r.id === nt)
  const used = (nt: NT) => events.some((e) => on(e.key, nt.id))
  // Attached notifications first, then by name as the API sorts them.
  const rows = [...(all.data?.results ?? [])].sort((a, b) => Number(used(b)) - Number(used(a)))

  const toggle = async (nt: NT, ev: string, attach: boolean) => {
    setBusy(`${nt.id}:${ev}`)
    try {
      await api.post(sub(ev), attach ? { id: nt.id } : { id: nt.id, disassociate: true })
      await invalidate(`${resource}/${id}/notification_templates_`)
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      setBusy(undefined)
    }
  }

  const cols: Column<NT>[] = [
    {
      key: 'name',
      header: 'Notification',
      render: (n) => (
        <>
          <Link to={`/notifications/${n.id}`} className="cell-title">
            {n.name}
          </Link>
          <div className="cell-sub">
            {n.notification_type} · {n.summary_fields.organization?.name ?? 'no organization'}
          </div>
        </>
      ),
    },
    ...events.map(
      (e): Column<NT> => ({
        key: e.key,
        header: e.label,
        width: 96,
        className: 'center',
        render: (n) => {
          const may = editable && n.summary_fields.user_capabilities?.edit !== false
          return (
            <input
              type="checkbox"
              aria-label={`${n.name} on ${e.label.toLowerCase()}`}
              checked={on(e.key, n.id)}
              disabled={!may || busy === `${n.id}:${e.key}`}
              title={may ? undefined : editable ? 'You may not change this notification' : 'You may not change this object'}
              onChange={(ev) => toggle(n, e.key, ev.target.checked)}
            />
          )
        },
      }),
    ),
  ]

  return (
    <div className="stack" style={{ gap: 10 }}>
      <div className="row wrap" style={{ gap: 8 }}>
        <p className="muted grow" style={{ margin: 0 }}>
          Choose which notifications fire when a job {approvals ? 'starts, succeeds, fails or waits for approval' : 'starts, succeeds or fails'}.
        </p>
        <input className="input" type="search" placeholder="Filter notifications…" aria-label="Filter notifications" value={q} onChange={(e) => setQ(e.target.value)} style={{ maxWidth: 260 }} />
      </div>
      <DataTable
        label="Notifications"
        rows={rows}
        columns={cols}
        loading={all.isPending || attached.some((a) => a.isPending)}
        error={all.error ?? attached.find((a) => a.error)?.error}
        keyboard={false}
        empty={
          <Empty title={q ? 'No match' : 'No notifications yet'} action={q ? undefined : <Link to="/notifications/new" className="btn primary">New notification</Link>}>
            {q ? 'No notification matches this filter.' : 'Create a notification first, then come back to choose its events.'}
          </Empty>
        }
      />
    </div>
  )
}

/** The notifications tab in a dialog, for objects without a page (inventory sources). */
export function NotificationsDialog({ title, onClose, ...props }: Parameters<typeof NotificationsTab>[0] & { title: string; onClose: () => void }) {
  return (
    <Dialog title={title} onClose={onClose} wide footer={<button className="btn" onClick={onClose}>Done</button>}>
      <NotificationsTab {...props} />
    </Dialog>
  )
}
