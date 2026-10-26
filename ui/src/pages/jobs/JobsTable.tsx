import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Job } from '../../api/types'
import { useOrg } from '../../app/context'
import { FilterBar } from '../../components/filterbar'
import { Icon } from '../../components/icons'
import { useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Elapsed, Empty, Status } from '../../components/ui'
import { active, jobApi, jobPath, jobsNewestFirst, typeLabel, userName } from '../../lib/format'
import { relaunchJob } from '../../lib/jobs'
import { compile, statusValue, type KeyTable } from '../../lib/query'

export const jobKeys: KeyTable = {
  status: { field: 'status', map: statusValue, help: 'failed' },
  type: {
    field: 'type',
    map: (v) =>
      ({ playbook: 'job', job: 'job', workflow: 'workflow_job', sync: 'project_update', project: 'project_update', inventory: 'inventory_update', command: 'ad_hoc_command', system: 'system_job' })[v] ?? v,
    help: 'workflow',
  },
  template: { field: 'name', help: 'deploy*' },
  by: { field: 'created_by__username', help: 'user' },
  node: { field: 'execution_node', help: 'host' },
  launch: { field: 'launch_type', help: 'scheduled' },
  id: { field: 'id', help: '42' },
}

/**
 * A page of jobs with live status, cancel and relaunch. `fixed` narrows the
 * list (e.g. one template's jobs) and is not shown in the filter.
 */
export function JobsTable({ fixed = {}, filter = true, compact = false }: { fixed?: Record<string, string | number>; filter?: boolean; compact?: boolean }) {
  const ls = useListState({ order: jobsNewestFirst })
  const nav = useNavigate()
  const { scope } = useOrg()
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const [selected, setSelected] = useState<Set<number | string>>(new Set())
  const compiled = compile(ls.q, jobKeys)
  const params = { not__type: 'workflow_approval', ...scope, ...fixed, ...compiled.params, order_by: ls.order, page: ls.page, page_size: ls.pageSize }
  const list = useList<Job>('unified_jobs/', params, { refetchInterval: (q) => (q.state.data?.results.some((j) => active(j.status)) ? 5000 : false) })
  const rows = list.data?.results ?? []
  const open = (j: Job) => nav(jobPath(j))

  const relaunch = async (j: Job) => {
    try {
      nav(jobPath(await relaunchJob(j)))
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  const cancel = async (js: Job[]) => {
    const running = js.filter((j) => active(j.status))
    if (!running.length) return
    if (!(await confirm({ title: running.length === 1 ? `Cancel #${running[0].id}?` : `Cancel ${running.length} jobs?`, body: 'Running playbooks are stopped; hosts may be left part-way through a task.', confirm: 'Cancel jobs', danger: true }))) return
    await Promise.allSettled(running.map((j) => api.post(`${jobApi(j.type, j.id)}cancel/`, {})))
    invalidate('unified_jobs')
    setSelected(new Set())
  }

  const columns: Column<Job>[] = [
    { key: 'status', header: 'Status', sort: 'status', className: 'nowrap', render: (j) => <Status status={j.status} /> },
    {
      key: 'name',
      header: 'Job',
      sort: 'name',
      render: (j) => (
        <>
          <Link to={jobPath(j)} className="cell-title">
            {j.name}
          </Link>
          <div className="cell-sub">
            #{j.id} · <span style={{ fontFamily: 'var(--sans)' }}>{typeLabel(j.type)}</span>
            {j.job_explanation && <span className="fail-text" style={{ fontFamily: 'var(--sans)' }}> · {j.job_explanation.slice(0, 80)}</span>}
          </div>
        </>
      ),
    },
    {
      key: 'by',
      header: 'Started by',
      className: 'nowrap',
      render: (j) => (j.launch_type === 'scheduled' ? <span className="muted">schedule</span> : j.launch_type === 'webhook' ? <span className="muted">webhook</span> : j.launch_type === 'workflow' ? <span className="muted">workflow</span> : userName(j.summary_fields.created_by)),
    },
    ...(compact ? [] : [{ key: 'node', header: 'Node', className: 'nowrap mono small', render: (j: Job) => j.execution_node || '—' }]),
    { key: 'started', header: 'Started', sort: 'started', className: 'nowrap small', render: (j) => <Ago at={j.started ?? j.created} /> },
    { key: 'dur', header: 'Duration', sort: 'elapsed', className: 'nowrap', render: (j) => (j.started ? <Elapsed job={j} /> : <span className="faint">—</span>) },
    {
      key: 'act',
      header: <span className="sr-only">Actions</span>,
      className: 'end nowrap',
      render: (j) =>
        active(j.status) ? (
          <button className="btn sm" onClick={() => cancel([j])}>
            <Icon name="stop" size={10} />
            Cancel
          </button>
        ) : j.type !== 'workflow_approval' ? (
          <button className="btn sm" onClick={() => relaunch(j)} title="Relaunch with the same values">
            <Icon name="refresh" size={12} />
            Relaunch
          </button>
        ) : null,
    },
  ]

  return (
    <div className="stack" style={{ gap: 10 }}>
      {filter && <FilterBar value={ls.q} onChange={ls.setQ} keys={jobKeys} placeholder="name…  try  status:failed  type:workflow  by:priya" />}
      {selected.size > 0 && (
        <div className="bulkbar" role="region" aria-label="Bulk actions">
          <span className="n">{selected.size} selected</span>
          <button className="btn warn" onClick={() => cancel(rows.filter((j) => selected.has(j.id)))}>
            Cancel running
          </button>
          <button
            className="btn"
            onClick={async () => {
              const js = rows.filter((j) => selected.has(j.id) && !active(j.status))
              if (!(await confirm({ title: `Delete ${js.length} finished jobs?`, body: 'Their output and events are removed.', confirm: 'Delete', danger: true }))) return
              await Promise.allSettled(js.map((j) => api.del(jobApi(j.type, j.id))))
              invalidate('unified_jobs')
              setSelected(new Set())
            }}
          >
            Delete
          </button>
          <button className="btn clear" onClick={() => setSelected(new Set())}>
            Clear
          </button>
        </div>
      )}
      <DataTable
        label="Jobs"
        rows={rows}
        columns={columns}
        loading={list.isPending}
        error={list.error}
        selected={selected}
        onSelect={setSelected}
        onOpen={open}
        order={ls.order}
        onOrder={ls.setOrder}
        keys={{ r: relaunch }}
        minWidth={compact ? 640 : 860}
        empty={<Empty title={ls.q ? 'No jobs match' : 'No jobs yet'}>{ls.q ? 'Remove a filter to see more.' : 'Jobs appear here when templates run.'}</Empty>}
        footer={
          <div className="table-foot">
            <span>{list.data ? `${list.data.count} job${list.data.count === 1 ? '' : 's'}` : ''}</span>
            <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            <span className="mono">j/k move · ↵ open · r relaunch</span>
          </div>
        }
      />
    </div>
  )
}
