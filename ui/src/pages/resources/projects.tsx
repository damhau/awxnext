import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { Link, Outlet, useNavigate, useOutletContext, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import { useLive } from '../../api/live'
import type { Base, Job, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { useCopy } from '../../components/CopyDialog'
import { BulkBar, BulkDelete, useBulk, useSelection } from '../../components/BulkBar'
import { Check, FormError, Lookup, Segmented, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Menu, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Elapsed, Empty, ErrorNote, KV, Loading, PageHead, Panel, RunBars, Status, StatusBadge, Tab } from '../../components/ui'
import { shortSha, plural } from '../../lib/format'
import { compile, statusValue, type KeyTable } from '../../lib/query'
import { SchedulesTab } from '../automation/SchedulesTab'
import { applyErrors, FilterBox, ManagedNotice, NameCell, Provenance, refOf, useDelete, useOrgColumn, folderColumn, folderKey } from './common'
import { NotificationsTab } from '../../components/NotificationsTab'
import { scmTypeLabel, scmTypeOptions, sourceBody, type ScmType } from '../../lib/projectSource'

export interface Project extends Base {
  scm_type: ScmType
  scm_url: string
  scm_branch: string
  scm_refspec: string
  scm_revision: string
  scm_clean: boolean
  scm_delete_on_update: boolean
  scm_track_submodules: boolean
  scm_update_on_launch: boolean
  scm_update_cache_timeout: number
  allow_override: boolean
  credential: number | null
  signature_validation_credential: number | null
  default_environment: number | null
  local_path: string
  timeout: number
  status: string
  last_updated: string | null
  last_update_failed: boolean
  organization: number
  folder: number | null
  managed_by?: string
}

const keys: KeyTable = {
  status: { field: 'status', map: statusValue },
  org: { field: 'organization__name', lookup: 'iexact' },
  folder: folderKey,
  type: { field: 'scm_type', map: (v) => (v === 'manual' ? '' : v) },
  url: { field: 'scm_url', lookup: 'icontains' },
}

/** Starts a project sync and reports where to follow it. */
export function useSync() {
  const toast = useToast()
  const invalidate = useInvalidate()
  return async (p: Pick<Project, 'id' | 'name'>) => {
    try {
      const r = await api.post<{ project_update?: number; id?: number }>(`projects/${p.id}/update/`)
      const jid = r.project_update ?? r.id
      toast(
        <span>
          Syncing {p.name}
          {jid ? (
            <>
              {' · '}
              <Link to={`/jobs/project_update/${jid}`}>follow output</Link>
            </>
          ) : null}
        </span>,
      )
      invalidate('projects', 'project_updates')
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Sync failed', 'error')
    }
  }
}

function urlLabel(u: string) {
  return u.replace(/^https?:\/\//, '').replace(/^git@/, '').replace(/\.git$/, '')
}

export function ProjectList() {
  const ls = useListState({ order: 'name' })
  const { scope } = useOrg()
  const nav = useNavigate()
  const q = compile(ls.q, keys)
  const params = { ...q.params, ...scope, order_by: ls.order, page: ls.page, page_size: ls.pageSize }
  const list = useList<Project>('projects/', params)
  const sel = useSelection<Project>(list.data?.results)
  const run = useBulk<Project>(['project', 'projects'], 'projects')
  const sync = useSync()
  const del = useDelete('projects')
  const copy = useCopy('projects', { to: (id) => `/projects/${id}`, what: 'Git projects get their own checkout, synced right away.' })
  const orgCol = useOrgColumn<Project>()
  const invalidate = useInvalidate()
  useLive({ jobs: ['status_changed'] }, (m) => m.type === 'project_update' && invalidate('projects'))
  const cols: Column<Project>[] = [
    {
      key: 'name',
      header: 'Project',
      sort: 'name',
      render: (p) => <NameCell mono to={`/projects/${p.id}`} name={p.name} sub={p.scm_type ? urlLabel(p.scm_url) : `manual · ${p.local_path || 'no directory'}`} />,
    },
    { key: 'runs', header: 'Recent syncs', render: (p) => (p.scm_type ? <RunBars jobs={p.summary_fields.recent_jobs} slots={10} /> : <span className="faint small">—</span>) },
    {
      key: 'status',
      header: 'Last sync',
      sort: 'last_job_run',
      render: (p) => (
        <div className="small nowrap">
          <Status status={p.status} />
          <div className="faint" style={{ marginTop: 2 }}>
            {p.last_updated ? <Ago at={p.last_updated} /> : 'never'}
          </div>
        </div>
      ),
    },
    {
      key: 'rev',
      header: 'Revision',
      render: (p) =>
        p.scm_revision ? (
          <span className="mono small" title={p.scm_revision}>
            {p.scm_branch || 'default'}@{shortSha(p.scm_revision)}
          </span>
        ) : (
          <span className="faint small">—</span>
        ),
    },
    ...orgCol,
    ...folderColumn(list.data?.results),
    {
      key: 'act',
      header: <span className="sr-only">Actions</span>,
      className: 'end',
      render: (p) => (
        <div className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
          {p.scm_type && (
            <button className="btn sm" onClick={() => sync(p)} disabled={p.status === 'running' || p.status === 'pending' || p.status === 'waiting'}>
              <Icon name="refresh" size={14} /> Sync
            </button>
          )}
          <Menu label={`Actions for ${p.name}`} className="btn sm icon ghost">
            <Link to={`/projects/${p.id}/edit`}>Edit</Link>
            <Link to={`/projects/${p.id}/jobs`}>Sync history</Link>
            {p.summary_fields.user_capabilities?.copy && <button onClick={() => copy.open(p)}>Copy…</button>}
            <hr />
            <button className="danger" onClick={() => del(p, 'project')}>
              Delete…
            </button>
          </Menu>
        </div>
      ),
    },
  ]
  return (
    <div className="page">
      <PageHead
        title="Projects"
        subtitle="Git repositories (or directories on the controller) holding playbooks, roles and collection requirements."
        bordered={false}
        actions={
          <Link to="/projects/new" className="btn primary">
            New project
          </Link>
        }
      />
      {copy.dialog}
      <div className="page-body">
        <FilterBox value={ls.q} onApply={ls.setQ} placeholder="name…  try  status:failed  type:manual  folder:checkout" />
        <BulkBar sel={sel} page={list.data?.results} total={list.data?.count} path="projects/" params={params} style={{ marginBottom: 12 }}>
          <button
            className="btn"
            onClick={() =>
              run('Sync', sel.rows, (p) => (p.scm_type ? api.post(`projects/${p.id}/update/`, {}) : Promise.reject(new Error(`${p.name} is a manual project, with nothing to sync`))))
            }
          >
            Sync
          </button>
          <BulkDelete sel={sel} resource="projects" noun={['project', 'projects']} name={(p) => p.name} note="Templates using them stop working until given another project; job history stays." />
        </BulkBar>
        <DataTable
          label="Projects"
          rows={list.data?.results}
          columns={cols}
          selected={sel.selected}
          onSelect={sel.onSelect}
          columnsKey="projects"
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(p) => nav(`/projects/${p.id}`)}
          keys={{ s: (p) => p.scm_type && sync(p) }}
          empty={
            <Empty title={ls.q ? 'No project matches' : 'No projects yet'} action={!ls.q && <Link to="/projects/new" className="btn primary">New project</Link>}>
              {ls.q ? 'Try a shorter filter.' : 'A project points at the Git repository that holds your playbooks.'}
            </Empty>
          }
          footer={
            <div className="table-foot">
              <span>{list.data ? `${plural(list.data.count, 'project', 'projects')}` : ''}</span>
              <span className="mono">j/k move · s sync · enter open</span>
              <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
    </div>
  )
}

export function ProjectForm() {
  const { id } = useParams()
  const existing = useObject<Project>(id ? `projects/${id}/` : null)
  if (id && existing.isPending) return <Loading />
  if (existing.error) return <div className="page-body"><ErrorNote error={existing.error} /></div>
  return <ProjectFormInner key={id ?? 'new'} p={existing.data} />
}

function ProjectFormInner({ p }: { p?: Project }) {
  const { org } = useOrg()
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const toast = useToast()
  const paths = useQuery({ queryKey: ['config'], queryFn: () => api.get<{ project_local_paths: string[]; project_base_dir: string }>('config/'), staleTime: 60_000 })
  const f = useForm({
    name: p?.name ?? '',
    description: p?.description ?? '',
    organization: refOf(p?.summary_fields.organization, p?.organization) ?? (org as Ref | null),
    folder: (p?.summary_fields.folder as Ref | undefined) ?? null,
    scm_type: (p?.scm_type ?? 'git') as ScmType,
    scm_url: p?.scm_url ?? '',
    scm_branch: p?.scm_branch ?? '',
    scm_refspec: p?.scm_refspec ?? '',
    credential: refOf(p?.summary_fields.credential, p?.credential),
    signature_validation_credential: refOf(p?.summary_fields.signature_validation_credential, p?.signature_validation_credential),
    default_environment: refOf(p?.summary_fields.default_environment, p?.default_environment),
    scm_clean: p?.scm_clean ?? false,
    scm_delete_on_update: p?.scm_delete_on_update ?? false,
    scm_track_submodules: p?.scm_track_submodules ?? false,
    scm_update_on_launch: p?.scm_update_on_launch ?? false,
    scm_update_cache_timeout: p?.scm_update_cache_timeout ?? 0,
    allow_override: p?.allow_override ?? false,
    local_path: p?.local_path ?? '',
    timeout: p?.timeout ?? 0,
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !p)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const v = f.values
  const git = v.scm_type === 'git'
  const scm = v.scm_type !== ''
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    const body: Record<string, unknown> = {
      name: v.name,
      description: v.description,
      organization: v.organization?.id ?? null,
      folder: v.folder?.id ?? null,
      scm_type: v.scm_type,
      default_environment: v.default_environment?.id ?? null,
      timeout: Number(v.timeout) || 0,
    }
    Object.assign(
      body,
      sourceBody(v.scm_type, { ...v, credential: v.credential?.id ?? null, signature_validation_credential: v.signature_validation_credential?.id ?? null }),
    )
    try {
      const saved = p ? await api.patch<Project>(`projects/${p.id}/`, body) : await api.post<Project>('projects/', body)
      invalidate('projects')
      toast(p ? `Saved ${saved.name}` : scm ? `Created ${saved.name}; first sync started` : `Created ${saved.name}`)
      nav(`/projects/${saved.id}`)
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: 'Projects', to: '/projects' }, ...(p ? [{ label: p.name, to: `/projects/${p.id}` }] : []), { label: p ? 'Edit' : 'New' }]}
        title={p ? `Edit ${p.name}` : 'New project'}
      />
      <form className="page-body form" onSubmit={submit} style={{ maxWidth: 920 }}>
        <ManagedNotice managedBy={p?.managed_by} />
        <div className="form-grid">
          <TextInput label="Name" value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus />
          <Lookup label="Organization" resource="organizations/" value={v.organization} onChange={(x) => f.set('organization', x)} error={f.errors.organization} />
          <TextInput label="Description" optional value={v.description} onChange={(x) => f.set('description', x)} full />
          <Lookup
            label="Folder"
            optional
            resource="folders/"
            params={v.organization ? { organization: v.organization.id } : undefined}
            value={v.folder}
            onChange={(x) => f.set('folder', x)}
            error={f.errors.folder}
          />
          <Lookup
            label="Default execution environment"
            optional
            resource="execution_environments/"
            value={v.default_environment}
            onChange={(x) => f.set('default_environment', x)}
            error={f.errors.default_environment}
            help="Used by templates of this project that do not pick one."
          />
        </div>
        <div className="form-section">
          <h2>Source</h2>
          <Segmented legend="Where the content lives" value={v.scm_type} onChange={(x) => f.set('scm_type', x)} options={scmTypeOptions} />
          {scm ? (
            <>
              <div className="form-grid">
                <TextInput
                  label={git ? 'Repository URL' : 'Archive URL'}
                  mono
                  value={v.scm_url}
                  onChange={(x) => f.set('scm_url', x)}
                  error={f.errors.scm_url}
                  placeholder={git ? 'https://github.com/acme/playbooks.git' : 'https://github.com/acme/playbooks/archive/refs/tags/v1.0.tar.gz'}
                  help={
                    git
                      ? 'HTTPS or SSH (git@host:org/repo.git).'
                      : "A zip or tar (gzip, bzip2 or xz) over HTTP or HTTPS. A single top-level directory is removed; the revision is the archive's SHA-1."
                  }
                  full
                />
                {git && (
                  <>
                    <TextInput label="Branch, tag or commit" optional mono value={v.scm_branch} onChange={(x) => f.set('scm_branch', x)} error={f.errors.scm_branch} help="Empty uses the default branch." />
                    <TextInput label="Refspec" optional mono value={v.scm_refspec} onChange={(x) => f.set('scm_refspec', x)} help="Extra refs to fetch, e.g. refs/pull/*:refs/remotes/origin/pull/*." />
                  </>
                )}
                <Lookup
                  label="Source control credential"
                  optional
                  resource="credentials/"
                  params={{ credential_type__kind: 'scm' }}
                  value={v.credential}
                  onChange={(x) => f.set('credential', x)}
                  error={f.errors.credential}
                  help={git ? 'Needed for private repositories.' : 'Its username and password are sent with basic auth.'}
                />
                <Lookup
                  label="Signature validation credential"
                  optional
                  resource="credentials/"
                  params={{ credential_type__kind: 'cryptography' }}
                  value={v.signature_validation_credential}
                  onChange={(x) => f.set('signature_validation_credential', x)}
                  error={f.errors.signature_validation_credential}
                  help="A GPG public key. Each sync then checks the ansible-sign signature and checksums, and fails if a file changed."
                />
              </div>
              <div className="form-section">
                <h2>Sync behaviour</h2>
                <div className="form-grid">
                  <Check
                    label="Sync before each job"
                    checked={v.scm_update_on_launch}
                    onChange={(x) => f.set('scm_update_on_launch', x)}
                    help={git ? 'Jobs always run the latest commit.' : 'Jobs always run what the URL serves.'}
                  />
                  {v.scm_update_on_launch && (
                    <TextInput
                      label="Cache timeout (seconds)"
                      type="number"
                      value={v.scm_update_cache_timeout}
                      onChange={(x) => f.set('scm_update_cache_timeout', Number(x))}
                      help="Reuse a sync newer than this instead of fetching again."
                    />
                  )}
                  {git && (
                    <>
                      <Check label="Allow branch override" checked={v.allow_override} onChange={(x) => f.set('allow_override', x)} help="Templates and launches may pick another branch." />
                      <Check label="Discard local changes" checked={v.scm_clean} onChange={(x) => f.set('scm_clean', x)} />
                      <Check label="Fresh clone on every sync" checked={v.scm_delete_on_update} onChange={(x) => f.set('scm_delete_on_update', x)} help="Slower; use when the checkout gets into a bad state." />
                      <Check label="Track submodules" checked={v.scm_track_submodules} onChange={(x) => f.set('scm_track_submodules', x)} help="Follow the latest commit of each submodule branch." />
                    </>
                  )}
                </div>
              </div>
            </>
          ) : (
            <div className="form-grid">
              <div className="field full">
                <label htmlFor="lp">Directory</label>
                {paths.data?.project_local_paths?.length || v.local_path ? (
                  <select id="lp" className="select" value={v.local_path} onChange={(e) => f.set('local_path', e.target.value)}>
                    <option value="">Choose a directory…</option>
                    {[...new Set([...(paths.data?.project_local_paths ?? []), ...(v.local_path ? [v.local_path] : [])])].map((x) => (
                      <option key={x} value={x}>
                        {x}
                      </option>
                    ))}
                  </select>
                ) : (
                  <div className="notice">
                    No unused directory under <code>{paths.data?.project_base_dir ?? 'the projects directory'}</code>. Create one on the controller, then reload.
                  </div>
                )}
                {f.errors.local_path ? <span className="err">{f.errors.local_path}</span> : <span className="help">Directories under {paths.data?.project_base_dir ?? 'the projects directory'} not used by another project.</span>}
              </div>
            </div>
          )}
          <div className="form-grid">
            <TextInput label="Sync timeout (seconds)" type="number" value={v.timeout} onChange={(x) => f.set('timeout', Number(x))} help="0 means no timeout." />
          </div>
        </div>
        <FormError error={error} />
        <div className="form-actions">
          <button className="btn primary" disabled={busy || !v.name || !v.organization || (scm && !v.scm_url)}>
            {busy ? 'Saving…' : p ? 'Save' : 'Create project'}
          </button>
          <button type="button" className="btn ghost" onClick={() => nav(-1)}>
            Cancel
          </button>
        </div>
      </form>
    </div>
  )
}

export function ProjectDetail() {
  const { id } = useParams()
  const proj = useObject<Project>(`projects/${id}/`)
  const sync = useSync()
  const del = useDelete('projects', '/projects')
  const copy = useCopy('projects', { to: (id) => `/projects/${id}`, what: 'Git projects get their own checkout, synced right away.' })
  const invalidate = useInvalidate()
  useLive({ jobs: ['status_changed'] }, (m) => m.type === 'project_update' && String(m.unified_job_template_id) === String(id) && invalidate(`projects/${id}`, 'project_updates'))
  if (proj.isPending) return <Loading />
  if (proj.error || !proj.data) return <div className="page-body"><ErrorNote error={proj.error ?? 'Not found'} /></div>
  const p = proj.data
  const caps = p.summary_fields.user_capabilities ?? {}
  const base = `/projects/${p.id}`
  return (
    <div className="page">
      {copy.dialog}
      <PageHead
        crumbs={[{ label: 'Projects', to: '/projects' }, { label: p.name }]}
        title={p.name}
        badge={<StatusBadge status={p.status} />}
        subtitle={p.description || undefined}
        actions={
          <>
            {p.scm_type && (
              <button className="btn primary" onClick={() => sync(p)} disabled={p.status === 'running' || p.status === 'pending' || p.status === 'waiting'}>
                <Icon name="refresh" size={14} /> Sync now
              </button>
            )}
            {caps.edit && !p.managed_by && (
              <Link className="btn" to={`${base}/edit`}>
                Edit
              </Link>
            )}
            {(caps.copy || caps.delete) && (
              <Menu label="More actions">
                {caps.copy && <button onClick={() => copy.open(p)}>Copy…</button>}
                {caps.copy && caps.delete && <hr />}
                {caps.delete && (
                  <button className="danger" onClick={() => del(p, 'project')}>
                    Delete…
                  </button>
                )}
              </Menu>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              Details
            </Tab>
            <Tab to={`${base}/jobs`}>Syncs</Tab>
            <Tab to={`${base}/schedules`}>Schedules</Tab>
            <Tab to={`${base}/notifications`}>Notifications</Tab>
            <Tab to={`${base}/access`}>Access</Tab>
          </>
        }
      />
      <Outlet context={p} />
    </div>
  )
}

export const useProject = () => useOutletContext<Project>()

export function ProjectDetailsTab() {
  const p = useProject()
  const playbooks = useQuery({ queryKey: [`projects/${p.id}/playbooks/`, p.scm_revision], queryFn: () => api.get<string[]>(`projects/${p.id}/playbooks/`) })
  const templates = useList<Base>('job_templates/', { project: p.id, page_size: 10, order_by: 'name' })
  const inventories = useList<Base>('inventory_sources/', { source_project: p.id, page_size: 10, order_by: 'name' })
  const last = p.summary_fields.last_job as (Base & { status: string; finished: string }) | undefined
  const [filter, setFilter] = useState('')
  const books = (playbooks.data ?? []).filter((b) => !filter || b.toLowerCase().includes(filter.toLowerCase()))
  return (
    <div className="page-body">
      <ManagedNotice managedBy={p.managed_by} />
      <div className="split">
        <div className="wide stack" style={{ gap: 16 }}>
          <Panel title="Source">
            <KV
              items={[
                ['Type', scmTypeLabel(p.scm_type)],
                p.scm_type ? [p.scm_type === 'git' ? 'Repository' : 'Archive URL', <span className="mono">{p.scm_url}</span>] : ['Directory', <span className="mono">{p.local_path}</span>],
                p.scm_type === 'git' ? ['Branch', <span className="mono">{p.scm_branch || 'default branch'}</span>] : [null, null],
                p.scm_type ? ['Revision', p.scm_revision ? <span className="mono" title={p.scm_revision}>{p.scm_revision}</span> : <span className="faint">not synced yet</span>] : [null, null],
                p.scm_refspec ? ['Refspec', <span className="mono">{p.scm_refspec}</span>] : [null, null],
                p.scm_type ? ['Credential', p.summary_fields.credential ? <Link to={`/credentials/${p.credential}`}>{p.summary_fields.credential.name}</Link> : <span className="faint">none (public)</span>] : [null, null],
                p.summary_fields.signature_validation_credential
                  ? ['Signature validation', <Link to={`/credentials/${p.signature_validation_credential}`}>{p.summary_fields.signature_validation_credential.name}</Link>]
                  : [null, null],
                [
                  'Options',
                  [
                    p.scm_update_on_launch && `sync before each job${p.scm_update_cache_timeout ? ` (cache ${p.scm_update_cache_timeout}s)` : ''}`,
                    p.allow_override && 'branch override allowed',
                    p.scm_clean && 'discard local changes',
                    p.scm_delete_on_update && 'fresh clone',
                    p.scm_track_submodules && 'track submodules',
                  ]
                    .filter(Boolean)
                    .join(' · ') || '—',
                ],
              ].filter(([k]) => k !== null) as [string, React.ReactNode][]}
            />
          </Panel>
          <Panel
            title={`Playbooks · ${playbooks.data?.length ?? '…'}`}
            actions={
              (playbooks.data?.length ?? 0) > 8 && (
                <input className="input mono" style={{ height: 28, width: 200 }} placeholder="filter…" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter playbooks" />
              )
            }
          >
            {playbooks.isPending ? (
              <Loading rows={3} />
            ) : books.length ? (
              <ul className="list-plain mono small" style={{ gap: 4 }}>
                {books.map((b) => (
                  <li key={b} style={{ justifyContent: 'flex-start' }}>
                    {b}
                  </li>
                ))}
              </ul>
            ) : (
              <span className="faint small">{p.scm_revision || !p.scm_type ? 'No playbooks found.' : 'Sync the project to list its playbooks.'}</span>
            )}
          </Panel>
        </div>
        <div className="narrow stack" style={{ gap: 16 }}>
          <Panel title="Last sync">
            {last ? (
              <div className="stack small" style={{ gap: 6 }}>
                <Link to={`/jobs/project_update/${last.id}`} className="row" style={{ gap: 6 }}>
                  <Status status={last.status} />
                  <span className="mono faint">#{last.id}</span>
                </Link>
                <span className="faint">
                  <Ago at={last.finished} />
                </span>
              </div>
            ) : (
              <span className="faint small">Never synced.</span>
            )}
          </Panel>
          <Panel title="Used by">
            <div className="stack small" style={{ gap: 6 }}>
              {(templates.data?.results ?? []).map((t) => (
                <Link key={t.id} to={`/templates/job/${t.id}`} className="ellipsis">
                  {t.name}
                </Link>
              ))}
              {(inventories.data?.results ?? []).map((s) => (
                <Link key={'s' + s.id} to={`/inventories/${s.inventory}/sources?source=${s.id}`} className="ellipsis">
                  {s.name} <span className="faint">· inventory source</span>
                </Link>
              ))}
              {!templates.data?.count && !inventories.data?.count && <span className="faint">Nothing uses this project yet.</span>}
              {(templates.data?.count ?? 0) > 10 && (
                <Link to={`/templates?q=${encodeURIComponent(`project:"${p.name}"`)}`} className="faint">
                  all {templates.data!.count} templates
                </Link>
              )}
            </div>
          </Panel>
          <Panel title="About">
            <KV
              items={[
                ['Organization', p.summary_fields.organization ? <Link to={`/organizations/${p.organization}`}>{p.summary_fields.organization.name}</Link> : '—'],
                ['Folder', p.summary_fields.folder?.name ?? ''],
                ['Default environment', p.summary_fields.default_environment?.name ?? <span className="faint">platform default</span>],
                ['History', <Provenance obj={p} />],
              ]}
            />
          </Panel>
        </div>
      </div>
    </div>
  )
}

export function ProjectJobsTab() {
  const p = useProject()
  const ls = useListState({ order: '-id' })
  const nav = useNavigate()
  const jobs = useList<Job>('project_updates/', { project: p.id, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const cols: Column<Job>[] = [
    { key: 'status', header: 'Status', width: 130, render: (j) => <Status status={j.status} /> },
    {
      key: 'id',
      header: 'Sync',
      sort: 'id',
      render: (j) => (
        <div>
          <Link to={`/jobs/project_update/${j.id}`} className="cell-title mono">
            #{j.id}
          </Link>
          <div className="cell-sub">{j.job_type === 'run' ? 'checkout for a job' : 'project sync'}</div>
        </div>
      ),
    },
    { key: 'rev', header: 'Revision', render: (j) => <span className="mono small">{shortSha(j.scm_revision) || '—'}</span> },
    { key: 'by', header: 'Started by', render: (j) => <span className="small">{j.launched_by?.name ?? j.launch_type}</span> },
    { key: 'took', header: 'Took', render: (j) => <Elapsed job={j} /> },
    { key: 'when', header: 'Finished', sort: 'finished', render: (j) => <span className="small nowrap">{j.finished ? <Ago at={j.finished} /> : <span className="faint">{j.status}</span>}</span> },
  ]
  return (
    <div className="page-body">
      <DataTable
        label="Project syncs"
        rows={jobs.data?.results}
        columns={cols}
        loading={jobs.isPending}
        error={jobs.error}
        order={ls.order}
        onOrder={ls.setOrder}
        onOpen={(j) => nav(`/jobs/project_update/${j.id}`)}
        empty={<Empty title="No syncs yet" />}
        footer={
          <div className="table-foot">
            <span>{jobs.data ? `${plural(jobs.data.count, 'sync', 'syncs')}` : ''}</span>
            <Pager count={jobs.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
          </div>
        }
      />
    </div>
  )
}

export function ProjectSchedulesTab() {
  const p = useProject()
  return (
    <div className="page-body">
      <SchedulesTab template={{ id: p.id, type: 'project', name: p.name }} />
    </div>
  )
}

export function ProjectNotificationsTab() {
  const p = useProject()
  return (
    <div className="page-body">
      <NotificationsTab resource="projects" id={p.id} editable={!!p.summary_fields.user_capabilities?.edit && !p.managed_by} />
    </div>
  )
}

export function ProjectAccessTab() {
  const p = useProject()
  return (
    <div className="page-body">
      <AccessTab resource="projects" id={p.id} name={p.name} />
    </div>
  )
}
