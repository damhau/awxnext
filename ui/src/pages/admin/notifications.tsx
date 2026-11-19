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
import { Check, ChipsControl, Field, FormError, Lookup, SelectField, TextArea, TextInput, useForm } from '../../components/form'
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
  isSlackColor,
  MESSAGE_EVENTS,
  messageEntries,
  parseRecipients,
  securityOf,
  slackChannel,
  slackConfig,
  slackValues,
  type EmailConfig,
  type EmailValues,
  type MessageEntry,
  type Messages,
  type Security,
  type SlackConfig,
  type SlackValues,
} from '../../lib/notifications'
import { ListPage, useDelete } from './common'
import { asIs, tr } from '../../lib/i18n'

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
  notification_configuration: WebhookConfig & EmailConfig & SlackConfig
  messages: Messages
  managed_by?: string
}

const SECURITY_LABEL: Record<Security, string> = { starttls: 'STARTTLS', ssl: 'TLS from the start', none: 'None' }

/** One line saying where a notification goes. */
function destination(n: NT): string {
  const cfg = n.notification_configuration ?? {}
  if (n.notification_type === 'email') return `Email to ${(cfg.recipients ?? []).join(', ')}`
  if (n.notification_type === 'slack') return `Slack to ${(cfg.channels ?? []).join(' · ')}`
  if (n.notification_type === 'teams') return tr('Microsoft Teams')
  return `${cfg.http_method ?? 'POST'} ${cfg.url ?? ''}`
}

type Notification = Base & { status: string; error: string; notifications_sent: number; subject: string; notification_type: string }

function LastDelivery({ nt }: { nt: NT }) {
  const last = (nt.summary_fields.recent_notifications as { status: string; created: string; error: string }[] | undefined)?.[0]
  if (!last) return <span className="faint small">{tr('Not sent yet')}</span>
  const ok = last.status === 'successful'
  return (
    <span className="status" title={last.error || undefined}>
      <Cell state={ok ? 'ok' : last.status === 'pending' ? 'pending' : 'failed'} />
      <span>
        {ok ? tr('Delivered') : last.status === 'pending' ? tr('Sending') : tr('Failed')} <span className="faint small"><Ago at={last.created} /></span>
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
      toast(tr('Test sent with {name}. The result appears under Deliveries.', { name: nt.name }))
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
      title={tr('Notifications')}
      subtitle={tr('Emails, Slack and Teams messages, and webhooks sent when jobs start, succeed or fail, and when approvals need a decision.')}
      path="notification_templates/"
      params={scope}
      placeholder={tr('Filter notification templates…')}
      actions={
        <Link to="/notifications/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New notification')}
        </Link>
      }
      onOpen={(n) => nav(`/notifications/${n.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Notification'),
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
        { key: 'org', header: tr('Organization'), render: (n) => n.summary_fields.organization?.name ?? '—' },
        { key: 'last', header: tr('Last delivery'), render: (n) => <LastDelivery nt={n} /> },
        {
          key: 'x',
          header: <span className="sr-only">{tr('Actions')}</span>,
          className: 'end',
          render: (n) => (
            <div className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
              <button className="btn sm" onClick={() => test(n)}>
                {tr('Send test')}
              </button>
              {n.summary_fields.user_capabilities?.copy && (
                <Menu label={tr('Actions for {name}', { name: n.name })} className="btn sm icon ghost">
                  <Link to={`/notifications/${n.id}/edit`}>{tr('Edit')}</Link>
                  <button onClick={() => copy.open(n)}>{tr('Copy…')}</button>
                </Menu>
              )}
            </div>
          ),
        },
      ]}
      empty={<Empty title={tr('No notifications yet')}>{tr('Create an email, a Slack or Teams message, or a webhook, then attach it to templates, projects or organizations.')}</Empty>}
    >
      {copy.dialog}
    </ListPage>
  )
}

export function NotificationNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Notifications'), to: '/notifications' }, { label: tr('New') }]} title={tr('New notification')} />
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
  const isSlack = n.notification_type === 'slack'
  const isTeams = n.notification_type === 'teams'
  return (
    <div className="page">
      {copy.dialog}
      <PageHead
        crumbs={[{ label: tr('Notifications'), to: '/notifications' }, { label: n.name }]}
        title={n.name}
        subtitle={n.description || undefined}
        actions={
          <>
            <button className="btn" onClick={() => test(n)}>
              {tr('Send test')}
            </button>
            {n.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {n.summary_fields.user_capabilities?.copy && (
              <button className="btn" onClick={() => copy.open(n)}>
                {tr('Copy')}
              </button>
            )}
            {n.summary_fields.user_capabilities?.delete && (
              <button className="btn danger" onClick={() => del({ path: `notification_templates/${id}/`, name: n.name, what: tr('notification'), then: '/notifications' })}>
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
            <Tab to={`${base}/deliveries`}>{tr('Deliveries')}</Tab>
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
                  <Panel title={isEmail ? tr('Email') : isSlack ? tr('Slack') : isTeams ? tr('Microsoft Teams') : tr('Webhook')}>
                    <KV
                      items={isTeams ? [
                        [tr('Workflow URL'), cfg.url ? tr('Set') : <span className="fail-text">{tr('Not set')}</span>],
                        [tr('Card'), tr('The message as its title, the job’s status, template, launcher and times, and a button to the job or approval')],
                        [tr('Messages'), custom ? tr('Custom') : tr('Default messages')],
                        [tr('Organization'), <Link to={`/organizations/${n.organization}`}>{n.summary_fields.organization?.name}</Link>],
                        [tr('Changed'), <Ago at={n.modified} />],
                      ] : isSlack ? [
                        [tr('Channels'), <span className="chips">{(cfg.channels ?? []).map((c) => <span key={c} className="chip">{c}</span>)}</span>],
                        [tr('Token'), cfg.token ? tr('Set') : <span className="fail-text">{tr('Not set')}</span>],
                        [tr('Color'), cfg.hex_color ? <SlackColor color={cfg.hex_color} /> : <span className="faint">{tr('None')}</span>],
                        [tr('Messages'), custom ? tr('Custom') : tr('Default messages')],
                        [tr('Organization'), <Link to={`/organizations/${n.organization}`}>{n.summary_fields.organization?.name}</Link>],
                        [tr('Changed'), <Ago at={n.modified} />],
                      ] : isEmail ? [
                        [tr('Server'), <span className="mono">{cfg.host}:{cfg.port}</span>],
                        [tr('Security'), SECURITY_LABEL[securityOf(cfg)]],
                        [tr('Sender'), <span className="mono">{cfg.sender}</span>],
                        [tr('Recipients'), <span className="mono">{(cfg.recipients ?? []).join(', ')}</span>],
                        [tr('Login'), cfg.username ? <span className="mono">{cfg.username} {tr('· password')} {cfg.password ? tr('set') : tr('not set')}</span> : <span className="faint">{tr('None')}</span>],
                        [tr('Timeout'), tr('{v} seconds', { v: cfg.timeout ?? 30 })],
                        [tr('Messages'), custom ? tr('Custom') : tr('Default subject and body')],
                        [tr('Organization'), <Link to={`/organizations/${n.organization}`}>{n.summary_fields.organization?.name}</Link>],
                        [tr('Changed'), <Ago at={n.modified} />],
                      ] : [
                        [tr('URL'), <span className="mono">{cfg.url}</span>],
                        [tr('Method'), cfg.http_method ?? tr('POST')],
                        [tr('Headers'), cfg.headers && Object.keys(cfg.headers).length ? <pre className="code">{Object.entries(cfg.headers).map(([k, v]) => `${k}: ${v}`).join('\n')}</pre> : <span className="faint">{tr('None')}</span>],
                        [tr('Basic auth'), cfg.username ? <span className="mono">{cfg.username} {tr('· password')} {cfg.password ? tr('set') : tr('not set')}</span> : <span className="faint">{tr('None')}</span>],
                        [tr('TLS'), cfg.disable_ssl_verification ? <span className="fail-text">{tr('Certificate not verified')}</span> : tr('Certificate verified')],
                        [tr('Messages'), custom ? tr('Custom') : tr('Default JSON body')],
                        [tr('Organization'), <Link to={`/organizations/${n.organization}`}>{n.summary_fields.organization?.name}</Link>],
                        [tr('Changed'), <Ago at={n.modified} />],
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

function SlackColor({ color }: { color: string }) {
  if (!isSlackColor(color)) return <span className="mono">{color} <span className="faint">{tr('· not #RGB or #RRGGBB: sent without a color')}</span></span>
  return (
    <span className="row" style={{ gap: 6 }}>
      <span aria-hidden style={{ width: 12, height: 12, borderRadius: 3, background: color, border: '1px solid var(--line)' }} />
      <span className="mono">{color}</span>
    </span>
  )
}

function Deliveries({ id }: { id: number }) {
  const list = useList<Notification>(`notification_templates/${id}/notifications/`, { order_by: '-id', page_size: 50 }, { refetchInterval: 10_000 })
  return (
    <DataTable
      label={tr('Deliveries')}
      rows={list.data?.results}
      loading={list.isPending}
      error={list.error}
      empty={<Empty title={tr('Nothing sent yet')}>{tr('Use “Send test” to try it.')}</Empty>}
      columns={[
        {
          key: 's',
          header: tr('Result'),
          render: (d) => (
            <span className="status">
              <Cell state={d.status === 'successful' ? 'ok' : d.status === 'pending' ? 'pending' : 'failed'} />
              {d.status === 'successful' ? tr('Delivered') : d.status === 'pending' ? tr('Sending') : tr('Failed')}
            </span>
          ),
        },
        { key: 'subj', header: tr('Subject'), render: (d) => <span className="ellipsis" style={{ display: 'block', maxWidth: 520 }}>{d.subject || '—'}</span> },
        { key: 'err', header: tr('Error'), render: (d) => (d.error ? <span className="small fail-text">{d.error}</span> : <span className="faint">—</span>) },
        { key: 't', header: tr('When'), className: 'small nowrap', render: (d) => <Ago at={d.created} /> },
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
  slack: SlackValues
  teamsUrl: string
  custom: boolean
  entries: Record<string, MessageEntry>
}

function NTForm({ nt }: { nt?: NT }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const cfg = nt?.notification_configuration ?? {}
  const wasEmail = nt?.notification_type === 'email'
  const wasSlack = nt?.notification_type === 'slack'
  const wasTeams = nt?.notification_type === 'teams'
  const notWebhook = wasEmail || wasSlack || wasTeams
  const f = useForm<NTValues>({
    name: nt?.name ?? '',
    description: nt?.description ?? '',
    organization: nt?.summary_fields.organization ?? org,
    type: nt?.notification_type ?? 'email',
    url: notWebhook ? '' : cfg.url ?? '',
    http_method: cfg.http_method ?? 'POST',
    headers: Object.entries(cfg.headers ?? {}),
    username: notWebhook ? '' : cfg.username ?? '',
    password: notWebhook ? '' : cfg.password ?? '',
    disable_ssl_verification: !!cfg.disable_ssl_verification,
    email: emailValues(wasEmail ? cfg : {}),
    slack: slackValues(wasSlack ? cfg : {}),
    teamsUrl: wasTeams ? cfg.url ?? '' : '',
    custom: hasCustomMessages(nt?.messages ?? null),
    entries: messageEntries(nt?.messages ?? null),
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !nt)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const isEmail = f.values.type === 'email'
  const isSlack = f.values.type === 'slack'
  const isTeams = f.values.type === 'teams'
  const messageOnly = isSlack || isTeams
  const e = f.values.email
  const sl = f.values.slack
  const setEmail = (patch: Partial<EmailValues>) => f.set('email', { ...f.values.email, ...patch })
  const setSlack = (patch: Partial<SlackValues>) => f.set('slack', { ...f.values.slack, ...patch })
  const badChannels = sl.channels.filter((c) => !slackChannel(c))
  const setEntry = (key: string, patch: Partial<MessageEntry>) =>
    f.set('entries', { ...f.values.entries, [key]: { ...(f.values.entries[key] ?? { subject: '', body: '' }), ...patch } })
  const ready = isEmail
    ? !!e.host.trim() && !!e.sender.trim() && parseRecipients(e.recipients).length > 0
    : isSlack
      ? !!sl.token && sl.channels.length > 0 && !badChannels.length
      : isTeams
        ? !!f.values.teamsUrl.trim()
        : !!f.values.url.trim()
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
        : isSlack
          ? slackConfig(sl)
          : isTeams
            ? { url: f.values.teamsUrl.trim() }
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
        <TextInput label={tr('Name')} value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!nt} />
        <Lookup label={tr('Organization')} resource="organizations/" value={f.values.organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} />
        <SelectField
          label={tr('Type')}
          value={f.values.type}
          onChange={(v) => f.set('type', v)}
          options={[
            ['email', tr('Email')],
            ['slack', tr('Slack')],
            ['teams', tr('Microsoft Teams')],
            ['webhook', tr('Webhook')],
          ]}
          error={f.errors.notification_type}
        />
        <TextArea label={tr('Description')} optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      {isEmail ? (
        <section className="form-section">
          <h2>{tr('Email')}</h2>
          <div className="form-grid">
            <TextInput label={tr('SMTP server')} mono value={e.host} onChange={(v) => setEmail({ host: v })} placeholder={tr('smtp.example.com')} />
            <SelectField
              label={tr('Security')}
              value={e.security}
              onChange={(v) => setSecurity(v as Security)}
              options={[
                ['starttls', 'STARTTLS (usually port 587)'],
                ['ssl', 'TLS from the start (usually port 465)'],
                ['none', tr('None (usually port 25)')],
              ]}
            />
            <TextInput label={tr('Port')} type="number" value={e.port} onChange={(v) => setEmail({ port: v })} placeholder={String(DEFAULT_PORT[e.security])} />
            <TextInput label={tr('Timeout (seconds)')} type="number" value={e.timeout} onChange={(v) => setEmail({ timeout: v })} help={tr('For the whole exchange with the server.')} />
            <TextInput label={tr('Sender')} value={e.sender} onChange={(v) => setEmail({ sender: v })} placeholder={tr('AWX <awx@example.com>')} />
            <TextArea label={tr('Recipients')} mono rows={3} value={e.recipients} onChange={(v) => setEmail({ recipients: v })} help={tr('One address per line, or separated by commas.')} />
            <TextInput label={tr('Username')} optional value={e.username} onChange={(v) => setEmail({ username: v })} help={tr('awxnext logs in only when both are set.')} />
            <TextInput label={tr('Password')} optional type="password" value={e.password} onChange={(v) => setEmail({ password: v })} help={nt && wasEmail && cfg.password ? tr('Stored encrypted; leave as is to keep it.') : undefined} />
          </div>
          {e.security === 'none' && e.username && <div className="help">{tr('Without STARTTLS or TLS, the password crosses the network unencrypted.')}</div>}
          {f.errors.notification_configuration && <div className="form-error">{f.errors.notification_configuration}</div>}
        </section>
      ) : isSlack ? (
        <section className="form-section">
          <h2>{tr('Slack')}</h2>
          <div className="form-grid">
            <TextInput
              label={tr('Bot token')}
              type="password"
              mono
              value={sl.token}
              onChange={(v) => setSlack({ token: v })}
              placeholder={asIs('xoxb-…')}
              help={nt && wasSlack && cfg.token ? tr('Stored encrypted; leave as is to keep it.') : tr('A bot token with the chat:write scope. Invite the bot to each channel.')}
            />
            <Field
              label={tr('Color')}
              optional
              htmlFor="slack-color"
              error={sl.color.trim() && !isSlackColor(sl.color) ? tr('#RGB or #RRGGBB, or Slack sends the message without a color.') : undefined}
              help={tr('Sends the message as an attachment with this color bar.')}
            >
              <div className="row" style={{ gap: 6 }}>
                <input id="slack-color" className="input mono" value={sl.color} placeholder={asIs('#36a64f')} onChange={(ev) => setSlack({ color: ev.target.value })} />
                <input
                  type="color"
                  aria-label={tr('Pick a color')}
                  value={isSlackColor(sl.color) && sl.color.trim().length === 7 ? sl.color.trim() : '#36a64f'}
                  onChange={(ev) => setSlack({ color: ev.target.value })}
                  style={{ width: 36, height: 32, padding: 2, border: '1px solid var(--line)', borderRadius: 4, background: 'none' }}
                />
              </div>
            </Field>
            <Field
              label={tr('Channels')}
              full
              htmlFor="slack-channels"
              error={badChannels.length ? tr('Not a channel: {list}. Write #name, or #name, <message ts>.', { list: badChannels.join('; ') }) : f.errors.notification_configuration}
              help={tr('One per line, as #name or a channel ID. To reply in a thread, add the message’s ts after a comma: #ops, 1700000000.000100.')}
            >
              <ChipsControl id="slack-channels" lines value={sl.channels} onChange={(v) => setSlack({ channels: v as string[] })} disabled={false} placeholder={asIs('#ops')} />
            </Field>
          </div>
        </section>
      ) : isTeams ? (
        <section className="form-section">
          <h2>{tr('Microsoft Teams')}</h2>
          <div className="form-grid">
            <TextInput
              label={tr('Workflow URL')}
              type="password"
              mono
              full
              value={f.values.teamsUrl}
              onChange={(v) => f.set('teamsUrl', v)}
              placeholder={asIs('https://…/workflows/…/triggers/manual/paths/invoke?…&sig=…')}
              error={f.errors.notification_configuration}
              help={nt && wasTeams && cfg.url ? tr('Stored encrypted; leave as is to keep it.') : tr('Its signature lets anyone who has it post to the channel, so it is stored encrypted.')}
            />
          </div>
          <div className="help">
            {tr('To make it, open the channel’s ⋯ menu in Teams, then Workflows, and pick “Send webhook alerts to a channel” (earlier “Post to a channel when a webhook request is received”). Copy the URL the workflow shows once saved.')}{' '}
            {tr('Each event posts a card: the message as its title, the job’s status, template, launcher and times, and a button to the job or approval.')}
          </div>
        </section>
      ) : (
        <section className="form-section">
          <h2>{tr('Webhook')}</h2>
          <div className="form-grid">
            <TextInput label={tr('URL')} mono value={f.values.url} onChange={(v) => f.set('url', v)} placeholder={tr('https://hooks.example.com/awx')} error={f.errors.notification_configuration} full />
            <SelectField label={tr('Method')} value={f.values.http_method} onChange={(v) => f.set('http_method', v)} options={[['POST', 'POST'], ['PUT', 'PUT']]} />
            <TextInput label={tr('Username')} optional value={f.values.username} onChange={(v) => f.set('username', v)} help={tr('For HTTP basic authentication.')} />
            <TextInput label={tr('Password')} optional type="password" value={f.values.password} onChange={(v) => f.set('password', v)} help={nt && !wasEmail && cfg.password ? tr('Stored encrypted; leave as is to keep it.') : undefined} />
            <Field label={tr('Headers')} optional full help={tr('Sent with every request, e.g. an Authorization or routing header.')}>
              <div className="stack" style={{ gap: 6 }}>
                {f.values.headers.map(([k, v], i) => (
                  <div key={i} className="row">
                    <input className="input mono" style={{ flex: '0 1 260px' }} aria-label={tr('Header name')} placeholder={tr('X-Header')} value={k} onChange={(ev) => setHeader(i, ev.target.value, v)} />
                    <input className="input mono grow" aria-label={tr('Header value')} value={v} onChange={(ev) => setHeader(i, k, ev.target.value)} />
                    <button type="button" className="btn ghost icon" aria-label={tr('Remove header')} onClick={() => f.set('headers', f.values.headers.filter((_, j) => j !== i))}>
                      <Icon name="x" size={14} />
                    </button>
                  </div>
                ))}
                <div>
                  <button type="button" className="btn sm" onClick={() => f.set('headers', [...f.values.headers, ['', '']])}>
                    <Icon name="plus" size={12} />
                    {tr('Add header')}
                  </button>
                </div>
              </div>
            </Field>
          </div>
          <Check label={tr('Skip TLS certificate verification')} checked={f.values.disable_ssl_verification} onChange={(v) => f.set('disable_ssl_verification', v)} help={tr('Only for test endpoints with self-signed certificates.')} />
        </section>
      )}
      <section className="form-section">
        <h2>{tr('Messages')}</h2>
        <Check
          label={isEmail ? tr('Customize the subject and body per event') : messageOnly ? tr('Customize the message per event') : tr('Customize the request body per event')}
          checked={f.values.custom}
          onChange={(v) => f.set('custom', v)}
          help={
            <>
              {isEmail ? tr('Subjects and bodies') : messageOnly ? tr('Messages') : tr('Bodies')} {tr('are templates:')} <span className="mono">{tr('{{ job.name }}')}</span>, <span className="mono">{tr('{{ job.status }}')}</span>, <span className="mono">{tr('{{ url }}')}</span>,{' '}
              <span className="mono">{tr('{{ job_metadata }}')}</span>, <span className="mono">{tr('{{ approval_node_name }}')}</span>,{' '}
              <span className="mono">{tr('{{ median_elapsed }}')}</span> {tr('(slow runs)')}.{' '}
              {isEmail ? tr('Empty fields use the default subject and body.') : messageOnly ? tr('Empty events use the default message.') : tr('Empty events use the default JSON body.')}
            </>
          }
        />
        {f.values.custom && (
          <div className="form-grid">
            {MESSAGE_EVENTS.map((ev) =>
              messageOnly ? (
                <TextInput key={ev.key} label={ev.label} optional mono value={f.values.entries[ev.key]?.subject ?? ''} onChange={(v) => setEntry(ev.key, { subject: v })} />
              ) : isEmail ? (
                <div key={ev.key} className="stack" style={{ gap: 6 }}>
                  <TextInput label={ev.label} optional mono value={f.values.entries[ev.key]?.subject ?? ''} onChange={(v) => setEntry(ev.key, { subject: v })} placeholder={tr('Subject')} />
                  <TextArea label={<span className="sr-only">{ev.label} {tr('body')}</span>} mono rows={4} value={f.values.entries[ev.key]?.body ?? ''} onChange={(v) => setEntry(ev.key, { body: v })} />
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
          {busy ? tr('Saving…') : nt ? tr('Save changes') : tr('Create notification')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(nt ? `/notifications/${nt.id}` : '/notifications')}>
          {tr('Cancel')}
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
    <Panel title={tr('Where it is used')}>
      {used.error ? (
        <ErrorNote error={used.error} />
      ) : rows.length ? (
        <ul className="stack" style={{ gap: 8, margin: 0, padding: 0, listStyle: 'none' }}>
          {rows.map((u) => (
            <li key={`${u.type}:${u.id}`}>
              <Link to={usePath(u)}>{u.name}</Link>
              <div className="faint small">
                {typeLabel(u.type)} {tr('· on')} {u.events.map(eventLabel).join(', ')}
              </div>
            </li>
          ))}
        </ul>
      ) : (
        !used.isPending && (
          <p className="muted small" style={{ margin: 0 }}>
            {tr('Not attached to anything you can see. Attach it from the Notifications tab of a job template, workflow, project, inventory source, organization or management job, choosing which events trigger it.')}
          </p>
        )
      )}
    </Panel>
  )
}
