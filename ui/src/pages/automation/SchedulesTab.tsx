// Schedules of one template (job, workflow, project, inventory source, management job).
import { useState } from 'react'
import { api } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Any } from '../../api/types'
import { Icon } from '../../components/icons'
import { Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, type Column } from '../../components/table'
import { Empty, Upcoming } from '../../components/ui'
import './automation.css'
import { describeText } from './rrule'
import { ScheduleDialog, templateResource } from './ScheduleForm'

export { RRuleEditor, ScheduleDialog } from './ScheduleForm'

/** Enable switch for a schedule; saves immediately. */
export function EnabledToggle({ schedule, label = true }: { schedule: Any; label?: boolean }) {
  const invalidate = useInvalidate()
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const can = schedule.summary_fields?.user_capabilities?.edit !== false
  return (
    <label className="toggle" title={schedule.enabled ? 'Enabled' : 'Disabled'}>
      <input
        type="checkbox"
        checked={!!schedule.enabled}
        disabled={busy || !can}
        aria-label={`${schedule.enabled ? 'Disable' : 'Enable'} ${schedule.name}`}
        onChange={async (e) => {
          setBusy(true)
          try {
            await api.patch(`schedules/${schedule.id}/`, { enabled: e.target.checked })
            invalidate('schedules', 'job_templates', 'workflow_job_templates', 'projects', 'inventory_sources', 'system_job_templates', 'unified_job_templates')
          } catch (err) {
            toast(err instanceof Error ? err.message : 'Could not change the schedule', 'error')
          } finally {
            setBusy(false)
          }
        }}
      />
      <span className="track" aria-hidden="true" />
      {label && <span className="lbl">{schedule.enabled ? 'On' : 'Off'}</span>}
    </label>
  )
}

/** Row menu: edit and delete. */
export function ScheduleMenu({ schedule, onEdit }: { schedule: Any; onEdit: () => void }) {
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const caps = schedule.summary_fields?.user_capabilities ?? {}
  return (
    <Menu label={`Actions for ${schedule.name}`} className="btn sm icon">
      <button onClick={onEdit} disabled={caps.edit === false}>
        <Icon name="edit" size={14} />
        Edit
      </button>
      <button
        className="danger"
        disabled={caps.delete === false}
        onClick={async () => {
          if (!(await confirm({ title: `Delete “${schedule.name}”?`, body: <p style={{ margin: 0 }}>Its future runs are cancelled. Jobs it already launched stay.</p>, confirm: 'Delete schedule', danger: true }))) return
          try {
            await api.del(`schedules/${schedule.id}/`)
            invalidate('schedules', 'job_templates', 'workflow_job_templates', 'projects', 'inventory_sources', 'system_job_templates', 'unified_job_templates')
            toast(`Deleted “${schedule.name}”`)
          } catch (e) {
            toast(e instanceof Error ? e.message : 'Delete failed', 'error')
          }
        }}
      >
        <Icon name="trash" size={14} />
        Delete
      </button>
    </Menu>
  )
}

export function SchedulesTab({ template }: { template: { id: number; type: string; name: string } }) {
  const [page, setPage] = useState(1)
  const [editing, setEditing] = useState<Any | null | 'new'>(null)
  const path = `${templateResource(template.type)}/${template.id}/schedules/`
  const list = useList<Any>(path, { order_by: 'next_run,name', page, page_size: 20 })
  const rows = list.data?.results

  const columns: Column<Any>[] = [
    {
      key: 'name',
      header: 'Schedule',
      render: (s) => (
        <div>
          <button className="link-btn cell-title" style={{ color: 'var(--ink)' }} onClick={() => setEditing(s)}>
            {s.name}
          </button>
          {s.description && <div className="small faint">{s.description}</div>}
        </div>
      ),
    },
    { key: 'when', header: 'When', render: (s) => <span>{describeText(s.rrule)}</span> },
    { key: 'next', header: 'Next run', className: 'nowrap', render: (s) => (s.enabled ? <Upcoming at={s.next_run} /> : <span className="faint">Paused</span>) },
    { key: 'enabled', header: 'Enabled', width: 90, render: (s) => <EnabledToggle schedule={s} /> },
    { key: 'menu', header: <span className="sr-only">Actions</span>, className: 'end', width: 48, render: (s) => <ScheduleMenu schedule={s} onEdit={() => setEditing(s)} /> },
  ]

  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row wrap">
        <p className="muted grow" style={{ margin: 0 }}>
          {rows?.length ? `Runs ${template.name} on ${list.data!.count === 1 ? 'one schedule' : `${list.data!.count} schedules`}.` : 'Run this template at set times.'}
        </p>
        <button className="btn primary" onClick={() => setEditing('new')}>
          <Icon name="plus" size={14} />
          Add schedule
        </button>
      </div>
      <DataTable
        label={`Schedules of ${template.name}`}
        rows={rows}
        columns={columns}
        loading={list.isPending}
        error={list.error}
        onOpen={(s) => setEditing(s)}
        empty={
          <Empty title="No schedules" action={<button className="btn" onClick={() => setEditing('new')}>Add a schedule</button>}>
            Nightly runs, weekly maintenance windows, a one-off run tomorrow at 06:00.
          </Empty>
        }
        footer={
          list.data && list.data.count > 20 ? (
            <div className="table-foot">
              <span>{list.data.count} schedules</span>
              <Pager count={list.data.count} page={page} pageSize={20} onPage={setPage} />
            </div>
          ) : undefined
        }
      />
      {editing && <ScheduleDialog schedule={editing === 'new' ? undefined : editing} template={template} onClose={() => setEditing(null)} />}
    </div>
  )
}
