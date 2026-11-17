import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, ApiError, getAll } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any, Ref } from '../../api/types'
import { useQuery } from '@tanstack/react-query'
import { useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Check, FormError, Lookup, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog } from '../../components/overlay'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { importLogText } from '../../lib/hub'
import { asIs, tr } from '../../lib/i18n'
import { CopyButton, ListPage, useDelete } from '../admin/common'

type Ns = Any & { id: number }

export function NamespaceList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Ns>
      title={tr('Namespaces')}
      subtitle={tr('Who owns a collection: its namespace’s owners, uploaders and approvers.')}
      path="collection_namespaces/"
      params={scope}
      placeholder={tr('Filter namespaces…')}
      actions={
        <Link to="/collection-namespaces/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New namespace')}
        </Link>
      }
      onOpen={(n) => nav(`/collection-namespaces/${n.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Namespace'),
          sort: 'name',
          render: (n) => (
            <div>
              <Link to={`/collection-namespaces/${n.id}`} className="cell-title mono">
                {n.name}
              </Link>
              {(n.description || n.company) && <div className="cell-sub">{n.description || n.company}</div>}
            </div>
          ),
        },
        { key: 'approval', header: tr('Uploads'), render: (n) => <span className="small">{n.auto_approve ? tr('Published at once') : tr('Wait for approval')}</span> },
        { key: 'org', header: tr('Organization'), render: (n) => n.summary_fields?.organization?.name ?? '—' },
        { key: 'modified', header: tr('Changed'), sort: 'modified', render: (n) => <Ago at={n.modified} /> },
      ]}
      empty={<Empty title={tr('No namespaces yet')}>{tr('A namespace holds collections: its name is the first part of theirs, such as acme in acme.network.')}</Empty>}
    />
  )
}

export function NamespaceNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Namespaces'), to: '/collection-namespaces' }, { label: tr('New') }]} title={tr('New namespace')} />
      <div className="page-body">
        <NamespaceForm />
      </div>
    </div>
  )
}

const ROLES: [string, string][] = [
  [asIs('CollectionNamespace Admin'), tr('Owners')],
  [asIs('CollectionNamespace Upload'), tr('Uploaders')],
  [asIs('CollectionNamespace Approve'), tr('Approvers')],
  [asIs('CollectionNamespace Read'), tr('Readers')],
]

interface Assignment {
  id: number
  user?: number
  team?: number
  summary_fields: { role_definition?: { name: string }; user?: { id: number; username: string }; team?: { id: number; name: string } }
}

/** The people of a namespace, by role. */
function People({ ns }: { ns: Ns }) {
  const q = useQuery({
    queryKey: [`role_access/collection_namespaces/${ns.id}`, 'people'],
    queryFn: async () => {
      const params = { content_type: 'collectionnamespace', object_id: ns.id }
      const [users, teams] = await Promise.all([getAll<Assignment>('role_user_assignments/', params), getAll<Assignment>('role_team_assignments/', params)])
      return [...users, ...teams]
    },
  })
  if (q.error) return <ErrorNote error={q.error} />
  if (!q.data) return <Loading rows={3} />
  const org = ns.summary_fields?.organization
  return (
    <div className="stack" style={{ gap: 12 }}>
      <KV
        items={ROLES.map(([role, label]) => {
          const who = q.data.filter((a) => a.summary_fields.role_definition?.name === role)
          return [
            label,
            who.length ? (
              <span>
                {who.map((a, i) => (
                  <span key={a.id + (a.user ? 'u' : 't')}>
                    {i > 0 && ', '}
                    {a.summary_fields.user ? (
                      a.summary_fields.user.username
                    ) : (
                      <Link to={`/teams/${a.summary_fields.team?.id}`}>
                        {tr('team {name}', { name: a.summary_fields.team?.name ?? '' })}
                      </Link>
                    )}
                  </span>
                ))}
              </span>
            ) : (
              ''
            ),
          ]
        })}
      />
      {org && (
        <p className="small faint" style={{ margin: 0 }}>
          {tr('The admins of {org} own it too, and its members read its published collections.', { org: org.name })}
        </p>
      )}
    </div>
  )
}

export function NamespaceDetail() {
  const id = Number(useParams().id)
  const ns = useObject<Ns>(`collection_namespaces/${id}/`)
  const del = useDelete()
  if (ns.error)
    return (
      <div className="page-body">
        <ErrorNote error={ns.error} />
      </div>
    )
  if (!ns.data) return <Loading />
  const n = ns.data
  const caps = n.summary_fields?.user_capabilities ?? {}
  const base = `/collection-namespaces/${id}`
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Namespaces'), to: '/collection-namespaces' }, { label: n.name }]}
        title={n.name}
        badge={n.auto_approve ? <span className="badge ok">{tr('Published at once')}</span> : undefined}
        subtitle={n.description || undefined}
        actions={
          <>
            {caps.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path: `collection_namespaces/${id}/`, name: n.name, what: tr('namespace'), then: '/collection-namespaces' })}>
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
            {(caps.upload || caps.approve) && <Tab to={`${base}/imports`}>{tr('Uploads')}</Tab>}
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
                  <NamespaceCollections ns={n} />
                </div>
                <div className="narrow stack" style={{ gap: 16 }}>
                  <Panel title={tr('People')}>
                    <People ns={n} />
                  </Panel>
                  <Panel title={tr('Details')}>
                    <KV
                      items={[
                        [tr('Organization'), <Link to={`/organizations/${n.organization}`}>{n.summary_fields?.organization?.name}</Link>],
                        [tr('Uploads'), n.auto_approve ? tr('Published at once') : tr('Wait for an approver')],
                        [tr('Company'), n.company],
                        [tr('Email'), n.email],
                        [tr('Changed'), <Ago at={n.modified} />],
                      ]}
                    />
                  </Panel>
                  {caps.upload && (
                    <Panel title={tr('Upload')}>
                      <UploadHint ns={n.name} />
                    </Panel>
                  )}
                </div>
              </div>
            }
          />
          <Route path="imports" element={<Imports ns={n} />} />
          <Route path="edit" element={<NamespaceForm ns={n} />} />
          <Route path="access" element={<AccessTab resource="collection_namespaces" id={id} name={n.name} />} />
        </Routes>
      </div>
    </div>
  )
}

function UploadHint({ ns }: { ns: string }) {
  const server = `${window.location.origin}/api/galaxy/`
  const cmd = `ansible-galaxy collection publish ${ns}-<name>-<version>.tar.gz \\\n  --server ${server} \\\n  --token <token>`
  return (
    <div className="stack" style={{ gap: 6 }}>
      <div className="row">
        <span className="small faint grow">{tr('With ansible-galaxy, as an uploader')}</span>
        <CopyButton text={cmd} label={tr('Copy')} />
      </div>
      <pre className="code hub-url">{cmd}</pre>
    </div>
  )
}

function NamespaceCollections({ ns }: { ns: Ns }) {
  const list = useList<Any>('collections/', { namespace: ns.id, order_by: 'name', page_size: 100 })
  return (
    <Panel title={tr('Collections')} pad={false} actions={<Link to={`/collections?q=${encodeURIComponent('namespace:' + ns.name)}`} className="small">{tr('Search them')}</Link>}>
      {list.isPending ? (
        <div className="panel-body">
          <Loading rows={3} />
        </div>
      ) : !list.data?.results.length ? (
        <div className="panel-body faint small">{tr('No collection you can see.')}</div>
      ) : (
        <ul className="hub-doc-rows">
          {list.data.results.map((c) => (
            <li key={c.id}>
              <Link to={`/collections/${c.id}`} className="mono">
                {ns.name}.{c.name}
              </Link>
              <span className="muted small">
                {c.summary_fields?.latest_version ? (
                  <>
                    <span className="mono">{c.summary_fields.latest_version.version}</span>
                    {c.summary_fields.latest_version.description ? asIs(` · ${c.summary_fields.latest_version.description}`) : ''}
                  </>
                ) : null}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

const importCell = (state: string) => (state === 'completed' ? 'ok' : state === 'failed' ? 'failed' : 'running')

export function ImportLog({ imp }: { imp: Any }) {
  const messages: Any[] = imp.messages ?? []
  return (
    <div className="stack" style={{ gap: 8 }}>
      {imp.error?.description && <div className="notice warn small">{imp.error.description}</div>}
      <ol className="hub-log">
        {messages.map((m, i) => (
          <li key={i} className={`hub-log-${String(m.level ?? 'info').toLowerCase()}`}>
            <span className="hub-log-level">{m.level}</span>
            <span className="hub-log-msg">{m.message}</span>
          </li>
        ))}
      </ol>
      {messages.length > 0 && (
        <div>
          <CopyButton text={importLogText(messages)} label={tr('Copy the log')} />
        </div>
      )}
    </div>
  )
}

function Imports({ ns }: { ns: Ns }) {
  const list = useList<Any>('collection_imports/', { namespace: ns.id, order_by: '-id', page_size: 50 })
  const [open, setOpen] = useState<Any | null>(null)
  if (list.error) return <ErrorNote error={list.error} />
  if (!list.data) return <Loading />
  if (!list.data.results.length) return <Empty title={tr('No uploads yet')}>{tr('Each upload to the namespace is imported and checked; its log stays here for a while.')}</Empty>
  return (
    <div className="panel">
      <div className="table-wrap">
        <table className="data stack-sm">
          <thead>
            <tr>
              <th>{tr('Upload')}</th>
              <th>{tr('State')}</th>
              <th>{tr('By')}</th>
              <th>{tr('When')}</th>
            </tr>
          </thead>
          <tbody>
            {list.data.results.map((i) => (
              <tr key={i.id}>
                <td data-label="">
                  <button type="button" className="link-btn mono" onClick={() => setOpen(i)}>
                    {i.collection_name ? asIs(`${i.namespace_name}.${i.collection_name} ${i.version}`) : i.filename}
                  </button>
                </td>
                <td data-label={tr('State')}>
                  <span className="status">
                    <Cell state={importCell(i.state)} />
                    {i.state === 'completed' ? tr('Imported') : i.state === 'failed' ? tr('Failed') : tr('Importing')}
                  </span>
                </td>
                <td data-label={tr('By')}>{i.summary_fields?.created_by?.username ?? '—'}</td>
                <td data-label={tr('When')}>
                  <Ago at={i.created} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {open && (
        <Dialog title={tr('Import of {name}', { name: open.filename })} onClose={() => setOpen(null)} wide>
          <ImportLog imp={open} />
          {open.collection_version && (
            <p className="small" style={{ marginBottom: 0 }}>
              <Link to={`/collection-versions/${open.collection_version}`}>{tr('Open the version')}</Link>
            </p>
          )}
        </Dialog>
      )}
    </div>
  )
}

interface NsValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  company: string
  email: string
  auto_approve: boolean
}

function NamespaceForm({ ns }: { ns?: Ns }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const invalidate = useInvalidate()
  const f = useForm<NsValues>({
    name: ns?.name ?? '',
    description: ns?.description ?? '',
    organization: ns?.summary_fields?.organization ?? org,
    company: ns?.company ?? '',
    email: ns?.email ?? '',
    auto_approve: ns?.auto_approve ?? false,
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !ns)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    setError(null)
    const body = { ...f.values, organization: f.values.organization?.id }
    try {
      const saved = ns ? await api.patch<Ns>(`collection_namespaces/${ns.id}/`, body) : await api.post<Ns>('collection_namespaces/', body)
      invalidate('collection_namespaces')
      nav(`/collection-namespaces/${saved.id}`)
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
      style={{ maxWidth: 820 }}
      onSubmit={(ev) => {
        ev.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput
          label={tr('Name')}
          value={f.values.name}
          onChange={(v) => f.set('name', v)}
          error={f.errors.name}
          autoFocus={!ns}
          help={tr('Lowercase letters, digits and underscores: the first part of its collections’ names.')}
        />
        <Lookup label={tr('Organization')} resource="organizations/" value={f.values.organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} help={tr('Its members see and install the namespace’s published collections.')} />
        <TextInput label={tr('Company')} optional value={f.values.company} onChange={(v) => f.set('company', v)} error={f.errors.company} />
        <TextInput label={tr('Email')} optional value={f.values.email} onChange={(v) => f.set('email', v)} error={f.errors.email} />
        <TextArea label={tr('Description')} optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      <Check
        label={tr('Publish uploads at once')}
        checked={f.values.auto_approve}
        onChange={(v) => f.set('auto_approve', v)}
        help={tr('Off: each upload waits in the approval queue until an approver of the namespace publishes it.')}
      />
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !f.values.organization}>
          {busy ? tr('Saving…') : ns ? tr('Save changes') : tr('Create namespace')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(ns ? `/collection-namespaces/${ns.id}` : '/collection-namespaces')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}
