import { useState } from 'react'
import { Link } from 'react-router'
import { api } from '../api/client'
import { useInvalidate, useList } from '../api/hooks'
import type { Any, Ref } from '../api/types'
import { attachedLabel, attachedPath, launchVerdict, modeLabel, outcomeView, type CheckDecision } from '../lib/launchPolicies'
import { jobPath } from '../lib/format'
import { asIs, tr, trn } from '../lib/i18n'
import { Lookup } from './form'
import { useToast } from './overlay'
import { DataTable, Pager } from './table'
import { Ago, Cell, Empty, ErrorNote, Panel } from './ui'

export function PolicyOutcome({ outcome }: { outcome: string }) {
  const v = outcomeView(outcome)
  return (
    <span className="status">
      <Cell state={v.state} />
      <span>{v.label}</span>
    </span>
  )
}

export function PolicyMode({ mode }: { mode: string }) {
  return <span className={`chip ${mode === 'enforce' ? 'fail-text' : ''}`}>{modeLabel(mode)}</span>
}

/** What a launch check says before Launch: the policies that refuse it,
 * and those in shadow mode that would. Nothing when every policy allows it. */
export function PolicyWarning({ decisions, error }: { decisions?: CheckDecision[]; error?: unknown }) {
  const { refused, shadow } = launchVerdict(decisions)
  if (error) return <ErrorNote error={error} />
  if (!refused.length && !shadow.length) return null
  return (
    <div className="stack" style={{ gap: 8 }} role="status" aria-live="polite">
      {refused.length > 0 && (
        <div className="notice warn">
          <strong>{trn(refused.length, 'A launch policy refuses this launch.', '{n} launch policies refuse this launch.')}</strong>
          <ul style={{ margin: '6px 0 0', paddingLeft: 18 }}>
            {refused.map((d) => (
              <li key={d.id}>
                <Link to={`/launch-policies/${d.id}`}>{d.name}</Link>
                {d.message ? asIs(`: ${d.message}`) : ''}
                {d.error && <div className="small mono">{tr('Its condition failed: {error}', { error: d.error })}</div>}
              </li>
            ))}
          </ul>
        </div>
      )}
      {shadow.length > 0 && (
        <div className="notice">
          {trn(shadow.length, 'A policy in shadow mode would refuse it; it only records that:', '{n} policies in shadow mode would refuse it; they only record that:')}{' '}
          {shadow.map((d, i) => (
            <span key={d.id}>
              {i > 0 && ', '}
              <Link to={`/launch-policies/${d.id}`}>{d.name}</Link>
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

/** The decisions of a policy (path launch_policies/N/decisions/) or of a
 * job (jobs/N/launch_policy_decisions/), the latest first. */
export function PolicyDecisions({ path, of }: { path: string; of: 'policy' | 'job' }) {
  const [page, setPage] = useState(1)
  const [outcome, setOutcome] = useState('')
  const list = useList<Any>(path, { page, page_size: 25, order_by: '-id', ...(outcome ? { outcome } : {}) })
  const columns =
    of === 'job'
      ? [
          { key: 'policy', header: tr('Policy'), render: (d: Any) => (d.launch_policy ? <Link to={`/launch-policies/${d.launch_policy}`}>{d.policy_name}</Link> : <span>{d.policy_name}</span>) },
          { key: 'mode', header: tr('Mode'), render: (d: Any) => modeLabel(d.mode) },
          { key: 'outcome', header: tr('Outcome'), render: (d: Any) => <PolicyOutcome outcome={d.outcome} /> },
          { key: 'why', header: tr('Message'), render: (d: Any) => <Why d={d} /> },
        ]
      : [
          { key: 'when', header: tr('When'), render: (d: Any) => <Ago at={d.created} /> },
          { key: 'outcome', header: tr('Outcome'), render: (d: Any) => <PolicyOutcome outcome={d.outcome} /> },
          {
            key: 'launch',
            header: tr('Launch'),
            render: (d: Any) => (
              <div>
                {d.job ? <Link to={jobPath({ id: d.job, type: d.job_type })}>{d.template_name || asIs(`#${d.job}`)}</Link> : <span>{d.template_name || tr('Ad hoc command')}</span>}
                <div className="cell-sub">{d.job ? d.launch_type : tr('Refused at its caller: no job')}</div>
              </div>
            ),
          },
          { key: 'user', header: tr('User'), render: (d: Any) => d.summary_fields?.user?.username ?? '—' },
          { key: 'why', header: tr('Message'), render: (d: Any) => <Why d={d} /> },
        ]
  return (
    <div className="stack" style={{ gap: 12 }}>
      {of === 'policy' && (
        <div className="row" style={{ gap: 6 }} role="group" aria-label={tr('Outcome')}>
          {[
            ['', tr('All')],
            ['refused', tr('Refused')],
            ['would_refuse', tr('Would have refused')],
            ['allowed', tr('Allowed')],
          ].map(([v, l]) => (
            <button
              key={v}
              type="button"
              className={`btn sm ${outcome === v ? 'primary' : ''}`}
              aria-pressed={outcome === v}
              onClick={() => {
                setOutcome(v)
                setPage(1)
              }}
            >
              {l}
            </button>
          ))}
        </div>
      )}
      <DataTable
        rows={list.data?.results}
        columns={columns}
        loading={list.isPending}
        error={list.error}
        label={tr('Launch policy decisions')}
        empty={<Empty title={tr('No decisions')}>{of === 'job' ? tr('No launch policy checked this launch.') : tr('The policy decides at each launch it checks.')}</Empty>}
      />
      <Pager count={list.data?.count ?? 0} page={page} pageSize={25} onPage={setPage} />
    </div>
  )
}

function Why({ d }: { d: Any }) {
  if (d.outcome === 'allowed') return <span className="faint">—</span>
  return (
    <div className="small">
      {d.message || <span className="faint">{tr('No message')}</span>}
      {d.error && <div className="mono fail-text">{tr('Its condition failed: {error}', { error: d.error })}</div>}
    </div>
  )
}

/** The decisions of a job's launch, on its page: nothing when no policy checked it. */
export function JobPolicyDecisions({ path }: { path: string }) {
  const list = useList<Any>(path, { page_size: 1 })
  if (!list.data?.count) return null
  return (
    <Panel title={tr('Launch policies')}>
      <PolicyDecisions path={path} of="job" />
    </Panel>
  )
}

/** The launch policies attached to an organization, inventory or template,
 * and the ones of its organization to attach. */
export function LaunchPoliciesTab({ resource, id, org, editable }: { resource: string; id: number; org?: number | null; editable: boolean }) {
  const path = `${resource}/${id}/launch_policies/`
  const list = useList<Any>(path, { page_size: 200, order_by: 'name' })
  const global = useList<Any>('launch_policies/', { organization__isnull: 'true', page_size: 1 })
  const invalidate = useInvalidate()
  const toast = useToast()
  const [pick, setPick] = useState<Ref | null>(null)
  const [busy, setBusy] = useState(false)
  const change = async (pid: number, attach: boolean) => {
    setBusy(true)
    try {
      await api.post(path, attach ? { id: pid } : { id: pid, disassociate: true })
      invalidate('launch_policies', path)
      setPick(null)
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('The change failed'), 'error')
    } finally {
      setBusy(false)
    }
  }
  const attached = new Set((list.data?.results ?? []).map((p) => p.id))
  return (
    <div className="stack" style={{ gap: 16 }}>
      <p className="small muted" style={{ margin: 0 }}>
        {resource === 'organizations'
          ? tr('These policies check every launch of the organization’s templates and on its inventories.')
          : tr('These policies check its launches, with those of its organization.')}{' '}
        {global.data?.count
          ? trn(global.data.count, 'A global policy checks every launch too.', '{n} global policies check every launch too.')
          : ''}{' '}
        <Link to="/launch-policies">{tr('All launch policies')}</Link>
      </p>
      <DataTable
        rows={list.data?.results}
        loading={list.isPending}
        error={list.error}
        label={tr('Launch policies')}
        columns={[
          {
            key: 'name',
            header: tr('Policy'),
            render: (p) => (
              <div>
                <Link to={`/launch-policies/${p.id}`} className="cell-title">
                  {p.name}
                </Link>
                <div className="cell-sub mono" style={{ maxWidth: 520, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {p.condition}
                </div>
              </div>
            ),
          },
          { key: 'mode', header: tr('Mode'), render: (p) => <PolicyMode mode={p.mode} /> },
          {
            key: 'detach',
            header: '',
            label: tr('Detach'),
            render: (p) =>
              editable && p.summary_fields?.user_capabilities?.edit ? (
                <button type="button" className="btn sm ghost" disabled={busy} onClick={() => change(p.id, false)}>
                  {tr('Detach')}
                </button>
              ) : null,
          },
        ]}
        empty={<Empty title={tr('No policy attached')}>{tr('A policy attached here refuses the launches its condition matches, or records them in shadow mode.')}</Empty>}
      />
      {editable && (
        <form
          className="row"
          style={{ gap: 8, alignItems: 'flex-end' }}
          onSubmit={(e) => {
            e.preventDefault()
            if (pick && !attached.has(pick.id)) change(pick.id, true)
          }}
        >
          <Lookup
            label={tr('Attach a policy')}
            resource="launch_policies/"
            params={org ? { organization: org } : { organization__isnull: 'false' }}
            value={pick}
            onChange={setPick}
          />
          <button className="btn" disabled={!pick || busy || attached.has(pick.id)}>
            {tr('Attach')}
          </button>
        </form>
      )}
    </div>
  )
}

/** The objects a policy is attached to, as links. */
export function AttachedList({ attached }: { attached: { type: string; id: number; name: string }[] }) {
  if (!attached.length) return <span className="faint">{tr('Nothing yet: attach it from an organization, inventory or template.')}</span>
  return (
    <ul style={{ margin: 0, paddingLeft: 18 }}>
      {attached.map((a) => (
        <li key={`${a.type}-${a.id}`}>
          <span className="muted small">{attachedLabel(a.type)}</span> <Link to={attachedPath(a)}>{a.name}</Link>
        </li>
      ))}
    </ul>
  )
}
