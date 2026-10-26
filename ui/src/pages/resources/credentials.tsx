import { useQuery } from '@tanstack/react-query'
import { useId, useState, type FormEvent, type ReactNode } from 'react'
import { Link, Outlet, useNavigate, useOutletContext, useParams } from 'react-router'
import { api, getAll } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { useOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Field, FormError, Lookup, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, Menu, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { plural } from '../../lib/format'
import { compile, type KeyTable } from '../../lib/query'
import { applyErrors, FilterBox, KindTag, ManagedNotice, NameCell, Provenance, refOf, SECRET, useDelete, useOrgColumn } from './common'

export interface Credential extends Base {
  credential_type: number
  kind: string
  inputs: Record<string, unknown>
  organization: number | null
  folder: number | null
  managed: boolean
  managed_by?: string
  cloud: boolean
}

interface InputField {
  id: string
  label: string
  type?: 'string' | 'boolean'
  secret?: boolean
  multiline?: boolean
  help_text?: string
  choices?: string[]
  default?: unknown
  format?: string
  ask_at_runtime?: boolean
}

interface CredType extends Base {
  kind: string
  namespace?: string
  managed?: boolean
  inputs: { fields?: InputField[]; metadata?: InputField[]; required?: string[]; dependencies?: Record<string, string[]> }
}

interface InputSource extends Base {
  input_field_name: string
  metadata: Record<string, unknown>
  source_credential: number
  target_credential: number
}

const kindNames: Record<string, string> = {
  ssh: 'machine',
  vault: 'vault',
  net: 'network',
  scm: 'source control',
  cloud: 'cloud',
  registry: 'registry',
  token: 'token',
  insights: 'insights',
  external: 'secret lookup',
  kubernetes: 'kubernetes',
  galaxy: 'galaxy',
  cryptography: 'signing',
}

const keys: KeyTable = {
  type: { field: 'credential_type__name', lookup: 'icontains' },
  kind: { field: 'credential_type__kind', map: (v) => Object.entries(kindNames).find(([, n]) => n === v.toLowerCase())?.[0] ?? v },
  org: { field: 'organization__name', lookup: 'iexact' },
}

export function CredentialList() {
  const ls = useListState({ order: 'name' })
  const { scope } = useOrg()
  const nav = useNavigate()
  const q = compile(ls.q, keys)
  const list = useList<Credential>('credentials/', { ...q.params, ...scope, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const del = useDelete('credentials')
  const orgCol = useOrgColumn<Credential>()
  const cols: Column<Credential>[] = [
    { key: 'name', header: 'Credential', sort: 'name', render: (c) => <NameCell to={`/credentials/${c.id}`} name={c.name} sub={c.description || undefined} /> },
    {
      key: 'type',
      header: 'Type',
      sort: 'credential_type__name',
      render: (c) => (
        <span className="row" style={{ gap: 6 }}>
          <span className="small">{c.summary_fields.credential_type?.name}</span>
          {c.kind === 'external' && <KindTag>secret lookup</KindTag>}
        </span>
      ),
    },
    ...orgCol,
    { key: 'modified', header: 'Edited', sort: 'modified', render: (c) => <span className="small faint nowrap"><Ago at={c.modified} /></span> },
    {
      key: 'menu',
      header: <span className="sr-only">Actions</span>,
      className: 'end',
      render: (c) => (
        <Menu label={`Actions for ${c.name}`} className="btn sm icon ghost">
          <Link to={`/credentials/${c.id}/edit`}>Edit</Link>
          <Link to={`/credentials/${c.id}/access`}>Access</Link>
          <hr />
          <button className="danger" onClick={() => del(c, 'credential')}>
            Delete…
          </button>
        </Menu>
      ),
    },
  ]
  return (
    <div className="page">
      <PageHead
        title="Credentials"
        subtitle="Secrets for machines, clouds, repositories and registries. Values are encrypted at rest and never shown again once saved."
        bordered={false}
        actions={
          <Link to="/credentials/new" className="btn primary">
            New credential
          </Link>
        }
      />
      <div className="page-body">
        <FilterBox value={ls.q} onApply={ls.setQ} placeholder="name…  try  kind:machine  type:vault*  org:Acme*" />
        <DataTable
          label="Credentials"
          rows={list.data?.results}
          columns={cols}
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(c) => nav(`/credentials/${c.id}`)}
          empty={
            <Empty title={ls.q ? 'No credential matches' : 'No credentials yet'} action={!ls.q && <Link to="/credentials/new" className="btn primary">New credential</Link>}>
              {ls.q ? 'Try a shorter filter.' : null}
            </Empty>
          }
          footer={
            <div className="table-foot">
              <span>{list.data ? `${plural(list.data.count, 'credential', 'credentials')}` : ''}</span>
              <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
    </div>
  )
}

function useCredType(id: number | null | undefined) {
  return useObject<CredType>(id ? `credential_types/${id}/` : null, { staleTime: 5 * 60_000 })
}

function useInputSources(credId: number | undefined) {
  return useQuery({
    queryKey: [`credentials/${credId}/input_sources/`, 'all'],
    queryFn: () => getAll<InputSource>(`credentials/${credId}/input_sources/`),
    enabled: !!credId,
  })
}

export function CredentialForm() {
  const { id } = useParams()
  const existing = useObject<Credential>(id ? `credentials/${id}/` : null)
  const sources = useInputSources(id ? Number(id) : undefined)
  if (id && (existing.isPending || sources.isPending)) return <Loading />
  if (existing.error) return <div className="page-body"><ErrorNote error={existing.error} /></div>
  return <CredentialFormInner key={id ?? 'new'} cred={existing.data} sources={sources.data ?? []} />
}

interface Link_ {
  source: Ref
  metadata: Record<string, unknown>
  existing?: number
}

function CredentialFormInner({ cred, sources }: { cred?: Credential; sources: InputSource[] }) {
  const { org } = useOrg()
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const toast = useToast()
  const f = useForm({
    name: cred?.name ?? '',
    description: cred?.description ?? '',
    organization: refOf(cred?.summary_fields.organization, cred?.organization) ?? (org as Ref | null),
    folder: (cred?.summary_fields.folder as Ref | undefined) ?? null,
    credential_type: refOf(cred?.summary_fields.credential_type, cred?.credential_type),
  })
  const type = useCredType(f.values.credential_type?.id)
  const [inputs, setInputs] = useState<Record<string, unknown>>(cred?.inputs ?? {})
  const [links, setLinks] = useState<Record<string, Link_>>(() =>
    Object.fromEntries(
      sources.map((s) => [s.input_field_name, { source: { id: s.source_credential, name: s.summary_fields.source_credential?.name ?? `#${s.source_credential}` }, metadata: s.metadata, existing: s.id }]),
    ),
  )
  const [linking, setLinking] = useState<InputField | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const fields = type.data?.inputs.fields ?? []
  const required = new Set(type.data?.inputs.required ?? [])
  const missing = fields.filter((x) => required.has(x.id) && x.type !== 'boolean' && !links[x.id] && (inputs[x.id] === undefined || inputs[x.id] === ''))
  const external = type.data?.kind === 'external'

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    const clean: Record<string, unknown> = {}
    for (const fd of fields) {
      if (links[fd.id]) continue
      const v = inputs[fd.id]
      if (v === undefined || v === '') continue
      clean[fd.id] = v
    }
    const body = {
      name: f.values.name,
      description: f.values.description,
      organization: f.values.organization?.id ?? null,
      folder: f.values.folder?.id ?? null,
      credential_type: f.values.credential_type?.id,
      inputs: clean,
    }
    try {
      const saved = cred ? await api.patch<Credential>(`credentials/${cred.id}/`, body) : await api.post<Credential>('credentials/', body)
      // Reconcile secret lookups.
      for (const s of sources) {
        const l = links[s.input_field_name]
        if (!l) await api.del(`credential_input_sources/${s.id}/`)
        else if (l.source.id !== s.source_credential || JSON.stringify(l.metadata) !== JSON.stringify(s.metadata))
          await api.patch(`credential_input_sources/${s.id}/`, { source_credential: l.source.id, metadata: l.metadata })
      }
      for (const [field, l] of Object.entries(links)) {
        if (l.existing) continue
        await api.post('credential_input_sources/', { target_credential: saved.id, source_credential: l.source.id, input_field_name: field, metadata: l.metadata })
      }
      invalidate('credentials', `credentials/${saved.id}`)
      toast(cred ? `Saved ${saved.name}` : `Created ${saved.name}`)
      nav(`/credentials/${saved.id}`)
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: 'Credentials', to: '/credentials' }, ...(cred ? [{ label: cred.name, to: `/credentials/${cred.id}` }] : []), { label: cred ? 'Edit' : 'New' }]}
        title={cred ? `Edit ${cred.name}` : 'New credential'}
      />
      <form className="page-body form" onSubmit={submit} style={{ maxWidth: 920 }} autoComplete="off">
        <ManagedNotice managedBy={cred?.managed_by} />
        <div className="form-grid">
          <TextInput label="Name" value={f.values.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus />
          <Lookup
            label="Type"
            resource="credential_types/"
            value={f.values.credential_type}
            onChange={(x) => {
              f.set('credential_type', x)
              if (!cred) {
                setInputs({})
                setLinks({})
              }
            }}
            error={f.errors.credential_type}
            meta={(r) => kindNames[(r as unknown as CredType).kind] ?? (r as unknown as CredType).kind}
            help={cred ? 'Changing the type clears inputs it does not have.' : undefined}
          />
          <TextInput label="Description" optional value={f.values.description} onChange={(x) => f.set('description', x)} full />
          <Lookup
            label="Organization"
            optional
            resource="organizations/"
            value={f.values.organization}
            onChange={(x) => f.set('organization', x)}
            error={f.errors.organization}
            help="Organization members with the right roles can use it. Without one, only you (and admins) can."
          />
          <Lookup
            label="Folder"
            optional
            resource="folders/"
            params={f.values.organization ? { organization: f.values.organization.id } : undefined}
            value={f.values.folder}
            onChange={(x) => f.set('folder', x)}
            error={f.errors.folder}
          />
        </div>
        {f.values.credential_type && (
          <div className="form-section">
            <h2>{type.data?.name ?? 'Inputs'}</h2>
            {type.isPending ? (
              <Loading rows={3} />
            ) : fields.length === 0 ? (
              <span className="faint small">This type has no inputs.</span>
            ) : (
              <div className="form-grid">
                {fields.map((fd) => (
                  <InputControl
                    key={fd.id}
                    field={fd}
                    required={required.has(fd.id)}
                    value={inputs[fd.id]}
                    stored={cred?.inputs?.[fd.id] === SECRET}
                    onChange={(v) => setInputs((prev) => ({ ...prev, [fd.id]: v }))}
                    error={f.errors.inputs && f.errors.inputs.includes(fd.id) ? f.errors.inputs : undefined}
                    link={links[fd.id]}
                    onLink={external || fd.type === 'boolean' ? undefined : () => setLinking(fd)}
                    onUnlink={() =>
                      setLinks((prev) => {
                        const n = { ...prev }
                        delete n[fd.id]
                        return n
                      })
                    }
                  />
                ))}
              </div>
            )}
            {f.errors.inputs && <div className="form-error">{f.errors.inputs}</div>}
          </div>
        )}
        {external && type.data && <TestLookup type={type.data} cred={cred} inputs={inputs} />}
        <FormError error={error} />
        <div className="form-actions">
          <button className="btn primary" disabled={busy || !f.values.name || !f.values.credential_type || missing.length > 0}>
            {busy ? 'Saving…' : cred ? 'Save' : 'Create credential'}
          </button>
          <button type="button" className="btn ghost" onClick={() => nav(-1)}>
            Cancel
          </button>
          {missing.length > 0 && <span className="small faint">Required: {missing.map((m) => m.label).join(', ')}</span>}
        </div>
      </form>
      {linking && (
        <LinkDialog
          field={linking}
          current={links[linking.id]}
          onClose={() => setLinking(null)}
          onSave={(l) => {
            setLinks((prev) => ({ ...prev, [linking.id]: { ...l, existing: prev[linking.id]?.existing } }))
            setLinking(null)
          }}
        />
      )}
    </div>
  )
}

function InputControl({
  field,
  required,
  value,
  stored,
  onChange,
  error,
  link,
  onLink,
  onUnlink,
}: {
  field: InputField
  required: boolean
  value: unknown
  stored: boolean
  onChange: (v: unknown) => void
  error?: string
  link?: Link_
  onLink?: () => void
  onUnlink: () => void
}) {
  const id = useId()
  const [replacing, setReplacing] = useState(false)
  const full = !!field.multiline
  const label = (
    <span className="row" style={{ gap: 8 }}>
      <span>
        {field.label}
        {!required && <span className="opt"> · optional</span>}
      </span>
      {onLink && !link && (
        <button type="button" className="link-btn small" style={{ fontWeight: 400, marginLeft: 'auto' }} onClick={onLink} title="Resolve this value from HashiCorp Vault or Azure Key Vault at run time">
          <Icon name="link" size={12} /> lookup
        </button>
      )}
    </span>
  )
  if (field.type === 'boolean') {
    return (
      <div className="field">
        <label className="check">
          <input type="checkbox" checked={value === true || (value === undefined && field.default === true)} onChange={(e) => onChange(e.target.checked)} />
          <span>{field.label}</span>
        </label>
        {field.help_text && <span className="help" style={{ marginLeft: 22 }}>{field.help_text}</span>}
      </div>
    )
  }
  let control: ReactNode
  if (link) {
    control = (
      <div className="lookup-box" style={{ cursor: 'default' }}>
        <Icon name="key" size={14} style={{ color: 'var(--accent)' }} />
        <span className="small grow ellipsis">
          from <b style={{ fontWeight: 500 }}>{link.source.name}</b>
          <span className="mono faint"> {describeMeta(link.metadata)}</span>
        </span>
        <button type="button" className="btn ghost sm" onClick={onLink}>
          Change
        </button>
        <button type="button" className="btn ghost sm" onClick={onUnlink}>
          Remove
        </button>
      </div>
    )
  } else if (field.ask_at_runtime && value === 'ASK') {
    control = (
      <div className="lookup-box" style={{ cursor: 'default' }}>
        <span className="small grow muted">Asked when a job launches</span>
        <button type="button" className="btn ghost sm" onClick={() => onChange('')}>
          Set a value instead
        </button>
      </div>
    )
  } else if (field.secret && stored && !replacing && (value === SECRET || value === undefined)) {
    control = (
      <div className="lookup-box" style={{ cursor: 'default' }}>
        <span className="mono faint grow">••••••••</span>
        <span className="small faint">stored encrypted</span>
        <button
          type="button"
          className="btn ghost sm"
          onClick={() => {
            setReplacing(true)
            onChange('')
          }}
        >
          Replace
        </button>
      </div>
    )
  } else if (field.choices?.length) {
    control = (
      <select id={id} className="select" value={String(value ?? field.default ?? '')} onChange={(e) => onChange(e.target.value)}>
        {!required && <option value="">—</option>}
        {field.choices.map((c) => (
          <option key={c} value={c}>
            {c}
          </option>
        ))}
      </select>
    )
  } else if (field.multiline) {
    control = (
      <textarea
        id={id}
        className="textarea mono"
        rows={field.format === 'ssh_private_key' || field.secret ? 6 : 4}
        spellCheck={false}
        value={String(value ?? '')}
        placeholder={field.format === 'ssh_private_key' ? '-----BEGIN OPENSSH PRIVATE KEY-----' : undefined}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={!!error}
        style={field.secret ? ({ WebkitTextSecurity: value ? 'disc' : undefined } as React.CSSProperties) : undefined}
      />
    )
  } else {
    control = (
      <input
        id={id}
        className={`input ${field.secret ? 'mono' : ''}`}
        type={field.secret ? 'password' : 'text'}
        autoComplete={field.secret ? 'new-password' : 'off'}
        value={String(value ?? '')}
        placeholder={field.default !== undefined ? String(field.default) : undefined}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={!!error}
      />
    )
  }
  return (
    <Field label={label} help={field.help_text} error={error} htmlFor={id} full={full}>
      {control}
      {field.ask_at_runtime && !link && value !== 'ASK' && (
        <label className="check small" style={{ marginTop: 2 }}>
          <input type="checkbox" checked={false} onChange={() => onChange('ASK')} />
          <span>Ask at launch instead</span>
        </label>
      )}
    </Field>
  )
}

function describeMeta(m: Record<string, unknown>): string {
  const path = m.secret_path ?? m.secret_field ?? ''
  const key = m.secret_key ?? ''
  return [path, key].filter(Boolean).join(' : ')
}

/** Links an input to a secret lookup credential (HashiCorp Vault, Azure Key Vault). */
function LinkDialog({ field, current, onClose, onSave }: { field: InputField; current?: Link_; onClose: () => void; onSave: (l: Link_) => void }) {
  const [source, setSource] = useState<Ref | null>(current?.source ?? null)
  const [meta, setMeta] = useState<Record<string, unknown>>(current?.metadata ?? {})
  const srcCred = useObject<Credential>(source ? `credentials/${source.id}/` : null)
  const srcType = useCredType(srcCred.data?.credential_type)
  const [test, setTest] = useState<{ ok: boolean; msg: string } | null>(null)
  const [testing, setTesting] = useState(false)
  const md = srcType.data?.inputs.metadata ?? []
  const req = new Set(srcType.data?.inputs.required ?? [])
  const missing = md.filter((m) => req.has(m.id) && !meta[m.id])
  const runTest = async () => {
    if (!source) return
    setTesting(true)
    setTest(null)
    try {
      await api.post(`credentials/${source.id}/test/`, { metadata: meta })
      setTest({ ok: true, msg: 'The lookup returned a value.' })
    } catch (e) {
      setTest({ ok: false, msg: e instanceof Error ? e.message : 'Lookup failed' })
    } finally {
      setTesting(false)
    }
  }
  return (
    <Dialog
      title={`Look up "${field.label}" from a secret store`}
      onClose={onClose}
      wide
      footer={
        <>
          {test && <span className={`small grow ${test.ok ? 'ok-text' : 'fail-text'}`}>{test.msg}</span>}
          <button className="btn" disabled={!source || missing.length > 0 || testing} onClick={runTest}>
            {testing ? 'Testing…' : 'Test'}
          </button>
          <button className="btn primary" disabled={!source || missing.length > 0} onClick={() => source && onSave({ source, metadata: meta })}>
            Use this lookup
          </button>
        </>
      }
    >
      <p className="muted" style={{ margin: 0 }}>
        The value is fetched each time a job uses this credential; it is never stored here.
      </p>
      <Lookup
        label="Secret store credential"
        resource="credentials/"
        params={{ credential_type__kind: 'external' }}
        value={source}
        onChange={(x) => {
          setSource(x)
          setTest(null)
        }}
        help="A HashiCorp Vault or Azure Key Vault credential."
      />
      {source && srcType.isPending && <Loading rows={2} />}
      {md.length > 0 && (
        <div className="form-grid">
          {md.map((m) => (
            <TextInput
              key={m.id}
              label={m.label}
              optional={!req.has(m.id)}
              mono
              value={String(meta[m.id] ?? '')}
              onChange={(v) => {
                setMeta((prev) => ({ ...prev, [m.id]: v }))
                setTest(null)
              }}
              help={m.help_text && m.help_text.length < 140 ? m.help_text : undefined}
            />
          ))}
        </div>
      )}
    </Dialog>
  )
}

/** Test panel for secret lookup credentials: try a path before saving. */
function TestLookup({ type, cred, inputs }: { type: CredType; cred?: Credential; inputs: Record<string, unknown> }) {
  const [meta, setMeta] = useState<Record<string, unknown>>({})
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const md = type.inputs.metadata ?? []
  const run = async () => {
    setBusy(true)
    setResult(null)
    try {
      if (cred) await api.post(`credentials/${cred.id}/test/`, { inputs, metadata: meta })
      else await api.post(`credential_types/${type.id}/test/`, { inputs, metadata: meta })
      setResult({ ok: true, msg: 'Connected; the lookup returned a value.' })
    } catch (e) {
      setResult({ ok: false, msg: e instanceof Error ? e.message : 'Lookup failed' })
    } finally {
      setBusy(false)
    }
  }
  if (!md.length) return null
  return (
    <div className="form-section">
      <h2>Try a lookup</h2>
      <p className="muted small" style={{ margin: 0 }}>
        Checks the connection with the values above, without saving.
      </p>
      <div className="form-grid">
        {md.map((m) => (
          <TextInput key={m.id} label={m.label} optional mono value={String(meta[m.id] ?? '')} onChange={(v) => setMeta((p) => ({ ...p, [m.id]: v }))} />
        ))}
      </div>
      <div className="row">
        <button type="button" className="btn" onClick={run} disabled={busy}>
          {busy ? 'Testing…' : 'Test lookup'}
        </button>
        {result && <span className={`small ${result.ok ? 'ok-text' : 'fail-text'}`}>{result.msg}</span>}
      </div>
    </div>
  )
}

export function CredentialDetail() {
  const { id } = useParams()
  const cred = useObject<Credential>(`credentials/${id}/`)
  const del = useDelete('credentials', '/credentials')
  if (cred.isPending) return <Loading />
  if (cred.error || !cred.data) return <div className="page-body"><ErrorNote error={cred.error ?? 'Not found'} /></div>
  const c = cred.data
  const caps = c.summary_fields.user_capabilities ?? {}
  const base = `/credentials/${c.id}`
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: 'Credentials', to: '/credentials' }, { label: c.name }]}
        title={c.name}
        badge={<KindTag>{c.summary_fields.credential_type?.name}</KindTag>}
        subtitle={c.description || undefined}
        actions={
          <>
            {caps.edit && !c.managed_by && !c.managed && (
              <Link className="btn" to={`${base}/edit`}>
                Edit
              </Link>
            )}
            {caps.delete && !c.managed && (
              <Menu label="More actions">
                <button className="danger" onClick={() => del(c, 'credential; templates using it will fail until they get another one')}>
                  Delete…
                </button>
              </Menu>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              Details
            </Tab>
            <Tab to={`${base}/access`}>Access</Tab>
          </>
        }
      />
      <Outlet context={c} />
    </div>
  )
}

export function CredentialDetailsTab() {
  const c = useOutletContext<Credential>()
  const type = useCredType(c.credential_type)
  const sources = useInputSources(c.id)
  const templates = useList<Base>('job_templates/', { credentials__id: c.id, page_size: 10, order_by: 'name' })
  const projects = useList<Base>('projects/', { credential: c.id, page_size: 10, order_by: 'name' })
  const fields = type.data?.inputs.fields ?? []
  const linked = new Map((sources.data ?? []).map((s) => [s.input_field_name, s]))
  const rows: [ReactNode, ReactNode][] = fields
    .filter((fd) => c.inputs[fd.id] !== undefined || linked.has(fd.id))
    .map((fd) => {
      const s = linked.get(fd.id)
      const v = c.inputs[fd.id]
      let shown: ReactNode
      if (s)
        shown = (
          <span className="small">
            <Icon name="key" size={12} /> from <Link to={`/credentials/${s.source_credential}`}>{s.summary_fields.source_credential?.name}</Link>{' '}
            <span className="mono faint">{describeMeta(s.metadata)}</span>
          </span>
        )
      else if (v === 'ASK') shown = <span className="muted">asked at launch</span>
      else if (v === SECRET) shown = <span className="mono faint">•••••••• stored encrypted</span>
      else if (typeof v === 'boolean') shown = v ? 'Yes' : 'No'
      else shown = <span className="mono">{String(v)}</span>
      return [fd.label, shown]
    })
  return (
    <div className="page-body">
      <ManagedNotice managedBy={c.managed_by} />
      <div className="split">
        <div className="wide stack" style={{ gap: 16 }}>
          <Panel title={c.summary_fields.credential_type?.name ?? 'Inputs'}>
            {type.isPending ? <Loading rows={3} /> : rows.length ? <KV items={rows} /> : <span className="faint small">No inputs set.</span>}
          </Panel>
        </div>
        <div className="narrow stack" style={{ gap: 16 }}>
          <Panel title="Used by">
            <div className="stack small" style={{ gap: 6 }}>
              {(templates.data?.results ?? []).map((t) => (
                <Link key={t.id} to={`/templates/job/${t.id}`} className="ellipsis">
                  {t.name}
                </Link>
              ))}
              {(projects.data?.results ?? []).map((p) => (
                <Link key={'p' + p.id} to={`/projects/${p.id}`} className="ellipsis">
                  {p.name} <span className="faint">· project</span>
                </Link>
              ))}
              {!templates.data?.count && !projects.data?.count && <span className="faint">Not used by any template or project.</span>}
            </div>
          </Panel>
          <Panel title="About">
            <KV
              items={[
                ['Organization', c.summary_fields.organization ? <Link to={`/organizations/${c.organization}`}>{c.summary_fields.organization.name}</Link> : <span className="faint">personal</span>],
                ['Folder', c.summary_fields.folder?.name ?? ''],
                ['Type', c.summary_fields.credential_type?.name],
                ['History', <Provenance obj={c} />],
              ]}
            />
          </Panel>
        </div>
      </div>
    </div>
  )
}

export function CredentialAccessTab() {
  const c = useOutletContext<Credential>()
  return (
    <div className="page-body">
      <AccessTab resource="credentials" id={c.id} name={c.name} />
    </div>
  )
}
