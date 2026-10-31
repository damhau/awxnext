import { useState, type ReactNode } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Job, Ref } from '../../api/types'
import { useSession } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Field, FormError, Lookup, MultiLookup, TextInput, useForm, varsError } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, Menu, useToast } from '../../components/overlay'
import { DataTable } from '../../components/table'
import { Ago, Cell, Elapsed, Empty, ErrorNote, KV, Loading, PageHead, Panel, Status, Tab } from '../../components/ui'
import type { CellState } from '../../lib/cells'
import { adjustedCapacity, describeAdjustment } from '../../lib/capacity'
import { jobPath, jobsNewestFirst } from '../../lib/format'
import { checkGroupForm, describePolicy, installerGroup, memberReason, parseHosts } from '../../lib/instanceGroups'
import { OneTimeSecret, useDelete } from './common'
import { tr, trn } from '../../lib/i18n'

type Instance = Base & {
  hostname: string
  node_type: 'control' | 'execution' | 'hybrid' | 'hop'
  node_state: string
  capacity: number
  consumed_capacity: number
  jobs_running: number
  jobs_total: number
  enabled: boolean
  last_seen: string | null
  version: string
  cpu: number
  memory: number
  cpu_capacity: number
  mem_capacity: number
  errors: string
  managed: boolean
  managed_by_policy: boolean
  capacity_adjustment: number
  agent_runtime?: string
  uuid: string
  ip_address: string
}

type Group = Base & {
  capacity: number
  consumed_capacity: number
  instances: number
  jobs_running: number
  jobs_total: number
  is_container_group: boolean
  max_concurrent_jobs: number
  max_forks: number
  pod_spec_override: string
  credential: number | null
  policy_instance_minimum: number
  policy_instance_percentage: number
  policy_instance_list: string[]
  percent_capacity_remaining: number
}

const stateTone: Record<string, CellState> = { ready: 'ok', installed: 'pending', provisioning: 'pending', unavailable: 'failed', deprovisioning: 'canceled', 'provision-fail': 'failed', 'deprovision-fail': 'failed' }

function NodeState({ i }: { i: Instance }) {
  const tone: CellState = !i.enabled ? 'canceled' : (stateTone[i.node_state] ?? 'empty')
  const label = !i.enabled ? 'Disabled' : i.node_state === 'installed' ? tr('Waiting for agent') : i.node_state.charAt(0).toUpperCase() + i.node_state.slice(1)
  return (
    <span className="status">
      <Cell state={tone} />
      {label}
    </span>
  )
}

export function CapacityBar({ used, total, label }: { used: number; total: number; label: string }) {
  const pct = total ? Math.min(100, Math.round((100 * used) / total)) : 0
  return (
    <div style={{ minWidth: 120 }}>
      <div className="row small">
        <span className="mono">{pct}%</span>
        <span className="faint">
          {used} / {total}
        </span>
      </div>
      <div className={`meter ${pct > 80 ? 'hot' : ''}`} role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
        <div style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

export function InstanceList() {
  const { me } = useSession()
  const nav = useNavigate()
  const instances = useList<Instance>('instances/', { order_by: 'hostname', page_size: 200 })
  const groups = useList<Group>('instance_groups/', { order_by: 'name', page_size: 200 })
  const [adding, setAdding] = useState(false)
  const [token, setToken] = useState<{ host: string; command: string; token: string } | null>(null)
  const toast = useToast()
  const invalidate = useInvalidate()
  const issue = async (i: Instance) => {
    try {
      const r = await api.post<{ token: string; command: string }>(`instances/${i.id}/agent_token/`)
      setToken({ host: i.hostname, ...r })
      invalidate('instances')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  const toggle = async (i: Instance) => {
    try {
      await api.patch(`instances/${i.id}/`, { enabled: !i.enabled })
      invalidate('instances')
      toast(i.enabled ? tr('{hostname} disabled — running jobs finish, no new jobs start', { hostname: i.hostname }) : tr('{hostname} enabled', { hostname: i.hostname }))
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="page">
      <PageHead
        title={tr('Instances')}
        subtitle={tr('Nodes that run jobs, and the groups that route jobs to them.')}
        bordered={false}
        actions={
          me.is_superuser && (
            <>
              <Link className="btn" to="/instance-groups/new">
                {tr('New instance group')}
              </Link>
              <button className="btn primary" onClick={() => setAdding(true)}>
                <Icon name="plus" size={14} />
                {tr('Add execution node')}
              </button>
            </>
          )
        }
      />
      <div className="page-body">
        <DataTable
          label={tr('Instances')}
          rows={instances.data?.results}
          loading={instances.isPending}
          error={instances.error}
          onOpen={(i) => nav(`/instances/${i.id}`)}
          columns={[
            {
              key: 'host',
              header: tr('Node'),
              render: (i) => (
                <div>
                  <Link to={`/instances/${i.id}`} className="cell-title mono" style={{ fontSize: 13 }}>
                    {i.hostname}
                  </Link>
                  <div className="small faint">
                    {i.node_type}
                    {i.version ? tr(' · {version}', { version: i.version }) : ''}
                    {i.agent_runtime ? tr(' · {agent_runtime}', { agent_runtime: i.agent_runtime }) : ''}
                  </div>
                </div>
              ),
            },
            { key: 'state', header: tr('State'), render: (i) => <NodeState i={i} /> },
            { key: 'cap', header: tr('Capacity used'), render: (i) => (i.node_type === 'hop' ? <span className="faint">—</span> : <CapacityBar used={i.consumed_capacity} total={i.capacity} label={tr('{hostname} capacity', { hostname: i.hostname })} />) },
            { key: 'jobs', header: tr('Running'), className: 'num', render: (i) => i.jobs_running },
            { key: 'seen', header: tr('Last seen'), className: 'small nowrap', render: (i) => <Ago at={i.last_seen} /> },
            {
              key: 'x',
              header: <span className="sr-only">{tr('Actions')}</span>,
              className: 'end',
              render: (i) =>
                me.is_superuser && (
                  <Menu label={tr('Actions for {hostname}', { hostname: i.hostname })} className="btn sm icon">
                    <button onClick={() => toggle(i)}>{i.enabled ? tr('Disable (drain)') : tr('Enable')}</button>
                    {i.node_type === 'execution' && <button onClick={() => issue(i)}>{tr('Issue new agent token…')}</button>}
                  </Menu>
                ),
            },
          ]}
        />
        <section className="stack" style={{ gap: 10 }}>
          <div className="row">
            <h2 className="grow">{tr('Instance groups')}</h2>
            <span className="small faint">{tr('Templates, inventories and organizations pick groups in order; the first with free capacity runs the job.')}</span>
          </div>
          <DataTable
            label={tr('Instance groups')}
            rows={groups.data?.results}
            loading={groups.isPending}
            error={groups.error}
            onOpen={(g) => nav(`/instance-groups/${g.id}`)}
            columns={[
              {
                key: 'name',
                header: tr('Group'),
                render: (g) => (
                  <div>
                    <Link to={`/instance-groups/${g.id}`} className="cell-title">
                      {g.name}
                    </Link>
                    <div className="small faint">{g.is_container_group ? tr('Container group · a pod per job') : trn(g.instances, '{n} node', '{n} nodes')}</div>
                  </div>
                ),
              },
              { key: 'cap', header: tr('Capacity used'), render: (g) => (g.is_container_group ? <span className="small faint">{g.max_concurrent_jobs ? tr('up to {max_concurrent_jobs} jobs', { max_concurrent_jobs: g.max_concurrent_jobs }) : tr('Elastic')}</span> : <CapacityBar used={g.consumed_capacity} total={g.capacity} label={tr('{name} capacity', { name: g.name })} />) },
              { key: 'run', header: tr('Running'), className: 'num', render: (g) => g.jobs_running },
              { key: 'total', header: tr('Jobs run'), className: 'num faint', render: (g) => g.jobs_total },
            ]}
          />
        </section>
      </div>
      {adding && (
        <AddNode
          onClose={() => setAdding(false)}
          onCreated={(host, r) => {
            setAdding(false)
            setToken({ host, ...r })
          }}
        />
      )}
      {token && (
        <Dialog
          title={tr('Connect {host}', { host: token.host })}
          onClose={() => setToken(null)}
          wide
          footer={
            <button className="btn primary" onClick={() => setToken(null)}>
              {tr('Done')}
            </button>
          }
        >
          <p style={{ margin: 0 }}>
            {tr('Run this on the node. The agent dials back to the control plane over HTTPS — no inbound ports are needed. Use')} <span className="mono">{tr('--runtime docker')}</span> {tr('or')} <span className="mono">{tr('--runtime none')}</span> {tr('if Podman is not installed.')}
          </p>
          <OneTimeSecret value={token.command} note={tr('This token is shown once. Issuing a new token disconnects the agent using the old one.')} />
        </Dialog>
      )}
    </div>
  )
}

function AddNode({ onClose, onCreated }: { onClose: () => void; onCreated: (host: string, r: { token: string; command: string }) => void }) {
  const [host, setHost] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const i = await api.post<Instance>('instances/', { hostname: host.trim(), node_type: 'execution' })
      const r = await api.post<{ token: string; command: string }>(`instances/${i.id}/agent_token/`)
      invalidate('instances', 'instance_groups')
      onCreated(i.hostname, r)
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={tr('Add an execution node')}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={busy || !host.trim()} onClick={submit}>
            {tr('Add and issue token')}
          </button>
        </>
      }
    >
      <TextInput label={tr('Hostname')} value={host} onChange={setHost} mono placeholder={tr('exec-dc2-01')} autoFocus help={tr('A name for the node; it does not need to resolve. It identifies the agent in job details.')} />
      <FormError error={error} />
    </Dialog>
  )
}

export function InstanceDetail() {
  const id = Number(useParams().id)
  const { me } = useSession()
  const inst = useObject<Instance>(`instances/${id}/`)
  const groups = useList<Ref>(`instances/${id}/instance_groups/`, { page_size: 50 })
  const jobs = useList<Job>('unified_jobs/', inst.data ? { execution_node: inst.data.hostname, order_by: jobsNewestFirst, page_size: 10 } : undefined, { enabled: !!inst.data })
  const [token, setToken] = useState<string | null>(null)
  const toast = useToast()
  const invalidate = useInvalidate()
  const del = useDelete()
  if (inst.error) return <div className="page-body"><ErrorNote error={inst.error} /></div>
  if (!inst.data) return <Loading />
  const i = inst.data
  const issue = async () => {
    try {
      const r = await api.post<{ command: string }>(`instances/${id}/agent_token/`)
      setToken(r.command)
      invalidate('instances')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Instances'), to: '/instances' }, { label: i.hostname }]}
        title={<span className="mono" style={{ fontSize: 'inherit' }}>{i.hostname}</span>}
        badge={<NodeState i={i} />}
        subtitle={i.managed ? tr('{node_type} node · registered by the control plane', { node_type: i.node_type }) : tr('{node_type} node', { node_type: i.node_type })}
        actions={
          me.is_superuser && (
            <>
              <button
                className="btn"
                onClick={async () => {
                  await api.patch(`instances/${id}/`, { enabled: !i.enabled }).catch((e) => toast((e as Error).message, 'error'))
                  invalidate('instances')
                }}
              >
                {i.enabled ? tr('Disable') : tr('Enable')}
              </button>
              {i.node_type === 'execution' && (
                <button className="btn" onClick={issue}>
                  {tr('New agent token')}
                </button>
              )}
              {!i.managed && (
                <button className="btn danger" onClick={() => del({ path: `instances/${id}/`, name: i.hostname, what: tr('instance'), then: '/instances' })}>
                  {tr('Remove')}
                </button>
              )}
            </>
          )
        }
      />
      <div className="page-body">
        {token && <OneTimeSecret value={token} note={tr('Run this on the node to connect its agent. The token is shown once; the previous one stops working.')} />}
        {i.errors && <div className="form-error">{i.errors}</div>}
        <div className="split">
          <div className="wide stack" style={{ gap: 16 }}>
            <Panel title={tr('Capacity')}>
              <div className="stack" style={{ gap: 12 }}>
                <CapacityBar used={i.consumed_capacity} total={i.capacity} label={tr('capacity used')} />
                <KV
                  items={[
                    [tr('Running jobs'), <span className="mono">{i.jobs_running}</span>],
                    [tr('Jobs run'), <span className="mono">{i.jobs_total}</span>],
                    [tr('CPU'), i.cpu ? tr('{cpu} cores', { cpu: i.cpu }) : '—'],
                    [tr('Memory'), i.memory ? tr('{toFixed} GiB', { toFixed: (i.memory / 1024 ** 3).toFixed(1) }) : '—'],
                  ]}
                />
                {me.is_superuser && <CapacityAdjust inst={i} />}
              </div>
            </Panel>
            <Panel title={tr('Recent jobs on this node')} pad={false}>
              {jobs.data?.results.length ? (
                <table className="data">
                  <tbody>
                    {jobs.data.results.map((j) => (
                      <tr key={j.id}>
                        <td style={{ width: 120 }}>
                          <Status status={j.status} />
                        </td>
                        <td>
                          <Link to={jobPath(j)}>{j.name}</Link>
                        </td>
                        <td className="mono small faint">#{j.id}</td>
                        <td className="small nowrap">{j.finished ? <Ago at={j.finished} /> : <Elapsed job={j} />}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <Empty title={tr('No jobs ran here yet')} />
              )}
            </Panel>
          </div>
          <div className="narrow">
            <Panel title={tr('Details')}>
              <KV
                items={[
                  [tr('Version'), <span className="mono">{i.version || '—'}</span>],
                  [tr('Runtime'), i.agent_runtime || (i.node_type === 'execution' ? '—' : tr('local'))],
                  [tr('Last seen'), <Ago at={i.last_seen} />],
                  [tr('UUID'), <span className="mono small">{i.uuid}</span>],
                  [tr('IP address'), i.ip_address || '—'],
                  [
                    tr('Policy'),
                    me.is_superuser && i.node_type !== 'hop' ? (
                      <label className="check" title={tr('Off: the instance joins only the groups that list it.')}>
                        <input
                          type="checkbox"
                          checked={i.managed_by_policy}
                          onChange={async (e) => {
                            await api.patch(`instances/${id}/`, { managed_by_policy: e.target.checked }).catch((err) => toast((err as Error).message, 'error'))
                            invalidate('instances', 'instance_groups')
                          }}
                        />
                        <span>{tr('Managed by policy')}</span>
                      </label>
                    ) : i.managed_by_policy ? (
                      tr('Managed by policy')
                    ) : (
                      tr('Listed groups only')
                    ),
                  ],
                  [
                    tr('Groups'),
                    groups.data?.results.length ? (
                      groups.data.results.map((g, n) => (
                        <span key={g.id}>
                          {n > 0 && ', '}
                          <Link to={`/instance-groups/${g.id}`}>{g.name}</Link>
                        </span>
                      ))
                    ) : (
                      <span className="faint">{tr('None — add it to a group to receive jobs')}</span>
                    ),
                  ],
                ]}
              />
            </Panel>
          </div>
        </div>
      </div>
    </div>
  )
}

function CapacityAdjust({ inst }: { inst: Instance }) {
  const [v, setV] = useState(inst.capacity_adjustment)
  const invalidate = useInvalidate()
  const toast = useToast()
  return (
    <div className="field">
      <label htmlFor="adj">{tr('Capacity adjustment')}</label>
      <div className="row">
        <input
          id="adj"
          type="range"
          min={0}
          max={1}
          step={0.05}
          value={v}
          className="grow"
          onChange={(e) => setV(Number(e.target.value))}
          onMouseUp={async () => {
            await api.patch(`instances/${inst.id}/`, { capacity_adjustment: v }).catch((e) => toast((e as Error).message, 'error'))
            invalidate('instances')
          }}
          onKeyUp={async () => {
            await api.patch(`instances/${inst.id}/`, { capacity_adjustment: v }).catch(() => {})
            invalidate('instances')
          }}
        />
        <span className="mono nowrap" style={{ minWidth: 40 }}>
          {Math.round(v * 100)}%
        </span>
        {(inst.cpu_capacity > 0 || inst.mem_capacity > 0) && (
          <span className="mono small nowrap" title={tr('Capacity at this adjustment')}>
            {adjustedCapacity(inst.cpu_capacity, inst.mem_capacity, v)} {tr('forks')}
          </span>
        )}
      </div>
      <span className="help">{describeAdjustment(inst.cpu_capacity, inst.mem_capacity)}</span>
    </div>
  )
}

export function GroupDetail() {
  const id = Number(useParams().id)
  const { me } = useSession()
  const g = useObject<Group>(`instance_groups/${id}/`)
  const del = useDelete()
  if (g.error) return <div className="page-body"><ErrorNote error={g.error} /></div>
  if (!g.data) return <Loading />
  const grp = g.data
  const base = `/instance-groups/${id}`
  const builtin = grp.name === 'controlplane' || grp.name === 'default'
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Instances'), to: '/instances' }, { label: grp.name }]}
        title={grp.name}
        subtitle={grp.is_container_group ? tr('Container group — each job runs in its own pod') : tr('Instance group')}
        actions={
          <>
            {grp.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {me.is_superuser && !builtin && (
              <button className="btn danger" onClick={() => del({ path: `instance_groups/${id}/`, name: grp.name, what: tr('instance group'), then: '/instances' })}>
                {tr('Delete')}
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              {tr('Details')}
            </Tab>
            {!grp.is_container_group && (
              <Tab to={`${base}/instances`} n={grp.instances}>
                {tr('Instances')}
              </Tab>
            )}
            <Tab to={`${base}/jobs`}>{tr('Jobs')}</Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route
            index
            element={
              <div className="split">
                <div className="wide">
                  <Panel title={tr('Routing')}>
                    <KV
                      items={[
                        [tr('Capacity used'), grp.is_container_group ? tr('Elastic') : <CapacityBar used={grp.consumed_capacity} total={grp.capacity} label={tr('capacity')} />],
                        [tr('Running jobs'), <span className="mono">{grp.jobs_running}</span>],
                        [tr('Max concurrent jobs'), grp.max_concurrent_jobs || tr('No limit')],
                        [tr('Max forks'), grp.max_forks || tr('No limit')],
                        ...(!grp.is_container_group
                          ? ([
                              [tr('Membership'), describePolicy(grp)],
                              [tr('Policy: minimum nodes'), grp.policy_instance_minimum],
                              [tr('Policy: share of nodes'), tr('{policy_instance_percentage}%', { policy_instance_percentage: grp.policy_instance_percentage })],
                              [tr('Always include'), grp.policy_instance_list.length ? <span className="mono">{grp.policy_instance_list.join(', ')}</span> : '—'],
                            ] as [string, ReactNode][])
                          : []),
                      ]}
                    />
                  </Panel>
                </div>
                {grp.is_container_group && (
                  <div className="narrow">
                    <Panel title={tr('Pod spec override')}>{grp.pod_spec_override ? <pre className="code">{grp.pod_spec_override}</pre> : <p className="muted" style={{ margin: 0 }}>{tr('Default pod spec.')}</p>}</Panel>
                  </div>
                )}
              </div>
            }
          />
          <Route path="edit" element={<GroupForm group={grp} />} />
          <Route path="instances" element={<GroupInstances group={grp} />} />
          <Route path="jobs" element={<GroupJobs group={grp} />} />
          <Route path="access" element={<AccessTab resource="instance_groups" id={id} name={grp.name} />} />
        </Routes>
      </div>
    </div>
  )
}

function GroupInstances({ group }: { group: Group }) {
  const list = useList<Instance>(`instance_groups/${group.id}/instances/`, { order_by: 'hostname', page_size: 200 })
  const [adding, setAdding] = useState(false)
  const [picked, setPicked] = useState<Ref[]>([])
  const invalidate = useInvalidate()
  const toast = useToast()
  const { me } = useSession()
  const add = async () => {
    try {
      for (const i of picked) await api.post(`instance_groups/${group.id}/instances/`, { id: i.id })
      invalidate('instance_groups', 'instances')
      setAdding(false)
      setPicked([])
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="stack" style={{ gap: 12 }}>
      {me.is_superuser && (
        <div className="row">
          <span className="grow" />
          <button className="btn primary" onClick={() => setAdding(true)}>
            <Icon name="plus" size={14} />
            {tr('Add instances')}
          </button>
        </div>
      )}
      <DataTable
        label={tr('Instances')}
        rows={list.data?.results}
        loading={list.isPending}
        error={list.error}
        empty={<Empty title={tr('No instances in this group')} />}
        columns={[
          {
            key: 'host',
            header: tr('Node'),
            render: (i) => (
              <Link to={`/instances/${i.id}`} className="mono">
                {i.hostname}
              </Link>
            ),
          },
          { key: 'type', header: tr('Type'), render: (i) => i.node_type },
          { key: 'state', header: tr('State'), render: (i) => <NodeState i={i} /> },
          {
            key: 'why',
            header: tr('In the group'),
            render: (i) =>
              memberReason(i.hostname, group) === 'listed' ? (
                <span className="tag">{tr('Listed')}</span>
              ) : (
                <span className="tag" title={tr('Chosen by the group\'s minimum or share of instances. It stays while they select it; turn off its policy management to take it out.')}>
                  {tr('By policy')}
                </span>
              ),
          },
          { key: 'cap', header: tr('Capacity used'), render: (i) => <CapacityBar used={i.consumed_capacity} total={i.capacity} label={tr('{hostname} capacity', { hostname: i.hostname })} /> },
          {
            key: 'x',
            header: <span className="sr-only">{tr('Actions')}</span>,
            className: 'end',
            render: (i) =>
              me.is_superuser &&
              memberReason(i.hostname, group) === 'listed' && (
                <button
                  className="btn sm ghost"
                  onClick={async () => {
                    await api.post(`instance_groups/${group.id}/instances/`, { id: i.id, disassociate: true }).catch((e) => toast((e as Error).message, 'error'))
                    invalidate('instance_groups', 'instances')
                  }}
                >
                  {tr('Remove')}
                </button>
              ),
          },
        ]}
      />
      {adding && (
        <Dialog
          title={tr('Add instances to {name}', { name: group.name })}
          onClose={() => setAdding(false)}
          footer={
            <>
              <button className="btn" onClick={() => setAdding(false)}>
                {tr('Cancel')}
              </button>
              <button className="btn primary" disabled={!picked.length} onClick={add}>
                {tr('Add')}
              </button>
            </>
          }
        >
          <MultiLookup
            label={tr('Instances')}
            resource="instances/"
            order="hostname"
            params={{ node_type__in: 'execution,hybrid' }}
            values={picked}
            onChange={setPicked}
            display={(r) => r.hostname}
            meta={(r) => r.node_type}
            help={tr('Added instances are listed in the group\'s policy, so they stay in it.')}
            full
          />
        </Dialog>
      )}
    </div>
  )
}

function GroupJobs({ group }: { group: Group }) {
  const jobs = useList<Job>('unified_jobs/', { instance_group: group.id, order_by: jobsNewestFirst, page_size: 25 })
  return (
    <DataTable
      label={tr('Jobs')}
      rows={jobs.data?.results}
      loading={jobs.isPending}
      error={jobs.error}
      empty={<Empty title={tr('No jobs ran in this group yet')} />}
      columns={[
        { key: 's', header: tr('Status'), render: (j) => <Status status={j.status} /> },
        { key: 'n', header: tr('Job'), render: (j) => <Link to={jobPath(j)}>{j.name}</Link> },
        { key: 'id', header: tr('ID'), className: 'num faint', render: (j) => `#${j.id}` },
        { key: 'node', header: tr('Node'), className: 'num', render: (j) => j.execution_node || '—' },
        { key: 't', header: tr('Finished'), className: 'small nowrap', render: (j) => (j.finished ? <Ago at={j.finished} /> : <Elapsed job={j} />) },
      ]}
    />
  )
}

interface GroupValues extends Record<string, unknown> {
  name: string
  container: boolean
  credential: Ref | null
  pod_spec_override: string
  max_concurrent_jobs: string
  max_forks: string
  policy_instance_minimum: string
  policy_instance_percentage: string
  policy_instance_list: string
}

export function GroupForm({ group }: { group?: Group }) {
  const nav = useNavigate()
  const cred = useObject<Ref>(group?.credential ? `credentials/${group.credential}/` : null)
  if (group?.credential && !cred.data) return <Loading />
  return <GroupFormInner group={group} credential={cred.data ?? null} onDone={(id) => nav(`/instance-groups/${id}`)} />
}

function GroupFormInner({ group, credential, onDone }: { group?: Group; credential: Ref | null; onDone: (id: number) => void }) {
  const f = useForm<GroupValues>({
    name: group?.name ?? '',
    container: group?.is_container_group ?? false,
    credential,
    pod_spec_override: group?.pod_spec_override ?? '',
    max_concurrent_jobs: String(group?.max_concurrent_jobs ?? 0),
    max_forks: String(group?.max_forks ?? 0),
    policy_instance_minimum: String(group?.policy_instance_minimum ?? 0),
    policy_instance_percentage: String(group?.policy_instance_percentage ?? 0),
    policy_instance_list: (group?.policy_instance_list ?? []).join(', '),
  })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const nav = useNavigate()
  const podError = f.values.container ? varsError(f.values.pod_spec_override) : null
  const checks = checkGroupForm(f.values)
  const err = (k: keyof typeof checks) => checks[k] ?? f.errors[k]
  // AWX keeps the installer's groups' names and policies.
  const locked = !!group && installerGroup(group.name)
  const save = async () => {
    setBusy(true)
    setError(null)
    const body: Record<string, unknown> = {
      ...(locked ? {} : { name: f.values.name }),
      is_container_group: f.values.container,
      max_concurrent_jobs: Number(f.values.max_concurrent_jobs) || 0,
      max_forks: Number(f.values.max_forks) || 0,
    }
    if (f.values.container) {
      body.credential = f.values.credential?.id ?? null
      body.pod_spec_override = f.values.pod_spec_override
    } else {
      body.policy_instance_minimum = Number(f.values.policy_instance_minimum) || 0
      if (!locked) {
        body.policy_instance_percentage = Number(f.values.policy_instance_percentage) || 0
        body.policy_instance_list = parseHosts(f.values.policy_instance_list)
      }
    }
    try {
      const saved = group ? await api.patch<Group>(`instance_groups/${group.id}/`, body) : await api.post<Group>('instance_groups/', body)
      invalidate('instance_groups')
      onDone(saved.id)
    } catch (e) {
      setError(e)
      if (e && typeof e === 'object' && 'fields' in e) f.setErrors((e as { fields: Record<string, string> }).fields)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      className="form"
      style={{ maxWidth: 900 }}
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      {!group && (
        <div className="segmented" role="radiogroup" aria-label={tr('Kind')}>
          {(
            [
              [false, tr('Instance group — runs on registered nodes')],
              [true, tr('Container group — a Kubernetes pod per job')],
            ] as const
          ).map(([v, l]) => (
            <label key={String(v)} className={f.values.container === v ? 'on' : ''}>
              <input type="radio" name="ig-kind" checked={f.values.container === v} onChange={() => f.set('container', v)} />
              {l}
            </label>
          ))}
        </div>
      )}
      <div className="form-grid">
        <TextInput
          label={tr('Name')}
          value={f.values.name}
          onChange={(v) => f.set('name', v)}
          error={f.errors.name}
          autoFocus={!group}
          disabled={locked}
          help={locked ? tr('Created by the installer; the name cannot change.') : undefined}
        />
        <TextInput
          label={tr('Max concurrent jobs')}
          type="number"
          value={f.values.max_concurrent_jobs}
          onChange={(v) => f.set('max_concurrent_jobs', v)}
          help={tr('Jobs running on the group\'s instances, whichever group sent them. 0 means no limit.')}
          error={err('max_concurrent_jobs')}
        />
        <TextInput
          label={tr('Max forks')}
          type="number"
          value={f.values.max_forks}
          onChange={(v) => f.set('max_forks', v)}
          help={tr('Total forks across those jobs. 0 means no limit.')}
          error={err('max_forks')}
        />
        {f.values.container ? (
          <>
            <Lookup
              label={tr('Kubernetes credential')}
              optional
              resource="credentials/"
              params={{ credential_type__kind: 'kubernetes' }}
              value={f.values.credential}
              onChange={(v) => f.set('credential', v)}
              help={tr('Leave empty to use the service account of the control plane pod.')}
              error={f.errors.credential}
            />
            <Field label={tr('Pod spec override')} optional full error={podError ?? f.errors.pod_spec_override} help={tr('YAML merged into the default job pod: namespace, resources, node selectors, tolerations, service account.')}>
              <textarea
                className="textarea mono"
                rows={12}
                spellCheck={false}
                value={f.values.pod_spec_override}
                placeholder={tr('apiVersion: v1\nkind: Pod\nmetadata:\n  namespace: awx-jobs\nspec:\n  containers:\n    - name: worker\n      resources:\n        requests: {cpu: 250m, memory: 100Mi}')}
                onChange={(e) => f.set('pod_spec_override', e.target.value)}
              />
            </Field>
          </>
        ) : (
          <>
            <TextInput
              label={tr('Policy: minimum nodes')}
              type="number"
              value={f.values.policy_instance_minimum}
              onChange={(v) => f.set('policy_instance_minimum', v)}
              help={tr('Instances kept in this group, taken from those in the fewest groups.')}
              error={err('policy_instance_minimum')}
            />
            <TextInput
              label={tr('Policy: share of nodes')}
              type="number"
              value={f.values.policy_instance_percentage}
              onChange={(v) => f.set('policy_instance_percentage', v)}
              help={locked ? tr('Every instance, as the installer set it.') : tr('Percent of all instances kept in this group.')}
              error={err('policy_instance_percentage')}
              disabled={locked}
            />
            <TextInput
              label={tr('Always include')}
              optional
              mono
              value={f.values.policy_instance_list}
              onChange={(v) => f.set('policy_instance_list', v)}
              help={locked ? tr('Set by the installer.') : tr('Hostnames, comma separated. Instances added by hand are listed here.')}
              error={err('policy_instance_list')}
              disabled={locked}
              full
            />
          </>
        )}
      </div>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !!podError || Object.keys(checks).length > 0}>
          {busy ? tr('Saving…') : group ? tr('Save changes') : tr('Create group')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(group ? `/instance-groups/${group.id}` : '/instances')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}

export function GroupNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Instances'), to: '/instances' }, { label: tr('New instance group') }]} title={tr('New instance group')} />
      <div className="page-body">
        <GroupForm />
      </div>
    </div>
  )
}

