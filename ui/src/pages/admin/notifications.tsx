import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { useCopy } from '../../components/CopyDialog'
import { notificationEvents } from '../../components/NotificationsTab'
import { Check, Field, FormError, Lookup, SelectField, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Menu, useToast } from '../../components/overlay'
import { DataTable } from '../../components/table'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { typeLabel } from '../../lib/format'
import {
  buildMessages,
  DEFAULT_PORT,
  emailConfig,
  emailValues,
  hasCustomMessages,
  MESSAGE_EVENTS,
  messageEntries,
  parseRecipients,
  securityOf,
  type EmailConfig,
  type EmailValues,
  type MessageEntry,
  type Messages,
  type Security,
} from '../../lib/notifications'
import { ListPage, useDelete } from './common'

interface WebhookConfig {
  url?: string
  http_method?: string
  headers?: Record<string, string>
  username?: string
  password?: string
  disable_ssl_verification?: boolean
}

type NT = Base & {
  organization: number
  notification_type: string
  notification_configuration: WebhookConfig & EmailConfig
  messages: Messages
  managed_by?: string
}

const SECURITY_LABEL: Record<Security, string> = { starttls: 'STARTTLS', ssl: 'TLS from the start', none: 'None' }

/** One line saying where a notification goes. */
function destination(n: NT): string {
  const cfg = n.notification_configuration ?? {}
  if (n.notification_type === 'email') return `Email to ${(cfg.recipients ?? []).join(', ')}`
  return `${cfg.http_method ?? 'POST'} ${cfg.url ?? ''}`
}

type Notification = Base & { status: string; error: string; notifications_sent: number; subject: string; notification_type: string }

function LastDelivery({ nt }: { nt: NT }) {
  const last = (nt.summary_fields.recent_notifications as { status: string; created: string; error: string }[] | undefined)?.[0]
  if (!last) return <span className="faint small">Not sent yet</span>
  const ok = last.status === 'successful'
  return (
    <span className="status" title={last.error || undefined}>
      <Cell state={ok ? 'ok' : last.status === 'pending' ? 'pending' : 'failed'} />
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
  const copy = useCopy('notification_templates', { to: (id) => `/notifications/${id}` })
  return (
    <ListPage<NT>
      title="Notifications"
      subtitle="Emails and webhooks sent when jobs start, succeed or fail, and when approvals need a decision."
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
              <div className="cell-sub">{destination(n)}</div>
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
            <div className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
              <button className="btn sm" onClick={() => test(n)}>
                Send test
              </button>
              {n.summary_fields.user_capabilities?.copy && (
                <Menu label={`Actions for ${n.name}`} className="btn sm icon ghost">
                  <Link to={`/notifications/${n.id}/edit`}>Edit</Link>
                  <button onClick={() => copy.open(n)}>Copy…</button>
                </Menu>
              )}
            </div>
          ),
        },
      ]}
      empty={<Empty title="No notifications yet">Create an email or a webhook, then attach it to templates, projects or organizations.</Empty>}
    >
      {copy.dialog}
    </ListPage>
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
  const copy = useCopy('notification_templates', { to: (id) => `/notifications/${id}` })
  if (nt.error) return <div className="page-body"><ErrorNote error={nt.error} /></div>
  if (!nt.data) return <Loading />
  const n = nt.data
  const cfg = n.notification_configuration ?? {}
  const base = `/notifications/${id}`
  const custom = hasCustomMessages(n.messages)
  const isEmail = n.notification_type === 'email'
  return (
    <div className="page">
      {copy.dialog}
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
            {n.summary_fields.user_capabilities?.copy && (
              <button className="btn" onClick={() => copy.open(n)}>
                Copy
              </button>
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
                  <Panel title={isEmail ? 'Email' : 'Webhook'}>
                    <KV
                      items={isEmail ? [
                        ['Server', <span className="mono">{cfg.host}:{cfg.port}</span>],
                        ['Security', SECURITY_LABEL[securityOf(cfg)]],
                        ['Sender', <span className="mono">{cfg.sender}</span>],
                        ['Recipients', <span className="mono">{(cfg.recipients ?? []).join(', ')}</span>],
                        ['Login', cfg.username ? <span className="mono">{cfg.username} · password {cfg.password ? 'set' : 'not set'}</span> : <span className="faint">None</span>],
                        ['Timeout', `${cfg.timeout ?? 30} seconds`],
                        ['Messages', custom ? 'Custom' : 'Default subject and body'],
                        ['Organization', <Link to={`/organizations/${n.organization}`}>{n.summary_fields.organization?.name}</Link>],
                        ['Changed', <Ago at={n.modified} />],
                      ] : [
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
                  <UsedBy id={id} />
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
      empty={<Empty title="Nothing sent yet">Use “Send test” to try it.</Empty>}
      columns={[
        {
          key: 's',
          header: 'Result',
          render: (d) => (
            <span className="status">
              <Cell state={d.status === 'successful' ? 'ok' : d.status === 'pending' ? 'pending' : 'failed'} />
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

interface NTValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  type: string
  url: string
  http_method: string
  headers: [string, string][]
  username: string
  password: string
  disable_ssl_verification: boolean
  email: EmailValues
  custom: boolean
  entries: Record<string, MessageEntry>
}

function NTForm({ nt }: { nt?: NT }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const cfg = nt?.notification_configuration ?? {}
  const wasEmail = nt?.notification_type === 'email'
  const f = useForm<NTValues>({
    name: nt?.name ?? '',
    description: nt?.description ?? '',
    organization: nt?.summary_fields.organization ?? org,
    type: nt?.notification_type ?? 'email',
    url: wasEmail ? '' : cfg.url ?? '',
    http_method: cfg.http_method ?? 'POST',
    headers: Object.entries(cfg.headers ?? {}),
    username: wasEmail ? '' : cfg.username ?? '',
    password: wasEmail ? '' : cfg.password ?? '',
    disable_ssl_verification: !!cfg.disable_ssl_verification,
    email: emailValues(wasEmail ? cfg : {}),
    custom: hasCustomMessages(nt?.messages ?? null),
    entries: messageEntries(nt?.messages ?? null),
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !nt)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const isEmail = f.values.type === 'email'
  const e = f.values.email
  const setEmail = (patch: Partial<EmailValues>) => f.set('email', { ...f.values.email, ...patch })
  const setEntry = (key: string, patch: Partial<MessageEntry>) =>
    f.set('entries', { ...f.values.entries, [key]: { ...(f.values.entries[key] ?? { subject: '', body: '' }), ...patch } })
  const ready = isEmail ? !!e.host.trim() && !!e.sender.trim() && parseRecipients(e.recipients).length > 0 : !!f.values.url.trim()
  const save = async () => {
    setBusy(true)
    setError(null)
    const headers: Record<string, string> = {}
    for (const [k, v] of f.values.headers) if (k.trim()) headers[k.trim()] = v
    const body = {
      name: f.values.name,
      description: f.values.description,
      organization: f.values.organization?.id,
      notification_type: f.values.type,
      notification_configuration: isEmail
        ? emailConfig(e)
        : {
            url: f.values.url,
            http_method: f.values.http_method,
            headers,
            username: f.values.username,
            password: f.values.password,
            disable_ssl_verification: f.values.disable_ssl_verification,
          },
      messages: f.values.custom ? buildMessages(f.values.type, f.values.entries) : null,
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
  // Changing the security mode moves a port left at a usual value to the new mode's.
  const setSecurity = (sec: Security) => setEmail({ security: sec, port: !e.port || Object.values(DEFAULT_PORT).map(String).includes(e.port) ? String(DEFAULT_PORT[sec]) : e.port })
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
        <TextInput label="Name" value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!nt} />
        <Lookup label="Organization" resource="organizations/" value={f.values.organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} />
        <SelectField
          label="Type"
          value={f.values.type}
          onChange={(v) => f.set('type', v)}
          options={[
            ['email', 'Email'],
            ['webhook', 'Webhook'],
          ]}
          error={f.errors.notification_type}
        />
        <TextArea label="Description" optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      {isEmail ? (
        <section className="form-section">
          <h2>Email</h2>
          <div className="form-grid">
            <TextInput label="SMTP server" mono value={e.host} onChange={(v) => setEmail({ host: v })} placeholder="smtp.example.com" />
            <SelectField
              label="Security"
              value={e.security}
              onChange={(v) => setSecurity(v as Security)}
              options={[
                ['starttls', 'STARTTLS (usually port 587)'],
                ['ssl', 'TLS from the start (usually port 465)'],
                ['none', 'None (usually port 25)'],
              ]}
            />
            <TextInput label="Port" type="number" value={e.port} onChange={(v) => setEmail({ port: v })} placeholder={String(DEFAULT_PORT[e.security])} />
            <TextInput label="Timeout (seconds)" type="number" value={e.timeout} onChange={(v) => setEmail({ timeout: v })} help="For the whole exchange with the server." />
            <TextInput label="Sender" value={e.sender} onChange={(v) => setEmail({ sender: v })} placeholder="AWX <awx@example.com>" />
            <TextArea label="Recipients" mono rows={3} value={e.recipients} onChange={(v) => setEmail({ recipients: v })} help="One address per line, or separated by commas." />
            <TextInput label="Username" optional value={e.username} onChange={(v) => setEmail({ username: v })} help="awxnext logs in only when both are set." />
            <TextInput label="Password" optional type="password" value={e.password} onChange={(v) => setEmail({ password: v })} help={nt && wasEmail && cfg.password ? 'Stored encrypted; leave as is to keep it.' : undefined} />
          </div>
          {e.security === 'none' && e.username && <div className="help">Without STARTTLS or TLS, the password crosses the network unencrypted.</div>}
          {f.errors.notification_configuration && <div className="form-error">{f.errors.notification_configuration}</div>}
        </section>
      ) : (
        <section className="form-section">
          <h2>Webhook</h2>
          <div className="form-grid">
            <TextInput label="URL" mono value={f.values.url} onChange={(v) => f.set('url', v)} placeholder="https://hooks.example.com/awx" error={f.errors.notification_configuration} full />
            <SelectField label="Method" value={f.values.http_method} onChange={(v) => f.set('http_method', v)} options={[['POST', 'POST'], ['PUT', 'PUT']]} />
            <TextInput label="Username" optional value={f.values.username} onChange={(v) => f.set('username', v)} help="For HTTP basic authentication." />
            <TextInput label="Password" optional type="password" value={f.values.password} onChange={(v) => f.set('password', v)} help={nt && !wasEmail && cfg.password ? 'Stored encrypted; leave as is to keep it.' : undefined} />
            <Field label="Headers" optional full help="Sent with every request, e.g. an Authorization or routing header.">
              <div className="stack" style={{ gap: 6 }}>
                {f.values.headers.map(([k, v], i) => (
                  <div key={i} className="row">
                    <input className="input mono" style={{ flex: '0 1 260px' }} aria-label="Header name" placeholder="X-Header" value={k} onChange={(ev) => setHeader(i, ev.target.value, v)} />
                    <input className="input mono grow" aria-label="Header value" value={v} onChange={(ev) => setHeader(i, k, ev.target.value)} />
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
      )}
      <section className="form-section">
        <h2>Messages</h2>
        <Check
          label={isEmail ? 'Customize the subject and body per event' : 'Customize the request body per event'}
          checked={f.values.custom}
          onChange={(v) => f.set('custom', v)}
          help={
            <>
              {isEmail ? 'Subjects and bodies' : 'Bodies'} are templates: <span className="mono">{'{{ job.name }}'}</span>, <span className="mono">{'{{ job.status }}'}</span>, <span className="mono">{'{{ url }}'}</span>,{' '}
              <span className="mono">{'{{ job_metadata }}'}</span>, <span className="mono">{'{{ approval_node_name }}'}</span>. {isEmail ? 'Empty fields use the default subject and body.' : 'Empty events use the default JSON body.'}
            </>
          }
        />
        {f.values.custom && (
          <div className="form-grid">
            {MESSAGE_EVENTS.map((ev) =>
              isEmail ? (
                <div key={ev.key} className="stack" style={{ gap: 6 }}>
                  <TextInput label={ev.label} optional mono value={f.values.entries[ev.key]?.subject ?? ''} onChange={(v) => setEntry(ev.key, { subject: v })} placeholder="Subject" />
                  <TextArea label={<span className="sr-only">{ev.label} body</span>} mono rows={4} value={f.values.entries[ev.key]?.body ?? ''} onChange={(v) => setEntry(ev.key, { body: v })} />
                </div>
              ) : (
                <TextArea
                  key={ev.key}
                  label={ev.label}
                  optional
                  mono
                  rows={4}
                  full={false}
                  value={f.values.entries[ev.key]?.body ?? ''}
                  onChange={(v) => setEntry(ev.key, { body: v })}
                />
              ),
            )}
          </div>
        )}
        {f.errors.messages && <div className="form-error">{f.errors.messages}</div>}
      </section>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !ready || !f.values.organization}>
          {busy ? 'Saving…' : nt ? 'Save changes' : 'Create notification'}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(nt ? `/notifications/${nt.id}` : '/notifications')}>
          Cancel
        </button>
      </div>
    </form>
  )
}

type Use = { type: string; id: number; name: string; events: string[]; inventory?: number }

/** Where a use of a notification is changed: the Notifications tab of its object. */
function usePath(u: Use): string {
  switch (u.type) {
    case 'job_template':
      return `/templates/job/${u.id}/notifications`
    case 'workflow_job_template':
      return `/templates/workflow/${u.id}/notifications`
    case 'project':
      return `/projects/${u.id}/notifications`
    case 'inventory_source':
      return `/inventories/${u.inventory}/sources?source=${u.id}&notifications=1`
    case 'organization':
      return `/organizations/${u.id}/notifications`
    case 'system_job_template':
      return `/management-jobs/${u.id}/notifications`
  }
  return '/'
}

const eventLabel = (e: string) => notificationEvents.find((x) => x.key === e)?.label.toLowerCase() ?? e

function UsedBy({ id }: { id: number | string | undefined }) {
  const used = useQuery({ queryKey: [`notification_templates/${id}/used_by/`], queryFn: () => api.get<{ results: Use[] }>(`notification_templates/${id}/used_by/`) })
  const rows = used.data?.results ?? []
  return (
    <Panel title="Where it is used">
      {used.error ? (
        <ErrorNote error={used.error} />
      ) : rows.length ? (
        <ul className="stack" style={{ gap: 8, margin: 0, padding: 0, listStyle: 'none' }}>
          {rows.map((u) => (
            <li key={`${u.type}:${u.id}`}>
              <Link to={usePath(u)}>{u.name}</Link>
              <div className="faint small">
                {typeLabel(u.type)} · on {u.events.map(eventLabel).join(', ')}
              </div>
            </li>
          ))}
        </ul>
      ) : (
        !used.isPending && (
          <p className="muted small" style={{ margin: 0 }}>
            Not attached to anything you can see. Attach it from the Notifications tab of a job template, workflow, project, inventory source, organization or management job, choosing which events trigger it.
          </p>
        )
      )}
    </Panel>
  )
}
