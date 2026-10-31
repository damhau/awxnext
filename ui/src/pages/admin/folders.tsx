// Folders: browse the folder tree, see everything a folder holds, move
// objects in and out, and manage who has access to it.
import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, type Page } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Base, Folder, Ref } from '../../api/types'
import { useOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { FolderDialog } from '../../components/FolderDialog'
import { MultiLookup, SelectField } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, type Column } from '../../components/table'
import { Empty, ErrorNote, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { buildTree, directCounts, flatten, totalObjects, useFolders, type FolderNode } from '../../lib/folders'
import { plural } from '../../lib/format'
import { tr } from '../../lib/i18n'

interface Kind {
  res: string
  label: string
  one: string
  path: (id: number) => string
  sub: (o: Base) => string | undefined
}

/** Object types a folder can hold, in display order. */
const kinds: Kind[] = [
  { res: 'job_templates', label: tr('Job templates'), one: 'job template', path: (id) => `/templates/job/${id}`, sub: (o) => o.playbook },
  { res: 'workflow_job_templates', label: tr('Workflow templates'), one: 'workflow template', path: (id) => `/templates/workflow/${id}`, sub: (o) => o.description },
  { res: 'inventories', label: tr('Inventories'), one: 'inventory', path: (id) => `/inventories/${id}`, sub: (o) => [o.kind || 'inventory', plural(o.total_hosts ?? 0, 'host')].join(' · ') },
  { res: 'projects', label: tr('Projects'), one: 'project', path: (id) => `/projects/${id}`, sub: (o) => o.scm_url || 'manual' },
  { res: 'credentials', label: tr('Credentials'), one: 'credential', path: (id) => `/credentials/${id}`, sub: (o) => o.summary_fields?.credential_type?.name },
  { res: 'notification_templates', label: tr('Notification templates'), one: 'notification template', path: (id) => `/notifications/${id}`, sub: (o) => o.notification_type },
]

const keyOf = (res: string, id: number) => `${res}:${id}`
const parseKey = (k: string | number) => {
  const [res, id] = String(k).split(':')
  return { res, id: Number(id) }
}

/** Folder trees per organization, limited to the organization chosen in the top bar. */
function useTrees() {
  const { org, orgs } = useOrg()
  const folders = useFolders({ allOrgs: true })
  return useMemo(() => {
    const all = folders.data ?? []
    const byId = buildTree(all).byId
    const groups = orgs
      .filter((o) => !org || o.id === org.id)
      .map((o) => ({ org: o, roots: buildTree(all.filter((f) => f.organization === o.id)).roots }))
      .filter((g) => g.roots.length > 0)
    return { groups, byId, loading: folders.isPending, error: folders.error }
  }, [folders.data, folders.isPending, folders.error, org, orgs])
}

function FolderTree({ selected }: { selected?: number }) {
  const { groups } = useTrees()
  const nav = useNavigate()
  const multi = groups.length > 1
  return (
    <aside className="sidelist" aria-label={tr('Folders')}>
      <div className="group">
        <button className={`item ${!selected ? 'active' : ''}`} onClick={() => nav('/folders')}>
          {tr('All folders')}
        </button>
      </div>
      {groups.map((g) => (
        <div key={g.org.id} className="group">
          {multi && (
            <div className="group-head">
              <span>{g.org.name}</span>
            </div>
          )}
          {flatten(g.roots).map((n) => (
            <button
              key={n.folder.id}
              className={`item ${n.folder.id === selected ? 'active' : ''}`}
              style={{ paddingLeft: 8 + n.depth * 16 }}
              onClick={() => nav(`/folders/${n.folder.id}`)}
              aria-current={n.folder.id === selected ? 'page' : undefined}
            >
              <Icon name="folder" size={14} />
              <span className="ellipsis grow">{n.folder.name}</span>
              <span className="n">{totalObjects(n)}</span>
            </button>
          ))}
        </div>
      ))}
    </aside>
  )
}

function CountCell({ n }: { n: number }) {
  return n ? <span className="mono">{n}</span> : <span className="faint">—</span>
}

export function FolderList() {
  const { groups, loading, error } = useTrees()
  const nav = useNavigate()
  const [creating, setCreating] = useState(false)
  const invalidate = useInvalidate()
  const multi = groups.length > 1
  const rows = groups.flatMap((g) => flatten(g.roots))
  const columns: Column<FolderNode>[] = [
    {
      key: 'name',
      header: tr('Folder'),
      render: (n) => (
        <div style={{ paddingLeft: n.depth * 18 }}>
          <Link to={`/folders/${n.folder.id}`} className="cell-title row" style={{ gap: 6, display: 'inline-flex' }}>
            <Icon name="folder" size={14} />
            {n.folder.name}
          </Link>
          {n.folder.description && <div className="small muted" style={{ marginLeft: 20 }}>{n.folder.description}</div>}
        </div>
      ),
    },
    ...(multi ? [{ key: 'org', header: tr('Organization'), className: 'small', render: (n: FolderNode) => n.folder.summary_fields?.organization?.name }] : []),
    { key: 'tpl', header: tr('Templates'), className: 'end', render: (n) => <CountCell n={directCounts(n.folder).templates} /> },
    { key: 'inv', header: tr('Inventories'), className: 'end', render: (n) => <CountCell n={directCounts(n.folder).inventories} /> },
    { key: 'proj', header: tr('Projects'), className: 'end', render: (n) => <CountCell n={directCounts(n.folder).projects} /> },
    { key: 'cred', header: tr('Credentials'), className: 'end', render: (n) => <CountCell n={directCounts(n.folder).credentials} /> },
    { key: 'sub', header: tr('Subfolders'), className: 'end', render: (n) => <CountCell n={n.children.length} /> },
  ]
  return (
    <div className="body" style={{ minHeight: '100%' }}>
      <FolderTree />
      <div className="page" style={{ minWidth: 0 }}>
        <PageHead
          title={tr('Folders')}
          subtitle={tr('Group templates, inventories, projects and credentials. A role granted on a folder applies to everything in it and in its subfolders.')}
          bordered={false}
          actions={
            <button className="btn primary" onClick={() => setCreating(true)}>
              <Icon name="plus" size={14} />
              {tr('New folder')}
            </button>
          }
        />
        <div className="page-body">
          <DataTable
            label={tr('Folders')}
            rows={rows}
            columns={columns}
            rowKey={(n) => n.folder.id}
            loading={loading}
            error={error}
            onOpen={(n) => nav(`/folders/${n.folder.id}`)}
            empty={
              <Empty title={tr('No folders yet')} action={<button className="btn primary" onClick={() => setCreating(true)}>{tr('New folder')}</button>}>
                {tr('For example')} <span className="mono">{tr('apps/checkout')}</span>{tr(': put the checkout templates, inventory and credentials in it, then give the team that owns them the Admin role on the folder.')}
              </Empty>
            }
            footer={
              rows.length > 0 && (
                <div className="table-foot">
                  <span>{plural(rows.length, 'folder')}</span>
                  <span>{tr('Counts are objects directly in each folder.')}</span>
                </div>
              )
            }
          />
        </div>
      </div>
      {creating && (
        <FolderDialog
          roots={groups.flatMap((g) => g.roots)}
          onClose={() => setCreating(false)}
          onSaved={(id) => {
            setCreating(false)
            invalidate('folders')
            nav(`/folders/${id}`)
          }}
        />
      )}
    </div>
  )
}

/** Everything directly in a folder, by type. */
function useContents(id: number) {
  return useQuery({
    queryKey: ['folders/', id, 'contents'],
    queryFn: async () => {
      const pages = await Promise.all(kinds.map((k) => api.get<Page<Base>>(`folders/${id}/${k.res}/`, { order_by: 'name', page_size: 200 })))
      return kinds.map((k, i) => ({ kind: k, items: pages[i].results, count: pages[i].count }))
    },
  })
}

export function FolderDetail() {
  const { id: raw } = useParams()
  const id = Number(raw)
  const folder = useObject<Folder>(`folders/${id}/`)
  const contents = useContents(id)
  const trees = useTrees()
  const nav = useNavigate()
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const [dialog, setDialog] = useState<null | 'new' | 'edit' | 'not-empty'>(null)

  if (folder.isPending) return <div className="page"><Loading /></div>
  if (folder.error || !folder.data) return <div className="page page-body"><ErrorNote error={folder.error} /></div>
  const f = folder.data
  const node = trees.byId.get(id)
  const path: Ref[] = f.summary_fields?.path ?? [{ id: f.id, name: f.name }]
  const base = `/folders/${id}`
  const objects = (contents.data ?? []).reduce((sum, c) => sum + c.count, 0)
  const subfolders = node?.children.length ?? directCounts(f).folders
  const orgRoots = trees.groups.flatMap((g) => g.roots).filter((r) => r.folder.organization === f.organization)
  const canEdit = f.summary_fields?.user_capabilities?.edit !== false

  const remove = async () => {
    if (objects + subfolders > 0) {
      setDialog('not-empty')
      return
    }
    if (!(await confirm({ title: tr('Delete folder {list}?', { list: path.map((p) => p.name).join('/') }), body: tr('Roles granted on this folder are removed with it.'), confirm: tr('Delete folder'), danger: true }))) return
    try {
      await api.del(`folders/${id}/`)
      invalidate('folders')
      nav(f.parent ? `/folders/${f.parent}` : '/folders')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }

  return (
    <div className="body" style={{ minHeight: '100%' }}>
      <FolderTree selected={id} />
      <div className="page" style={{ minWidth: 0 }}>
        <PageHead
          crumbs={[{ label: tr('Folders'), to: '/folders' }, ...path.slice(0, -1).map((p) => ({ label: p.name, to: `/folders/${p.id}` }))]}
          title={f.name}
          badge={<span className="small muted">{f.summary_fields?.organization?.name}</span>}
          subtitle={f.description || undefined}
          actions={
            canEdit && (
              <>
                <button className="btn" onClick={() => setDialog('new')}>
                  {tr('New subfolder')}
                </button>
                <button className="btn" onClick={() => setDialog('edit')} disabled={!node}>
                  {tr('Rename or move')}
                </button>
                <Menu label={tr('More actions')}>
                  <Link to={`/templates?folder=${id}`}>{tr('Show in Templates')}</Link>
                  <hr />
                  <button className="danger" onClick={remove}>
                    {tr('Delete folder')}
                  </button>
                </Menu>
              </>
            )
          }
          tabs={
            <>
              <Tab to={base} end n={contents.data ? objects : undefined}>
                {tr('Contents')}
              </Tab>
              <Tab to={`${base}/subfolders`} n={subfolders || undefined}>
                {tr('Subfolders')}
              </Tab>
              <Tab to={`${base}/access`}>{tr('Access')}</Tab>
            </>
          }
        />
        <Routes>
          <Route index element={<ContentsTab key={id} folder={f} contents={contents} orgRoots={orgRoots} />} />
          <Route path="subfolders" element={<SubfoldersTab node={node} onNew={() => setDialog('new')} canEdit={canEdit} />} />
          <Route
            path="access"
            element={
              <div className="page-body">
                <p className="muted" style={{ margin: 0 }}>
                  {tr('These roles apply to every template, inventory, project and credential in')} <span className="mono">{path.map((p) => p.name).join('/')}</span> {tr('and in its subfolders, including objects added later.')}
                </p>
                <AccessTab resource="folders" id={f.id} name={path.map((p) => p.name).join('/')} />
              </div>
            }
          />
        </Routes>
      </div>
      {(dialog === 'new' || dialog === 'edit') && (
        <FolderDialog
          roots={orgRoots}
          editing={dialog === 'edit' ? node : undefined}
          parent={dialog === 'new' ? node : undefined}
          onClose={() => setDialog(null)}
          onSaved={(saved) => {
            setDialog(null)
            invalidate('folders')
            nav(`/folders/${saved}`)
          }}
        />
      )}
      {dialog === 'not-empty' && (
        <Dialog
          title={tr('This folder isn\'t empty')}
          onClose={() => setDialog(null)}
          footer={
            <>
              <button className="btn" onClick={() => setDialog(null)}>
                {tr('Close')}
              </button>
              <button
                className="btn primary"
                onClick={() => {
                  setDialog(null)
                  nav(objects ? base : `${base}/subfolders`)
                }}
              >
                {tr('Show')} {objects ? tr('contents') : tr('subfolders')}
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>{tr('Move or delete what\'s inside first:')}</p>
          <ul style={{ margin: 0, paddingLeft: 18 }}>
            {(contents.data ?? [])
              .filter((c) => c.count > 0)
              .map((c) => (
                <li key={c.kind.res}>{plural(c.count, c.kind.one)}</li>
              ))}
            {subfolders > 0 && <li>{plural(subfolders, 'subfolder')}</li>}
          </ul>
        </Dialog>
      )}
    </div>
  )
}

function ContentsTab({
  folder,
  contents,
  orgRoots,
}: {
  folder: Folder
  contents: ReturnType<typeof useContents>
  orgRoots: FolderNode[]
}) {
  const [selected, setSelected] = useState<Set<number | string>>(new Set())
  const [dialog, setDialog] = useState<null | 'move' | 'add'>(null)
  const toast = useToast()
  const invalidate = useInvalidate()
  const confirm = useConfirm()

  if (contents.isPending) return <Loading />
  if (contents.error) return <div className="page-body"><ErrorNote error={contents.error} /></div>
  const sections = (contents.data ?? []).filter((c) => c.count > 0)

  /** Sets the folder of several objects, reporting what failed (e.g. objects managed from Git). */
  const moveObjects = async (keys: (string | number)[], target: number | null, verb: string) => {
    const results = await Promise.allSettled(keys.map((k) => {
      const { res, id } = parseKey(k)
      return api.patch(`${res}/${id}/`, { folder: target })
    }))
    const failed = results.filter((r): r is PromiseRejectedResult => r.status === 'rejected')
    if (failed.length) toast(tr('{verb}: {count} of {count2} failed: {message}', { verb, count: failed.length, count2: results.length, message: (failed[0].reason as Error).message }), 'error')
    else toast(tr('{verb}: {count}', { verb, count: plural(results.length, 'object') }))
    setSelected(new Set())
    invalidate('folders', 'unified_job_templates', ...kinds.map((k) => k.res))
  }

  return (
    <div className="page-body">
      <div className="row wrap">
        <span className="muted">{tr('Objects directly in this folder. Subfolders have their own contents.')}</span>
        <button className="btn" style={{ marginLeft: 'auto' }} onClick={() => setDialog('add')}>
          <Icon name="plus" size={14} />
          {tr('Add objects…')}
        </button>
      </div>
      {selected.size > 0 && (
        <div className="bulkbar" role="region" aria-label={tr('Bulk actions')}>
          <span className="n">{selected.size} {tr('selected')}</span>
          <button className="btn" onClick={() => setDialog('move')}>
            {tr('Move to folder…')}
          </button>
          <button
            className="btn"
            onClick={async () => {
              if (!(await confirm({ title: tr('Take {count} out of the folder?', { count: plural(selected.size, 'object') }), body: tr('They stay in the organization, without the access granted on this folder.'), confirm: tr('Take out') }))) return
              moveObjects([...selected], null, tr('Taken out'))
            }}
          >
            {tr('Take out of folder')}
          </button>
          <button className="btn clear" onClick={() => setSelected(new Set())}>
            {tr('Clear')}
          </button>
        </div>
      )}
      {sections.length === 0 ? (
        <Panel>
          <Empty title={tr('Nothing in this folder')} action={<button className="btn primary" onClick={() => setDialog('add')}>{tr('Add objects…')}</button>}>
            {tr('Add templates, inventories, projects or credentials of')} {folder.summary_fields?.organization?.name ?? tr('the organization')}{tr('. You can also pick the folder in an object\'s edit form.')}
          </Empty>
        </Panel>
      ) : (
        sections.map((c) => (
          <section key={c.kind.res} className="stack" style={{ gap: 6 }}>
            <h2>
              {c.kind.label} <span className="mono faint" style={{ fontWeight: 400 }}>{c.count}</span>
            </h2>
            <DataTable
              label={c.kind.label}
              rows={c.items}
              rowKey={(o) => keyOf(c.kind.res, o.id)}
              keyboard={false}
              selected={selected}
              onSelect={setSelected}
              columns={[
                {
                  key: 'name',
                  header: tr('Name'),
                  render: (o) => (
                    <>
                      <Link to={c.kind.path(o.id)} className="cell-title">
                        {o.name}
                      </Link>
                      {o.managed_by && (
                        <span className="tag" style={{ marginLeft: 6 }} title={tr('Managed by {managed_by}: move it in its repository', { managed_by: o.managed_by })}>
                          {tr('git')}
                        </span>
                      )}
                      {c.kind.sub(o) && <div className="cell-sub">{c.kind.sub(o)}</div>}
                    </>
                  ),
                },
              ]}
              footer={c.count > c.items.length && <div className="table-foot">{tr('Showing the first')} {c.items.length} {tr('of')} {c.count}.</div>}
            />
          </section>
        ))
      )}
      {dialog === 'move' && (
        <MoveDialog
          folder={folder}
          roots={orgRoots}
          count={selected.size}
          onClose={() => setDialog(null)}
          onMove={(target) => {
            setDialog(null)
            moveObjects([...selected], target, 'Moved')
          }}
        />
      )}
      {dialog === 'add' && (
        <AddDialog
          folder={folder}
          onClose={() => setDialog(null)}
          onAdd={(res, items) => {
            setDialog(null)
            moveObjects(items.map((o) => keyOf(res, o.id)), folder.id, 'Added')
          }}
        />
      )}
    </div>
  )
}

function MoveDialog({ folder, roots, count, onClose, onMove }: { folder: Folder; roots: FolderNode[]; count: number; onClose: () => void; onMove: (target: number | null) => void }) {
  const options = flatten(roots).filter((n) => n.folder.id !== folder.id)
  const [target, setTarget] = useState(options[0] ? String(options[0].folder.id) : '')
  return (
    <Dialog
      title={tr('Move {count}', { count: plural(count, 'object') })}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" onClick={() => onMove(target ? Number(target) : null)}>
            {tr('Move')}
          </button>
        </>
      }
    >
      <SelectField
        label={tr('To')}
        value={target}
        onChange={setTarget}
        options={[...options.map((n): [string, string] => [String(n.folder.id), `${'   '.repeat(n.depth)}${n.path}`]), ['', tr('No folder (organization level)')]]}
        help={tr('Objects take on the access granted on their new folder, and lose what this folder gave them.')}
      />
    </Dialog>
  )
}

function AddDialog({ folder, onClose, onAdd }: { folder: Folder; onClose: () => void; onAdd: (res: string, items: Ref[]) => void }) {
  const [res, setRes] = useState(kinds[0].res)
  const [items, setItems] = useState<Ref[]>([])
  const kind = kinds.find((k) => k.res === res)!
  return (
    <Dialog
      title={tr('Add to {name}', { name: folder.name })}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={!items.length} onClick={() => onAdd(res, items)}>
            {tr('Add')} {items.length ? plural(items.length, kind.one) : ''}
          </button>
        </>
      }
    >
      <SelectField
        label={tr('Type')}
        value={res}
        onChange={(v) => {
          setRes(v)
          setItems([])
        }}
        options={kinds.map((k): [string, string] => [k.res, k.label])}
      />
      <MultiLookup
        key={res}
        label={kind.label}
        resource={`${res}/`}
        params={{ organization: folder.organization }}
        values={items}
        onChange={setItems}
        meta={(o) => o.summary_fields?.folder?.name ?? ''}
        help={folder.summary_fields?.organization?.name ? tr('Only objects of {org} can go in this folder. Picking one already in another folder moves it.', { org: folder.summary_fields.organization.name }) : tr('Only objects of this organization can go in this folder. Picking one already in another folder moves it.')}
      />
    </Dialog>
  )
}

function SubfoldersTab({ node, onNew, canEdit }: { node?: FolderNode; onNew: () => void; canEdit: boolean }) {
  const nav = useNavigate()
  const children = node?.children ?? []
  return (
    <div className="page-body">
      <DataTable
        label={tr('Subfolders')}
        rows={children}
        rowKey={(n) => n.folder.id}
        onOpen={(n) => nav(`/folders/${n.folder.id}`)}
        columns={[
          {
            key: 'name',
            header: tr('Folder'),
            render: (n) => (
              <>
                <Link to={`/folders/${n.folder.id}`} className="cell-title">
                  {n.folder.name}
                </Link>
                {n.folder.description && <div className="small muted">{n.folder.description}</div>}
              </>
            ),
          },
          { key: 'objects', header: tr('Objects, with subfolders'), className: 'end', render: (n) => <CountCell n={totalObjects(n)} /> },
          { key: 'sub', header: tr('Subfolders'), className: 'end', render: (n) => <CountCell n={n.children.length} /> },
        ]}
        empty={
          <Empty title={tr('No subfolders')} action={canEdit && <button className="btn" onClick={onNew}>{tr('New subfolder')}</button>}>
            {tr('Subfolders inherit the roles granted on this folder and can add their own.')}
          </Empty>
        }
      />
    </div>
  )
}
