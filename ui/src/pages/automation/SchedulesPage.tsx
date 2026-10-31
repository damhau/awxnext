import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useInvalidate, useList } from '../../api/hooks'
import { useLive } from '../../api/live'
import type { Any } from '../../api/types'
import { useOrg } from '../../app/context'
import { Icon } from '../../components/icons'
import { api } from '../../api/client'
import { BulkBar, BulkDelete, useBulk, useSelection } from '../../components/BulkBar'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Empty, PageHead, Upcoming } from '../../components/ui'
import { templatePath, typeLabel } from '../../lib/format'
import { compile, getTerm, setTerm, type KeyTable } from '../../lib/query'
import { describeText } from './rrule'
import { ScheduleDialog, ujtTypeOf } from './ScheduleForm'
import { EnabledToggle, ScheduleMenu } from './SchedulesTab'
import { tr } from '../../lib/i18n'

const typeMap: Record<string, string> = {
  job: 'job_template',
  template: 'job_template',
  workflow: 'workflow_job_template',
  project: 'project',
  inventory: 'inventory_source',
  source: 'inventory_source',
  system: 'system_job_template',
  management: 'system_job_template',
}

const KEYS: KeyTable = {
  name: { field: 'name', lookup: 'icontains', help: tr('schedule name') },
  template: { field: 'unified_job_template__name', lookup: 'icontains', help: tr('what it runs') },
  type: { field: 'unified_job_template__type', map: (v) => v.split(',').map((x) => typeMap[x.toLowerCase()] ?? x).join(','), help: tr('job, workflow, project, inventory, management') },
  enabled: { field: 'enabled', map: (v) => (['no', 'false', 'off', '0'].includes(v.toLowerCase()) ? 'false' : 'true'), help: tr('true or false') },
}

export function SchedulesPage() {
  const ls = useListState({ order: 'next_run', pageSize: 25 })
  const { scope } = useOrg()
  const [draft, setDraft] = useState(ls.q)
  const [editing, setEditing] = useState<Any | 'new' | null>(null)
  const compiled = useMemo(() => compile(ls.q, KEYS), [ls.q])
  const params = {
    ...compiled.params,
    ...(scope.organization ? { unified_job_template__organization: scope.organization } : {}),
    order_by: ls.order || 'next_run',
    page: ls.page,
    page_size: ls.pageSize,
  }
  const list = useList<Any>('schedules/', params)
  const sel = useSelection<Any>(list.data?.results)
  const run = useBulk<Any>(['schedule', 'schedules'], 'schedules', 'job_templates', 'workflow_job_templates', 'projects', 'inventory_sources', 'system_job_templates', 'unified_job_templates')
  const turn = (on: boolean) => run(on ? tr('Turn on') : tr('Turn off'), sel.rows, (s) => api.patch(`schedules/${s.id}/`, { enabled: on }))
  // Next runs move as the scheduler runs them and as schedules change.
  const invalidate = useInvalidate()
  useLive({ schedules: ['changed'] }, () => invalidate('schedules'))
  const enabledFilter = getTerm(ls.q, 'enabled')

  const columns: Column<Any>[] = [
    {
      key: 'name',
      header: tr('Schedule'),
      sort: 'name',
      render: (s) => (
        <div>
          <button className="link-btn cell-title" style={{ color: 'var(--ink)', textAlign: 'left' }} onClick={() => setEditing(s)}>
            {s.name}
          </button>
          <div className="small muted">{describeText(s.rrule)}</div>
        </div>
      ),
    },
    {
      key: 'template',
      header: tr('Runs'),
      render: (s) => {
        const t = s.summary_fields?.unified_job_template
        if (!t) return <span className="faint">—</span>
        const type = ujtTypeOf(t.unified_job_type)
        return (
          <div>
            <Link to={templatePath({ id: t.id, type })} className="ellipsis">
              {t.name}
            </Link>
            <div className="small faint">{typeLabel(type)}</div>
          </div>
        )
      },
    },
    { key: 'next', header: tr('Next run'), sort: 'next_run', className: 'nowrap', render: (s) => (s.enabled ? <Upcoming at={s.next_run} /> : <span className="faint">{tr('Paused')}</span>) },
    { key: 'tz', header: tr('Time zone'), className: 'nowrap small', render: (s) => <span className="mono">{s.timezone || tr('UTC')}</span> },
    { key: 'enabled', header: tr('Enabled'), width: 90, render: (s) => <EnabledToggle schedule={s} /> },
    { key: 'menu', header: <span className="sr-only">{tr('Actions')}</span>, className: 'end', width: 48, render: (s) => <ScheduleMenu schedule={s} onEdit={() => setEditing(s)} /> },
  ]

  return (
    <div className="page">
      <PageHead
        title={tr('Schedules')}
        subtitle={tr('Everything that runs on its own. Next runs show in your time zone.')}
        bordered={false}
        actions={
          <button className="btn primary" onClick={() => setEditing('new')}>
            <Icon name="plus" size={14} />
            {tr('New schedule')}
          </button>
        }
      >
        <div className="filterbar">
          <form
            className="filter-input"
            onSubmit={(e) => {
              e.preventDefault()
              ls.setQ(draft)
            }}
          >
            <Icon name="search" size={14} className="faint" />
            <label htmlFor="sched-q" className="sr-only">
              {tr('Filter schedules')}
            </label>
            <input
              id="sched-q"
              data-filter
              value={draft}
              placeholder={tr('name…  try  template:deploy  type:workflow  enabled:false')}
              onChange={(e) => setDraft(e.target.value)}
              onBlur={() => draft !== ls.q && ls.setQ(draft)}
            />
          </form>
          <div className="segmented" role="group" aria-label={tr('Show')}>
            {[
              [null, tr('All')],
              ['true', tr('Enabled')],
              ['false', tr('Paused')],
            ].map(([v, l]) => (
              <label key={l} className={enabledFilter === v ? 'on' : ''}>
                <input
                  type="radio"
                  name="sched-enabled"
                  checked={enabledFilter === v}
                  onChange={() => {
                    const q = setTerm(ls.q, 'enabled', v)
                    setDraft(q)
                    ls.setQ(q)
                  }}
                />
                {l}
              </label>
            ))}
          </div>
        </div>
        {compiled.unknown.length > 0 && (
          <div className="small fail-text">
            {tr('Unknown filter')} {compiled.unknown.map((k) => `${k}:`).join(', ')} {tr('— use')} {Object.keys(KEYS).map((k) => `${k}:`).join(' ')}
          </div>
        )}
      </PageHead>
      <div className="page-body" style={{ paddingTop: 12 }}>
        <BulkBar sel={sel} page={list.data?.results} total={list.data?.count} path="schedules/" params={params} style={{ marginBottom: 12 }}>
          <button className="btn" onClick={() => turn(true)}>
            {tr('Turn on')}
          </button>
          <button className="btn" onClick={() => turn(false)}>
            {tr('Turn off')}
          </button>
          <BulkDelete sel={sel} resource="schedules" noun={['schedule', 'schedules']} name={(s) => s.name} note={tr('What they launch stays; it no longer runs on these schedules.')} />
        </BulkBar>
        <DataTable
          label={tr('Schedules')}
          rows={list.data?.results}
          columns={columns}
          selected={sel.selected}
          onSelect={sel.onSelect}
          columnsKey="schedules"
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(s) => setEditing(s)}
          minWidth={820}
          empty={
            ls.q ? (
              <Empty title={tr('No schedule matches')} action={<button className="btn" onClick={() => (setDraft(''), ls.setQ(''))}>{tr('Clear filter')}</button>} />
            ) : (
              <Empty title={tr('Nothing is scheduled')} action={<button className="btn" onClick={() => setEditing('new')}>{tr('New schedule')}</button>}>
                {tr('Schedules launch templates, project syncs and inventory syncs at set times.')}
              </Empty>
            )
          }
          footer={
            list.data && list.data.count > 0 ? (
              <div className="table-foot">
                <span>
                  {list.data.count} {tr('schedule')}{list.data.count === 1 ? '' : 's'}
                </span>
                <span className="hint">{tr('j/k move · enter edit · / filter')}</span>
                <Pager count={list.data.count} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
              </div>
            ) : undefined
          }
        />
      </div>
      {editing && <ScheduleDialog schedule={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
    </div>
  )
}
