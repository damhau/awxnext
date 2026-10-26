import { useState } from 'react'
import { Link } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Any } from '../../api/types'
import { useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, type Column } from '../../components/table'
import { Ago, Empty, PageHead, Status, useTick } from '../../components/ui'
import { absolute, duration, parseDate } from '../../lib/format'
import './automation.css'

/** Time left before an approval times out, as text and a draining bar. */
export function Countdown({ approval }: { approval: Any }) {
  useTick(15_000)
  const exp = parseDate(approval.approval_expiration)
  const created = parseDate(approval.created)
  if (!exp || !created) return <span className="faint small">No time limit</span>
  const total = exp.getTime() - created.getTime()
  const left = exp.getTime() - Date.now()
  const frac = total > 0 ? Math.max(0, Math.min(1, left / total)) : 0
  const late = left <= 0
  return (
    <span className={`countdown ${frac < 0.2 ? 'late' : ''}`} title={`Times out ${absolute(approval.approval_expiration)}`}>
      <span className="small">{late ? 'Timing out…' : `${duration(left / 1000)} left`}</span>
      <span className="bar" aria-hidden="true">
        <div style={{ width: `${frac * 100}%` }} />
      </span>
    </span>
  )
}

/** Approve or deny one approval, with confirmation. */
export function useDecide() {
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  return async (approvals: Any[], action: 'approve' | 'deny') => {
    if (!approvals.length) return false
    const one = approvals.length === 1
    const what = one ? `“${approvals[0].name}”` : `${approvals.length} approvals`
    const ok = await confirm({
      title: action === 'approve' ? `Approve ${what}?` : `Deny ${what}?`,
      body: (
        <p style={{ margin: 0 }}>
          {action === 'approve'
            ? 'The workflow continues with the steps that follow approval.'
            : 'The workflow takes its failure path; without one it stops as failed.'}
        </p>
      ),
      confirm: action === 'approve' ? 'Approve' : 'Deny',
      danger: action === 'deny',
    })
    if (!ok) return false
    const results = await Promise.allSettled(approvals.map((a) => api.post(`workflow_approvals/${a.id}/${action}/`)))
    const failed = results.filter((r) => r.status === 'rejected') as PromiseRejectedResult[]
    invalidate('workflow_approvals', 'workflow_jobs', 'unified_jobs')
    if (failed.length) toast(`${failed.length} of ${approvals.length} could not be ${action === 'approve' ? 'approved' : 'denied'}: ${String(failed[0].reason?.message ?? failed[0].reason)}`, 'error')
    else toast(`${one ? approvals[0].name : `${approvals.length} approvals`} ${action === 'approve' ? 'approved' : 'denied'}`)
    return true
  }
}

function decision(a: Any): string {
  if (a.status === 'successful') return 'Approved'
  if (a.timed_out) return 'Timed out'
  if (a.status === 'failed') return 'Denied'
  if (a.status === 'canceled') return 'Canceled'
  return a.status
}

export function ApprovalsPage() {
  const [selected, setSelected] = useState<Set<number | string>>(new Set())
  const [page, setPage] = useState(1)
  const pending = useList<Any>('workflow_approvals/', { status: 'pending', order_by: 'created', page_size: 100 })
  const decided = useList<Any>('workflow_approvals/', { not__status: 'pending', order_by: '-finished', page, page_size: 20 })
  const decide = useDecide()
  const rows = pending.data?.results ?? []
  const actionable = rows.filter((a) => a.can_approve_or_deny)
  const chosen = rows.filter((a) => selected.has(a.id) && a.can_approve_or_deny)

  const run = async (list: Any[], action: 'approve' | 'deny') => {
    if (await decide(list, action)) setSelected(new Set())
  }

  const workflowCell = (a: Any) => {
    const wf = a.summary_fields?.source_workflow_job
    return wf ? (
      <Link to={`/jobs/workflow_job/${wf.id}`} className="ellipsis">
        {wf.name} <span className="mono faint">#{wf.id}</span>
      </Link>
    ) : (
      <span className="faint">—</span>
    )
  }

  const pendingCols: Column<Any>[] = [
    {
      key: 'name',
      header: 'Approval',
      render: (a) => (
        <div>
          <div className="cell-title">{a.name}</div>
          {a.description && <div className="small muted">{a.description}</div>}
        </div>
      ),
    },
    { key: 'wf', header: 'Workflow', render: workflowCell },
    { key: 'asked', header: 'Requested', className: 'nowrap', render: (a) => <Ago at={a.created} /> },
    { key: 'left', header: 'Time left', render: (a) => <Countdown approval={a} /> },
    {
      key: 'act',
      header: <span className="sr-only">Decision</span>,
      className: 'end',
      render: (a) =>
        a.can_approve_or_deny ? (
          <span className="approval-actions">
            <button className="btn sm" onClick={() => run([a], 'deny')}>
              Deny
            </button>
            <button className="btn sm primary" onClick={() => run([a], 'approve')}>
              Approve
            </button>
          </span>
        ) : (
          <span className="small faint">Not yours to decide</span>
        ),
    },
  ]

  const decidedCols: Column<Any>[] = [
    { key: 'st', header: 'Decision', width: 130, render: (a) => <Status status={a.status}>{decision(a)}</Status> },
    { key: 'name', header: 'Approval', render: (a) => <span className="cell-title">{a.name}</span> },
    { key: 'wf', header: 'Workflow', render: workflowCell },
    {
      key: 'by',
      header: 'Decided by',
      render: (a) => {
        const by = a.summary_fields?.approved_or_denied_by
        if (by) return <Link to={`/users/${by.id}`}>{by.username}</Link>
        return <span className="faint">{a.timed_out ? 'timeout' : '—'}</span>
      },
    },
    { key: 'when', header: 'When', className: 'nowrap', render: (a) => <Ago at={a.finished ?? a.modified} /> },
  ]

  return (
    <div className="page">
      <PageHead title="Approvals" subtitle="Workflows paused for a human decision. Approving continues the workflow; denying takes its failure path." bordered={false} />
      <div className="page-body">
        <div className="section-title">
          <h2>Waiting for a decision</h2>
          <span className="small muted">{rows.length ? `${rows.length} pending, oldest first` : ''}</span>
        </div>
        {chosen.length > 0 && (
          <div className="bulkbar" role="region" aria-label="Bulk actions">
            <span className="n">{chosen.length} selected</span>
            <button className="btn" onClick={() => run(chosen, 'approve')}>
              Approve
            </button>
            <button className="btn warn" onClick={() => run(chosen, 'deny')}>
              Deny
            </button>
            <button className="btn clear" onClick={() => setSelected(new Set())}>
              Clear
            </button>
          </div>
        )}
        <DataTable
          label="Pending approvals"
          rows={rows}
          columns={pendingCols}
          loading={pending.isPending}
          error={pending.error}
          selected={actionable.length ? selected : undefined}
          onSelect={actionable.length ? setSelected : undefined}
          keys={{ a: (a) => a.can_approve_or_deny && run([a], 'approve'), d: (a) => a.can_approve_or_deny && run([a], 'deny') }}
          empty={<Empty title="Nothing waits for you">When a workflow reaches an approval step, it shows up here.</Empty>}
          footer={
            rows.length > 0 ? (
              <div className="table-foot">
                <span>
                  {actionable.length} of {rows.length} can be decided by you
                </span>
                <span className="hint">j/k move · x select · a approve · d deny</span>
              </div>
            ) : undefined
          }
        />

        <div className="section-title" style={{ marginTop: 12 }}>
          <h2>Decided</h2>
        </div>
        <DataTable
          label="Decided approvals"
          rows={decided.data?.results}
          columns={decidedCols}
          loading={decided.isPending}
          error={decided.error}
          keyboard={false}
          empty={<Empty title="No decisions yet" />}
          footer={
            decided.data && decided.data.count > 20 ? (
              <div className="table-foot">
                <span>{decided.data.count} decided</span>
                <Pager count={decided.data.count} page={page} pageSize={20} onPage={setPage} />
              </div>
            ) : undefined
          }
        />
      </div>
    </div>
  )
}
