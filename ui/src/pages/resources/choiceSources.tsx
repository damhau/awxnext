import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Any, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Field, FormError, Lookup, Segmented, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { useToast } from '../../components/overlay'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { answerNames, parseParameters, refreshState, sourceOrigin, type ChoiceSource, type TestResult } from '../../lib/choiceSources'
import { tr } from '../../lib/i18n'
import { ListPage, useDelete } from '../admin/common'

type Source = Any & ChoiceSource

const API_CREDENTIALS = { credential_type__namespace__in: 'http_api_token,http_api_basic,oauth2_client_credentials' }

function SourceState({ s }: { s: Source }) {
  const state = refreshState(s)
  if (!state) return <span className="small muted">{tr('JSON API')}</span>
  if (s.summary_fields.last_job && ['pending', 'waiting', 'running'].includes(s.summary_fields.last_job.status))
    return (
      <span className="status">
        <Cell state="running" />
        {tr('Refreshing')}
      </span>
    )
  if (state === 'empty')
    return (
      <span className="status">
        <Cell state="empty" />
        {tr('No list yet')}
      </span>
    )
  return (
    <span className="status">
      <Cell state={state === 'failed' ? 'failed' : 'ok'} />
      <span>
        {state === 'failed' ? tr('Last refresh failed') : tr('Refreshed')} {s.last_refresh && <Ago at={s.last_refresh} />}
      </span>
    </span>
  )
}

export function ChoiceSourceList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Source>
      title={tr('Choice sources')}
      subtitle={tr('Lists for survey questions from outside awxnext: a JSON API that the server calls, or what a job publishes.')}
      path="choice_sources/"
      params={scope}
      placeholder={tr('Filter sources…')}
      actions={
        <Link to="/choice-sources/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New source')}
        </Link>
      }
      onOpen={(s) => nav(`/choice-sources/${s.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Source'),
          sort: 'name',
          render: (s) => (
            <div>
              <Link to={`/choice-sources/${s.id}`} className="cell-title">
                {s.name}
              </Link>
              <div className="cell-sub">{sourceOrigin(s)}</div>
            </div>
          ),
        },
        { key: 'state', header: tr('State'), render: (s) => <SourceState s={s} /> },
        { key: 'org', header: tr('Organization'), render: (s) => s.summary_fields.organization?.name ?? '—' },
      ]}
      empty={<Empty title={tr('No choice sources yet')}>{tr('A source lists the choices of a survey question from NetBox, a CMDB or a job, narrowed by the earlier answers.')}</Empty>}
    />
  )
}

export function ChoiceSourceNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Choice sources'), to: '/choice-sources' }, { label: tr('New') }]} title={tr('New choice source')} />
      <div className="page-body">
        <SourceForm />
      </div>
    </div>
  )
}

export function ChoiceSourceDetail() {
  const id = Number(useParams().id)
  const path = `choice_sources/${id}/`
  const source = useObject<Source>(path, {
    refetchInterval: (q) => (['pending', 'waiting', 'running'].includes(q.state.data?.summary_fields.last_job?.status) ? 3000 : false),
  })
  const del = useDelete()
  const toast = useToast()
  const invalidate = useInvalidate()
  const [busy, setBusy] = useState(false)
  if (source.error) return <div className="page-body"><ErrorNote error={source.error} /></div>
  if (!source.data) return <Loading />
  const s = source.data
  const caps = s.summary_fields.user_capabilities ?? {}
  const base = `/choice-sources/${id}`
  const refresh = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ job: number | null }>(`${path}refresh/`, {})
      toast(r.job ? tr('Refresh started: job #{id}', { id: r.job }) : tr('Cache emptied: the next survey calls the API again.'))
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      // A refused refresh records why in last_error.
      invalidate(path)
      setBusy(false)
    }
  }
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Choice sources'), to: '/choice-sources' }, { label: s.name }]}
        title={s.name}
        badge={<SourceState s={s} />}
        subtitle={s.description || sourceOrigin(s)}
        actions={
          <>
            {caps.edit && (
              <button className="btn" disabled={busy} onClick={refresh}>
                <Icon name="refresh" size={14} />
                {s.kind === 'job' ? tr('Refresh now') : tr('Empty the cache')}
              </button>
            )}
            {caps.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path, name: s.name, what: tr('choice source'), then: '/choice-sources' })}>
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
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route index element={<SourceDetails s={s} canTest={!!caps.edit} />} />
          <Route path="edit" element={<SourceForm source={s} />} />
          <Route path="access" element={<AccessTab resource="choice_sources" id={id} name={s.name} />} />
        </Routes>
      </div>
    </div>
  )
}

const mono = (v: string, empty = tr('None')) => (v ? <span className="mono small">{v}</span> : <span className="faint">{empty}</span>)

function SourceDetails({ s, canTest }: { s: Source; canTest: boolean }) {
  const sf = s.summary_fields
  const params = Object.entries((s.parameters ?? {}) as Record<string, string>)
  return (
    <div className="stack" style={{ gap: 16 }}>
      {s.kind === 'job' && s.last_error && (
        <div className="notice warn" role="status">
          {s.last_error}{' '}
          {s.last_job && (
            <Link to={`/jobs/job/${s.last_job}`}>
              {tr('Open job #{id}', { id: s.last_job })}
            </Link>
          )}
        </div>
      )}
      <Panel title={s.kind === 'job' ? tr('Published by a job') : tr('JSON API')}>
        <KV
          items={
            s.kind === 'job'
              ? [
                  [tr('Job template'), sf.job_template ? <Link to={`/templates/job/${s.job_template}`}>{sf.job_template.name}</Link> : <span className="faint">{tr('Deleted')}</span>],
                  [tr('Refresh'), s.refresh_minutes ? tr('Every {n} minutes', { n: s.refresh_minutes }) : tr('By hand')],
                  [tr('Next refresh'), s.next_refresh ? <Ago at={s.next_refresh} /> : ''],
                  [
                    tr('List published'),
                    s.last_refresh ? (
                      <span>
                        <Ago at={s.last_refresh} />
                        {s.last_refresh_job && (
                          <>
                            {' · '}
                            <Link to={`/jobs/job/${s.last_refresh_job}`}>{tr('job #{id}', { id: s.last_refresh_job })}</Link>
                          </>
                        )}
                      </span>
                    ) : (
                      <span className="faint">{tr('Not yet')}</span>
                    ),
                  ],
                ]
              : [
                  [tr('URL'), mono(s.url)],
                  [tr('Parameters'), params.length ? <span className="mono small">{params.map(([k, v]) => `${k}=${v}`).join('  ')}</span> : <span className="faint">{tr('None')}</span>],
                  [tr('Credential'), sf.credential ? <Link to={`/credentials/${s.credential}`}>{sf.credential.name}</Link> : <span className="faint">{tr('None')}</span>],
                  [tr('Cache'), tr('{n} seconds', { n: s.cache_seconds })],
                ]
          }
        />
      </Panel>
      <Panel title={tr('Reading the list')}>
        <KV
          items={[
            [tr('Items'), mono(s.items, s.kind === 'job' ? tr('The published data') : tr('The reply'))],
            [tr('Value'), mono(s.value, tr('The item'))],
            [tr('Label'), mono(s.label, tr('The value'))],
            [tr('Filter'), mono(s.filter, tr('Every item'))],
            [tr('Organization'), <Link to={`/organizations/${s.organization}`}>{sf.organization?.name}</Link>],
            [tr('Changed'), <Ago at={s.modified} />],
          ]}
        />
      </Panel>
      {canTest && <TestPanel s={s} />}
    </div>
  )
}

function TestPanel({ s }: { s: Source }) {
  const names = answerNames((s.parameters ?? {}) as Record<string, string>, s.filter ?? '')
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [result, setResult] = useState<TestResult | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setBusy(true)
    setError(null)
    try {
      const given = Object.fromEntries(Object.entries(answers).filter(([, v]) => v !== ''))
      setResult(await api.post<TestResult>(`choice_sources/${s.id}/test/`, { answers: given }))
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Panel
      title={tr('Test')}
      actions={
        <button className="btn primary sm" disabled={busy} onClick={run}>
          {busy ? tr('Listing…') : tr('List the choices')}
        </button>
      }
    >
      <div className="stack" style={{ gap: 12 }}>
        <p className="small muted" style={{ margin: 0 }}>
          {s.kind === 'job' ? tr('Lists the choices from the last published list, as a survey would.') : tr('Calls the API now, as a survey would, without using the cache.')}
        </p>
        {names.length > 0 && (
          <div className="form-grid">
            {names.map((n) => (
              <TextInput key={n} label={<span className="mono">{tr('answers.{name}', { name: n })}</span>} optional value={answers[n] ?? ''} onChange={(v) => setAnswers((a) => ({ ...a, [n]: v }))} />
            ))}
          </div>
        )}
        <FormError error={error} />
        {result && <TestOutcome r={result} />}
      </div>
    </Panel>
  )
}

function TestOutcome({ r }: { r: TestResult }) {
  return (
    <div className="stack" style={{ gap: 10 }}>
      {r.url && (
        <div className="small">
          {tr('Called')} <span className="mono">{r.url}</span>
        </div>
      )}
      {r.error ? (
        <div className="notice warn" role="status">
          {r.error}
        </div>
      ) : (
        <div className="small">{r.count > r.choices.length ? tr('{n} choices; the first {shown}:', { n: r.count, shown: r.choices.length }) : tr('{n} choices:', { n: r.count })}</div>
      )}
      {r.choices.length > 0 && (
        <table className="data">
          <thead>
            <tr>
              <th>{tr('Value')}</th>
              <th>{tr('Label')}</th>
            </tr>
          </thead>
          <tbody>
            {r.choices.map((c) => (
              <tr key={c.value}>
                <td className="mono small">{c.value}</td>
                <td>{c.label}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {r.sample.length > 0 && (
        <Field label={tr('Sample items, to write the expressions from')}>
          <pre className="code" style={{ margin: 0, maxHeight: 280, overflow: 'auto' }}>
            {JSON.stringify(r.sample, null, 2)}
          </pre>
        </Field>
      )}
      {r.reply && (
        <Field label={tr('The start of the reply')}>
          <pre className="code" style={{ margin: 0, maxHeight: 280, overflow: 'auto' }}>
            {r.reply}
          </pre>
        </Field>
      )}
    </div>
  )
}

interface SourceValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  kind: 'http' | 'job'
  url: string
  parameters: string
  credential: Ref | null
  cache_seconds: string
  job_template: Ref | null
  refresh_minutes: string
  items: string
  value: string
  label: string
  filter: string
}

function paramsText(p: Record<string, string> | undefined): string {
  return Object.entries(p ?? {})
    .map(([k, v]) => `${k}: ${JSON.stringify(v)}`)
    .join('\n')
}

function SourceForm({ source }: { source?: Source }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const invalidate = useInvalidate()
  const sf = source?.summary_fields
  const f = useForm<SourceValues>({
    name: source?.name ?? '',
    description: source?.description ?? '',
    organization: sf?.organization ?? org,
    kind: source?.kind ?? 'http',
    url: source?.url ?? '',
    parameters: paramsText(source?.parameters),
    credential: sf?.credential ?? null,
    cache_seconds: String(source?.cache_seconds ?? 300),
    job_template: sf?.job_template ?? null,
    refresh_minutes: String(source?.refresh_minutes ?? 0),
    items: source?.items ?? '',
    value: source?.value ?? '',
    label: source?.label ?? '',
    filter: source?.filter ?? '',
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !source)
  const v = f.values
  const params = parseParameters(v.parameters)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    setError(null)
    const common = { name: v.name, description: v.description, organization: v.organization?.id, kind: v.kind, items: v.items, value: v.value, label: v.label, filter: v.filter }
    const body =
      v.kind === 'http'
        ? { ...common, url: v.url, parameters: params.value, credential: v.credential?.id ?? null, cache_seconds: Number(v.cache_seconds) }
        : { ...common, job_template: v.job_template?.id ?? null, refresh_minutes: Number(v.refresh_minutes) }
    try {
      const saved = source ? await api.patch<Source>(`choice_sources/${source.id}/`, body) : await api.post<Source>('choice_sources/', body)
      invalidate('choice_sources')
      nav(`/choice-sources/${saved.id}`)
    } catch (e) {
      setError(e)
      if (e instanceof ApiError) f.setErrors(e.fields)
    } finally {
      setBusy(false)
    }
  }
  const ready = v.name.trim() && v.organization && !params.error && (v.kind === 'http' ? v.url.trim() : v.job_template)
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
        <TextInput label={tr('Name')} value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus={!source} />
        <Lookup label={tr('Organization')} resource="organizations/" value={v.organization} onChange={(x) => f.set('organization', x)} error={f.errors.organization} />
        <TextArea label={tr('Description')} optional rows={2} value={v.description} onChange={(x) => f.set('description', x)} />
        <Segmented
          legend={tr('The list comes from')}
          value={v.kind}
          onChange={(x) => f.set('kind', x)}
          options={[
            ['http', tr('A JSON API')],
            ['job', tr('A job template')],
          ]}
        />
      </div>
      {v.kind === 'http' ? (
        <section className="form-section">
          <h2>{tr('The API')}</h2>
          <p className="small muted" style={{ margin: 0 }}>
            {tr('awxnext calls it from the server, never the browser, and only on the hosts an administrator lists in CHOICE_SOURCE_HOSTS.')}
          </p>
          <div className="form-grid">
            <TextInput label={tr('URL')} value={v.url} onChange={(x) => f.set('url', x)} mono full placeholder={tr('https://netbox.example.com/api/ipam/vlans/')} error={f.errors.url} />
            <Lookup
              label={tr('Credential')}
              optional
              resource="credentials/"
              params={{ ...API_CREDENTIALS, ...(v.organization ? { organization: v.organization.id } : {}) }}
              value={v.credential}
              onChange={(x) => f.set('credential', x)}
              error={f.errors.credential}
              help={tr('An HTTP API Token, HTTP API Basic Authentication or OAuth 2.0 Client Credentials credential. Survey owners never see it.')}
            />
            <TextInput label={tr('Cache, in seconds')} type="number" value={v.cache_seconds} onChange={(x) => f.set('cache_seconds', x)} error={f.errors.cache_seconds} help={tr('How long a list is kept before the API is called again with the same parameters.')} />
            <Field label={tr('Query parameters')} optional full error={f.errors.parameters ?? params.error ?? undefined} help={<>{tr('YAML: one name: value per line. Values can use the earlier answers and the user:')} <span className="mono">{tr('site: "{{ answers.datacenter }}"')}</span>.</>}>
              <textarea className="textarea mono" rows={3} spellCheck={false} value={v.parameters} aria-invalid={!!params.error} onChange={(e) => f.set('parameters', e.target.value)} />
            </Field>
          </div>
        </section>
      ) : (
        <section className="form-section">
          <h2>{tr('The job')}</h2>
          <p className="small muted" style={{ margin: 0 }}>
            {tr('The template’s run publishes the list with set_stats. When a refresh fails, the source keeps its last list.')}
          </p>
          <div className="form-grid">
            <Lookup label={tr('Job template')} resource="job_templates/" value={v.job_template} onChange={(x) => f.set('job_template', x)} error={f.errors.job_template} help={tr('Picking one takes Execute on it.')} />
            <TextInput label={tr('Refresh every, in minutes')} type="number" value={v.refresh_minutes} onChange={(x) => f.set('refresh_minutes', x)} error={f.errors.refresh_minutes} help={tr('0: only by hand.')} />
          </div>
        </section>
      )}
      <section className="form-section">
        <h2>{tr('Reading the list')}</h2>
        <p className="small muted" style={{ margin: 0 }}>
          {tr('expr expressions. Save, then use Test on the source to see a sample of the items.')}
        </p>
        <div className="form-grid">
          <TextInput label={tr('Items')} optional mono value={v.items} onChange={(x) => f.set('items', x)} error={f.errors.items} placeholder={tr('data.results')} help={tr('The list, from data. Empty: data must be a list.')} />
          <TextInput label={tr('Value')} optional mono value={v.value} onChange={(x) => f.set('value', x)} error={f.errors.value} placeholder={tr('string(item.vid)')} help={tr('An item’s answer, from item. Empty: the item.')} />
          <TextInput label={tr('Label')} optional mono value={v.label} onChange={(x) => f.set('label', x)} error={f.errors.label} placeholder={tr('item.name + " (" + value + ")"')} help={tr('What people see, from item and value. Empty: the value.')} />
          <TextInput label={tr('Filter')} optional mono value={v.filter} onChange={(x) => f.set('filter', x)} error={f.errors.filter} placeholder={tr('item.site == answers.datacenter')} help={tr('Lists an item when true. It reads item, value, label, answers and user.')} />
        </div>
      </section>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !ready}>
          {busy ? tr('Saving…') : source ? tr('Save changes') : tr('Create source')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(source ? `/choice-sources/${source.id}` : '/choice-sources')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}
