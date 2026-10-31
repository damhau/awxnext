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
import { UsedByPanel } from '../../components/UsedBy'
import { tr } from '../../lib/i18n'

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
          {tr('Syncing')} {p.name}
          {jid ? (
            <>
              {' · '}
              <Link to={`/jobs/project_update/${jid}`}>{tr('follow output')}</Link>
            </>
          ) : null}
        </span>,
      )
      invalidate('projects', 'project_updates')
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Sync failed'), 'error')
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
  const copy = useCopy('projects', { to: (id) => `/projects/${id}`, what: tr('Git projects get their own checkout, synced right away.') })
  const orgCol = useOrgColumn<Project>()
  const invalidate = useInvalidate()
  useLive({ jobs: ['status_changed'] }, (m) => m.type === 'project_update' && invalidate('projects'))
  const cols: Column<Project>[] = [
    {
      key: 'name',
      header: tr('Project'),
      sort: 'name',
      render: (p) => <NameCell mono to={`/projects/${p.id}`} name={p.name} sub={p.scm_type ? urlLabel(p.scm_url) : `manual · ${p.local_path || 'no directory'}`} />,
    },
    { key: 'runs', header: tr('Recent syncs'), render: (p) => (p.scm_type ? <RunBars jobs={p.summary_fields.recent_jobs} slots={10} /> : <span className="faint small">—</span>) },
    {
      key: 'status',
      header: tr('Last sync'),
      sort: 'last_job_run',
      render: (p) => (
        <div className="small nowrap">
          <Status status={p.status} />
          <div className="faint" style={{ marginTop: 2 }}>
            {p.last_updated ? <Ago at={p.last_updated} /> : tr('never')}
          </div>
        </div>
      ),
    },
    {
      key: 'rev',
      header: tr('Revision'),
      render: (p) =>
        p.scm_revision ? (
          <span className="mono small" title={p.scm_revision}>
            {p.scm_branch || tr('default')}@{shortSha(p.scm_revision)}
          </span>
        ) : (
          <span className="faint small">—</span>
        ),
    },
    ...orgCol,
    ...folderColumn(list.data?.results),
    {
      key: 'act',
      header: <span className="sr-only">{tr('Actions')}</span>,
      className: 'end',
      render: (p) => (
        <div className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
          {p.scm_type && (
            <button className="btn sm" onClick={() => sync(p)} disabled={p.status === 'running' || p.status === 'pending' || p.status === 'waiting'}>
              <Icon name="refresh" size={14} /> {tr('Sync')}
            </button>
          )}
          <Menu label={tr('Actions for {name}', { name: p.name })} className="btn sm icon ghost">
            <Link to={`/projects/${p.id}/edit`}>{tr('Edit')}</Link>
            <Link to={`/projects/${p.id}/jobs`}>{tr('Sync history')}</Link>
            {p.summary_fields.user_capabilities?.copy && <button onClick={() => copy.open(p)}>{tr('Copy…')}</button>}
            <hr />
            <button className="danger" onClick={() => del(p, 'project')}>
              {tr('Delete…')}
            </button>
          </Menu>
        </div>
      ),
    },
  ]
  return (
    <div className="page">
      <PageHead
        title={tr('Projects')}
        subtitle={tr('Git repositories (or directories on the controller) holding playbooks, roles and collection requirements.')}
        bordered={false}
        actions={
          <Link to="/projects/new" className="btn primary">
            {tr('New project')}
          </Link>
        }
      />
      {copy.dialog}
      <div className="page-body">
        <FilterBox value={ls.q} onApply={ls.setQ} placeholder={tr('name…  try  status:failed  type:manual  folder:checkout')} />
        <BulkBar sel={sel} page={list.data?.results} total={list.data?.count} path="projects/" params={params} style={{ marginBottom: 12 }}>
          <button
            className="btn"
            onClick={() =>
              run('Sync', sel.rows, (p) => (p.scm_type ? api.post(`projects/${p.id}/update/`, {}) : Promise.reject(new Error(tr('{name} is a manual project, with nothing to sync', { name: p.name })))))
            }
          >
            {tr('Sync')}
          </button>
          <BulkDelete sel={sel} resource="projects" noun={['project', 'projects']} name={(p) => p.name} note={tr('Templates using them stop working until given another project; job history stays.')} />
        </BulkBar>
        <DataTable
          label={tr('Projects')}
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
            <Empty title={ls.q ? tr('No project matches') : tr('No projects yet')} action={!ls.q && <Link to="/projects/new" className="btn primary">{tr('New project')}</Link>}>
              {ls.q ? tr('Try a shorter filter.') : tr('A project points at the Git repository that holds your playbooks.')}
            </Empty>
          }
          footer={
            <div className="table-foot">
              <span>{list.data ? tr('{count}', { count: plural(list.data.count, 'project', 'projects') }) : ''}</span>
              <span className="mono">{tr('j/k move · s sync · enter open')}</span>
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
      toast(p ? tr('Saved {name}', { name: saved.name }) : scm ? tr('Created {name}; first sync started', { name: saved.name }) : tr('Created {name}', { name: saved.name }))
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
        crumbs={[{ label: tr('Projects'), to: '/projects' }, ...(p ? [{ label: p.name, to: `/projects/${p.id}` }] : []), { label: p ? tr('Edit') : tr('New') }]}
        title={p ? tr('Edit {name}', { name: p.name }) : tr('New project')}
      />
      <form className="page-body form" onSubmit={submit} style={{ maxWidth: 920 }}>
        <ManagedNotice managedBy={p?.managed_by} />
        <div className="form-grid">
          <TextInput label={tr('Name')} value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus />
          <Lookup label={tr('Organization')} resource="organizations/" value={v.organization} onChange={(x) => f.set('organization', x)} error={f.errors.organization} />
          <TextInput label={tr('Description')} optional value={v.description} onChange={(x) => f.set('description', x)} full />
          <Lookup
            label={tr('Folder')}
            optional
            resource="folders/"
            params={v.organization ? { organization: v.organization.id } : undefined}
            value={v.folder}
            onChange={(x) => f.set('folder', x)}
            error={f.errors.folder}
          />
          <Lookup
            label={tr('Default execution environment')}
            optional
            resource="execution_environments/"
            value={v.default_environment}
            onChange={(x) => f.set('default_environment', x)}
            error={f.errors.default_environment}
            help={tr('Used by templates of this project that do not pick one.')}
          />
        </div>
        <div className="form-section">
          <h2>{tr('Source')}</h2>
          <Segmented legend={tr('Where the content lives')} value={v.scm_type} onChange={(x) => f.set('scm_type', x)} options={scmTypeOptions} />
          {scm ? (
            <>
              <div className="form-grid">
                <TextInput
                  label={git ? tr('Repository URL') : tr('Archive URL')}
                  mono
                  value={v.scm_url}
                  onChange={(x) => f.set('scm_url', x)}
                  error={f.errors.scm_url}
                  placeholder={git ? tr('https://github.com/acme/playbooks.git') : tr('https://github.com/acme/playbooks/archive/refs/tags/v1.0.tar.gz')}
                  help={
                    git
                      ? tr('HTTPS or SSH (git@host:org/repo.git).')
                      : tr('A zip or tar (gzip, bzip2 or xz) over HTTP or HTTPS. A single top-level directory is removed; the revision is the archive\'s SHA-1.')
                  }
                  full
                />
                {git && (
                  <>
                    <TextInput label={tr('Branch, tag or commit')} optional mono value={v.scm_branch} onChange={(x) => f.set('scm_branch', x)} error={f.errors.scm_branch} help={tr('Empty uses the default branch.')} />
                    <TextInput label={tr('Refspec')} optional mono value={v.scm_refspec} onChange={(x) => f.set('scm_refspec', x)} help={tr('Extra refs to fetch, e.g. refs/pull/*:refs/remotes/origin/pull/*.')} />
                  </>
                )}
                <Lookup
                  label={tr('Source control credential')}
                  optional
                  resource="credentials/"
                  params={{ credential_type__kind: 'scm' }}
                  value={v.credential}
                  onChange={(x) => f.set('credential', x)}
                  error={f.errors.credential}
                  help={git ? tr('Needed for private repositories.') : tr('Its username and password are sent with basic auth.')}
                />
                <Lookup
                  label={tr('Signature validation credential')}
                  optional
                  resource="credentials/"
                  params={{ credential_type__kind: 'cryptography' }}
                  value={v.signature_validation_credential}
                  onChange={(x) => f.set('signature_validation_credential', x)}
                  error={f.errors.signature_validation_credential}
                  help={tr('A GPG public key. Each sync then checks the ansible-sign signature and checksums, and fails if a file changed.')}
                />
              </div>
              <div className="form-section">
                <h2>{tr('Sync behaviour')}</h2>
                <div className="form-grid">
                  <Check
                    label={tr('Sync before each job')}
                    checked={v.scm_update_on_launch}
                    onChange={(x) => f.set('scm_update_on_launch', x)}
                    help={git ? tr('Jobs always run the latest commit.') : tr('Jobs always run what the URL serves.')}
                  />
                  {v.scm_update_on_launch && (
                    <TextInput
                      label={tr('Cache timeout (seconds)')}
                      type="number"
                      value={v.scm_update_cache_timeout}
                      onChange={(x) => f.set('scm_update_cache_timeout', Number(x))}
                      help={tr('Reuse a sync newer than this instead of fetching again.')}
                    />
                  )}
                  {git && (
                    <>
                      <Check label={tr('Allow branch override')} checked={v.allow_override} onChange={(x) => f.set('allow_override', x)} help={tr('Templates and launches may pick another branch.')} />
                      <Check label={tr('Discard local changes')} checked={v.scm_clean} onChange={(x) => f.set('scm_clean', x)} />
                      <Check label={tr('Fresh clone on every sync')} checked={v.scm_delete_on_update} onChange={(x) => f.set('scm_delete_on_update', x)} help={tr('Slower; use when the checkout gets into a bad state.')} />
                      <Check label={tr('Track submodules')} checked={v.scm_track_submodules} onChange={(x) => f.set('scm_track_submodules', x)} help={tr('Follow the latest commit of each submodule branch.')} />
                    </>
                  )}
                </div>
              </div>
            </>
          ) : (
            <div className="form-grid">
              <div className="field full">
                <label htmlFor="lp">{tr('Directory')}</label>
                {paths.data?.project_local_paths?.length || v.local_path ? (
                  <select id="lp" className="select" value={v.local_path} onChange={(e) => f.set('local_path', e.target.value)}>
                    <option value="">{tr('Choose a directory…')}</option>
                    {[...new Set([...(paths.data?.project_local_paths ?? []), ...(v.local_path ? [v.local_path] : [])])].map((x) => (
                      <option key={x} value={x}>
                        {x}
                      </option>
                    ))}
                  </select>
                ) : (
                  <div className="notice">
                    {tr('No unused directory under')} <code>{paths.data?.project_base_dir ?? 'the projects directory'}</code>{tr('. Create one on the controller, then reload.')}
                  </div>
                )}
                {f.errors.local_path ? <span className="err">{f.errors.local_path}</span> : <span className="help">{tr('Directories under')} {paths.data?.project_base_dir ?? tr('the projects directory')} {tr('not used by another project.')}</span>}
              </div>
            </div>
          )}
          <div className="form-grid">
            <TextInput label={tr('Sync timeout (seconds)')} type="number" value={v.timeout} onChange={(x) => f.set('timeout', Number(x))} help={tr('0 means no timeout.')} />
          </div>
        </div>
        <FormError error={error} />
        <div className="form-actions">
          <button className="btn primary" disabled={busy || !v.name || !v.organization || (scm && !v.scm_url)}>
            {busy ? tr('Saving…') : p ? tr('Save') : tr('Create project')}
          </button>
          <button type="button" className="btn ghost" onClick={() => nav(-1)}>
            {tr('Cancel')}
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
  const copy = useCopy('projects', { to: (id) => `/projects/${id}`, what: tr('Git projects get their own checkout, synced right away.') })
  const invalidate = useInvalidate()
  useLive({ jobs: ['status_changed'] }, (m) => m.type === 'project_update' && String(m.unified_job_template_id) === String(id) && invalidate(`projects/${id}`, 'project_updates'))
  if (proj.isPending) return <Loading />
  if (proj.error || !proj.data) return <div className="page-body"><ErrorNote error={proj.error ?? tr('Not found')} /></div>
  const p = proj.data
  const caps = p.summary_fields.user_capabilities ?? {}
  const base = `/projects/${p.id}`
  return (
    <div className="page">
      {copy.dialog}
      <PageHead
        crumbs={[{ label: tr('Projects'), to: '/projects' }, { label: p.name }]}
        title={p.name}
        badge={<StatusBadge status={p.status} />}
        subtitle={p.description || undefined}
        actions={
          <>
            {p.scm_type && (
              <button className="btn primary" onClick={() => sync(p)} disabled={p.status === 'running' || p.status === 'pending' || p.status === 'waiting'}>
                <Icon name="refresh" size={14} /> {tr('Sync now')}
              </button>
            )}
            {caps.edit && !p.managed_by && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {(caps.copy || caps.delete) && (
              <Menu label={tr('More actions')}>
                {caps.copy && <button onClick={() => copy.open(p)}>{tr('Copy…')}</button>}
                {caps.copy && caps.delete && <hr />}
                {caps.delete && (
                  <button className="danger" onClick={() => del(p, 'project')}>
                    {tr('Delete…')}
                  </button>
                )}
              </Menu>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              {tr('Details')}
            </Tab>
            <Tab to={`${base}/jobs`}>{tr('Syncs')}</Tab>
            <Tab to={`${base}/schedules`}>{tr('Schedules')}</Tab>
            <Tab to={`${base}/notifications`}>{tr('Notifications')}</Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
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
  const last = p.summary_fields.last_job as (Base & { status: string; finished: string }) | undefined
  const [filter, setFilter] = useState('')
  const books = (playbooks.data ?? []).filter((b) => !filter || b.toLowerCase().includes(filter.toLowerCase()))
  return (
    <div className="page-body">
      <ManagedNotice managedBy={p.managed_by} />
      <div className="split">
        <div className="wide stack" style={{ gap: 16 }}>
          <Panel title={tr('Source')}>
            <KV
              items={[
                [tr('Type'), scmTypeLabel(p.scm_type)],
                p.scm_type ? [p.scm_type === 'git' ? 'Repository' : tr('Archive URL'), <span className="mono">{p.scm_url}</span>] : [tr('Directory'), <span className="mono">{p.local_path}</span>],
                p.scm_type === 'git' ? [tr('Branch'), <span className="mono">{p.scm_branch || tr('default branch')}</span>] : [null, null],
                p.scm_type ? [tr('Revision'), p.scm_revision ? <span className="mono" title={p.scm_revision}>{p.scm_revision}</span> : <span className="faint">{tr('not synced yet')}</span>] : [null, null],
                p.scm_refspec ? [tr('Refspec'), <span className="mono">{p.scm_refspec}</span>] : [null, null],
                p.scm_type ? [tr('Credential'), p.summary_fields.credential ? <Link to={`/credentials/${p.credential}`}>{p.summary_fields.credential.name}</Link> : <span className="faint">{tr('none (public)')}</span>] : [null, null],
                p.summary_fields.signature_validation_credential
                  ? [tr('Signature validation'), <Link to={`/credentials/${p.signature_validation_credential}`}>{p.summary_fields.signature_validation_credential.name}</Link>]
                  : [null, null],
                [
                  tr('Options'),
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
            title={tr('Playbooks · {count}', { count: playbooks.data?.length ?? '…' })}
            actions={
              (playbooks.data?.length ?? 0) > 8 && (
                <input className="input mono" style={{ height: 28, width: 200 }} placeholder={tr('filter…')} value={filter} onChange={(e) => setFilter(e.target.value)} aria-label={tr('Filter playbooks')} />
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
              <span className="faint small">{p.scm_revision || !p.scm_type ? tr('No playbooks found.') : tr('Sync the project to list its playbooks.')}</span>
            )}
          </Panel>
        </div>
        <div className="narrow stack" style={{ gap: 16 }}>
          <Panel title={tr('Last sync')}>
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
              <span className="faint small">{tr('Never synced.')}</span>
            )}
          </Panel>
          <UsedByPanel resource="projects" id={p.id} empty={tr('Nothing uses this project yet.')} />
          <Panel title={tr('About')}>
            <KV
              items={[
                [tr('Organization'), p.summary_fields.organization ? <Link to={`/organizations/${p.organization}`}>{p.summary_fields.organization.name}</Link> : '—'],
                [tr('Folder'), p.summary_fields.folder?.name ?? ''],
                [tr('Default environment'), p.summary_fields.default_environment?.name ?? <span className="faint">{tr('platform default')}</span>],
                [tr('History'), <Provenance obj={p} />],
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
    { key: 'status', header: tr('Status'), width: 130, render: (j) => <Status status={j.status} /> },
    {
      key: 'id',
      header: tr('Sync'),
      sort: 'id',
      render: (j) => (
        <div>
          <Link to={`/jobs/project_update/${j.id}`} className="cell-title mono">
            #{j.id}
          </Link>
          <div className="cell-sub">{j.job_type === 'run' ? tr('checkout for a job') : tr('project sync')}</div>
        </div>
      ),
    },
    { key: 'rev', header: tr('Revision'), render: (j) => <span className="mono small">{shortSha(j.scm_revision) || '—'}</span> },
    { key: 'by', header: tr('Started by'), render: (j) => <span className="small">{j.launched_by?.name ?? j.launch_type}</span> },
    { key: 'took', header: tr('Took'), render: (j) => <Elapsed job={j} /> },
    { key: 'when', header: tr('Finished'), sort: 'finished', render: (j) => <span className="small nowrap">{j.finished ? <Ago at={j.finished} /> : <span className="faint">{j.status}</span>}</span> },
  ]
  return (
    <div className="page-body">
      <DataTable
        label={tr('Project syncs')}
        rows={jobs.data?.results}
        columns={cols}
        loading={jobs.isPending}
        error={jobs.error}
        order={ls.order}
        onOrder={ls.setOrder}
        onOpen={(j) => nav(`/jobs/project_update/${j.id}`)}
        empty={<Empty title={tr('No syncs yet')} />}
        footer={
          <div className="table-foot">
            <span>{jobs.data ? tr('{count}', { count: plural(jobs.data.count, 'sync', 'syncs') }) : ''}</span>
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
