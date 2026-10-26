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
import { Ago, Elapsed, Empty, ErrorNote, KV, Loading, PageHead, Panel, Status, Tab } from '../../components/ui'
import { jobPath, jobsNewestFirst } from '../../lib/format'
import { OneTimeSecret, useDelete } from './common'

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
  errors: string
  managed: boolean
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

const stateTone: Record<string, string> = { ready: 'ok', installed: 'pending', provisioning: 'pending', unavailable: 'fail', deprovisioning: 'cancel', 'provision-fail': 'fail', 'deprovision-fail': 'fail' }

function NodeState({ i }: { i: Instance }) {
  const tone = !i.enabled ? 'cancel' : (stateTone[i.node_state] ?? 'none')
  const label = !i.enabled ? 'Disabled' : i.node_state === 'installed' ? 'Waiting for agent' : i.node_state.charAt(0).toUpperCase() + i.node_state.slice(1)
  return (
    <span className="status">
      <span aria-hidden="true" className={`sdot ${tone}`} />
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
      toast(`${i.hostname} ${i.enabled ? 'disabled — running jobs finish, no new jobs start' : 'enabled'}`)
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="page">
      <PageHead
        title="Instances"
        subtitle="Nodes that run jobs, and the groups that route jobs to them."
        bordered={false}
        actions={
          me.is_superuser && (
            <>
              <Link className="btn" to="/instance-groups/new">
                New instance group
              </Link>
              <button className="btn primary" onClick={() => setAdding(true)}>
                <Icon name="plus" size={14} />
                Add execution node
              </button>
            </>
          )
        }
      />
      <div className="page-body">
        <DataTable
          label="Instances"
          rows={instances.data?.results}
          loading={instances.isPending}
          error={instances.error}
          onOpen={(i) => nav(`/instances/${i.id}`)}
          columns={[
            {
              key: 'host',
              header: 'Node',
              render: (i) => (
                <div>
                  <Link to={`/instances/${i.id}`} className="cell-title mono" style={{ fontSize: 13 }}>
                    {i.hostname}
                  </Link>
                  <div className="small faint">
                    {i.node_type}
                    {i.version ? ` · ${i.version}` : ''}
                    {i.agent_runtime ? ` · ${i.agent_runtime}` : ''}
                  </div>
                </div>
              ),
            },
            { key: 'state', header: 'State', render: (i) => <NodeState i={i} /> },
            { key: 'cap', header: 'Capacity used', render: (i) => (i.node_type === 'hop' ? <span className="faint">—</span> : <CapacityBar used={i.consumed_capacity} total={i.capacity} label={`${i.hostname} capacity`} />) },
            { key: 'jobs', header: 'Running', className: 'num', render: (i) => i.jobs_running },
            { key: 'seen', header: 'Last seen', className: 'small nowrap', render: (i) => <Ago at={i.last_seen} /> },
            {
              key: 'x',
              header: <span className="sr-only">Actions</span>,
              className: 'end',
              render: (i) =>
                me.is_superuser && (
                  <Menu label={`Actions for ${i.hostname}`} className="btn sm icon">
                    <button onClick={() => toggle(i)}>{i.enabled ? 'Disable (drain)' : 'Enable'}</button>
                    {i.node_type === 'execution' && <button onClick={() => issue(i)}>Issue new agent token…</button>}
                  </Menu>
                ),
            },
          ]}
        />
        <section className="stack" style={{ gap: 10 }}>
          <div className="row">
            <h2 className="grow">Instance groups</h2>
            <span className="small faint">Templates, inventories and organizations pick groups in order; the first with free capacity runs the job.</span>
          </div>
          <DataTable
            label="Instance groups"
            rows={groups.data?.results}
            loading={groups.isPending}
            error={groups.error}
            onOpen={(g) => nav(`/instance-groups/${g.id}`)}
            columns={[
              {
                key: 'name',
                header: 'Group',
                render: (g) => (
                  <div>
                    <Link to={`/instance-groups/${g.id}`} className="cell-title">
                      {g.name}
                    </Link>
                    <div className="small faint">{g.is_container_group ? 'Container group · a pod per job' : `${g.instances} node${g.instances === 1 ? '' : 's'}`}</div>
                  </div>
                ),
              },
              { key: 'cap', header: 'Capacity used', render: (g) => (g.is_container_group ? <span className="small faint">{g.max_concurrent_jobs ? `up to ${g.max_concurrent_jobs} jobs` : 'Elastic'}</span> : <CapacityBar used={g.consumed_capacity} total={g.capacity} label={`${g.name} capacity`} />) },
              { key: 'run', header: 'Running', className: 'num', render: (g) => g.jobs_running },
              { key: 'total', header: 'Jobs run', className: 'num faint', render: (g) => g.jobs_total },
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
          title={`Connect ${token.host}`}
          onClose={() => setToken(null)}
          wide
          footer={
            <button className="btn primary" onClick={() => setToken(null)}>
              Done
            </button>
          }
        >
          <p style={{ margin: 0 }}>
            Run this on the node. The agent dials back to the control plane over HTTPS — no inbound ports are needed. Use <span className="mono">--runtime docker</span> or <span className="mono">--runtime none</span> if Podman is not
            installed.
          </p>
          <OneTimeSecret value={token.command} note="This token is shown once. Issuing a new token disconnects the agent using the old one." />
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
      title="Add an execution node"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !host.trim()} onClick={submit}>
            Add and issue token
          </button>
        </>
      }
    >
      <TextInput label="Hostname" value={host} onChange={setHost} mono placeholder="exec-dc2-01" autoFocus help="A name for the node; it does not need to resolve. It identifies the agent in job details." />
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
        crumbs={[{ label: 'Instances', to: '/instances' }, { label: i.hostname }]}
        title={<span className="mono" style={{ fontSize: 'inherit' }}>{i.hostname}</span>}
        badge={<NodeState i={i} />}
        subtitle={`${i.node_type} node${i.managed ? ' · registered by the control plane' : ''}`}
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
                {i.enabled ? 'Disable' : 'Enable'}
              </button>
              {i.node_type === 'execution' && (
                <button className="btn" onClick={issue}>
                  New agent token
                </button>
              )}
              {!i.managed && (
                <button className="btn danger" onClick={() => del({ path: `instances/${id}/`, name: i.hostname, what: 'instance', then: '/instances' })}>
                  Remove
                </button>
              )}
            </>
          )
        }
      />
      <div className="page-body">
        {token && <OneTimeSecret value={token} note="Run this on the node to connect its agent. The token is shown once; the previous one stops working." />}
        {i.errors && <div className="form-error">{i.errors}</div>}
        <div className="split">
          <div className="wide stack" style={{ gap: 16 }}>
            <Panel title="Capacity">
              <div className="stack" style={{ gap: 12 }}>
                <CapacityBar used={i.consumed_capacity} total={i.capacity} label="capacity used" />
                <KV
                  items={[
                    ['Running jobs', <span className="mono">{i.jobs_running}</span>],
                    ['Jobs run', <span className="mono">{i.jobs_total}</span>],
                    ['CPU', i.cpu ? `${i.cpu} cores` : '—'],
                    ['Memory', i.memory ? `${(i.memory / 1024 ** 3).toFixed(1)} GiB` : '—'],
                  ]}
                />
                {me.is_superuser && <CapacityAdjust inst={i} />}
              </div>
            </Panel>
            <Panel title="Recent jobs on this node" pad={false}>
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
                <Empty title="No jobs ran here yet" />
              )}
            </Panel>
          </div>
          <div className="narrow">
            <Panel title="Details">
              <KV
                items={[
                  ['Version', <span className="mono">{i.version || '—'}</span>],
                  ['Runtime', i.agent_runtime || (i.node_type === 'execution' ? '—' : 'local')],
                  ['Last seen', <Ago at={i.last_seen} />],
                  ['UUID', <span className="mono small">{i.uuid}</span>],
                  ['IP address', i.ip_address || '—'],
                  [
                    'Groups',
                    groups.data?.results.length ? (
                      groups.data.results.map((g, n) => (
                        <span key={g.id}>
                          {n > 0 && ', '}
                          <Link to={`/instance-groups/${g.id}`}>{g.name}</Link>
                        </span>
                      ))
                    ) : (
                      <span className="faint">None — add it to a group to receive jobs</span>
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
      <label htmlFor="adj">Capacity adjustment</label>
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
        <span className="mono" style={{ width: 40 }}>
          {Math.round(v * 100)}%
        </span>
      </div>
      <span className="help">0% sizes capacity by memory (fewer forks), 100% by CPU (more forks).</span>
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
        crumbs={[{ label: 'Instances', to: '/instances' }, { label: grp.name }]}
        title={grp.name}
        subtitle={grp.is_container_group ? 'Container group — each job runs in its own pod' : 'Instance group'}
        actions={
          <>
            {grp.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/edit`}>
                Edit
              </Link>
            )}
            {me.is_superuser && !builtin && (
              <button className="btn danger" onClick={() => del({ path: `instance_groups/${id}/`, name: grp.name, what: 'instance group', then: '/instances' })}>
                Delete
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              Details
            </Tab>
            {!grp.is_container_group && (
              <Tab to={`${base}/instances`} n={grp.instances}>
                Instances
              </Tab>
            )}
            <Tab to={`${base}/jobs`}>Jobs</Tab>
            <Tab to={`${base}/access`}>Access</Tab>
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
                  <Panel title="Routing">
                    <KV
                      items={[
                        ['Capacity used', grp.is_container_group ? 'Elastic' : <CapacityBar used={grp.consumed_capacity} total={grp.capacity} label="capacity" />],
                        ['Running jobs', <span className="mono">{grp.jobs_running}</span>],
                        ['Max concurrent jobs', grp.max_concurrent_jobs || 'No limit'],
                        ['Max forks', grp.max_forks || 'No limit'],
                        ...(!grp.is_container_group
                          ? ([
                              ['Policy: minimum nodes', grp.policy_instance_minimum],
                              ['Policy: share of nodes', `${grp.policy_instance_percentage}%`],
                              ['Always include', grp.policy_instance_list.length ? <span className="mono">{grp.policy_instance_list.join(', ')}</span> : '—'],
                            ] as [string, ReactNode][])
                          : []),
                      ]}
                    />
                  </Panel>
                </div>
                {grp.is_container_group && (
                  <div className="narrow">
                    <Panel title="Pod spec override">{grp.pod_spec_override ? <pre className="code">{grp.pod_spec_override}</pre> : <p className="muted" style={{ margin: 0 }}>Default pod spec.</p>}</Panel>
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
            Add instances
          </button>
        </div>
      )}
      <DataTable
        label="Instances"
        rows={list.data?.results}
        loading={list.isPending}
        error={list.error}
        empty={<Empty title="No instances in this group" />}
        columns={[
          {
            key: 'host',
            header: 'Node',
            render: (i) => (
              <Link to={`/instances/${i.id}`} className="mono">
                {i.hostname}
              </Link>
            ),
          },
          { key: 'type', header: 'Type', render: (i) => i.node_type },
          { key: 'state', header: 'State', render: (i) => <NodeState i={i} /> },
          { key: 'cap', header: 'Capacity used', render: (i) => <CapacityBar used={i.consumed_capacity} total={i.capacity} label={`${i.hostname} capacity`} /> },
          {
            key: 'x',
            header: <span className="sr-only">Actions</span>,
            className: 'end',
            render: (i) =>
              me.is_superuser && (
                <button
                  className="btn sm ghost"
                  onClick={async () => {
                    await api.post(`instance_groups/${group.id}/instances/`, { id: i.id, disassociate: true }).catch((e) => toast((e as Error).message, 'error'))
                    invalidate('instance_groups', 'instances')
                  }}
                >
                  Remove
                </button>
              ),
          },
        ]}
      />
      {adding && (
        <Dialog
          title={`Add instances to ${group.name}`}
          onClose={() => setAdding(false)}
          footer={
            <>
              <button className="btn" onClick={() => setAdding(false)}>
                Cancel
              </button>
              <button className="btn primary" disabled={!picked.length} onClick={add}>
                Add
              </button>
            </>
          }
        >
          <MultiLookup label="Instances" resource="instances/" order="hostname" values={picked} onChange={setPicked} display={(r) => r.hostname} meta={(r) => r.node_type} full />
        </Dialog>
      )}
    </div>
  )
}

function GroupJobs({ group }: { group: Group }) {
  const jobs = useList<Job>('unified_jobs/', { instance_group: group.id, order_by: jobsNewestFirst, page_size: 25 })
  return (
    <DataTable
      label="Jobs"
      rows={jobs.data?.results}
      loading={jobs.isPending}
      error={jobs.error}
      empty={<Empty title="No jobs ran in this group yet" />}
      columns={[
        { key: 's', header: 'Status', render: (j) => <Status status={j.status} /> },
        { key: 'n', header: 'Job', render: (j) => <Link to={jobPath(j)}>{j.name}</Link> },
        { key: 'id', header: 'ID', className: 'num faint', render: (j) => `#${j.id}` },
        { key: 'node', header: 'Node', className: 'num', render: (j) => j.execution_node || '—' },
        { key: 't', header: 'Finished', className: 'small nowrap', render: (j) => (j.finished ? <Ago at={j.finished} /> : <Elapsed job={j} />) },
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
  const save = async () => {
    setBusy(true)
    setError(null)
    const body: Record<string, unknown> = {
      name: f.values.name,
      is_container_group: f.values.container,
      max_concurrent_jobs: Number(f.values.max_concurrent_jobs) || 0,
      max_forks: Number(f.values.max_forks) || 0,
    }
    if (f.values.container) {
      body.credential = f.values.credential?.id ?? null
      body.pod_spec_override = f.values.pod_spec_override
    } else {
      body.policy_instance_minimum = Number(f.values.policy_instance_minimum) || 0
      body.policy_instance_percentage = Number(f.values.policy_instance_percentage) || 0
      body.policy_instance_list = f.values.policy_instance_list
        .split(/[\s,]+/)
        .map((s) => s.trim())
        .filter(Boolean)
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
        <div className="segmented" role="radiogroup" aria-label="Kind">
          {(
            [
              [false, 'Instance group — runs on registered nodes'],
              [true, 'Container group — a Kubernetes pod per job'],
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
        <TextInput label="Name" value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!group} />
        <TextInput label="Max concurrent jobs" type="number" value={f.values.max_concurrent_jobs} onChange={(v) => f.set('max_concurrent_jobs', v)} help="0 means no limit." error={f.errors.max_concurrent_jobs} />
        <TextInput label="Max forks" type="number" value={f.values.max_forks} onChange={(v) => f.set('max_forks', v)} help="Total forks across running jobs. 0 means no limit." error={f.errors.max_forks} />
        {f.values.container ? (
          <>
            <Lookup
              label="Kubernetes credential"
              optional
              resource="credentials/"
              params={{ credential_type__kind: 'kubernetes' }}
              value={f.values.credential}
              onChange={(v) => f.set('credential', v)}
              help="Leave empty to use the service account of the control plane pod."
              error={f.errors.credential}
            />
            <Field label="Pod spec override" optional full error={podError ?? f.errors.pod_spec_override} help="YAML merged into the default job pod: namespace, resources, node selectors, tolerations, service account.">
              <textarea
                className="textarea mono"
                rows={12}
                spellCheck={false}
                value={f.values.pod_spec_override}
                placeholder={'apiVersion: v1\nkind: Pod\nmetadata:\n  namespace: awx-jobs\nspec:\n  containers:\n    - name: worker\n      resources:\n        requests: {cpu: 250m, memory: 100Mi}'}
                onChange={(e) => f.set('pod_spec_override', e.target.value)}
              />
            </Field>
          </>
        ) : (
          <>
            <TextInput label="Policy: minimum nodes" type="number" value={f.values.policy_instance_minimum} onChange={(v) => f.set('policy_instance_minimum', v)} help="Nodes automatically kept in this group." />
            <TextInput label="Policy: share of nodes" type="number" value={f.values.policy_instance_percentage} onChange={(v) => f.set('policy_instance_percentage', v)} help="Percent of all nodes automatically in this group." />
            <TextInput label="Always include" optional mono value={f.values.policy_instance_list} onChange={(v) => f.set('policy_instance_list', v)} help="Hostnames, comma separated." full />
          </>
        )}
      </div>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !!podError}>
          {busy ? 'Saving…' : group ? 'Save changes' : 'Create group'}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(group ? `/instance-groups/${group.id}` : '/instances')}>
          Cancel
        </button>
      </div>
    </form>
  )
}

export function GroupNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: 'Instances', to: '/instances' }, { label: 'New instance group' }]} title="New instance group" />
      <div className="page-body">
        <GroupForm />
      </div>
    </div>
  )
}

