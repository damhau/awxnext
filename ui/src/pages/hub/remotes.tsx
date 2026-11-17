import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any, Ref } from '../../api/types'
import { useMe, useOrg, usePreselectOrg } from '../../app/context'
import { Check, FormError, Lookup, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { useToast } from '../../components/overlay'
import { Ago, Elapsed, Empty, ErrorNote, KV, Loading, PageHead, Panel, Status, Tab } from '../../components/ui'
import { jobPath } from '../../lib/format'
import { galaxyServer } from '../../lib/hub'
import { tr } from '../../lib/i18n'
import { CopyButton, ListPage, useDelete } from '../admin/common'
import { refOf, SECRET } from '../resources/common'

type Remote = Any & { id: number }

function useSync() {
  const nav = useNavigate()
  const toast = useToast()
  const invalidate = useInvalidate()
  return async (r: Remote) => {
    try {
      const res = await api.post<Any>(`collection_remotes/${r.id}/sync/`, {})
      invalidate('collection_remotes')
      nav(jobPath({ id: res.collection_sync ?? res.id, type: 'collection_sync' }))
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
}

export function RemoteList() {
  const nav = useNavigate()
  const me = useMe()
  const { scope } = useOrg()
  const sync = useSync()
  return (
    <ListPage<Remote>
      title={tr('Remotes')}
      subtitle={tr('Collections mirrored from another Galaxy server, such as Ansible Galaxy or Red Hat’s Automation Hub, into a repository of the hub.')}
      path="collection_remotes/"
      params={scope}
      placeholder={tr('Filter remotes…')}
      actions={
        me.is_superuser && (
          <Link to="/collection-remotes/new" className="btn primary">
            <Icon name="plus" size={14} />
            {tr('New remote')}
          </Link>
        )
      }
      onOpen={(r) => nav(`/collection-remotes/${r.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Remote'),
          sort: 'name',
          render: (r) => (
            <div>
              <Link to={`/collection-remotes/${r.id}`} className="cell-title">
                {r.name}
              </Link>
              <div className="cell-sub mono">{r.repository}</div>
            </div>
          ),
        },
        {
          key: 'status',
          header: tr('Last sync'),
          render: (r) =>
            r.last_job_run ? (
              <span className="row" style={{ gap: 8 }}>
                <Status status={r.status} />
                <Ago at={r.last_job_run} />
              </span>
            ) : (
              <span className="faint">{tr('Never synced')}</span>
            ),
        },
        { key: 'server', header: tr('Server'), render: (r) => r.summary_fields?.credential?.name ?? '—' },
        { key: 'org', header: tr('Organization'), render: (r) => r.summary_fields?.organization?.name ?? '—' },
        {
          key: 'actions',
          header: '',
          label: tr('Actions'),
          className: 'end',
          render: (r) =>
            r.summary_fields?.user_capabilities?.start ? (
              <button
                className="btn sm"
                onClick={(e) => {
                  e.stopPropagation()
                  sync(r)
                }}
              >
                <Icon name="refresh" size={12} />
                {tr('Sync')}
              </button>
            ) : null,
        },
      ]}
      empty={<Empty title={tr('No remotes yet')}>{tr('A remote lists the collections to mirror from another server; its syncs copy them, with their signatures, into the hub.')}</Empty>}
    />
  )
}

export function RemoteNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Remotes'), to: '/collection-remotes' }, { label: tr('New') }]} title={tr('New remote')} />
      <div className="page-body">
        <RemoteForm />
      </div>
    </div>
  )
}

export function RemoteDetail() {
  const id = Number(useParams().id)
  const remote = useObject<Remote>(`collection_remotes/${id}/`)
  const del = useDelete()
  const sync = useSync()
  if (remote.error)
    return (
      <div className="page-body">
        <ErrorNote error={remote.error} />
      </div>
    )
  if (!remote.data) return <Loading />
  const r = remote.data
  const caps = r.summary_fields?.user_capabilities ?? {}
  const base = `/collection-remotes/${id}`
  const server = galaxyServer(window.location.origin, r.repository)
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Remotes'), to: '/collection-remotes' }, { label: r.name }]}
        title={r.name}
        badge={r.last_job_run ? <Status status={r.status} /> : undefined}
        subtitle={r.description || undefined}
        actions={
          <>
            {caps.start && (
              <button className="btn primary" onClick={() => sync(r)}>
                <Icon name="refresh" size={14} />
                {tr('Sync')}
              </button>
            )}
            {caps.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path: `collection_remotes/${id}/`, name: r.name, what: tr('remote'), then: '/collection-remotes' })}>
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
            <Tab to={`${base}/syncs`}>{tr('Syncs')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route
            index
            element={
              <div className="split">
                <div className="wide stack" style={{ gap: 16 }}>
                  <Panel title={tr('Collections to mirror')}>
                    <pre className="code" style={{ margin: 0 }}>
                      {r.requirements_file}
                    </pre>
                  </Panel>
                  <Panel title={tr('Install from it')}>
                    <div className="stack" style={{ gap: 6 }}>
                      <div className="row">
                        <span className="small faint grow">{tr('The repository’s URL, for ansible-galaxy’s --server')}</span>
                        <CopyButton text={server} label={tr('Copy')} />
                      </div>
                      <pre className="code hub-url">{server}</pre>
                      {r.summary_fields?.galaxy_credential && (
                        <p className="small faint" style={{ margin: 0 }}>
                          {tr('Jobs install from it once an organization lists the credential')}{' '}
                          <Link to={`/credentials/${r.summary_fields.galaxy_credential.id}`}>{r.summary_fields.galaxy_credential.name}</Link>{' '}
                          {tr('among its Galaxy credentials.')}
                        </p>
                      )}
                    </div>
                  </Panel>
                </div>
                <div className="narrow">
                  <Panel title={tr('Details')}>
                    <KV
                      items={[
                        [tr('Repository'), <span className="mono">{r.repository}</span>],
                        [tr('Server'), r.summary_fields?.credential ? <Link to={`/credentials/${r.credential}`}>{r.summary_fields.credential.name}</Link> : ''],
                        [tr('Dependencies'), r.sync_dependencies ? tr('Mirrored too') : tr('Not mirrored')],
                        [
                          tr('Signatures'),
                          r.summary_fields?.signature_validation_credential ? (
                            <span>
                              {tr('Checked with')} <Link to={`/credentials/${r.signature_validation_credential}`}>{r.summary_fields.signature_validation_credential.name}</Link>
                            </span>
                          ) : (
                            tr('Not checked')
                          ),
                        ],
                        [tr('TLS'), r.tls_validation ? tr('Certificate checked') : tr('Certificate not checked')],
                        [tr('Proxy'), r.proxy_url ? tr('Set') : tr('The server’s, if any')],
                        [tr('Organization'), <Link to={`/organizations/${r.organization}`}>{r.summary_fields?.organization?.name}</Link>],
                        [tr('Last sync'), r.last_job_run ? <Ago at={r.last_job_run} /> : tr('Never')],
                      ]}
                    />
                  </Panel>
                </div>
              </div>
            }
          />
          <Route path="syncs" element={<Syncs remote={id} />} />
          <Route path="edit" element={<RemoteForm remote={r} />} />
        </Routes>
      </div>
    </div>
  )
}

function Syncs({ remote }: { remote: number }) {
  const list = useList<Any>(`collection_remotes/${remote}/collection_syncs/`, { order_by: '-id', page_size: 25 }, { refetchInterval: 10_000 })
  if (list.error) return <ErrorNote error={list.error} />
  if (!list.data) return <Loading />
  if (!list.data.results.length) return <Empty title={tr('No syncs yet')} />
  return (
    <div className="panel">
      <div className="table-wrap">
        <table className="data stack-sm">
          <thead>
            <tr>
              <th>{tr('Sync')}</th>
              <th>{tr('Status')}</th>
              <th>{tr('Started')}</th>
              <th>{tr('Took')}</th>
              <th>{tr('By')}</th>
            </tr>
          </thead>
          <tbody>
            {list.data.results.map((j) => (
              <tr key={j.id}>
                <td data-label="">
                  <Link to={jobPath({ id: j.id, type: 'collection_sync' })} className="mono">
                    #{j.id}
                  </Link>
                </td>
                <td data-label={tr('Status')}>
                  <Status status={j.status} />
                </td>
                <td data-label={tr('Started')}>
                  <Ago at={j.started ?? j.created} />
                </td>
                <td data-label={tr('Took')}>
                  <Elapsed job={j} />
                </td>
                <td data-label={tr('By')}>{j.summary_fields?.created_by?.username ?? j.launch_type ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

interface RemoteValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  credential: Ref | null
  requirements_file: string
  sync_dependencies: boolean
  signature_validation_credential: Ref | null
  proxy_url: string
  tls_validation: boolean
  repository: string
}

const EXAMPLE = 'collections:\n  - name: community.general\n    version: ">=9.0.0,<10.0.0"\n'

function RemoteForm({ remote }: { remote?: Remote }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const invalidate = useInvalidate()
  const f = useForm<RemoteValues>({
    name: remote?.name ?? '',
    description: remote?.description ?? '',
    organization: remote?.summary_fields?.organization ?? org,
    credential: refOf(remote?.summary_fields?.credential, remote?.credential),
    requirements_file: remote?.requirements_file ?? EXAMPLE,
    sync_dependencies: remote?.sync_dependencies ?? true,
    signature_validation_credential: refOf(remote?.summary_fields?.signature_validation_credential, remote?.signature_validation_credential),
    proxy_url: remote?.proxy_url ?? '',
    tls_validation: remote?.tls_validation ?? true,
    repository: remote?.repository ?? '',
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !remote)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    setError(null)
    const v = f.values
    const body: Any = {
      name: v.name,
      description: v.description,
      organization: v.organization?.id,
      credential: v.credential?.id ?? null,
      requirements_file: v.requirements_file,
      sync_dependencies: v.sync_dependencies,
      signature_validation_credential: v.signature_validation_credential?.id ?? null,
      tls_validation: v.tls_validation,
    }
    if (v.proxy_url !== SECRET) body.proxy_url = v.proxy_url
    if (!remote) body.repository = v.repository
    try {
      const saved = remote ? await api.patch<Remote>(`collection_remotes/${remote.id}/`, body) : await api.post<Remote>('collection_remotes/', body)
      invalidate('collection_remotes')
      nav(`/collection-remotes/${saved.id}`)
    } catch (e) {
      setError(e)
      if (e instanceof ApiError) f.setErrors(e.fields)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      className="form"
      style={{ maxWidth: 900 }}
      onSubmit={(ev) => {
        ev.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput label={tr('Name')} value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!remote} />
        <Lookup
          label={tr('Organization')}
          resource="organizations/"
          value={f.values.organization}
          onChange={(v) => f.set('organization', v)}
          error={f.errors.organization}
          help={tr('Its members and jobs read the mirrored collections; the namespaces syncs create are its.')}
        />
        <Lookup
          label={tr('Server')}
          resource="credentials/"
          params={{ credential_type__kind: 'galaxy' }}
          value={f.values.credential}
          onChange={(v) => f.set('credential', v)}
          error={f.errors.credential}
          help={tr('An Ansible Galaxy/Automation Hub API Token credential: the server’s URL and its token.')}
        />
        {!remote && (
          <TextInput
            label={tr('Repository')}
            optional
            value={f.values.repository}
            onChange={(v) => f.set('repository', v)}
            error={f.errors.repository}
            help={tr('Where the collections go, and clients install them from. Empty: one from the remote’s name. It cannot change later.')}
          />
        )}
        <TextArea label={tr('Description')} optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      <section className="form-section">
        <h2>{tr('Collections to mirror')}</h2>
        <TextArea
          label={tr('Requirements')}
          rows={8}
          mono
          value={f.values.requirements_file}
          onChange={(v) => f.set('requirements_file', v)}
          error={f.errors.requirements_file}
          help={tr('As a requirements.yml lists them: each name, with a version range of the versions to mirror.')}
        />
        <Check label={tr('Mirror their dependencies too')} checked={f.values.sync_dependencies} onChange={(v) => f.set('sync_dependencies', v)} help={tr('The highest version in the range each mirrored version asks for.')} />
      </section>
      <section className="form-section">
        <h2>{tr('Trust')}</h2>
        <div className="form-grid">
          <Lookup
            label={tr('Signatures checked with')}
            optional
            resource="credentials/"
            params={{ credential_type__kind: 'cryptography' }}
            value={f.values.signature_validation_credential}
            onChange={(v) => f.set('signature_validation_credential', v)}
            error={f.errors.signature_validation_credential}
            help={tr('A GPG Public Key credential: syncs keep only the versions its keys signed.')}
          />
          <TextInput
            label={tr('Proxy')}
            optional
            value={f.values.proxy_url}
            onChange={(v) => f.set('proxy_url', v)}
            error={f.errors.proxy_url}
            help={tr('Such as http://user:password@proxy.example.com:3128. Empty: the server’s HTTPS_PROXY, if any.')}
          />
        </div>
        <Check label={tr('Check the server’s TLS certificate')} checked={f.values.tls_validation} onChange={(v) => f.set('tls_validation', v)} />
      </section>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !f.values.organization || !f.values.credential}>
          {busy ? tr('Saving…') : remote ? tr('Save changes') : tr('Create remote')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(remote ? `/collection-remotes/${remote.id}` : '/collection-remotes')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}
