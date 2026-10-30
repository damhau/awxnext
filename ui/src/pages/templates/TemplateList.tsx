import { useQuery } from '@tanstack/react-query'
import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, getAll, type Page } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Base, Job, Ref, Template } from '../../api/types'
import { useMe, useOrg } from '../../app/context'
import { FilterBar } from '../../components/filterbar'
import { Lookup, MultiLookup, SelectField } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Cell, Crumbs, Empty, RunBars, Status, Upcoming } from '../../components/ui'
import { AccessTab } from '../../components/access/Access'
import { plural, userName } from '../../lib/format'
import { compile, statusValue, type KeyTable } from '../../lib/query'
import { usePref } from '../../lib/prefs'
import { FolderDialog } from '../../components/FolderDialog'
import { useCopy } from '../../components/CopyDialog'
import { ancestors, buildTree, flatten, subtreeIds, useFolders, type FolderNode } from '../../lib/folders'
import { shortRule } from '../automation/rrule'

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

type View = { name: string; q: string; folder?: number | null; show?: string | null }
type Sched = Base & { unified_job_template: number; rrule: string; next_run: string | null }

const tkey = (t: { type: string; id: number }) => `${t.type}:${t.id}`
const kindPath = (t: { type: string }) => (t.type === 'workflow_job_template' ? 'workflow' : 'job')

/**
 * Templates, browsed like a file manager: a folder shows its subfolders as
 * rows above its own templates, and the breadcrumb leads back up. The chips
 * are filters; with one on (or a search), the list is flat over the folder
 * and everything below it. The folder tree beside the list is optional.
 */
export function TemplateList() {
  const ls = useListState({ order: 'name' })
  const [sp] = useSearchParams()
  const nav = useNavigate()
  const me = useMe()
  const { org, scope } = useOrg()
  const folders = useFolders()
  const { roots, byId } = useMemo(() => buildTree(folders.data ?? []), [folders.data])
  const [starred, setStarred] = usePref<string[]>('starred-templates', [])
  const [views, setViews] = usePref<View[]>('template-views', [])
  const [tree, setTree] = usePref('templates-folder-tree', false)
  const [selected, setSelected] = useState<Set<number | string>>(new Set())
  const [dialog, setDialog] = useState<null | 'move' | 'labels' | 'grant' | 'save' | 'access' | 'folder-new' | 'folder-edit'>(null)
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()

  const folderId = Number(ls.get('folder')) || null
  const special = ls.get('show') // all | starred | mine
  const folder = folderId ? byId.get(folderId) : undefined
  const compiled = compile(ls.q, keys)
  const browse = !ls.q && !special

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

  const kinds = { type__in: 'job_template,workflow_job_template', ...scope }
  const where = browse ? (folder ? { folder: folder.folder.id } : { folder__isnull: true }) : folder ? { folder__in: subtreeIds(folder).join(',') } : {}
  const base = { ...kinds, ...where, ...(idFilter ? { id__in: idFilter } : {}) }
  const params = { ...base, ...compiled.params, order_by: ls.order, page: ls.page, page_size: ls.pageSize }
  const list = useList<Template>('unified_job_templates/', params)
  // How many the filter hides, for the footer.
  const unfiltered = useList<Template>('unified_job_templates/', { ...base, page_size: 1 }, { enabled: !!ls.q })
  const failing = useList<Template>('unified_job_templates/', { ...kinds, status__in: 'failed,error', page_size: 200 })
  const all = useList<Template>('unified_job_templates/', { ...kinds, page_size: 1 })

  const rows = list.data?.results ?? []
  const schedules = useList<Sched>(rows.length ? 'schedules/' : null, { unified_job_template__in: rows.map((r) => r.id).join(','), enabled: true, order_by: 'next_run', page_size: 200 })
  const scheduleOf = useMemo(() => {
    const by = new Map<number, Sched[]>()
    for (const sc of schedules.data?.results ?? []) by.set(sc.unified_job_template, [...(by.get(sc.unified_job_template) ?? []), sc])
    return by
  }, [schedules.data])
  const subfolders = browse && ls.page <= 1 ? (folder ? folder.children : roots) : []
  const failingIn = (n: FolderNode) => {
    const ids = new Set(subtreeIds(n))
    return (failing.data?.results ?? []).filter((t) => t.folder && ids.has(t.folder)).length
  }
  const failingHere = folder ? failingIn(folder) : (failing.data?.count ?? 0)
  const multiOrg = !org && new Set(rows.map((r) => r.organization)).size > 1
  const selectedRows = rows.filter((r) => selected.has(tkey(r)))
  const isStarred = (t: Template) => starred.includes(tkey(t))
  const toggleStar = (t: Template) => setStarred((s) => (s.includes(tkey(t)) ? s.filter((x) => x !== tkey(t)) : [...s, tkey(t)]))
  const open = (t: Template) => nav(`/templates/${kindPath(t)}/${t.id}`)
  const launch = (t: Template) => nav(`/launch/${kindPath(t)}/${t.id}`)
  // Moving between folders is navigation: it gets a history entry, so Back works.
  const goTo = (id: number | null) => {
    const n = new URLSearchParams(sp)
    if (id) n.set('folder', String(id))
    else n.delete('folder')
    n.delete('page')
    nav({ search: n.toString() })
  }
  const res = (t: Template) => (t.type === 'workflow_job_template' ? 'workflow_job_templates' : 'job_templates')
  const copyJob = useCopy('job_templates', { to: (id) => `/templates/job/${id}`, what: 'Credentials, labels and the survey are copied.', invalidates: ['unified_job_templates', 'folders'] })
  const copyWorkflow = useCopy('workflow_job_templates', { to: (id) => `/templates/workflow/${id}`, what: 'Nodes, links and the survey are copied.', invalidates: ['unified_job_templates', 'folders'] })
  const remove = async (t: Template) => {
    if (!(await confirm({ title: `Delete ${t.name}?`, body: 'Its schedules are deleted too. Job history stays.', confirm: 'Delete', danger: true }))) return
    try {
      await api.del(`${res(t)}/${t.id}/`)
      toast(`Deleted ${t.name}`)
      invalidate('unified_job_templates', 'job_templates', 'workflow_job_templates', 'folders')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }

  const columns: Column<Template>[] = [
    {
      key: 'name',
      header: 'Template',
      sort: 'name',
      render: (t) => {
        const info = t.summary_fields.template ?? {}
        const where = t.folder && t.folder !== folderId ? byId.get(t.folder)?.path : undefined
        return (
          <>
            <div className="row" style={{ gap: 6 }}>
              <button
                className="link-btn"
                aria-label={isStarred(t) ? `Unstar ${t.name}` : `Star ${t.name}`}
                aria-pressed={isStarred(t)}
                onClick={() => toggleStar(t)}
                style={{ color: isStarred(t) ? 'var(--star)' : 'var(--line-strong)', fontSize: 14, lineHeight: 1, textDecoration: 'none' }}
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
              {where && <span style={{ fontFamily: 'var(--sans)' }}> · {where}</span>}
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
              <Link to={`/jobs/${recent?.type ?? (t.type === 'workflow_job_template' ? 'workflow_job' : 'job')}/${jid}`} className="faint mono">
                #{jid}
              </Link>{' '}
              · <Ago at={recent?.finished ?? t.last_job_run} />
            </div>
          </>
        )
      },
    },
    {
      key: 'schedule',
      header: 'Schedule',
      sort: 'next_job_run',
      className: 'nowrap',
      render: (t) => {
        const list = scheduleOf.get(t.id) ?? []
        if (!list.length) return <span className="faint">—</span>
        return (
          <>
            <Link to={`/templates/${kindPath(t)}/${t.id}/schedules`} className="mono" style={{ color: 'var(--ink)' }} title={list.map((x) => x.name).join(', ')}>
              {shortRule(list[0].rrule)}
              {list.length > 1 && <span className="faint"> +{list.length - 1}</span>}
            </Link>
            <div className="faint small" style={{ marginTop: 2 }}>
              next <Upcoming at={list[0].next_run} />
            </div>
          </>
        )
      },
    },
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
      render: (t) => {
        const caps = t.summary_fields.user_capabilities ?? {}
        const base = `/templates/${kindPath(t)}/${t.id}`
        return (
          <div className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
            {caps.start !== false && (
              <button className="btn sm" onClick={() => launch(t)}>
                <Icon name="play" size={10} />
                Launch
              </button>
            )}
            <Menu label={`Actions for ${t.name}`} className="btn sm icon ghost">
              {caps.edit && !t.managed_by && <Link to={`${base}/edit`}>Edit</Link>}
              <Link to={`${base}/jobs`}>Jobs</Link>
              <Link to={`${base}/schedules`}>Schedules</Link>
              {caps.copy && <button onClick={() => (t.type === 'workflow_job_template' ? copyWorkflow : copyJob).open(t)}>Copy…</button>}
              {caps.delete && (
                <>
                  <hr />
                  <button className="danger" onClick={() => remove(t)}>
                    Delete…
                  </button>
                </>
              )}
            </Menu>
          </div>
        )
      },
    },
  ]

  const bulk = async (label: string, fn: (t: Template) => Promise<unknown>) => {
    const results = await Promise.allSettled(selectedRows.map(fn))
    const failed = results.filter((r) => r.status === 'rejected') as PromiseRejectedResult[]
    if (failed.length) toast(`${label}: ${failed.length} of ${results.length} failed — ${(failed[0].reason as Error).message}`, 'error')
    else toast(`${label}: done for ${results.length} template${results.length === 1 ? '' : 's'}`)
    invalidate('unified_job_templates', 'job_templates', 'workflow_job_templates', 'folders')
  }

  const trail = folder ? ancestors(folder, byId) : []
  const crumbs = folder ? [{ label: 'Templates', to: '/templates' }, ...trail.slice(0, -1).map((a) => ({ label: a.folder.name, to: `/templates?folder=${a.folder.id}` }))] : null
  const viewOn = (v: View) => ls.q === v.q && (v.folder ?? null) === folderId && (v.show ?? null) === (special ?? null)
  const failedView = ls.q === 'status:failed'
  const leadRows = subfolders.map((n) => {
    const bad = failingIn(n)
    return (
      <tr key={`f${n.folder.id}`} className="folder-row clickable" onClick={(e) => !(e.target as HTMLElement).closest('a,button') && goTo(n.folder.id)}>
        <td className="sel" />
        <td colSpan={columns.length}>
          <div className="folder-cell">
            <button className="folder-link" onClick={() => goTo(n.folder.id)}>
              <Icon name="folder" size={15} />
              <b>{n.folder.name}</b>
            </button>
            <span className="mono small muted">
              {plural(n.total, 'template')}
              {n.children.length > 0 && ` · ${plural(n.children.length, 'folder')}`}
            </span>
            {bad > 0 && (
              <span className="small fail-text folder-bad">
                <Cell state="failed" /> {bad} failing
              </span>
            )}
          </div>
        </td>
      </tr>
    )
  })

  return (
    <div className="body" style={{ minHeight: '100%' }}>
      {tree && (
        <aside className="sidelist folder-pane" aria-label="Folder tree">
          <div className="group">
            <div className="group-head">
              <span>Folders</span>
              <button aria-label="New folder" title="New folder" onClick={() => setDialog('folder-new')}>
                +
              </button>
            </div>
            <button className={`item ${!folderId ? 'active' : ''}`} onClick={() => goTo(null)} aria-current={!folderId ? 'page' : undefined}>
              <Icon name="templates" size={14} />
              <span className="ellipsis grow">Templates</span>
              <span className="n">{all.data?.count}</span>
            </button>
            {flatten(roots).map((n) => (
              <FolderLink key={n.folder.id} n={n} active={n.folder.id === folderId} onClick={() => goTo(n.folder.id)} />
            ))}
            {folders.data && roots.length === 0 && <span className="small faint" style={{ padding: '2px 8px' }}>No folders yet.</span>}
          </div>
        </aside>
      )}

      <div className="page" style={{ minWidth: 0 }}>
        <div className="page-head" style={{ paddingBottom: 0 }}>
          {crumbs && <Crumbs items={crumbs} />}
          <div className="title-row">
            <div style={{ flex: '1 1 320px' }}>
              <h1>{folder ? folder.folder.name : 'Templates'}</h1>
              <p className="subtitle mono small">
                {plural(folder ? folder.total : (all.data?.count ?? 0), 'template')}
                {failingHere > 0 && (
                  <>
                    {' · '}
                    <span className="fail-text">{failingHere} failing</span>
                  </>
                )}
                {folder?.folder.description ? <span style={{ fontFamily: 'var(--sans)' }}> · {folder.folder.description}</span> : null}
              </p>
            </div>
            <div className="actions">
              {folder && (
                <>
                  <button className="btn" onClick={() => setDialog('access')} title="Roles granted on this folder apply to everything in it">
                    Folder access
                  </button>
                  <Menu label="Folder actions">
                    <Link to={`/folders/${folder.folder.id}`}>Open folder page</Link>
                    <button onClick={() => setDialog('folder-new')}>New subfolder…</button>
                    <button onClick={() => setDialog('folder-edit')}>Rename or move…</button>
                    <hr />
                    <button
                      className="danger"
                      onClick={async () => {
                        if (!(await confirm({ title: `Delete folder ${folder.path}?`, body: 'Only empty folders can be deleted: move or delete what is inside first.', confirm: 'Delete folder', danger: true }))) return
                        try {
                          await api.del(`folders/${folder.folder.id}/`)
                          invalidate('folders')
                          goTo(folder.folder.parent ?? null)
                        } catch (e) {
                          toast((e as Error).message, 'error')
                        }
                      }}
                    >
                      Delete folder
                    </button>
                  </Menu>
                </>
              )}
              <Menu label="New" className="btn primary" trigger={<>New template <Icon name="chevron" size={12} /></>}>
                <Link to={`/templates/new/job${folder ? `?folder=${folder.folder.id}` : ''}`}>Job template</Link>
                <Link to={`/templates/new/workflow${folder ? `?folder=${folder.folder.id}` : ''}`}>Workflow template</Link>
                <hr />
                <button onClick={() => setDialog('folder-new')}>{folder ? 'Subfolder…' : 'Folder…'}</button>
              </Menu>
            </div>
          </div>
          <div className="chips" role="group" aria-label="Quick views">
            <Chip on={special === 'all'} onClick={() => ls.set({ show: special === 'all' ? null : 'all', page: null })} n={folder ? folder.total : all.data?.count}>
              All
            </Chip>
            <Chip on={special === 'starred'} onClick={() => ls.set({ show: special === 'starred' ? null : 'starred', page: null })} n={starred.length}>
              ★ Starred
            </Chip>
            <Chip on={special === 'mine'} onClick={() => ls.set({ show: special === 'mine' ? null : 'mine', page: null })}>
              Run by me
            </Chip>
            <Chip on={failedView} onClick={() => ls.set({ q: failedView ? null : 'status:failed', page: null })} n={failingHere} bad>
              Last run failed
            </Chip>
            {views.length > 0 && <span className="chip-sep" aria-hidden="true" />}
            {views.map((v) => (
              <Chip key={v.name} on={viewOn(v)} onClick={() => ls.set({ q: v.q || null, folder: v.folder ? String(v.folder) : null, show: v.show ?? null, page: null })} onRemove={() => setViews((list) => list.filter((x) => x.name !== v.name))}>
                {v.name}
              </Chip>
            ))}
            <button className="qchip add" onClick={() => setDialog('save')} disabled={browse && !folder} title="Keep the current filter and folder as a chip">
              + Save view
            </button>
            <button className={`qchip tree-toggle${tree ? ' on' : ''}`} aria-pressed={tree} onClick={() => setTree(!tree)} title="Show the folder tree beside the list">
              <Icon name="folder" size={13} />
              Folder tree
            </button>
          </div>
          <FilterBar
            value={ls.q}
            onChange={ls.setQ}
            keys={keys}
            placeholder="name, playbook, label…  try  status:failed  inventory:prod-*"
            chips={folder && !browse ? [{ label: <>in folder:<b>{folder.path}</b></>, onRemove: () => goTo(null) }] : []}
          />
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

        {copyJob.dialog}
        {copyWorkflow.dialog}
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
            lead={leadRows.length ? leadRows : undefined}
            empty={
              !browse ? (
                <Empty title="No templates match">Remove a filter, or pick another folder.</Empty>
              ) : folder ? (
                <Empty title="This folder is empty" action={<Link to={`/templates/new/job?folder=${folder.folder.id}`} className="btn primary">New job template here</Link>}>
                  Create a template here, or move templates in from the list with “Move to folder”.
                </Empty>
              ) : (
                <Empty title="No templates yet" action={<Link to="/templates/new/job" className="btn primary">New job template</Link>}>
                  A job template runs a playbook from a project against an inventory.
                </Empty>
              )
            }
            footer={
              <div className="table-foot">
                <span>
                  {list.data ? plural(list.data.count, 'template') : ''}
                  {browse && subfolders.length > 0 ? ` · ${plural(subfolders.length, 'folder')}` : ''}
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
      {(dialog === 'folder-new' || dialog === 'folder-edit') && (
        <FolderDialog
          roots={roots}
          editing={dialog === 'folder-edit' ? folder : undefined}
          parent={dialog === 'folder-new' ? folder : undefined}
          onClose={() => setDialog(null)}
          onSaved={(id) => {
            setDialog(null)
            invalidate('folders')
            goTo(id)
          }}
        />
      )}
      {dialog === 'save' && (
        <SaveViewDialog
          onClose={() => setDialog(null)}
          onSave={(name) => {
            setViews((all) => [...all.filter((v) => v.name !== name), { name, q: ls.q, folder: folderId, show: special }])
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

/** A quick-view filter; on when its filter is the one applied. Click again to clear it. */
function Chip({ on, onClick, n, bad, onRemove, children }: { on: boolean; onClick: () => void; n?: number; bad?: boolean; onRemove?: () => void; children: ReactNode }) {
  return (
    <span className={`qchip${on ? ' on' : ''}`}>
      <button className="qchip-main" aria-pressed={on} onClick={onClick}>
        {children}
        {n !== undefined && <span className={`n${bad && n ? ' bad' : ''}`}>{n}</span>}
      </button>
      {onRemove && (
        <button className="qchip-x" aria-label={`Delete view ${typeof children === 'string' ? children : ''}`} onClick={onRemove}>
          <Icon name="x" size={11} />
        </button>
      )}
    </span>
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
        <span className="help">Saved with your account, with the current folder: a chip above the list in every browser you sign in to.</span>
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

