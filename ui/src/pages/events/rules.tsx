import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any } from '../../api/types'
import { useOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Icon } from '../../components/icons'
import { useConfirm, useToast } from '../../components/overlay'
import { UsedByPanel } from '../../components/UsedBy'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { duration, userName } from '../../lib/format'
import { actionSummary, MODE_LABELS, outcomeView } from '../../lib/eventRules'
import { tr, trn } from '../../lib/i18n'
import { ListPage, useDelete } from '../admin/common'
import { Backtest } from './backtest'
import { DecisionList } from './decisions'
import { RuleEditor } from './RuleEditor'

function ModeBadge({ mode }: { mode: string }) {
  return <span className={`badge mode-${mode}`}>{MODE_LABELS[mode] ?? mode}</span>
}

function State({ r }: { r: Any }) {
  if (r.stopped_reason)
    return (
      <span className="status" title={r.stopped_reason}>
        <Cell state="failed" />
        {tr('Stopped')}
      </span>
    )
  if (!r.enabled)
    return (
      <span className="status">
        <Cell state="empty" />
        {tr('Disabled')}
      </span>
    )
  return (
    <span className="status">
      <Cell state="ok" />
      {tr('Enabled')}
    </span>
  )
}

export function RuleList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Any & { id: number }>
      title={tr('Event rules')}
      subtitle={tr('Conditions on the events of streams, and what to do when they hold: launch a template, notify, or post an event.')}
      path="event_rules/"
      params={scope}
      placeholder={tr('Filter event rules…')}
      actions={
        <Link to="/event-rules/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New rule')}
        </Link>
      }
      onOpen={(r) => nav(`/event-rules/${r.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Rule'),
          sort: 'name',
          render: (r) => (
            <div>
              <Link to={`/event-rules/${r.id}`} className="cell-title">
                {r.name}
              </Link>
              <div className="cell-sub">{actionSummary(r)}</div>
            </div>
          ),
        },
        {
          key: 'streams',
          header: tr('Streams'),
          render: (r) => (
            <span className="small">
              {((r.summary_fields.streams ?? []) as Any[]).map((s, i) => (
                <span key={s.id}>
                  {i > 0 && ', '}
                  <Link to={`/event-streams/${s.id}`}>{s.name}</Link>
                </span>
              ))}
            </span>
          ),
        },
        { key: 'mode', header: tr('Mode'), sort: 'mode', render: (r) => <ModeBadge mode={r.mode} /> },
        { key: 'state', header: tr('State'), render: (r) => <State r={r} /> },
        { key: 'fired', header: tr('Last fired'), sort: 'last_fired', render: (r) => (r.last_fired ? <Ago at={r.last_fired} /> : <span className="faint small">{tr('Never')}</span>) },
      ]}
      empty={<Empty title={tr('No event rules yet')}>{tr('Write a rule against a stream’s events. It starts in shadow mode: it records what it would do, and does nothing.')}</Empty>}
    />
  )
}

export function RuleNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Event rules'), to: '/event-rules' }, { label: tr('New') }]} title={tr('New event rule')} />
      <div className="page-body">
        <RuleEditor />
      </div>
    </div>
  )
}

export function RuleDetail() {
  const id = Number(useParams().id)
  const rule = useObject<Any>(`event_rules/${id}/`)
  const del = useDelete()
  const toast = useToast()
  const invalidate = useInvalidate()
  if (rule.error) return <div className="page-body"><ErrorNote error={rule.error} /></div>
  if (!rule.data) return <Loading />
  const r = rule.data
  const caps = r.summary_fields.user_capabilities ?? {}
  const base = `/event-rules/${id}`
  const toggle = async () => {
    try {
      await api.patch(`event_rules/${id}/`, { enabled: !r.enabled })
      invalidate('event_rules')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Event rules'), to: '/event-rules' }, { label: r.name }]}
        title={r.name}
        badge={
          <>
            <ModeBadge mode={r.mode} />
            <State r={r} />
          </>
        }
        subtitle={r.description || actionSummary(r)}
        actions={
          <>
            {caps.edit && (
              <button className="btn" onClick={toggle}>
                {r.enabled ? tr('Disable') : tr('Enable')}
              </button>
            )}
            {caps.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path: `event_rules/${id}/`, name: r.name, what: tr('event rule'), then: '/event-rules' })}>
                {tr('Delete')}
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              {tr('Overview')}
            </Tab>
            <Tab to={`${base}/decisions`}>{tr('Decisions')}</Tab>
            <Tab to={`${base}/backtest`}>{tr('Backtest')}</Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
          </>
        }
      />
      <div className="page-body">
        {r.stopped_reason && <div className="notice warn" style={{ marginBottom: 16 }}>{tr('The rule stopped: {reason} Enable it again once fixed.', { reason: r.stopped_reason })}</div>}
        <Routes>
          <Route index element={<Overview r={r} />} />
          <Route
            path="decisions"
            element={
              <DecisionList
                path={`event_rules/${id}/decisions/`}
                by="rule"
                filter={r.filter}
                empty={<Empty title={tr('No decisions yet')}>{tr('The rule decides on each event of its streams received while it is enabled.')}</Empty>}
              />
            }
          />
          <Route path="backtest" element={<Backtest path={`event_rules/${id}/backtest/`} filter={r.filter} />} />
          <Route path="edit" element={<RuleEditor key={r.modified} rule={r} />} />
          <Route path="access" element={<AccessTab resource="event_rules" id={id} name={r.name} />} />
        </Routes>
      </div>
    </div>
  )
}

const THROTTLES: Record<string, string> = { once_within: tr('Once within'), once_after: tr('Once after') }

function Overview({ r }: { r: Any }) {
  const sf = r.summary_fields
  const vars = Object.entries((r.extra_vars ?? {}) as Record<string, string>)
  const target =
    r.action === 'launch' && sf.unified_job_template ? (
      <Link to={sf.unified_job_template.unified_job_type === 'workflow_job' || sf.unified_job_template.type === 'workflow_job_template' ? `/templates/workflow/${r.unified_job_template}` : `/templates/job/${r.unified_job_template}`}>{sf.unified_job_template.name}</Link>
    ) : r.action === 'notify' && sf.notification_template ? (
      <Link to={`/notifications/${r.notification_template}`}>{sf.notification_template.name}</Link>
    ) : r.action === 'post_event' && sf.target_stream ? (
      <Link to={`/event-streams/${r.target_stream}`}>{sf.target_stream.name}</Link>
    ) : (
      actionSummary(r)
    )
  return (
    <div className="split">
      <div className="wide stack" style={{ gap: 16 }}>
        <Panel title={tr('Rule')}>
          <KV
            items={[
              [
                tr('Streams'),
                <span>
                  {((sf.streams ?? []) as Any[]).map((s, i) => (
                    <span key={s.id}>
                      {i > 0 && ', '}
                      <Link to={`/event-streams/${s.id}`}>{s.name}</Link>
                    </span>
                  ))}
                </span>,
              ],
              [tr('Filter'), <pre className="code">{r.filter}</pre>],
              [tr('Key'), r.key ? <span className="mono">{r.key}</span> : <span className="faint">{tr('One key for all events')}</span>],
              [tr('Throttle'), r.throttle ? tr('{kind}, with a window of {window}', { kind: THROTTLES[r.throttle] ?? r.throttle, window: duration(r.throttle_window) }) : tr('None: acts on every event')],
              [tr('Action'), target],
              [tr('Variables'), vars.length ? <pre className="code">{vars.map(([k, v]) => `${k}: ${v}`).join('\n')}</pre> : false],
              [tr('Limit'), r.action === 'launch' && r.limit ? <span className="mono">{r.limit}</span> : false],
              [tr('Message'), r.action === 'notify' && r.message ? <span className="mono">{r.message}</span> : false],
              [tr('Event type'), r.action === 'post_event' ? <span className="mono">{r.event_type}</span> : false],
              [tr('Event data'), r.action === 'post_event' && r.event_data ? <span className="mono">{r.event_data}</span> : false],
              [tr('Owner'), sf.owner ? <span title={tr('The rule runs with the rights of the user who last saved it.')}>{userName(sf.owner)}</span> : <span className="faint">—</span>],
              [tr('Last fired'), r.last_fired ? <Ago at={r.last_fired} /> : <span className="faint">{tr('Never')}</span>],
            ]}
          />
        </Panel>
        <Panel title={tr('Guardrails')}>
          <KV
            items={[
              [tr('Circuit breaker'), r.breaker_firings ? trn(r.breaker_firings, 'Trips after {n} firing of a key within {window}', 'Trips after {n} firings of a key within {window}', { window: duration(r.breaker_window) }) : <span className="faint">{tr('None')}</span>],
              [tr('Maintenance windows'), r.window_action === 'approval' ? tr('Ask for approval') : tr('Hold the action back')],
              [tr('Approvals wait'), r.mode === 'approval' || r.window_action === 'approval' ? (r.approval_timeout ? duration(r.approval_timeout) : tr('Until someone decides')) : false],
              [tr('Concurrent runs'), r.action === 'launch' ? (r.max_concurrent_runs || <span className="faint">{tr('No limit')}</span>) : false],
              [tr('Host lock'), r.action === 'launch' ? (r.host_lock ? tr('On') : tr('Off')) : false],
              [tr('Group share'), r.action === 'launch' ? (r.max_group_share ? tr('{n} %', { n: r.max_group_share }) : <span className="faint">{tr('No limit')}</span>) : false],
            ]}
          />
        </Panel>
        <Breakers r={r} />
      </div>
      <div className="narrow stack" style={{ gap: 16 }}>
        <RecentDecisions id={r.id} />
        <Panel title={tr('Success rate')}>
          <p className="small faint" style={{ margin: 0 }}>
            {tr('Not measured yet.')}
          </p>
        </Panel>
        <UsedByPanel resource="event_rules" id={r.id} empty={tr('No maintenance window lists this rule.')} />
      </div>
    </div>
  )
}

/** The last decisions as cells, oldest on the left. */
function RecentDecisions({ id }: { id: number }) {
  const list = useList<Any>(`event_rules/${id}/decisions/`, { order_by: '-received,-sequence,-id', page_size: 40 }, { refetchInterval: 10_000 })
  const rows = [...(list.data?.results ?? [])].reverse()
  const by: Record<string, number> = {}
  for (const d of rows) by[outcomeView(d.outcome).kind] = (by[outcomeView(d.outcome).kind] ?? 0) + 1
  return (
    <Panel title={tr('Recent decisions')} actions={<Link className="small" to={`/event-rules/${id}/decisions`}>{tr('All')}</Link>}>
      {list.isPending ? (
        <Loading rows={1} />
      ) : !rows.length ? (
        <p className="small faint" style={{ margin: 0 }}>
          {tr('None yet.')}
        </p>
      ) : (
        <div className="stack" style={{ gap: 8 }}>
          <div className="runbars" role="group" aria-label={tr('Recent decisions')}>
            {rows.map((d) => (
              <Cell key={d.id} state={outcomeView(d.outcome).cell} size="bar" to={`/event-streams/${d.stream}/events/${d.sequence}`} label={tr('#{seq}: {outcome}', { seq: d.sequence, outcome: outcomeView(d.outcome).label })} />
            ))}
          </div>
          <div className="small muted">
            {[
              by.acted && tr('{n} acted', { n: by.acted }),
              by.would && tr('{n} would have acted', { n: by.would }),
              by.waits && tr('{n} waiting', { n: by.waits }),
              by.held && tr('{n} held back', { n: by.held }),
              by.failed && tr('{n} failed', { n: by.failed }),
              by.missed && tr('{n} not matched', { n: by.missed }),
            ]
              .filter(Boolean)
              .join(' · ')}
          </div>
        </div>
      )}
    </Panel>
  )
}

function Breakers({ r }: { r: Any }) {
  const list = useList<Any>(`event_rules/${r.id}/breakers/`, { order_by: '-id', page_size: 50 }, { refetchInterval: 15_000 })
  const toast = useToast()
  const confirm = useConfirm()
  const invalidate = useInvalidate()
  const [busy, setBusy] = useState(false)
  const caps = r.summary_fields.user_capabilities ?? {}
  const rows = list.data?.results ?? []
  const tripped = rows.filter((b) => b.tripped_at)
  const reset = async (key?: string) => {
    const ok = await confirm({
      title: key === undefined ? tr('Reset every tripped breaker?') : tr('Reset the breaker of {key}?', { key: key || tr('all events') }),
      body: <p style={{ margin: 0 }}>{tr('The rule acts on the key’s events again, from the next one.')}</p>,
      confirm: tr('Reset'),
    })
    if (!ok) return
    setBusy(true)
    try {
      const res = await api.post<{ reset: number }>(`event_rules/${r.id}/reset_breaker/`, key === undefined ? {} : { key })
      toast(trn(res.reset, '{n} breaker reset', '{n} breakers reset'))
      invalidate(`event_rules/${r.id}/breakers/`)
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Panel
      title={tr('Circuit breakers')}
      actions={
        caps.edit && tripped.length > 1 ? (
          <button className="btn sm" disabled={busy} onClick={() => reset()}>
            {tr('Reset all')}
          </button>
        ) : null
      }
    >
      {list.error ? (
        <ErrorNote error={list.error} />
      ) : !rows.length ? (
        <p className="small faint" style={{ margin: 0 }}>
          {r.breaker_firings ? tr('None has tripped.') : tr('The rule has no circuit breaker.')}
        </p>
      ) : (
        <ul className="breakers">
          {rows.map((b) => (
            <li key={b.id}>
              <Cell state={b.tripped_at ? 'failed' : 'ok'} />
              <span className="mono ellipsis">{b.key || tr('all events')}</span>
              {b.shadow && <span className="tag">{tr('shadow')}</span>}
              <span className="small muted">
                {b.tripped_at ? (
                  <>
                    {trn(b.firings, 'Tripped after {n} firing', 'Tripped after {n} firings')} <Ago at={b.tripped_at} />
                  </>
                ) : (
                  <>
                    {tr('Reset')} <Ago at={b.reset_at} /> {b.summary_fields?.reset_by ? tr('by {name}', { name: userName(b.summary_fields.reset_by) }) : ''}
                  </>
                )}
              </span>
              {caps.edit && b.tripped_at && (
                <button className="btn sm" disabled={busy} onClick={() => reset(b.key)}>
                  {tr('Reset')}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}
