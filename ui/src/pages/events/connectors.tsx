import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams, useSearchParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any, Base, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Check, Field, FormError, Lookup, MultiLookup, SelectField, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { useToast } from '../../components/overlay'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, Maybe, PageHead, Panel, Tab, Upcoming } from '../../components/ui'
import { duration } from '../../lib/format'
import {
  type ConnectorStats,
  connectorCell,
  connectorStatus,
  parseArgs,
  parseFilters,
  pluginShort,
  presetArgs,
  RESTART_POLICIES,
  restartText,
  runStatus,
  SOURCE_PRESETS,
  sourceError,
  statsText,
  yamlText,
} from '../../lib/eventConnectors'
import { asIs, tr, trn } from '../../lib/i18n'
import { ListPage, useDelete } from '../admin/common'

type Connector = Base & {
  organization: number
  event_stream: number
  source: string
  args: Record<string, unknown>
  filters: Record<string, unknown>[]
  credentials: number[]
  execution_environment: number | null
  instance_group: number | null
  python: string
  enabled: boolean
  restart_policy: string
  restart_delay: number
  max_restarts: number
  buffer_size: number
  status: string
  status_explanation: string
  controller_node: string
  execution_node: string
  started: string | null
  next_start: string | null
  restart_count: number
  stats: ConnectorStats
}

function ConnectorState({ c }: { c: Connector }) {
  return (
    <span className="status">
      <Cell state={connectorCell(c.status)} />
      {c.status === 'restarting' && c.next_start ? (
        <span>
          {tr('Restarting')} <Upcoming at={c.next_start} />
        </span>
      ) : (
        connectorStatus(c.status)
      )}
    </span>
  )
}

/** Where it runs: its node, or its pod in a container group. */
function placement(c: Connector): string {
  if (!c.controller_node) return ''
  if (!c.execution_node) return tr('A pod started by {node}', { node: c.controller_node })
  if (c.execution_node === c.controller_node) return c.execution_node
  return tr('{node}, through {controller}', { node: c.execution_node, controller: c.controller_node })
}

export function ConnectorList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Connector>
      title={tr('Event connectors')}
      subtitle={tr('Event sources such as Kafka, run where jobs run, posting what they read to a stream.')}
      path="event_connectors/"
      params={scope}
      placeholder={tr('Filter connectors…')}
      actions={
        <Link to="/event-connectors/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New connector')}
        </Link>
      }
      onOpen={(c) => nav(`/event-connectors/${c.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Connector'),
          sort: 'name',
          render: (c) => (
            <div>
              <Link to={`/event-connectors/${c.id}`} className="cell-title">
                {c.name}
              </Link>
              <div className="cell-sub mono">{c.source}</div>
            </div>
          ),
        },
        { key: 'status', header: tr('Status'), sort: 'status', render: (c) => <ConnectorState c={c} /> },
        { key: 'stream', header: tr('Stream'), render: (c) => c.summary_fields.event_stream?.name ?? '—' },
        { key: 'stats', header: tr('Events'), render: (c) => <span className="small">{statsText(c.stats) || '—'}</span> },
        { key: 'node', header: tr('Runs on'), render: (c) => <span className="small mono">{placement(c) || '—'}</span> },
      ]}
      empty={<Empty title={tr('No event connectors yet')}>{tr('A connector runs an ansible.eda source plugin, such as Kafka, and posts its events to a stream for rules to act on.')}</Empty>}
    />
  )
}

export function ConnectorNew() {
  const [params] = useSearchParams()
  const streamId = params.get('stream')
  const stream = useObject<Any>(streamId ? `event_streams/${streamId}/` : null, { retry: false })
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Event connectors'), to: '/event-connectors' }, { label: tr('New') }]} title={tr('New event connector')} />
      <div className="page-body">{streamId && !stream.data && !stream.error ? <Loading /> : <ConnectorForm stream={stream.data} />}</div>
    </div>
  )
}

export function ConnectorDetail() {
  const id = Number(useParams().id)
  const conn = useObject<Connector>(`event_connectors/${id}/`, { refetchInterval: 3000 })
  const del = useDelete()
  const toast = useToast()
  const invalidate = useInvalidate()
  const [busy, setBusy] = useState(false)
  if (conn.error) return <div className="page-body"><ErrorNote error={conn.error} /></div>
  if (!conn.data) return <Loading />
  const c = conn.data
  const caps = c.summary_fields.user_capabilities ?? {}
  const base = `/event-connectors/${id}`
  const act = async (what: () => Promise<unknown>, done: string) => {
    setBusy(true)
    try {
      await what()
      invalidate('event_connectors')
      toast(done)
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Event connectors'), to: '/event-connectors' }, { label: c.name }]}
        title={c.name}
        badge={<ConnectorState c={c} />}
        subtitle={c.description || <span className="mono">{c.source}</span>}
        actions={
          <>
            {caps.edit && c.enabled && (
              <button className="btn" disabled={busy} onClick={() => act(() => api.post(`event_connectors/${id}/restart/`, {}), tr('The connector starts again.'))}>
                <Icon name="refresh" size={14} />
                {tr('Restart')}
              </button>
            )}
            {caps.edit && (
              <button
                className="btn"
                disabled={busy}
                onClick={() => act(() => api.patch(`event_connectors/${id}/`, { enabled: !c.enabled }), c.enabled ? tr('The connector is off: it stops.') : tr('The connector is on: it starts on a node.'))}
              >
                {c.enabled ? tr('Turn off') : tr('Turn on')}
              </button>
            )}
            {caps.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path: `event_connectors/${id}/`, name: c.name, what: tr('event connector'), then: '/event-connectors' })}>
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
            <Tab to={`${base}/runs`}>{tr('Runs')}</Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route index element={<Details c={c} />} />
          <Route path="runs" element={<Runs c={c} />} />
          <Route path="edit" element={<ConnectorForm conn={c} />} />
          <Route path="access" element={<AccessTab resource="event_connectors" id={id} name={c.name} />} />
        </Routes>
      </div>
    </div>
  )
}

function Details({ c }: { c: Connector }) {
  const sf = c.summary_fields
  const creds = (sf.credentials ?? []) as Ref[]
  const args = yamlText(c.args)
  const filters = yamlText(c.filters)
  const ee = useObject<Any>(c.execution_environment ? `execution_environments/${c.execution_environment}/` : null, { retry: false })
  const ig = useObject<Any>(c.instance_group ? `instance_groups/${c.instance_group}/` : null, { retry: false })
  const stats = c.stats ?? {}
  return (
    <div className="split">
      <div className="wide stack" style={{ gap: 16 }}>
        <Panel title={tr('Status')}>
          <div className="stack" style={{ gap: 10 }}>
            {c.status_explanation && <div className={`notice connector-why ${c.status === 'failed' ? 'fail' : ''}`}>{c.status_explanation}</div>}
            {stats.unreachable && <div className="notice accent">{tr('The connector can’t reach awxnext: it keeps the events and posts them when it can.')}</div>}
            <KV
              items={[
                [tr('Status'), <ConnectorState c={c} />],
                [tr('Runs on'), <Maybe v={placement(c)} mono />],
                [tr('Started'), c.started && c.controller_node ? <Ago at={c.started} /> : ''],
                [tr('Events'), <Maybe v={statsText(stats)} />],
                [tr('Retries'), stats.retries ? <span className="mono">{stats.retries}</span> : ''],
                [tr('Last error'), stats.last_error ? <span className="mono small">{stats.last_error}</span> : ''],
                [tr('Restarts in a row'), c.restart_count ? <span className="mono">{c.restart_count}</span> : ''],
              ]}
            />
          </div>
        </Panel>
        <Panel title={tr('Source')}>
          <KV
            items={[
              [tr('Plugin'), <span className="mono">{c.source}</span>],
              [tr('Arguments'), args ? <pre className="code connector-yaml">{args}</pre> : <span className="faint">{tr('None')}</span>],
              [tr('Filters'), filters ? <pre className="code connector-yaml">{filters}</pre> : <span className="faint">{tr('None')}</span>],
              [
                tr('Credentials'),
                creds.length ? (
                  <span>
                    {creds.map((r, i) => (
                      <span key={r.id}>
                        {i > 0 && ', '}
                        <Link to={`/credentials/${r.id}`}>{r.name}</Link>
                      </span>
                    ))}
                  </span>
                ) : (
                  ''
                ),
              ],
            ]}
          />
        </Panel>
      </div>
      <div className="narrow stack" style={{ gap: 16 }}>
        <Panel title={tr('Where it runs')}>
          <KV
            items={[
              [tr('Stream'), <Link to={`/event-streams/${c.event_stream}`}>{sf.event_stream?.name}</Link>],
              [tr('Image'), c.execution_environment ? (ee.data ? <span title={ee.data.image}>{ee.data.name}</span> : '…') : <span className="faint">{tr('The default execution environment')}</span>],
              [tr('Instance group'), c.instance_group ? (ig.data ? <Link to={`/instance-groups/${c.instance_group}`}>{ig.data.name}</Link> : '…') : <span className="faint">{asIs('default')}</span>],
              [tr('Python'), c.python ? <span className="mono">{c.python}</span> : <span className="faint">{tr('ansible-rulebook’s, else python3')}</span>],
              [tr('Buffer'), trn(c.buffer_size, '{n} event', '{n} events')],
              [tr('Restarts'), restartText(c.restart_policy, c.restart_delay, c.max_restarts)],
              [tr('Organization'), <Link to={`/organizations/${c.organization}`}>{sf.organization?.name}</Link>],
              [tr('Changed'), <Ago at={c.modified} />],
            ]}
          />
        </Panel>
      </div>
    </div>
  )
}

/** The connector's last runs, newest first, each with its log's tail. */
function Runs({ c }: { c: Connector }) {
  const list = useList<Any>(`event_connectors/${c.id}/runs/`, { order_by: '-id', page_size: 20 }, { refetchInterval: 3000 })
  const rows = list.data?.results
  if (list.error) return <ErrorNote error={list.error} />
  if (!rows) return <Loading rows={4} />
  if (!rows.length)
    return <Empty title={tr('No runs yet')}>{c.enabled ? tr('The connector starts when a node of its group is up.') : tr('Turn the connector on to start it.')}</Empty>
  return (
    <div className="events connector-runs" role="list" aria-label={tr('Runs')}>
      {rows.map((r, i) => {
        const [label, cell] = runStatus(r.status)
        const took = r.finished ? (new Date(r.finished).getTime() - new Date(r.started).getTime()) / 1000 : null
        return (
          <details key={r.id} className="event" role="listitem" open={i === 0}>
            <summary>
              <span className="status">
                <Cell state={cell} />
                {label}
              </span>
              <span className="small mono ellipsis">{r.execution_node === 'pod' ? tr('A pod started by {node}', { node: r.controller_node }) : r.execution_node}</span>
              <span className="small muted ellipsis run-why">{r.explanation}</span>
              <span className="small faint nowrap run-rc">{typeof r.rc === 'number' ? tr('exit {rc}', { rc: r.rc }) : ''}</span>
              <span className="small faint nowrap">
                <Ago at={r.started} />
                {took !== null && <> · {duration(Math.max(1, Math.round(took)))}</>}
              </span>
            </summary>
            <div className="stack" style={{ gap: 8 }}>
              {r.stats && statsText(r.stats) && <div className="small">{statsText(r.stats)}</div>}
              {r.log ? <pre className="code event-data">{r.log}</pre> : <span className="faint small">{tr('No output.')}</span>}
            </div>
          </details>
        )
      })}
    </div>
  )
}

interface ConnValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  stream: Ref | null
  source: string
  args: string
  filters: string
  credentials: Ref[]
  ee: Ref | null
  ig: Ref | null
  python: string
  enabled: boolean
  restart_policy: string
  restart_delay: string
  max_restarts: string
  buffer_size: string
}

function ConnectorForm({ conn, stream }: { conn?: Connector; stream?: Any }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const invalidate = useInvalidate()
  const sf = conn?.summary_fields
  const ee = useObject<Any>(conn?.execution_environment ? `execution_environments/${conn.execution_environment}/` : null, { retry: false })
  const ig = useObject<Any>(conn?.instance_group ? `instance_groups/${conn.instance_group}/` : null, { retry: false })
  const ready = (!conn?.execution_environment || ee.data || ee.error) && (!conn?.instance_group || ig.data || ig.error)
  if (!ready) return <Loading />
  const ref = (o?: Any): Ref | null => (o ? { id: o.id, name: o.name } : null)
  return (
    <ConnectorFields
      conn={conn}
      initial={{
        name: conn?.name ?? '',
        description: conn?.description ?? '',
        organization: sf?.organization ?? (stream ? { id: stream.organization, name: stream.summary_fields?.organization?.name } : org),
        stream: conn ? { id: conn.event_stream, name: sf?.event_stream?.name } : ref(stream),
        source: conn?.source ?? 'ansible.eda.kafka',
        args: conn ? yamlText(conn.args) : yamlText(presetArgs('ansible.eda.kafka')),
        filters: yamlText(conn?.filters),
        credentials: ((sf?.credentials ?? []) as Ref[]).map((r) => ({ id: r.id, name: r.name })),
        ee: ref(ee.data),
        ig: ref(ig.data),
        python: conn?.python ?? '',
        enabled: conn?.enabled ?? true,
        restart_policy: conn?.restart_policy ?? 'on-failure',
        restart_delay: String(conn?.restart_delay ?? 5),
        max_restarts: String(conn?.max_restarts ?? 0),
        buffer_size: String(conn?.buffer_size ?? 1000),
      }}
      onDone={(id) => nav(`/event-connectors/${id}`)}
      onCancel={() => nav(conn ? `/event-connectors/${conn.id}` : '/event-connectors')}
      invalidate={() => invalidate('event_connectors')}
    />
  )
}

function ConnectorFields({ conn, initial, onDone, onCancel, invalidate }: { conn?: Connector; initial: ConnValues; onDone: (id: number) => void; onCancel: () => void; invalidate: () => void }) {
  const f = useForm<ConnValues>(initial)
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !conn)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const orgId = f.values.organization?.id
  const args = parseArgs(f.values.args)
  const filters = parseFilters(f.values.filters)
  const srcErr = sourceError(f.values.source)
  const pickPreset = (source: string) => {
    const was = presetArgs(f.values.source)
    f.set('source', source)
    // Replace the arguments only while they are still the last preset's.
    if (!f.values.args.trim() || (was && yamlText(was) === f.values.args)) f.set('args', yamlText(presetArgs(source)))
  }
  const save = async () => {
    if (args.error || filters.error || srcErr) return
    setBusy(true)
    setError(null)
    const body = {
      name: f.values.name,
      description: f.values.description,
      organization: orgId,
      event_stream: f.values.stream?.id,
      source: f.values.source.trim(),
      args: args.args,
      filters: filters.filters,
      credentials: f.values.credentials.map((r) => r.id),
      execution_environment: f.values.ee?.id ?? null,
      instance_group: f.values.ig?.id ?? null,
      python: f.values.python.trim(),
      enabled: f.values.enabled,
      restart_policy: f.values.restart_policy,
      restart_delay: Number(f.values.restart_delay),
      max_restarts: Number(f.values.max_restarts),
      buffer_size: Number(f.values.buffer_size),
    }
    try {
      const saved = conn ? await api.patch<Connector>(`event_connectors/${conn.id}/`, body) : await api.post<Connector>('event_connectors/', body)
      invalidate()
      onDone(saved.id)
    } catch (e) {
      setError(e)
      if (e instanceof ApiError)
        f.setErrors({ ...e.fields, stream: e.fields.event_stream ?? '', ee: e.fields.execution_environment ?? '', ig: e.fields.instance_group ?? '' })
    } finally {
      setBusy(false)
    }
  }
  const preset = SOURCE_PRESETS.some((p) => p.source === f.values.source) ? f.values.source : ''
  return (
    <form
      className="form"
      style={{ maxWidth: 980 }}
      onSubmit={(ev) => {
        ev.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput label={tr('Name')} value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!conn} />
        <Lookup label={tr('Organization')} resource="organizations/" value={f.values.organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} />
        <Lookup
          label={tr('Event stream')}
          resource="event_streams/"
          params={orgId ? { organization: orgId } : undefined}
          value={f.values.stream}
          onChange={(v) => f.set('stream', v)}
          error={f.errors.stream}
          help={tr('The stream of the organization it posts to. It takes the right to change the stream.')}
        />
        <TextArea label={tr('Description')} optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      <section className="form-section">
        <h2>{tr('Source')}</h2>
        <div className="form-grid">
          <SelectField
            label={tr('Start from')}
            value={preset}
            onChange={(v) => v && pickPreset(v)}
            options={[['', tr('Another plugin')], ...SOURCE_PRESETS.map((p): [string, string] => [p.source, p.label])]}
            help={tr('Fills in the plugin and example arguments.')}
          />
          <TextInput
            label={tr('Plugin')}
            mono
            value={f.values.source}
            onChange={(v) => f.set('source', v)}
            error={srcErr ?? f.errors.source}
            help={tr('Its full name. The image must have its collection and the Python packages it imports.')}
          />
        </div>
        <TextArea label={tr('Arguments')} mono rows={6} full value={f.values.args} onChange={(v) => f.set('args', v)} error={args.error ?? f.errors.args} help={tr('YAML or JSON, as a rulebook’s source: the {plugin} plugin’s arguments.', { plugin: pluginShort(f.values.source) })} />
        <TextArea
          label={tr('Filters')}
          optional
          mono
          rows={3}
          full
          value={f.values.filters}
          onChange={(v) => f.set('filters', v)}
          error={filters.error ?? f.errors.filters}
          help={tr('Applied to each event in order, as a rulebook’s: - ansible.eda.json_filter: {exclude_keys: [password]}')}
        />
        <MultiLookup
          label={tr('Credentials')}
          optional
          full
          resource="credentials/"
          values={f.values.credentials}
          onChange={(v) => f.set('credentials', v)}
          error={f.errors.credentials}
          meta={(r) => <span className="faint small">{r.summary_fields?.credential_type?.name}</span>}
          help={tr('Their injectors set the plugin’s environment, its arguments (extra_vars) and private files, such as a Kafka login.')}
        />
      </section>
      <section className="form-section">
        <h2>{tr('Where it runs')}</h2>
        <div className="form-grid">
          <Lookup label={tr('Instance group')} optional resource="instance_groups/" value={f.values.ig} onChange={(v) => f.set('ig', v)} error={f.errors.ig} placeholder={asIs('default')} help={tr('Execution or hybrid nodes, or a container group. It runs on one node at a time and moves when that node is lost.')} />
          <Lookup label={tr('Image')} optional resource="execution_environments/" value={f.values.ee} onChange={(v) => f.set('ee', v)} error={f.errors.ee} meta={(e) => <span className="faint small mono">{e.image}</span>} help={tr('An execution environment with the collection, such as a decision environment.')} />
          <TextInput label={tr('Python')} optional mono value={f.values.python} onChange={(v) => f.set('python', v)} error={f.errors.python} placeholder={asIs('python3')} help={tr('Empty: ansible-rulebook’s Python if the image has it, else python3.')} />
          <TextInput label={tr('Buffer (events)')} type="number" value={f.values.buffer_size} onChange={(v) => f.set('buffer_size', v)} error={f.errors.buffer_size} help={tr('Held while awxnext is unreachable. When it is full, the source waits.')} />
        </div>
      </section>
      <section className="form-section">
        <h2>{tr('Restarts')}</h2>
        <div className="form-grid">
          <SelectField label={tr('Restart')} value={f.values.restart_policy} onChange={(v) => f.set('restart_policy', v)} options={RESTART_POLICIES} error={f.errors.restart_policy} />
          <TextInput label={tr('First delay (seconds)')} type="number" value={f.values.restart_delay} onChange={(v) => f.set('restart_delay', v)} error={f.errors.restart_delay} help={tr('It doubles with each restart in a row.')} />
          <TextInput label={tr('Restarts in a row, at most')} type="number" value={f.values.max_restarts} onChange={(v) => f.set('max_restarts', v)} error={f.errors.max_restarts} help={tr('Then it stays failed. 0 for no limit.')} />
        </div>
        <Field label={tr('State')}>
          <Check label={tr('Enabled: it runs')} checked={f.values.enabled} onChange={(v) => f.set('enabled', v)} />
        </Field>
      </section>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !f.values.organization || !f.values.stream || !f.values.source.trim() || !!args.error || !!filters.error || !!srcErr}>
          {busy ? tr('Saving…') : conn ? tr('Save changes') : tr('Create connector')}
        </button>
        <button type="button" className="btn ghost" onClick={onCancel}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}
