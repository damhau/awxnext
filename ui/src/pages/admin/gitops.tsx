import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { Check, FormError, Lookup, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, useToast } from '../../components/overlay'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import type { CellState } from '../../lib/cells'
import { shortSha } from '../../lib/format'
import { ListPage, useDelete } from './common'

interface Change {
  action: 'create' | 'update' | 'delete' | 'unchanged' | 'error'
  kind: string
  name: string
  id?: number
  fields?: string[]
  error?: string
  file?: string
}

interface Report {
  revision: string
  dry_run: boolean
  changes: Change[] | null
  summary: Record<string, number>
  error?: string
}

type Source = Base & {
  organization: number
  folder: number | null
  project: number | null
  path: string
  enabled: boolean
  prune: boolean
  adopt_existing: boolean
  sync_on_project_update: boolean
  status: string
  last_sync: string | null
  last_revision: string
  last_error: string
  last_report: Report | Record<string, never>
}

function SourceStatus({ s }: { s: Source }) {
  const state: CellState = !s.enabled ? 'canceled' : s.status === 'successful' ? 'ok' : s.status === 'failed' ? 'failed' : 'empty'
  const label = !s.enabled ? 'Paused' : s.status === 'never synced' ? 'Never synced' : s.status === 'successful' ? 'In sync' : s.status === 'failed' ? 'Failed' : s.status
  return (
    <span className="status" title={s.last_error || undefined}>
      <Cell state={state} />
      {label}
    </span>
  )
}

/** Runs a sync (or dry run) and returns the report, also for a failed sync. */
async function runSync(id: number, dry: boolean): Promise<Report> {
  try {
    return await api.post<Report>(`gitops_sources/${id}/sync/${dry ? '?dry_run=true' : ''}`)
  } catch (e) {
    if (e instanceof ApiError && e.body && typeof e.body === 'object' && 'changes' in (e.body as object)) return e.body as Report
    throw e
  }
}

export function GitOpsList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Source>
      title="GitOps"
      subtitle={
        <>
          Declare templates, inventories, schedules and teams as YAML in git; each source keeps an organization in sync with a project. <a href="https://github.com/damhau/awxnext/blob/main/docs/gitops.md" target="_blank" rel="noreferrer">Format reference</a>
        </>
      }
      path="gitops_sources/"
      params={scope}
      placeholder="Filter sources…"
      actions={
        <Link to="/gitops/new" className="btn primary">
          <Icon name="plus" size={14} />
          New source
        </Link>
      }
      onOpen={(s) => nav(`/gitops/${s.id}`)}
      columns={[
        {
          key: 'name',
          header: 'Source',
          sort: 'name',
          render: (s) => (
            <div>
              <Link to={`/gitops/${s.id}`} className="cell-title">
                {s.name}
              </Link>
              <div className="cell-sub">
                {s.summary_fields.project?.name ?? 'no project'}
                {s.path ? ` : ${s.path}` : ''}
              </div>
            </div>
          ),
        },
        { key: 'org', header: 'Applies to', render: (s) => [s.summary_fields.organization?.name, s.summary_fields.folder?.name].filter(Boolean).join(' / ') },
        { key: 'status', header: 'Status', render: (s) => <SourceStatus s={s} /> },
        { key: 'rev', header: 'Revision', className: 'num', render: (s) => shortSha(s.last_revision) || <span className="faint">—</span> },
        { key: 'sync', header: 'Last sync', sort: 'last_sync', className: 'small nowrap', render: (s) => <Ago at={s.last_sync} /> },
      ]}
      empty={<Empty title="No GitOps sources">Point a source at a project directory with YAML declarations to manage objects from git.</Empty>}
    />
  )
}

export function GitOpsNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: 'GitOps', to: '/gitops' }, { label: 'New' }]} title="New GitOps source" />
      <div className="page-body">
        <SourceForm />
      </div>
    </div>
  )
}

export function GitOpsDetail() {
  const id = Number(useParams().id)
  const src = useObject<Source>(`gitops_sources/${id}/`)
  const [plan, setPlan] = useState<Report | null>(null)
  const [busy, setBusy] = useState<'' | 'sync' | 'plan'>('')
  const toast = useToast()
  const invalidate = useInvalidate()
  const del = useDelete()
  if (src.error) return <div className="page-body"><ErrorNote error={src.error} /></div>
  if (!src.data) return <Loading />
  const s = src.data
  const base = `/gitops/${id}`
  const report = (s.last_report && 'changes' in s.last_report ? s.last_report : null) as Report | null
  const sync = async (dry: boolean) => {
    setBusy(dry ? 'plan' : 'sync')
    try {
      const r = await runSync(id, dry)
      if (dry) setPlan(r)
      else {
        toast(r.error ? `Sync failed: ${r.error}` : `Synced ${shortSha(r.revision)}: ${describeSummary(r.summary)}`, r.error ? 'error' : 'info')
        invalidate('gitops_sources')
      }
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      setBusy('')
    }
  }
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: 'GitOps', to: '/gitops' }, { label: s.name }]}
        title={s.name}
        badge={<SourceStatus s={s} />}
        subtitle={s.description || undefined}
        actions={
          <>
            <button className="btn" disabled={!!busy || !s.project} onClick={() => sync(true)}>
              {busy === 'plan' ? 'Comparing…' : 'Preview changes'}
            </button>
            <button className="btn primary" disabled={!!busy || !s.project} onClick={() => sync(false)}>
              <Icon name="refresh" size={14} />
              {busy === 'sync' ? 'Syncing…' : 'Sync now'}
            </button>
            {s.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/settings`}>
                Edit
              </Link>
            )}
            {s.summary_fields.user_capabilities?.delete && (
              <button
                className="btn danger"
                onClick={() =>
                  del({
                    path: `gitops_sources/${id}/`,
                    name: s.name,
                    what: 'source',
                    then: '/gitops',
                    body: <p style={{ margin: 0 }}>Objects it created are kept and become editable in the UI again.</p>,
                  })
                }
              >
                Delete
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              Last sync
            </Tab>
            <Tab to={`${base}/settings`}>Settings</Tab>
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
                  {s.last_error && <div className="form-error">{s.last_error}</div>}
                  {report ? (
                    <PlanView report={report} title={`Applied ${report.revision ? shortSha(report.revision) : ''}`} hideUnchanged />
                  ) : (
                    <Panel>
                      <Empty title="Not synced yet" action={<button className="btn" onClick={() => sync(true)}>Preview changes</button>}>
                        Preview to see what the declarations would create, then sync.
                      </Empty>
                    </Panel>
                  )}
                </div>
                <div className="narrow">
                  <Panel title="Source">
                    <KV
                      items={[
                        ['Project', s.project ? <Link to={`/projects/${s.project}`}>{s.summary_fields.project?.name}</Link> : <span className="fail-text">Missing</span>],
                        ['Directory', <span className="mono">{s.path || '/'}</span>],
                        ['Revision', s.last_revision ? <span className="mono">{shortSha(s.last_revision)}</span> : '—'],
                        ['Last sync', <Ago at={s.last_sync} />],
                        ['Organization', <Link to={`/organizations/${s.organization}`}>{s.summary_fields.organization?.name}</Link>],
                        ['Folder', s.summary_fields.folder?.name ?? <span className="faint">None</span>],
                        ['On project update', s.sync_on_project_update ? 'Sync automatically' : 'Manual'],
                        ['Prune', s.prune ? 'Delete objects removed from git' : 'Keep removed objects'],
                        ['Adopt existing', s.adopt_existing ? 'Take over same-named objects' : 'Fail on name conflicts'],
                      ]}
                    />
                  </Panel>
                </div>
              </div>
            }
          />
          <Route
            path="settings"
            element={
              <Panel>
                <SourceForm source={s} />
              </Panel>
            }
          />
        </Routes>
      </div>
      {plan && (
        <Dialog
          title="Preview: what a sync would change"
          wide
          onClose={() => setPlan(null)}
          footer={
            <>
              <button className="btn" onClick={() => setPlan(null)}>
                Close
              </button>
              <button
                className="btn primary"
                disabled={!!plan.error || !!busy}
                onClick={async () => {
                  setPlan(null)
                  await sync(false)
                }}
              >
                Apply these changes
              </button>
            </>
          }
        >
          <PlanView report={plan} title={`Revision ${shortSha(plan.revision)}`} hideUnchanged />
        </Dialog>
      )}
    </div>
  )
}

function describeSummary(s: Record<string, number> | undefined): string {
  if (!s) return 'no changes'
  const parts = (['create', 'update', 'delete', 'unchanged', 'error'] as const).filter((k) => s[k]).map((k) => `${s[k]} ${k === 'create' ? 'created' : k === 'update' ? 'updated' : k === 'delete' ? 'deleted' : k}`)
  return parts.join(', ') || 'no changes'
}

const marks: Record<Change['action'], { glyph: string; cls: string; label: string }> = {
  create: { glyph: '+', cls: 'ok-text', label: 'create' },
  update: { glyph: '~', cls: '', label: 'update' },
  delete: { glyph: '−', cls: 'fail-text', label: 'delete' },
  unchanged: { glyph: '=', cls: 'faint', label: 'unchanged' },
  error: { glyph: '!', cls: 'fail-text', label: 'error' },
}

const kindOrder = ['Label', 'Team', 'Project', 'Inventory', 'InventorySource', 'NotificationTemplate', 'JobTemplate', 'WorkflowJobTemplate', 'Schedule']

/** A sync report grouped by kind. */
export function PlanView({ report, title, hideUnchanged }: { report: Report; title: string; hideUnchanged?: boolean }) {
  const [showSame, setShowSame] = useState(!hideUnchanged)
  const changes = report.changes ?? []
  const byKind = new Map<string, Change[]>()
  for (const c of changes) byKind.set(c.kind, [...(byKind.get(c.kind) ?? []), c])
  const kinds = [...byKind.keys()].sort((a, b) => kindOrder.indexOf(a) - kindOrder.indexOf(b))
  const same = report.summary?.unchanged ?? 0
  return (
    <section className="panel">
      <div className="panel-head">
        <h2>{title}</h2>
        <span className="small faint">{describeSummary(report.summary)}</span>
        {same > 0 && (
          <label className="check small" style={{ marginLeft: 'auto' }}>
            <input type="checkbox" checked={showSame} onChange={(e) => setShowSame(e.target.checked)} />
            Show {same} unchanged
          </label>
        )}
      </div>
      {report.error && (
        <div style={{ padding: '10px 14px 0' }}>
          <div className="form-error">{report.error}</div>
          <p className="small muted" style={{ margin: '8px 0 0' }}>A sync is all or nothing: nothing was changed.</p>
        </div>
      )}
      <div className="plan">
        {kinds.map((k) => {
          const rows = byKind.get(k)!.filter((c) => showSame || c.action !== 'unchanged')
          if (!rows.length) return null
          return (
            <div key={k} className="plan-kind">
              <div className="plan-kind-head">{k.replace(/([a-z])([A-Z])/g, '$1 $2')}</div>
              {rows.map((c, i) => {
                const m = marks[c.action]
                return (
                  <div key={i} className={`plan-row ${c.action}`}>
                    <span className={`plan-mark mono ${m.cls}`} aria-label={m.label}>
                      {m.glyph}
                    </span>
                    <span className="grow">
                      <span style={{ fontWeight: 500 }}>{c.name}</span>
                      {c.fields && c.fields.length > 0 && <span className="small faint"> · {c.fields.join(', ')}</span>}
                      {c.error && <div className="small fail-text">{c.error}</div>}
                    </span>
                    {c.file && <span className="mono small faint">{c.file}</span>}
                  </div>
                )
              })}
            </div>
          )
        })}
        {!kinds.length && <div className="empty">The directory declares nothing.</div>}
      </div>
    </section>
  )
}

interface SourceValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  folder: Ref | null
  project: Ref | null
  path: string
  enabled: boolean
  prune: boolean
  adopt_existing: boolean
  sync_on_project_update: boolean
}

function SourceForm({ source }: { source?: Source }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const f = useForm<SourceValues>({
    name: source?.name ?? '',
    description: source?.description ?? '',
    organization: source?.summary_fields.organization ?? org,
    folder: source?.summary_fields.folder ?? null,
    project: source?.summary_fields.project ?? null,
    path: source?.path ?? '',
    enabled: source?.enabled ?? true,
    prune: source?.prune ?? true,
    adopt_existing: source?.adopt_existing ?? false,
    sync_on_project_update: source?.sync_on_project_update ?? true,
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !source)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const orgId = f.values.organization?.id
  const save = async () => {
    setBusy(true)
    setError(null)
    const body: Record<string, unknown> = {
      name: f.values.name,
      description: f.values.description,
      folder: f.values.folder?.id ?? null,
      project: f.values.project?.id ?? null,
      path: f.values.path,
      enabled: f.values.enabled,
      prune: f.values.prune,
      adopt_existing: f.values.adopt_existing,
      sync_on_project_update: f.values.sync_on_project_update,
    }
    if (!source) body.organization = orgId
    try {
      const saved = source ? await api.patch<Source>(`gitops_sources/${source.id}/`, body) : await api.post<Source>('gitops_sources/', body)
      invalidate('gitops_sources')
      nav(`/gitops/${saved.id}`)
    } catch (e) {
      setError(e)
      if (e && typeof e === 'object' && 'fields' in e) f.setErrors((e as { fields: Record<string, string> }).fields)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      className="form"
      style={{ maxWidth: 900 }}
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput label="Name" value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!source} />
        {source ? (
          <div className="field">
            <span className="label">Organization</span>
            <span style={{ padding: '7px 0' }}>{source.summary_fields.organization?.name}</span>
            <span className="help">Fixed once the source exists.</span>
          </div>
        ) : (
          <Lookup label="Organization" resource="organizations/" value={f.values.organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} />
        )}
        <Lookup
          key={'p' + orgId}
          label="Project"
          resource="projects/"
          params={orgId ? { organization: orgId } : undefined}
          value={f.values.project}
          onChange={(v) => f.set('project', v)}
          error={f.errors.project}
          help="Its synced revision is applied."
        />
        <TextInput label="Directory" optional mono value={f.values.path} onChange={(v) => f.set('path', v)} placeholder="awx/" help="Scanned recursively for YAML with a kind. Playbooks are ignored." error={f.errors.path} />
        <Lookup
          key={'f' + orgId}
          label="Folder"
          optional
          resource="folders/"
          params={orgId ? { organization: orgId } : undefined}
          value={f.values.folder}
          onChange={(v) => f.set('folder', v)}
          help="Declared objects are placed here."
        />
        <TextArea label="Description" optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      <div className="stack" style={{ gap: 10 }}>
        <Check label="Sync after each successful project update" checked={f.values.sync_on_project_update} onChange={(v) => f.set('sync_on_project_update', v)} />
        <Check label="Prune" checked={f.values.prune} onChange={(v) => f.set('prune', v)} help="Delete objects of this source that were removed from git." />
        <Check label="Adopt existing objects" checked={f.values.adopt_existing} onChange={(v) => f.set('adopt_existing', v)} help="Take over unmanaged objects with a declared name instead of failing." />
        <Check label="Enabled" checked={f.values.enabled} onChange={(v) => f.set('enabled', v)} help="Paused sources are not synced automatically." />
      </div>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !f.values.project || !orgId}>
          {busy ? 'Saving…' : source ? 'Save changes' : 'Create source'}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(source ? `/gitops/${source.id}` : '/gitops')}>
          Cancel
        </button>
      </div>
    </form>
  )
}
