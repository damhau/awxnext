import { useQueries } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { api, type Page } from '../api/client'
import { useInvalidate, useList } from '../api/hooks'
import type { Base } from '../api/types'
import { Dialog, useToast } from './overlay'
import { DataTable, type Column } from './table'
import { Ago, Empty } from './ui'
import { tr } from '../lib/i18n'

/** summary_fields.failing of a notification template: its sends that failed since its last success, once there are 3. */
export interface Failing {
  failures: number
  error: string
  since: string
}

/** A notification template's failing mark, with its last error on hover, or nothing. */
export function FailingMark({ failing }: { failing?: Failing | null }) {
  if (!failing) return null
  return (
    <span className="chip fail-text" title={failing.error}>
      {tr('Failing: {n} sends in a row', { n: failing.failures })}
    </span>
  )
}

/** Why a notification template is failing: how long, and its last error. */
export function FailingNote({ failing, deliveries }: { failing?: Failing | null; deliveries?: string }) {
  if (!failing) return null
  return (
    <div className="notice warn" role="status">
      <strong>{tr('Failing.')}</strong> {tr('Its last {n} sends failed, the first', { n: failing.failures })} <Ago at={failing.since} />.{' '}
      {tr('The next successful send clears this.')} {deliveries && <Link to={deliveries}>{tr('See the deliveries')}</Link>}
      <div className="small mono" style={{ marginTop: 6, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
        {failing.error}
      </div>
    </div>
  )
}

export interface NotificationEvent {
  key: string
  label: string
  /** What an awxnext event means: a change from the template's previous runs. */
  hint?: string
}

/** The events a notification can be attached for, as the API names its sub-lists: AWX's, then awxnext's. */
export const notificationEvents: NotificationEvent[] = [
  { key: 'started', label: tr('Start') },
  { key: 'success', label: tr('Success') },
  { key: 'error', label: tr('Failure') },
  { key: 'approvals', label: tr('Approval') },
  { key: 'first_error', label: tr('First failure'), hint: tr('a failure after a success') },
  { key: 'recovered', label: tr('Recovered'), hint: tr('a success after a failure') },
  { key: 'slow', label: tr('Slow'), hint: tr('more than twice the median time of the last 10 successful runs') },
]

type NT = Base & { notification_type: string }

/**
 * The notifications that fire for an object's jobs: every notification the
 * user can see, with a switch per event. `approvals` adds the approval event
 * (workflows and organizations), and `changes` awxnext's first failure,
 * recovered and slow; `events` replaces them for objects with other events,
 * such as catalog items. Attaching needs change on the object and on the
 * notification.
 */
export function NotificationsTab({
  resource,
  id,
  approvals,
  changes,
  editable,
  events: only,
  intro,
}: {
  resource: string
  id: number
  approvals?: boolean
  changes?: boolean
  editable: boolean
  events?: NotificationEvent[]
  intro?: ReactNode
}) {
  const [q, setQ] = useState('')
  const [busy, setBusy] = useState<string>()
  const toast = useToast()
  const invalidate = useInvalidate()
  const all = useList<NT>('notification_templates/', { search: q || undefined, order_by: 'name', page_size: 100 })
  const events = only ?? notificationEvents.filter((e) => (approvals || e.key !== 'approvals') && (changes || !e.hint))
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
      header: tr('Notification'),
      render: (n) => (
        <>
          <Link to={`/notifications/${n.id}`} className="cell-title">
            {n.name}
          </Link>
          <div className="cell-sub">
            {n.notification_type} · {n.summary_fields.organization?.name ?? tr('no organization')} <FailingMark failing={n.summary_fields.failing} />
          </div>
        </>
      ),
    },
    ...events.map(
      (e): Column<NT> => ({
        key: e.key,
        header: e.hint ? <span title={e.hint}>{e.label}</span> : e.label,
        width: 96,
        className: 'center',
        render: (n) => {
          const may = editable && n.summary_fields.user_capabilities?.edit !== false
          return (
            <input
              type="checkbox"
              aria-label={tr('{name} on {toLowerCase}', { name: n.name, toLowerCase: e.label.toLowerCase() })}
              checked={on(e.key, n.id)}
              disabled={!may || busy === `${n.id}:${e.key}`}
              title={may ? undefined : editable ? tr('You may not change this notification') : tr('You may not change this object')}
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
          {intro ?? (
            <>
              {tr('Choose which notifications fire when a job')} {approvals ? tr('starts, succeeds, fails or waits for approval') : tr('starts, succeeds or fails')}.
            </>
          )}
        </p>
        <input className="input" type="search" placeholder={tr('Filter notifications…')} aria-label={tr('Filter notifications')} value={q} onChange={(e) => setQ(e.target.value)} style={{ maxWidth: 260 }} />
      </div>
      {events.some((e) => e.hint) && (
        <p className="muted" style={{ margin: 0, fontSize: 13 }}>
          {events
            .filter((e) => e.hint)
            .map((e) => (
              <span key={e.key}>
                <strong>{e.label}</strong>: {e.hint}.{' '}
              </span>
            ))}
          {tr('Each compares a run with the template’s earlier runs, canceled runs aside, and fires when the result changes, not on the later runs that keep it.')}
        </p>
      )}
      <DataTable
        label={tr('Notifications')}
        rows={rows}
        columns={cols}
        loading={all.isPending || attached.some((a) => a.isPending)}
        error={all.error ?? attached.find((a) => a.error)?.error}
        keyboard={false}
        empty={
          <Empty title={q ? tr('No match') : tr('No notifications yet')} action={q ? undefined : <Link to="/notifications/new" className="btn primary">{tr('New notification')}</Link>}>
            {q ? tr('No notification matches this filter.') : tr('Create a notification first, then come back to choose its events.')}
          </Empty>
        }
      />
    </div>
  )
}

/** The notifications tab in a dialog, for objects without a page (inventory sources). */
export function NotificationsDialog({ title, onClose, ...props }: Parameters<typeof NotificationsTab>[0] & { title: string; onClose: () => void }) {
  return (
    <Dialog title={title} onClose={onClose} wide footer={<button className="btn" onClick={onClose}>{tr('Done')}</button>}>
      <NotificationsTab {...props} />
    </Dialog>
  )
}
