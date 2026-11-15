import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any, Base, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Check, Field, FormError, Lookup, Segmented, SelectField, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { useToast } from '../../components/overlay'
import { UsedByPanel } from '../../components/UsedBy'
import { Ago, Empty, ErrorNote, KV, Loading, Maybe, PageHead, Panel, Tab } from '../../components/ui'
import { absolute } from '../../lib/format'
import { setupSnippets, STREAM_TYPES, usualCredential } from '../../lib/eventRules'
import { asIs, tr, trn } from '../../lib/i18n'
import { CopyButton, FilterBox, ListPage, useDelete } from '../admin/common'
import { DecisionList } from './decisions'

type Stream = Base & {
  organization: number
  stream_type: string
  credential: number | null
  test_mode: boolean
  retention_days: number
  max_payload_bytes: number
  rate_limit: number
  mapping: Record<string, string>
  uuid: string
  receiver_url: string
  last_sequence: number
  last_event_at: string | null
}

const typeName = (t: string) => STREAM_TYPES.find(([v]) => v === t)?.[1] ?? t

function TestTag() {
  return (
    <span className="tag" title={tr('Rules don’t see the events of a stream in test mode.')}>
      {tr('test mode')}
    </span>
  )
}

export function StreamList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Stream>
      title={tr('Event streams')}
      subtitle={tr('Where Alertmanager, GitHub, GitLab and other senders post their events, for rules to act on.')}
      path="event_streams/"
      params={scope}
      placeholder={tr('Filter event streams…')}
      actions={
        <Link to="/event-streams/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New stream')}
        </Link>
      }
      onOpen={(s) => nav(`/event-streams/${s.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Stream'),
          sort: 'name',
          render: (s) => (
            <div>
              <span className="row" style={{ gap: 6 }}>
                <Link to={`/event-streams/${s.id}`} className="cell-title">
                  {s.name}
                </Link>
                {s.test_mode && <TestTag />}
              </span>
              <div className="cell-sub">{typeName(s.stream_type)}</div>
            </div>
          ),
        },
        { key: 'org', header: tr('Organization'), render: (s) => s.summary_fields.organization?.name ?? '—' },
        { key: 'events', header: tr('Events'), className: 'num', sort: 'last_sequence', render: (s) => <span className="mono">{s.last_sequence}</span> },
        { key: 'last', header: tr('Last event'), sort: 'last_event_at', render: (s) => (s.last_event_at ? <Ago at={s.last_event_at} /> : <span className="faint small">{tr('None yet')}</span>) },
      ]}
      empty={<Empty title={tr('No event streams yet')}>{tr('Create a stream, point a sender at its URL, then write rules on the events it receives.')}</Empty>}
    />
  )
}

export function StreamNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Event streams'), to: '/event-streams' }, { label: tr('New') }]} title={tr('New event stream')} />
      <div className="page-body">
        <StreamForm />
      </div>
    </div>
  )
}

export function StreamDetail() {
  const id = Number(useParams().id)
  const stream = useObject<Stream>(`event_streams/${id}/`)
  const del = useDelete()
  if (stream.error) return <div className="page-body"><ErrorNote error={stream.error} /></div>
  if (!stream.data) return <Loading />
  const s = stream.data
  const caps = s.summary_fields.user_capabilities ?? {}
  const base = `/event-streams/${id}`
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Event streams'), to: '/event-streams' }, { label: s.name }]}
        title={s.name}
        badge={s.test_mode ? <TestTag /> : undefined}
        subtitle={s.description || typeName(s.stream_type)}
        actions={
          <>
            <Link className="btn" to={`/event-rules/new?stream=${id}`}>
              {tr('New rule')}
            </Link>
            {caps.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path: `event_streams/${id}/`, name: s.name, what: tr('event stream'), then: '/event-streams' })}>
                {tr('Delete')}
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end n={s.last_sequence}>
              {tr('Events')}
            </Tab>
            <Tab to={`${base}/setup`}>{tr('Setup')}</Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route index element={<LiveEvents stream={s} />} />
          <Route path="setup" element={<Setup stream={s} />} />
          <Route path="edit" element={<StreamForm stream={s} />} />
          <Route path="access" element={<AccessTab resource="event_streams" id={id} name={s.name} />} />
        </Routes>
      </div>
    </div>
  )
}

const pretty = (v: unknown) => JSON.stringify(v, null, 2)

/** The stream's events as they arrive, newest first. */
function LiveEvents({ stream }: { stream: Stream }) {
  const [live, setLive] = useState(true)
  const [q, setQ] = useState('')
  const list = useList<Any>(`event_streams/${stream.id}/events/`, { order_by: '-sequence', page_size: 50, search: q || undefined }, { refetchInterval: live ? 3000 : false })
  const rows = list.data?.results
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row wrap" style={{ gap: 12 }}>
        <div className="grow" style={{ minWidth: 220 }}>
          <FilterBox value={q} onChange={setQ} placeholder={tr('Filter by type, subject, source or ID…')} />
        </div>
        <label className="check">
          <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} />
          <span>{tr('Live')}</span>
          {live && <span aria-hidden="true" className="live-dot" />}
        </label>
      </div>
      {stream.test_mode && <div className="notice accent">{tr('The stream is in test mode: it keeps and shows its events, and rules don’t see them.')}</div>}
      {list.error ? (
        <ErrorNote error={list.error} />
      ) : !rows ? (
        <Loading rows={4} />
      ) : !rows.length ? (
        q ? (
          <Empty title={tr('No event matches')} />
        ) : (
          <Empty title={tr('No events yet')} action={<Link className="btn" to={`/event-streams/${stream.id}/setup`}>{tr('Set up a sender')}</Link>}>
            {tr('Events appear here as the stream receives them.')}
          </Empty>
        )
      ) : (
        <div className="events" role="list" aria-label={tr('Events')}>
          {rows.map((e) => (
            <details key={e.id} className="event" role="listitem">
              <summary>
                <span className="mono faint">#{e.sequence}</span>
                <span className="mono event-type ellipsis">{e.event_type}</span>
                <span className="small muted ellipsis event-subject">{e.subject || e.source}</span>
                <span className="event-tag">{e.test_mode && <TestTag />}</span>
                <span className="small faint nowrap">
                  <Ago at={e.received} />
                </span>
              </summary>
              <div className="stack" style={{ gap: 8 }}>
                <pre className="code event-data">{pretty(e.data)}</pre>
                <div>
                  <Link to={`/event-streams/${stream.id}/events/${e.sequence}`}>{tr('Open the event and its decisions')}</Link>
                </div>
              </div>
            </details>
          ))}
        </div>
      )}
      {list.data && list.data.count > rows!.length && <p className="small faint">{tr('The latest {n} of {count} events. Filter to find older ones.', { n: rows!.length, count: list.data.count })}</p>}
    </div>
  )
}

/** The credential type's namespace, which says what the sender sends. */
function useCredentialKind(stream: Stream): string {
  const cred = useObject<Any>(stream.credential ? `credentials/${stream.credential}/` : null, { retry: false })
  const ct = cred.data?.credential_type
  const type = useObject<Any>(ct ? `credential_types/${ct}/` : null, { retry: false })
  return type.data?.namespace ?? usualCredential(stream.stream_type)
}

function Setup({ stream }: { stream: Stream }) {
  const toast = useToast()
  const invalidate = useInvalidate()
  const kind = useCredentialKind(stream)
  const snippets = setupSnippets(stream.receiver_url, kind)
  const preferred = snippets.find((x) => x.key === stream.stream_type)?.key ?? (stream.stream_type === 'grafana' ? 'alertmanager' : snippets[snippets.length - 1].key)
  const [shown, setShown] = useState(preferred)
  const snippet = snippets.find((x) => x.key === shown) ?? snippets[0]
  const [busy, setBusy] = useState(false)
  const caps = stream.summary_fields.user_capabilities ?? {}
  const toggleTest = async () => {
    setBusy(true)
    try {
      await api.patch(`event_streams/${stream.id}/`, { test_mode: !stream.test_mode })
      invalidate('event_streams')
      toast(stream.test_mode ? tr('Test mode is off: rules see the events the stream receives from now on.') : tr('Test mode is on: rules don’t see the events the stream receives.'))
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      setBusy(false)
    }
  }
  const cred = stream.summary_fields.credential
  return (
    <div className="split">
      <div className="wide stack" style={{ gap: 16 }}>
        <Panel title={tr('Receiver')}>
          <div className="stack" style={{ gap: 10 }}>
            <div className="row">
              <pre className="code grow" style={{ margin: 0 }}>
                {stream.receiver_url}
              </pre>
              <CopyButton text={stream.receiver_url} label={tr('Copy')} />
            </div>
            <KV
              items={[
                [tr('Format'), typeName(stream.stream_type)],
                [tr('Credential'), cred ? <Link to={`/credentials/${cred.id}`}>{cred.name}</Link> : <span className="fail-text">{tr('None: the stream refuses every request.')}</span>],
                [tr('Rate limit'), stream.rate_limit ? trn(stream.rate_limit, '{n} event a minute', '{n} events a minute') : tr('None')],
                [tr('Largest request'), tr('{n} KiB', { n: Math.round(stream.max_payload_bytes / 1024) })],
                [tr('Keeps events'), trn(stream.retention_days, '{n} day', '{n} days')],
              ]}
            />
          </div>
        </Panel>
        <Panel title={tr('Point a sender at it')}>
          <div className="stack" style={{ gap: 12 }}>
            <Segmented legend={<span className="sr-only">{tr('Sender')}</span>} value={shown} onChange={setShown} options={snippets.map((x): [string, string] => [x.key, x.title])} />
            {snippet.steps && (
              <ol className="steps small">
                {snippet.steps.map((s) => (
                  <li key={s}>{s}</li>
                ))}
              </ol>
            )}
            <div className="stack" style={{ gap: 6 }}>
              <pre className="code">{snippet.code}</pre>
              <div>
                <CopyButton text={snippet.code} label={tr('Copy')} />
              </div>
            </div>
          </div>
        </Panel>
      </div>
      <div className="narrow stack" style={{ gap: 16 }}>
        <Panel title={tr('Test mode')}>
          <div className="stack" style={{ gap: 10 }}>
            <p className="small muted" style={{ margin: 0 }}>
              {tr('In test mode, the stream keeps and shows the events it receives, and rules don’t see them, then or later. Use it to set up a sender and write rules against its real events before they act.')}
            </p>
            {caps.edit && (
              <div>
                <button className={`btn ${stream.test_mode ? 'primary' : ''}`} disabled={busy} onClick={toggleTest}>
                  {stream.test_mode ? tr('Leave test mode') : tr('Turn on test mode')}
                </button>
              </div>
            )}
          </div>
        </Panel>
        <UsedByPanel resource="event_streams" id={stream.id} empty={tr('No rule reads this stream yet.')} />
      </div>
    </div>
  )
}

/** One event of a stream, with each rule's decision on it. */
export function EventPage() {
  const { id, seq } = useParams()
  const stream = useObject<Stream>(`event_streams/${id}/`)
  const found = useList<Any>('stream_events/', { stream: id, sequence: seq, page_size: 1 })
  const ev = found.data?.results[0]
  const err = stream.error ?? found.error
  if (err) return <div className="page-body"><ErrorNote error={err} /></div>
  if (!stream.data || !found.data) return <Loading />
  const s = stream.data
  const crumbs = [{ label: tr('Event streams'), to: '/event-streams' }, { label: s.name, to: `/event-streams/${s.id}` }, { label: asIs(`#${seq}`) }]
  if (!ev)
    return (
      <div className="page">
        <PageHead crumbs={crumbs} title={tr('Event #{seq}', { seq })} />
        <div className="page-body">
          <Empty title={tr('No such event')}>{tr('The stream has no event of this number: it may be past its retention.')}</Empty>
        </div>
      </div>
    )
  return (
    <div className="page">
      <PageHead
        crumbs={crumbs}
        title={ev.event_type}
        badge={ev.test_mode ? <TestTag /> : undefined}
        subtitle={tr('Event #{seq} of {stream}, received {when}', { seq: ev.sequence, stream: s.name, when: absolute(ev.received) })}
        actions={
          <Link className="btn" to={`/event-rules/new?stream=${s.id}`}>
            {tr('Write a rule')}
          </Link>
        }
      />
      <div className="page-body">
        <div className="split">
          <div className="wide stack" style={{ gap: 16 }}>
            <Panel title={tr('Decisions')}>
              <DecisionList
                path={`stream_events/${ev.id}/decisions/`}
                by="event"
                empty={
                  <Empty title={tr('No decision on this event')}>
                    {ev.test_mode ? tr('Rules don’t see the events of a stream in test mode.') : tr('No rule read it yet, or no rule reads this stream.')}
                  </Empty>
                }
              />
            </Panel>
            <Panel title={tr('Data')}>
              <pre className="code event-data">{pretty(ev.data)}</pre>
            </Panel>
          </div>
          <div className="narrow">
            <Panel title={tr('Event')}>
              <KV
                items={[
                  [tr('ID'), <span className="mono">{ev.event_id}</span>],
                  [tr('Source'), <span className="mono">{ev.source}</span>],
                  [tr('Subject'), <Maybe v={ev.subject} mono />],
                  [tr('Time'), ev.event_time ? absolute(ev.event_time) : ''],
                  [tr('Received'), <Ago at={ev.received} />],
                  [tr('Content type'), <Maybe v={ev.data_content_type} mono />],
                  [tr('Extensions'), ev.extensions && Object.keys(ev.extensions).length ? <pre className="code">{pretty(ev.extensions)}</pre> : ''],
                ]}
              />
            </Panel>
          </div>
        </div>
      </div>
    </div>
  )
}

const MAPPING_FIELDS: [string, string][] = [
  ['id', tr('ID')],
  ['type', tr('Type')],
  ['source', tr('Source')],
  ['subject', tr('Subject')],
  ['time', tr('Time')],
]

interface StreamValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  stream_type: string
  credential: Ref | null
  test_mode: boolean
  retention_days: string
  max_payload_kib: string
  rate_limit: string
  mapping: Record<string, string>
}

function StreamForm({ stream }: { stream?: Stream }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const invalidate = useInvalidate()
  const f = useForm<StreamValues>({
    name: stream?.name ?? '',
    description: stream?.description ?? '',
    organization: stream?.summary_fields.organization ?? org,
    stream_type: stream?.stream_type ?? 'alertmanager',
    credential: stream?.summary_fields.credential ?? null,
    // A new stream starts in test mode, so that rules see nothing until its sender is set up.
    test_mode: stream?.test_mode ?? true,
    retention_days: String(stream?.retention_days ?? 7),
    max_payload_kib: String(Math.round((stream?.max_payload_bytes ?? 1 << 20) / 1024)),
    rate_limit: String(stream?.rate_limit ?? 6000),
    mapping: stream?.mapping ?? {},
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !stream)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    setError(null)
    const mapping: Record<string, string> = {}
    for (const [k] of MAPPING_FIELDS) if (f.values.mapping[k]?.trim()) mapping[k] = f.values.mapping[k].trim()
    const body = {
      name: f.values.name,
      description: f.values.description,
      organization: f.values.organization?.id,
      stream_type: f.values.stream_type,
      credential: f.values.credential?.id ?? null,
      test_mode: f.values.test_mode,
      retention_days: Number(f.values.retention_days),
      max_payload_bytes: Math.round(Number(f.values.max_payload_kib) * 1024),
      rate_limit: Number(f.values.rate_limit),
      mapping: f.values.stream_type === 'generic' ? mapping : {},
    }
    try {
      const saved = stream ? await api.patch<Stream>(`event_streams/${stream.id}/`, body) : await api.post<Stream>('event_streams/', body)
      invalidate('event_streams')
      nav(stream ? `/event-streams/${saved.id}` : `/event-streams/${saved.id}/setup`)
    } catch (e) {
      setError(e)
      if (e instanceof ApiError) f.setErrors({ ...e.fields, max_payload_kib: e.fields.max_payload_bytes ?? '' })
    } finally {
      setBusy(false)
    }
  }
  const usual = usualCredential(f.values.stream_type)
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
        <TextInput label={tr('Name')} value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!stream} />
        <Lookup label={tr('Organization')} resource="organizations/" value={f.values.organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} />
        <SelectField label={tr('Sender')} value={f.values.stream_type} onChange={(v) => f.set('stream_type', v)} options={STREAM_TYPES} error={f.errors.stream_type} help={tr('The format the sender posts. Each event becomes a CloudEvent.')} />
        <Lookup
          label={tr('Credential')}
          resource="credentials/"
          params={{ credential_type__namespace__startswith: 'event_stream' }}
          value={f.values.credential}
          onChange={(v) => f.set('credential', v)}
          error={f.errors.credential}
          meta={(r) => <span className="faint small">{r.summary_fields?.credential_type?.name}</span>}
          help={
            <>
              {usual === 'event_stream_github' ? tr('A GitHub Event Stream credential, with the webhook’s secret.') : usual === 'event_stream_gitlab' ? tr('A GitLab Event Stream credential, with the webhook’s token.') : tr('A Token Event Stream credential usually.')}{' '}
              <Link to="/credentials/new">{tr('New credential')}</Link>
            </>
          }
        />
        <TextArea label={tr('Description')} optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      {f.values.stream_type === 'generic' && (
        <section className="form-section">
          <h2>{tr('Mapping')}</h2>
          <p className="small muted" style={{ margin: 0 }}>
            {tr('JSONPaths to the event’s fields in the body, such as $.alert.id. Without an ID, every request is a new event.')}
          </p>
          <div className="form-grid">
            {MAPPING_FIELDS.map(([k, l]) => (
              <TextInput key={k} label={l} optional mono value={f.values.mapping[k] ?? ''} onChange={(v) => f.set('mapping', { ...f.values.mapping, [k]: v })} placeholder={tr('$.{field}', { field: k })} />
            ))}
          </div>
          {f.errors.mapping && <div className="form-error">{f.errors.mapping}</div>}
        </section>
      )}
      <section className="form-section">
        <h2>{tr('Limits')}</h2>
        <div className="form-grid">
          <TextInput label={tr('Keep events (days)')} type="number" value={f.values.retention_days} onChange={(v) => f.set('retention_days', v)} error={f.errors.retention_days} />
          <TextInput label={tr('Largest request (KiB)')} type="number" value={f.values.max_payload_kib} onChange={(v) => f.set('max_payload_kib', v)} error={f.errors.max_payload_kib} help={tr('At most 25 MiB.')} />
          <TextInput label={tr('Events a minute')} type="number" value={f.values.rate_limit} onChange={(v) => f.set('rate_limit', v)} error={f.errors.rate_limit} help={tr('On each node; 0 for no limit.')} />
        </div>
        <Field label={tr('Test mode')}>
          <Check label={tr('Keep and show the events, but hide them from rules')} checked={f.values.test_mode} onChange={(v) => f.set('test_mode', v)} />
        </Field>
      </section>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !f.values.organization || !f.values.credential}>
          {busy ? tr('Saving…') : stream ? tr('Save changes') : tr('Create stream')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(stream ? `/event-streams/${stream.id}` : '/event-streams')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}
