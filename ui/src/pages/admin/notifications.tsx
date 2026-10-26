import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { useOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Check, Field, FormError, Lookup, SelectField, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { useToast } from '../../components/overlay'
import { DataTable } from '../../components/table'
import { Ago, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { ListPage, useDelete } from './common'

interface WebhookConfig {
  url?: string
  http_method?: string
  headers?: Record<string, string>
  username?: string
  password?: string
  disable_ssl_verification?: boolean
}

type Message = { message?: string | null; body?: string | null } | null
type Messages = {
  started?: Message
  success?: Message
  error?: Message
  workflow_approval?: { approved?: Message; denied?: Message; running?: Message; timed_out?: Message } | null
} | null

type NT = Base & {
  organization: number
  notification_type: string
  notification_configuration: WebhookConfig
  messages: Messages
  managed_by?: string
}

type Notification = Base & { status: string; error: string; notifications_sent: number; subject: string; notification_type: string }

function LastDelivery({ nt }: { nt: NT }) {
  const last = (nt.summary_fields.recent_notifications as { status: string; created: string; error: string }[] | undefined)?.[0]
  if (!last) return <span className="faint small">Not sent yet</span>
  const ok = last.status === 'successful'
  return (
    <span className="status" title={last.error || undefined}>
      <span aria-hidden="true" className={`sdot ${ok ? 'ok' : last.status === 'pending' ? 'pending' : 'fail'}`} />
      <span>
        {ok ? 'Delivered' : last.status === 'pending' ? 'Sending' : 'Failed'} <span className="faint small"><Ago at={last.created} /></span>
      </span>
    </span>
  )
}

function useTest() {
  const toast = useToast()
  const invalidate = useInvalidate()
  return async (nt: { id: number; name: string }) => {
    try {
      await api.post(`notification_templates/${nt.id}/test/`)
      toast(`Test sent with ${nt.name}. The result appears under Deliveries.`)
      window.setTimeout(() => invalidate('notification_templates'), 1500)
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
}

export function NotificationList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  const test = useTest()
  return (
    <ListPage<NT>
      title="Notifications"
      subtitle="Webhooks called when jobs start, succeed or fail, and when approvals need a decision."
      path="notification_templates/"
      params={scope}
      placeholder="Filter notification templates…"
      actions={
        <Link to="/notifications/new" className="btn primary">
          <Icon name="plus" size={14} />
          New notification
        </Link>
      }
      onOpen={(n) => nav(`/notifications/${n.id}`)}
      columns={[
        {
          key: 'name',
          header: 'Notification',
          sort: 'name',
          render: (n) => (
            <div>
              <Link to={`/notifications/${n.id}`} className="cell-title">
                {n.name}
              </Link>
              <div className="cell-sub">
                {n.notification_configuration?.http_method ?? 'POST'} {n.notification_configuration?.url}
              </div>
            </div>
          ),
        },
        { key: 'org', header: 'Organization', render: (n) => n.summary_fields.organization?.name ?? '—' },
        { key: 'last', header: 'Last delivery', render: (n) => <LastDelivery nt={n} /> },
        {
          key: 'x',
          header: <span className="sr-only">Actions</span>,
          className: 'end',
          render: (n) => (
            <button className="btn sm" onClick={() => test(n)}>
              Send test
            </button>
          ),
        },
      ]}
      empty={<Empty title="No notifications yet">Create a webhook, then attach it to templates, projects or organizations.</Empty>}
    />
  )
}

export function NotificationNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: 'Notifications', to: '/notifications' }, { label: 'New' }]} title="New notification" />
      <div className="page-body">
        <NTForm />
      </div>
    </div>
  )
}

export function NotificationDetail() {
  const id = Number(useParams().id)
  const nt = useObject<NT>(`notification_templates/${id}/`)
  const del = useDelete()
  const test = useTest()
  if (nt.error) return <div className="page-body"><ErrorNote error={nt.error} /></div>
  if (!nt.data) return <Loading />
  const n = nt.data
  const cfg = n.notification_configuration ?? {}
  const base = `/notifications/${id}`
  const custom = !!n.messages && Object.values(n.messages).some(Boolean)
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: 'Notifications', to: '/notifications' }, { label: n.name }]}
        title={n.name}
        subtitle={n.description || undefined}
        actions={
          <>
            <button className="btn" onClick={() => test(n)}>
              Send test
            </button>
            {n.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/edit`}>
                Edit
              </Link>
            )}
            {n.summary_fields.user_capabilities?.delete && (
              <button className="btn danger" onClick={() => del({ path: `notification_templates/${id}/`, name: n.name, what: 'notification', then: '/notifications' })}>
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
            <Tab to={`${base}/deliveries`}>Deliveries</Tab>
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
                  <Panel title="Webhook">
                    <KV
                      items={[
                        ['URL', <span className="mono">{cfg.url}</span>],
                        ['Method', cfg.http_method ?? 'POST'],
                        ['Headers', cfg.headers && Object.keys(cfg.headers).length ? <pre className="code">{Object.entries(cfg.headers).map(([k, v]) => `${k}: ${v}`).join('\n')}</pre> : <span className="faint">None</span>],
                        ['Basic auth', cfg.username ? <span className="mono">{cfg.username} · password {cfg.password ? 'set' : 'not set'}</span> : <span className="faint">None</span>],
                        ['TLS', cfg.disable_ssl_verification ? <span className="fail-text">Certificate not verified</span> : 'Certificate verified'],
                        ['Messages', custom ? 'Custom' : 'Default JSON body'],
                        ['Organization', <Link to={`/organizations/${n.organization}`}>{n.summary_fields.organization?.name}</Link>],
                        ['Changed', <Ago at={n.modified} />],
                      ]}
                    />
                  </Panel>
                </div>
                <div className="narrow">
                  <Panel title="Where it is used">
                    <p className="muted small" style={{ margin: 0 }}>
                      Attach this notification from the Notifications tab of a job template, workflow, project, inventory source or organization, choosing which events trigger it.
                    </p>
                  </Panel>
                </div>
              </div>
            }
          />
          <Route path="edit" element={<NTForm nt={n} />} />
          <Route path="deliveries" element={<Deliveries id={id} />} />
          <Route path="access" element={<AccessTab resource="notification_templates" id={id} name={n.name} />} />
        </Routes>
      </div>
    </div>
  )
}

function Deliveries({ id }: { id: number }) {
  const list = useList<Notification>(`notification_templates/${id}/notifications/`, { order_by: '-id', page_size: 50 }, { refetchInterval: 10_000 })
  return (
    <DataTable
      label="Deliveries"
      rows={list.data?.results}
      loading={list.isPending}
      error={list.error}
      empty={<Empty title="Nothing sent yet">Use “Send test” to try the webhook.</Empty>}
      columns={[
        {
          key: 's',
          header: 'Result',
          render: (d) => (
            <span className="status">
              <span aria-hidden="true" className={`sdot ${d.status === 'successful' ? 'ok' : d.status === 'pending' ? 'pending' : 'fail'}`} />
              {d.status === 'successful' ? 'Delivered' : d.status === 'pending' ? 'Sending' : 'Failed'}
            </span>
          ),
        },
        { key: 'subj', header: 'Subject', render: (d) => <span className="ellipsis" style={{ display: 'block', maxWidth: 520 }}>{d.subject || '—'}</span> },
        { key: 'err', header: 'Error', render: (d) => (d.error ? <span className="small fail-text">{d.error}</span> : <span className="faint">—</span>) },
        { key: 't', header: 'When', className: 'small nowrap', render: (d) => <Ago at={d.created} /> },
      ]}
    />
  )
}

const events: { key: string; label: string; group?: 'workflow_approval' }[] = [
  { key: 'started', label: 'Job started' },
  { key: 'success', label: 'Job succeeded' },
  { key: 'error', label: 'Job failed' },
  { key: 'running', label: 'Approval requested', group: 'workflow_approval' },
  { key: 'approved', label: 'Approval granted', group: 'workflow_approval' },
  { key: 'denied', label: 'Approval denied', group: 'workflow_approval' },
  { key: 'timed_out', label: 'Approval timed out', group: 'workflow_approval' },
]

interface NTValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  url: string
  http_method: string
  headers: [string, string][]
  username: string
  password: string
  disable_ssl_verification: boolean
  custom: boolean
  bodies: Record<string, string>
}

function initialBodies(m: Messages): Record<string, string> {
  const out: Record<string, string> = {}
  if (!m) return out
  for (const e of events) {
    const msg = e.group ? (m.workflow_approval as Record<string, Message> | null | undefined)?.[e.key] : (m as Record<string, Message>)[e.key]
    if (msg?.body) out[e.key] = msg.body
  }
  return out
}

function NTForm({ nt }: { nt?: NT }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const cfg = nt?.notification_configuration ?? {}
  const f = useForm<NTValues>({
    name: nt?.name ?? '',
    description: nt?.description ?? '',
    organization: nt?.summary_fields.organization ?? org,
    url: cfg.url ?? '',
    http_method: cfg.http_method ?? 'POST',
    headers: Object.entries(cfg.headers ?? {}),
    username: cfg.username ?? '',
    password: cfg.password ?? '',
    disable_ssl_verification: !!cfg.disable_ssl_verification,
    custom: !!nt?.messages && Object.values(nt.messages).some(Boolean),
    bodies: initialBodies(nt?.messages ?? null),
  })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const save = async () => {
    setBusy(true)
    setError(null)
    const headers: Record<string, string> = {}
    for (const [k, v] of f.values.headers) if (k.trim()) headers[k.trim()] = v
    let messages: Messages = null
    if (f.values.custom) {
      const m: Record<string, unknown> = {}
      const wa: Record<string, unknown> = {}
      for (const e of events) {
        const body = f.values.bodies[e.key]?.trim()
        if (!body) continue
        if (e.group) wa[e.key] = { message: null, body }
        else m[e.key] = { message: null, body }
      }
      if (Object.keys(wa).length) m.workflow_approval = wa
      messages = m as Messages
    }
    const body = {
      name: f.values.name,
      description: f.values.description,
      organization: f.values.organization?.id,
      notification_type: 'webhook',
      notification_configuration: {
        url: f.values.url,
        http_method: f.values.http_method,
        headers,
        username: f.values.username,
        password: f.values.password,
        disable_ssl_verification: f.values.disable_ssl_verification,
      },
      messages,
    }
    try {
      const saved = nt ? await api.patch<NT>(`notification_templates/${nt.id}/`, body) : await api.post<NT>('notification_templates/', body)
      invalidate('notification_templates')
      nav(`/notifications/${saved.id}`)
    } catch (e) {
      setError(e)
      if (e && typeof e === 'object' && 'fields' in e) f.setErrors((e as { fields: Record<string, string> }).fields)
    } finally {
      setBusy(false)
    }
  }
  const setHeader = (i: number, k: string, v: string) => f.set('headers', f.values.headers.map((h, j) => (j === i ? [k, v] : h)))
  return (
    <form
      className="form"
      style={{ maxWidth: 980 }}
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput label="Name" value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!nt} />
        <Lookup label="Organization" resource="organizations/" value={f.values.organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} />
        <TextArea label="Description" optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      <section className="form-section">
        <h2>Webhook</h2>
        <div className="form-grid">
          <TextInput label="URL" mono value={f.values.url} onChange={(v) => f.set('url', v)} placeholder="https://hooks.example.com/awx" error={f.errors.notification_configuration} full />
          <SelectField label="Method" value={f.values.http_method} onChange={(v) => f.set('http_method', v)} options={[['POST', 'POST'], ['PUT', 'PUT']]} />
          <TextInput label="Username" optional value={f.values.username} onChange={(v) => f.set('username', v)} help="For HTTP basic authentication." />
          <TextInput label="Password" optional type="password" value={f.values.password} onChange={(v) => f.set('password', v)} help={nt && cfg.password ? 'Stored encrypted; leave as is to keep it.' : undefined} />
          <Field label="Headers" optional full help="Sent with every request, e.g. an Authorization or routing header.">
            <div className="stack" style={{ gap: 6 }}>
              {f.values.headers.map(([k, v], i) => (
                <div key={i} className="row">
                  <input className="input mono" style={{ flex: '0 1 260px' }} aria-label="Header name" placeholder="X-Header" value={k} onChange={(e) => setHeader(i, e.target.value, v)} />
                  <input className="input mono grow" aria-label="Header value" value={v} onChange={(e) => setHeader(i, k, e.target.value)} />
                  <button type="button" className="btn ghost icon" aria-label="Remove header" onClick={() => f.set('headers', f.values.headers.filter((_, j) => j !== i))}>
                    <Icon name="x" size={14} />
                  </button>
                </div>
              ))}
              <div>
                <button type="button" className="btn sm" onClick={() => f.set('headers', [...f.values.headers, ['', '']])}>
                  <Icon name="plus" size={12} />
                  Add header
                </button>
              </div>
            </div>
          </Field>
        </div>
        <Check label="Skip TLS certificate verification" checked={f.values.disable_ssl_verification} onChange={(v) => f.set('disable_ssl_verification', v)} help="Only for test endpoints with self-signed certificates." />
      </section>
      <section className="form-section">
        <h2>Messages</h2>
        <Check
          label="Customize the request body per event"
          checked={f.values.custom}
          onChange={(v) => f.set('custom', v)}
          help={
            <>
              Bodies are templates: <span className="mono">{'{{ job.name }}'}</span>, <span className="mono">{'{{ job.status }}'}</span>, <span className="mono">{'{{ url }}'}</span>, <span className="mono">{'{{ job_metadata }}'}</span>,{' '}
              <span className="mono">{'{{ approval_node_name }}'}</span>. Empty events use the default JSON body.
            </>
          }
        />
        {f.values.custom && (
          <div className="form-grid">
            {events.map((e) => (
              <TextArea
                key={e.key}
                label={e.label}
                optional
                mono
                rows={4}
                full={false}
                value={f.values.bodies[e.key] ?? ''}
                onChange={(v) => f.set('bodies', { ...f.values.bodies, [e.key]: v })}
              />
            ))}
          </div>
        )}
        {f.errors.messages && <div className="form-error">{f.errors.messages}</div>}
      </section>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !f.values.url.trim() || !f.values.organization}>
          {busy ? 'Saving…' : nt ? 'Save changes' : 'Create notification'}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(nt ? `/notifications/${nt.id}` : '/notifications')}>
          Cancel
        </button>
      </div>
    </form>
  )
}
