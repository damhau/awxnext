import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, getAll, type Page } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Job, Ref, Template } from '../../api/types'
import { useMe, useOrg } from '../../app/context'
import { FilterBar } from '../../components/filterbar'
import { Lookup, MultiLookup, SelectField } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Crumbs, Empty, RunBars, Status, Upcoming } from '../../components/ui'
import { AccessTab } from '../../components/access/Access'
import { userName } from '../../lib/format'
import { compile, statusValue, type KeyTable } from '../../lib/query'
import { useStored } from '../../lib/storage'
import { ancestors, buildTree, flatten, subtreeIds, useFolders, type FolderNode } from './folders'

const keys: KeyTable = {
  status: { field: 'status', map: statusValue, help: 'failed' },
  type: { field: 'type', map: (v) => (v.startsWith('w') ? 'workflow_job_template' : 'job_template'), help: 'workflow' },
  inventory: { field: 'inventory__name', help: 'prod-*' },
  project: { field: 'project__name', help: 'name' },
  label: { field: 'labels__name', help: 'web' },
  playbook: { field: 'playbook', help: 'deploy/*' },
  org: { field: 'organization__name', help: 'name' },
  by: { field: 'modified_by__username', help: 'user' },
}

type View = { name: string; q: string; folder?: number | null }

const tkey = (t: { type: string; id: number }) => `${t.type}:${t.id}`
const kindPath = (t: { type: string }) => (t.type === 'workflow_job_template' ? 'workflow' : 'job')

export function TemplateList() {
  const ls = useListState({ order: 'name' })
  const nav = useNavigate()
  const me = useMe()
  const { org, scope } = useOrg()
  const folders = useFolders()
  const { roots, byId } = useMemo(() => buildTree(folders.data ?? []), [folders.data])
  const [starred, setStarred] = useStored<string[]>('starred-templates', [])
  const [views, setViews] = useStored<View[]>('template-views', [])
  const [selected, setSelected] = useState<Set<number | string>>(new Set())
  const [dialog, setDialog] = useState<null | 'move' | 'labels' | 'grant' | 'save' | 'access'>(null)
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()

  const folderId = Number(ls.get('folder')) || null
  const special = ls.get('show') // starred | mine
  const folder = folderId ? byId.get(folderId) : undefined
  const compiled = compile(ls.q, keys)

  const mine = useQuery({
    queryKey: ['unified_jobs/', 'mine', me.id],
    enabled: special === 'mine',
    queryFn: async () => {
      const jobs = await api.get<Page<Job>>('unified_jobs/', { created_by: me.id, type__in: 'job,workflow_job', order_by: '-id', page_size: 100 })
      return [...new Set(jobs.results.map((j) => j.unified_job_template).filter(Boolean))] as number[]
    },
  })
  const idFilter =
    special === 'starred'
      ? starred.map((k) => Number(k.split(':')[1])).join(',') || '0'
      : special === 'mine'
        ? (mine.data ?? []).join(',') || '0'
        : undefined

  const base = {
    type__in: 'job_template,workflow_job_template',
    ...scope,
    ...(folder ? { folder__in: subtreeIds(folder).join(',') } : {}),
    ...(idFilter ? { id__in: idFilter } : {}),
  }
  const params = { ...base, ...compiled.params, order_by: ls.order, page: ls.page, page_size: ls.pageSize }
  const list = useList<Template>('unified_job_templates/', params)
  // How many the filter hides, for the footer.
  const unfiltered = useList<Template>('unified_job_templates/', { ...base, page_size: 1 }, { enabled: !!ls.q })
  const failing = useList<Template>('unified_job_templates/', { type__in: 'job_template,workflow_job_template', ...scope, status__in: 'failed,error', page_size: 1 })
  const all = useList<Template>('unified_job_templates/', { type__in: 'job_template,workflow_job_template', ...scope, page_size: 1 })

  const rows = list.data?.results ?? []
  const multiOrg = !org && new Set(rows.map((r) => r.organization)).size > 1
  const selectedRows = rows.filter((r) => selected.has(tkey(r)))
  const isStarred = (t: Template) => starred.includes(tkey(t))
  const toggleStar = (t: Template) => setStarred((s) => (s.includes(tkey(t)) ? s.filter((x) => x !== tkey(t)) : [...s, tkey(t)]))
  const open = (t: Template) => nav(`/templates/${kindPath(t)}/${t.id}`)
  const launch = (t: Template) => nav(`/launch/${kindPath(t)}/${t.id}`)

  const columns: Column<Template>[] = [
    {
      key: 'name',
      header: 'Template',
      sort: 'name',
      render: (t) => {
        const info = t.summary_fields.template ?? {}
        return (
          <>
            <div className="row" style={{ gap: 6 }}>
              <button
                className="link-btn"
                aria-label={isStarred(t) ? `Unstar ${t.name}` : `Star ${t.name}`}
                aria-pressed={isStarred(t)}
                onClick={() => toggleStar(t)}
                style={{ color: isStarred(t) ? 'var(--star)' : 'var(--line-strong)', fontSize: 14, lineHeight: 1 }}
              >
                ★
              </button>
              <Link to={`/templates/${kindPath(t)}/${t.id}`} className="cell-title">
                {t.name}
              </Link>
              {t.type === 'workflow_job_template' && <span className="tag">workflow</span>}
              {info.survey_enabled && <span className="tag">survey</span>}
              {t.managed_by && <span className="tag" title={`Managed by ${t.managed_by}`}>git</span>}
            </div>
            <div className="cell-sub" style={{ marginLeft: 20 }}>
              {t.type === 'workflow_job_template' ? `${info.workflow_nodes ?? 0} nodes` : info.playbook || '—'}
              {!folder && t.summary_fields.folder && <span style={{ fontFamily: 'var(--sans)' }}> · {t.summary_fields.folder.name}</span>}
              {multiOrg && t.summary_fields.organization && <span style={{ fontFamily: 'var(--sans)' }}> · {t.summary_fields.organization.name}</span>}
            </div>
          </>
        )
      },
    },
    { key: 'runs', header: 'Last runs', render: (t) => <RunBars jobs={t.summary_fields.recent_jobs} slots={10} /> },
    {
      key: 'inventory',
      header: 'Inventory',
      className: 'nowrap',
      render: (t) => <span className="mono">{t.summary_fields.inventory?.name ?? (t.type === 'workflow_job_template' ? '—' : 'prompted')}</span>,
    },
    {
      key: 'last',
      header: 'Last run',
      sort: 'last_job_run',
      className: 'nowrap',
      render: (t) => {
        const lj = t.summary_fields.last_job
        if (!lj && !t.summary_fields.recent_jobs?.length) return <span className="faint">Never run</span>
        const recent = t.summary_fields.recent_jobs?.[0]
        const st = recent?.status ?? lj?.status
        const jid = recent?.id ?? lj?.id
        return (
          <>
            <Status status={st} />
            <div className="faint small" style={{ marginTop: 2 }}>
              <Link to={`/jobs/${recent?.type ?? (t.type === 'workflow_job_template' ? 'workflow_job' : 'job')}/${jid}`} className="faint">
                #{jid}
              </Link>{' '}
              · <Ago at={recent?.finished ?? t.last_job_run} />
            </div>
          </>
        )
      },
    },
    { key: 'next', header: 'Next scheduled', sort: 'next_job_run', className: 'nowrap', render: (t) => (t.next_job_run ? <Upcoming at={t.next_job_run} /> : <span className="faint">Manual</span>) },
    {
      key: 'edited',
      header: 'Edited',
      sort: 'modified',
      className: 'nowrap small muted',
      render: (t) => (
        <>
          <Ago at={t.modified} /> · {userName(t.summary_fields.modified_by)}
        </>
      ),
    },
    {
      key: 'act',
      header: <span className="sr-only">Actions</span>,
      className: 'end',
      render: (t) =>
        t.summary_fields.user_capabilities?.start !== false && (
          <button className="btn sm" onClick={() => launch(t)}>
            <Icon name="play" size={10} />
            Launch
          </button>
        ),
    },
  ]

  const bulk = async (label: string, fn: (t: Template) => Promise<unknown>) => {
    const results = await Promise.allSettled(selectedRows.map(fn))
    const failed = results.filter((r) => r.status === 'rejected') as PromiseRejectedResult[]
    if (failed.length) toast(`${label}: ${failed.length} of ${results.length} failed — ${(failed[0].reason as Error).message}`, 'error')
    else toast(`${label}: done for ${results.length} template${results.length === 1 ? '' : 's'}`)
    invalidate('unified_job_templates', 'job_templates', 'workflow_job_templates', 'folders')
  }
  const res = (t: Template) => (t.type === 'workflow_job_template' ? 'workflow_job_templates' : 'job_templates')

  const title = special === 'starred' ? 'Starred' : special === 'mine' ? 'Recently run by me' : folder ? folder.folder.name : 'All templates'
  const crumbs = folder ? [{ label: 'Templates', to: '/templates' }, ...ancestors(folder, byId).map((a, i, arr) => (i === arr.length - 1 ? { label: a.folder.name } : { label: a.folder.name, to: `/templates?folder=${a.folder.id}` }))] : [{ label: 'Templates' }]

  return (
    <div className="body" style={{ minHeight: '100%' }}>
      <aside className="sidelist" aria-label="Folders and views">
        <div className="group">
          <SideLink active={!folderId && !special} onClick={() => ls.set({ folder: null, show: null, page: null })} label="All templates" n={all.data?.count} />
          <SideLink active={special === 'starred'} onClick={() => ls.set({ show: 'starred', folder: null, page: null })} label="Starred" n={starred.length} />
          <SideLink active={special === 'mine'} onClick={() => ls.set({ show: 'mine', folder: null, page: null })} label="Recently run by me" />
        </div>
        <div className="group">
          <div className="group-head">
            <span>Saved views</span>
            <button aria-label="Save current view" title="Save current view" onClick={() => setDialog('save')}>
              +
            </button>
          </div>
          <SideLink active={ls.q === 'status:failed' && !folderId} onClick={() => ls.set({ q: 'status:failed', folder: null, show: null, page: null })} label="Last run failed" n={failing.data?.count} bad />
          {views.map((v) => (
            <div key={v.name} className={`item ${ls.q === v.q && (v.folder ?? null) === folderId ? 'active' : ''}`} role="link" tabIndex={0}
              onClick={() => ls.set({ q: v.q, folder: v.folder ? String(v.folder) : null, show: null, page: null })}
              onKeyDown={(e) => e.key === 'Enter' && ls.set({ q: v.q, folder: v.folder ? String(v.folder) : null, show: null, page: null })}>
              <span className="ellipsis">{v.name}</span>
              <button className="x" aria-label={`Delete view ${v.name}`} onClick={(e) => { e.stopPropagation(); setViews((all) => all.filter((x) => x.name !== v.name)) }}>
                <Icon name="x" size={12} />
              </button>
            </div>
          ))}
          {views.length === 0 && <span className="small faint" style={{ padding: '2px 8px' }}>Filter the list, then + to keep it here.</span>}
        </div>
        <div className="group">
          <div className="group-head">
            <span>Folders</span>
          </div>
          {flatten(roots).map((n) => (
            <FolderLink key={n.folder.id} n={n} active={n.folder.id === folderId} onClick={() => ls.set({ folder: String(n.folder.id), show: null, page: null })} />
          ))}
          {folders.data && roots.length === 0 && <span className="small faint" style={{ padding: '2px 8px' }}>No folders yet.</span>}
        </div>
      </aside>

      <div className="page" style={{ minWidth: 0 }}>
        <div className="page-head" style={{ paddingBottom: 0 }}>
          <Crumbs items={crumbs} />
          <div className="title-row">
            <div style={{ flex: '1 1 320px' }}>
              <h1>{title}</h1>
              {folder && (
                <p className="subtitle">
                  {folder.folder.description ? folder.folder.description + ' · ' : ''}
                  Roles granted on this folder apply to all {folder.total} templates below.
                </p>
              )}
            </div>
            <div className="actions">
              {folder && (
                <button className="btn" onClick={() => setDialog('access')}>
                  Folder access
                </button>
              )}
              <Menu label="New template" className="btn primary" trigger={<>New template <Icon name="chevron" size={12} /></>}>
                <Link to={`/templates/new/job${folder ? `?folder=${folder.folder.id}` : ''}`}>Job template</Link>
                <Link to={`/templates/new/workflow${folder ? `?folder=${folder.folder.id}` : ''}`}>Workflow template</Link>
              </Menu>
            </div>
          </div>
          <FilterBar
            value={ls.q}
            onChange={ls.setQ}
            keys={keys}
            placeholder="name, playbook, label…  try  status:failed  inventory:prod-*"
            chips={folder ? [{ label: <>folder:<b>{folder.path}</b></>, onRemove: () => ls.set({ folder: null, page: null }) }] : []}
          >
            <button className="btn" onClick={() => setDialog('save')} disabled={!ls.q && !folder}>
              Save view
            </button>
          </FilterBar>
        </div>

        {selected.size > 0 && (
          <div className="bulkbar" role="region" aria-label="Bulk actions" style={{ margin: '12px 24px 0' }}>
            <span className="n">{selected.size} selected</span>
            <button
              className="btn"
              onClick={async () => {
                if (!(await confirm({ title: `Launch ${selectedRows.length} templates?`, body: 'Each starts with its default values. Templates that need answers (surveys, prompts) will fail to start; launch those one by one.', confirm: 'Launch all' }))) return
                bulk('Launch', (t) => api.post(`${res(t)}/${t.id}/launch/`, {}))
              }}
            >
              Launch
            </button>
            <button className="btn" onClick={() => setDialog('move')}>
              Move to folder…
            </button>
            <button className="btn" onClick={() => setDialog('labels')}>
              Labels…
            </button>
            <button className="btn" onClick={() => setDialog('grant')}>
              Grant access…
            </button>
            <button
              className="btn"
              onClick={() =>
                bulk('Pause schedules', async (t) => {
                  const s = await getAll<{ id: number; enabled: boolean }>(`${res(t)}/${t.id}/schedules/`)
                  await Promise.all(s.filter((x) => x.enabled).map((x) => api.patch(`schedules/${x.id}/`, { enabled: false })))
                })
              }
            >
              Pause schedules
            </button>
            <button
              className="btn warn"
              onClick={async () => {
                if (!(await confirm({ title: `Delete ${selectedRows.length} templates?`, body: <p style={{ margin: 0 }}>{selectedRows.map((t) => t.name).join(', ')}. Their job history stays.</p>, confirm: 'Delete', danger: true }))) return
                await bulk('Delete', (t) => api.del(`${res(t)}/${t.id}/`))
                setSelected(new Set())
              }}
            >
              Delete
            </button>
            <button className="btn clear" onClick={() => setSelected(new Set())}>
              Clear
            </button>
          </div>
        )}

        <div style={{ margin: '8px 24px 24px' }}>
          <DataTable
            label="Templates"
            rows={rows}
            columns={columns}
            rowKey={tkey}
            loading={list.isPending}
            error={list.error}
            selected={selected}
            onSelect={setSelected}
            onOpen={open}
            order={ls.order}
            onOrder={ls.setOrder}
            keys={{ l: launch, s: toggleStar }}
            minWidth={900}
            empty={
              ls.q || folder || special ? (
                <Empty title="No templates match">Remove a filter, or pick another folder.</Empty>
              ) : (
                <Empty title="No templates yet" action={<Link to="/templates/new/job" className="btn primary">New job template</Link>}>
                  A job template runs a playbook from a project against an inventory.
                </Empty>
              )
            }
            footer={
              <div className="table-foot">
                <span>
                  {list.data ? `${list.data.count} template${list.data.count === 1 ? '' : 's'}` : ''}
                  {ls.q && unfiltered.data && unfiltered.data.count > (list.data?.count ?? 0) ? ` · ${unfiltered.data.count - (list.data?.count ?? 0)} hidden by the filter` : ''}
                </span>
                <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
                <span className="mono">j/k move · x select · l launch · s star · / filter</span>
              </div>
            }
          />
        </div>
      </div>

      {dialog === 'access' && folder && (
        <Dialog title={`Access to ${folder.path}`} wide onClose={() => setDialog(null)}>
          <p className="muted" style={{ margin: 0 }}>
            Roles on a folder apply to every template, inventory, project and credential inside it and in its subfolders.
          </p>
          <AccessTab resource="folders" id={folder.folder.id} name={folder.path} />
        </Dialog>
      )}
      {dialog === 'save' && (
        <SaveViewDialog
          onClose={() => setDialog(null)}
          onSave={(name) => {
            setViews((all) => [...all.filter((v) => v.name !== name), { name, q: ls.q, folder: folderId }])
            setDialog(null)
          }}
        />
      )}
      {dialog === 'move' && (
        <MoveDialog
          roots={roots}
          onClose={() => setDialog(null)}
          onMove={async (fid) => {
            setDialog(null)
            await bulk('Move', (t) => api.patch(`${res(t)}/${t.id}/`, { folder: fid }))
          }}
        />
      )}
      {dialog === 'labels' && (
        <LabelsDialog
          onClose={() => setDialog(null)}
          onApply={async (labels) => {
            setDialog(null)
            await bulk('Labels', (t) => Promise.all(labels.map((l) => api.post(`${res(t)}/${t.id}/labels/`, l.id ? { id: l.id } : { name: l.name, organization: t.organization }))))
          }}
        />
      )}
      {dialog === 'grant' && (
        <GrantDialog
          onClose={() => setDialog(null)}
          onGrant={async (who, role) => {
            setDialog(null)
            await bulk('Grant access', async (t) => {
              const full = await api.get<Template>(`${res(t)}/${t.id}/`)
              const r = full.summary_fields.object_roles?.[role]
              if (!r) throw new Error(`${t.name} has no ${role.replace('_role', '')} role`)
              await api.post(`${who.kind === 'user' ? 'users' : 'teams'}/${who.ref.id}/roles/`, { id: r.id })
            })
          }}
        />
      )}
    </div>
  )
}

function SideLink({ label, n, active, onClick, bad }: { label: string; n?: number; active: boolean; onClick: () => void; bad?: boolean }) {
  return (
    <button className={`item ${active ? 'active' : ''}`} onClick={onClick} aria-current={active ? 'page' : undefined}>
      <span className="ellipsis">{label}</span>
      {n !== undefined && <span className={`n ${bad && n ? 'bad' : ''}`}>{n}</span>}
    </button>
  )
}

function FolderLink({ n, active, onClick }: { n: FolderNode; active: boolean; onClick: () => void }) {
  return (
    <button className={`item ${active ? 'active' : ''}`} style={{ paddingLeft: 8 + n.depth * 16 }} onClick={onClick} aria-current={active ? 'page' : undefined}>
      <Icon name="folder" size={14} />
      <span className="ellipsis grow">{n.folder.name}</span>
      <span className="n">{n.total}</span>
    </button>
  )
}

function SaveViewDialog({ onClose, onSave }: { onClose: () => void; onSave: (name: string) => void }) {
  const [name, setName] = useState('')
  return (
    <Dialog
      title="Save view"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!name.trim()} onClick={() => onSave(name.trim())}>
            Save
          </button>
        </>
      }
    >
      <div className="field">
        <label htmlFor="vn">Name</label>
        <input id="vn" className="input" value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && name.trim() && onSave(name.trim())} placeholder="Prod deploys · EU" />
        <span className="help">Saved in this browser. Shows in the sidebar under Saved views.</span>
      </div>
    </Dialog>
  )
}

function MoveDialog({ roots, onClose, onMove }: { roots: FolderNode[]; onClose: () => void; onMove: (id: number | null) => void }) {
  const [target, setTarget] = useState<string>('')
  const opts: [string, string][] = [['', 'Top level (no folder)'], ...flatten(roots).map((n): [string, string] => [String(n.folder.id), `${'  '.repeat(n.depth)}${n.path}`])]
  return (
    <Dialog
      title="Move to folder"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={() => onMove(target ? Number(target) : null)}>
            Move
          </button>
        </>
      }
    >
      <SelectField label="Folder" value={target} onChange={setTarget} options={opts} help="Templates take on the access granted on the folder." />
    </Dialog>
  )
}

function LabelsDialog({ onClose, onApply }: { onClose: () => void; onApply: (labels: Ref[]) => void }) {
  const [labels, setLabels] = useState<Ref[]>([])
  const { scope } = useOrg()
  return (
    <Dialog
      title="Add labels"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!labels.length} onClick={() => onApply(labels)}>
            Add labels
          </button>
        </>
      }
    >
      <MultiLookup label="Labels" resource="labels/" params={scope} values={labels} onChange={setLabels} help="Existing labels of the template's organization." />
    </Dialog>
  )
}

function GrantDialog({ onClose, onGrant }: { onClose: () => void; onGrant: (who: { kind: 'user' | 'team'; ref: Ref }, role: string) => void }) {
  const [kind, setKind] = useState<'team' | 'user'>('team')
  const [who, setWho] = useState<Ref | null>(null)
  const [role, setRole] = useState('execute_role')
  return (
    <Dialog
      title="Grant access"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!who} onClick={() => who && onGrant({ kind, ref: who }, role)}>
            Grant
          </button>
        </>
      }
    >
      <SelectField label="To a" value={kind} onChange={(v) => { setKind(v as 'team' | 'user'); setWho(null) }} options={[['team', 'Team'], ['user', 'User']]} />
      {kind === 'team' ? (
        <Lookup label="Team" resource="teams/" value={who} onChange={setWho} />
      ) : (
        <Lookup label="User" resource="users/" order="username" value={who} onChange={setWho} display={(u) => u.username ?? u.name} />
      )}
      <SelectField
        label="Role"
        value={role}
        onChange={setRole}
        options={[
          ['execute_role', 'Execute — may launch'],
          ['read_role', 'Read — may view'],
          ['admin_role', 'Admin — may change and delete'],
        ]}
      />
    </Dialog>
  )
}

