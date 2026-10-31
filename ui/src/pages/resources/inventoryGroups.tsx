import { useState, type FormEvent } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { api, type Page } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { FormError, MultiLookup, TextInput, VarsEditor, useForm, varsError } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Empty, ErrorNote, Loading, Panel } from '../../components/ui'
import { plural } from '../../lib/format'
import { applyErrors, FilterBox } from './common'
import { useSubList, type Inventory } from './inventories'
import { AdHocDialog } from './inventoryHosts'
import { tr } from '../../lib/i18n'

interface Group extends Base {
  inventory: number
  variables: string
}

/** Groups of an inventory. ?group=ID opens one group with its members and children. */
export function InventoryGroupsTab({ inv }: { inv: Inventory }) {
  const [sp, setSp] = useSearchParams()
  const open = sp.get('group')
  if (open) return <GroupView inv={inv} id={Number(open)} onBack={() => setSp({}, { replace: false })} />
  return <GroupList inv={inv} />
}

function GroupList({ inv }: { inv: Inventory }) {
  const ls = useListState({ order: 'name', pageSize: 50 })
  const [, setSp] = useSearchParams()
  const path = `inventories/${inv.id}/groups/`
  const groups = useSubList<Group>(path, { search: ls.q || undefined, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const [edit, setEdit] = useState<null | { group?: Group }>(null)
  const [adhoc, setAdhoc] = useState<string | null>(null)
  const del = useGroupDelete(inv.id)
  const editable = inv.kind === '' && !inv.managed_by && !!inv.summary_fields.user_capabilities?.edit
  const cols: Column<Group>[] = [
    {
      key: 'name',
      header: tr('Group'),
      sort: 'name',
      render: (g) => (
        <div>
          <button className="link-btn cell-title mono" style={{ fontSize: 13 }} onClick={() => setSp({ group: String(g.id) })}>
            {g.name}
          </button>
          {g.description && <div className="cell-sub">{g.description}</div>}
        </div>
      ),
    },
    { key: 'hosts', header: tr('Hosts'), render: (g) => <SubCount path={`groups/${g.id}/hosts/`} /> },
    { key: 'children', header: tr('Child groups'), render: (g) => <SubCount path={`groups/${g.id}/children/`} /> },
    {
      key: 'vars',
      header: tr('Variables'),
      render: (g) => {
        const n = g.variables?.trim() ? g.variables.trim().split('\n').length : 0
        return n ? <span className="mono small ellipsis" style={{ maxWidth: 280, display: 'inline-block' }}>{g.variables.trim().split('\n')[0]}{n > 1 ? ' …' : ''}</span> : <span className="faint small">—</span>
      },
    },
    {
      key: 'menu',
      header: <span className="sr-only">{tr('Actions')}</span>,
      className: 'end',
      render: (g) => (
        <Menu label={tr('Actions for {name}', { name: g.name })} className="btn sm icon ghost">
          <button onClick={() => setSp({ group: String(g.id) })}>{tr('Open')}</button>
          <button onClick={() => setAdhoc(g.name)}>{tr('Run command…')}</button>
          {editable && <button onClick={() => setEdit({ group: g })}>{tr('Edit…')}</button>}
          {editable && (
            <>
              <hr />
              <button className="danger" onClick={() => del(g)}>
                {tr('Delete…')}
              </button>
            </>
          )}
        </Menu>
      ),
    },
  ]
  return (
    <div className="page-body">
      <div className="row wrap" style={{ gap: 8 }}>
        <div className="grow">
          <FilterBox value={ls.q} onApply={ls.setQ} placeholder={tr('group name…')} />
        </div>
        {editable && (
          <button className="btn primary" onClick={() => setEdit({})}>
            {tr('New group')}
          </button>
        )}
      </div>
      <DataTable
        label={tr('Groups')}
        rows={groups.data?.results}
        columns={cols}
        loading={groups.isPending}
        error={groups.error}
        order={ls.order}
        onOrder={ls.setOrder}
        onOpen={(g) => setSp({ group: String(g.id) })}
        empty={
          <Empty title={ls.q ? tr('No group matches') : tr('No groups')} action={editable && !ls.q ? <button className="btn primary" onClick={() => setEdit({})}>{tr('New group')}</button> : undefined}>
            {ls.q ? null : tr('Groups let jobs target sets of hosts and share variables (group_vars).')}
          </Empty>
        }
        footer={
          <div className="table-foot">
            <span>{groups.data ? tr('{count}', { count: plural(groups.data.count, 'group', 'groups') }) : ''}</span>
            <Pager count={groups.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
          </div>
        }
      />
      {edit && <GroupDialog inv={inv} group={edit.group} onClose={() => setEdit(null)} />}
      {adhoc && <AdHocDialog inventory={inv} limit={adhoc} onClose={() => setAdhoc(null)} />}
    </div>
  )
}

function SubCount({ path }: { path: string }) {
  const r = useList<Base>(path, { page_size: 1 }, { staleTime: 60_000 })
  return <span className="mono small">{r.data ? r.data.count : '·'}</span>
}

function useGroupDelete(inventory: number) {
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  return async (g: Group) => {
    const ok = await confirm({
      title: tr('Delete group {name}?', { name: g.name }),
      body: <p style={{ margin: 0 }}>{tr('Hosts stay in the inventory; they just leave this group. Child groups are not deleted.')}</p>,
      confirm: tr('Delete group'),
      danger: true,
    })
    if (!ok) return false
    try {
      await api.del(`groups/${g.id}/`)
      toast(tr('Deleted {name}', { name: g.name }))
      invalidate(`inventories/${inventory}`, 'inventories', 'groups')
      return true
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Delete failed'), 'error')
      return false
    }
  }
}

function GroupDialog({ inv, group, onClose }: { inv: Inventory; group?: Group; onClose: () => void }) {
  const f = useForm({ name: group?.name ?? '', description: group?.description ?? '', variables: group?.variables ?? '' })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const invalidate = useInvalidate()
  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if (group) await api.patch(`groups/${group.id}/`, f.values)
      else await api.post(`inventories/${inv.id}/groups/`, f.values)
      toast(group ? tr('Saved {name}', { name: f.values.name }) : tr('Created {name}', { name: f.values.name }))
      invalidate(`inventories/${inv.id}`, 'inventories', 'groups')
      onClose()
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={group ? tr('Edit {name}', { name: group.name }) : tr('New group')}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={busy || !f.values.name || !!varsError(f.values.variables)} onClick={() => submit()}>
            {busy ? tr('Saving…') : group ? tr('Save') : tr('Create group')}
          </button>
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <div className="form-grid">
          <TextInput label={tr('Name')} mono value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus />
          <TextInput label={tr('Description')} optional value={f.values.description} onChange={(v) => f.set('description', v)} />
        </div>
        <VarsEditor label={tr('Group variables')} value={f.values.variables} onChange={(v) => f.set('variables', v)} error={f.errors.variables} />
        <FormError error={error} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}

/** One group: its hosts and child groups, with add/remove. */
function GroupView({ inv, id, onBack }: { inv: Inventory; id: number; onBack: () => void }) {
  const group = useObject<Group>(`groups/${id}/`)
  const hosts = useList<Base>(`groups/${id}/hosts/`, { page_size: 200, order_by: 'name' })
  const children = useList<Base>(`groups/${id}/children/`, { page_size: 200, order_by: 'name' })
  const [edit, setEdit] = useState(false)
  const [adding, setAdding] = useState<null | 'hosts' | 'children'>(null)
  const [adhoc, setAdhoc] = useState(false)
  const invalidate = useInvalidate()
  const toast = useToast()
  const nav = useNavigate()
  const del = useGroupDelete(inv.id)
  const editable = inv.kind === '' && !inv.managed_by && !!inv.summary_fields.user_capabilities?.edit
  if (group.isPending) return <Loading />
  if (group.error || !group.data) return <div className="page-body"><ErrorNote error={group.error ?? tr('Not found')} /></div>
  const g = group.data
  const detach = async (sub: 'hosts' | 'children', obj: Base) => {
    try {
      await api.post(`groups/${id}/${sub}/`, { id: obj.id, disassociate: true })
      invalidate(`groups/${id}`, `inventories/${inv.id}`)
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Failed'), 'error')
    }
  }
  return (
    <div className="page-body">
      <div className="row wrap" style={{ gap: 8 }}>
        <button className="btn ghost sm" onClick={onBack}>
          {tr('← All groups')}
        </button>
        <h2 className="mono" style={{ fontSize: 15 }}>
          {g.name}
        </h2>
        {g.description && <span className="muted">{g.description}</span>}
        <div className="row" style={{ marginLeft: 'auto' }}>
          <button className="btn" onClick={() => setAdhoc(true)}>
            <Icon name="terminal" size={14} /> {tr('Run command…')}
          </button>
          {editable && (
            <button className="btn" onClick={() => setEdit(true)}>
              {tr('Edit')}
            </button>
          )}
          {editable && (
            <Menu label={tr('More actions')}>
              <button className="danger" onClick={async () => (await del(g)) && onBack()}>
                {tr('Delete group…')}
              </button>
            </Menu>
          )}
        </div>
      </div>
      <div className="split">
        <div className="wide stack" style={{ gap: 16 }}>
          <Panel
            title={tr('Hosts · {count}', { count: hosts.data?.count ?? '…' })}
            pad={false}
            actions={
              editable && (
                <button className="btn sm" onClick={() => setAdding('hosts')}>
                  {tr('Add hosts')}
                </button>
              )
            }
          >
            <MemberList items={hosts.data?.results} empty={tr('No hosts in this group.')} onOpen={(h) => nav(`/hosts/${h.id}`)} onRemove={editable ? (h) => detach('hosts', h) : undefined} mono />
          </Panel>
          <Panel
            title={tr('Child groups · {count}', { count: children.data?.count ?? '…' })}
            pad={false}
            actions={
              editable && (
                <button className="btn sm" onClick={() => setAdding('children')}>
                  {tr('Add groups')}
                </button>
              )
            }
          >
            <MemberList
              items={children.data?.results}
              empty={tr('No child groups. Hosts of child groups are members of this group too.')}
              onOpen={(c) => nav(`/inventories/${inv.id}/groups?group=${c.id}`)}
              onRemove={editable ? (c) => detach('children', c) : undefined}
              mono
            />
          </Panel>
        </div>
        <div className="narrow">
          <Panel title={tr('Variables')}>{g.variables?.trim() ? <pre className="code">{g.variables}</pre> : <span className="faint small">{tr('No group variables.')}</span>}</Panel>
        </div>
      </div>
      {edit && <GroupDialog inv={inv} group={g} onClose={() => setEdit(false)} />}
      {adhoc && <AdHocDialog inventory={inv} limit={g.name} onClose={() => setAdhoc(false)} />}
      {adding && <AttachDialog inv={inv} group={g} sub={adding} onClose={() => setAdding(null)} />}
    </div>
  )
}

function MemberList({ items, empty, onOpen, onRemove, mono }: { items?: Base[]; empty: string; onOpen: (b: Base) => void; onRemove?: (b: Base) => void; mono?: boolean }) {
  if (!items) return <Loading rows={3} />
  if (!items.length) return <div className="panel-body faint small">{empty}</div>
  return (
    <table className="data">
      <tbody>
        {items.map((it) => (
          <tr key={it.id}>
            <td>
              <button className={`link-btn ${mono ? 'mono' : ''}`} onClick={() => onOpen(it)}>
                {it.name}
              </button>
            </td>
            <td className="end">
              {onRemove && (
                <button className="btn ghost sm" onClick={() => onRemove(it)} title={tr('Remove from group (does not delete)')}>
                  {tr('Remove')}
                </button>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function AttachDialog({ inv, group, sub, onClose }: { inv: Inventory; group: Group; sub: 'hosts' | 'children'; onClose: () => void }) {
  const [picked, setPicked] = useState<Ref[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const invalidate = useInvalidate()
  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      for (const p of picked) await api.post(`groups/${group.id}/${sub}/`, { id: p.id })
      invalidate(`groups/${group.id}`, `inventories/${inv.id}`)
      onClose()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={sub === 'hosts' ? tr('Add hosts to {name}', { name: group.name }) : tr('Add child groups to {name}', { name: group.name })}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={busy || !picked.length} onClick={save}>
            {tr('Add')} {picked.length || ''}
          </button>
        </>
      }
    >
      <MultiLookup
        label={sub === 'hosts' ? tr('Hosts') : tr('Groups')}
        resource={sub === 'hosts' ? `inventories/${inv.id}/hosts/` : `groups/${group.id}/potential_children/`}
        values={picked}
        onChange={setPicked}
        help={sub === 'hosts' ? tr('Hosts of this inventory.') : tr('Groups that would not create a cycle.')}
      />
      <FormError error={error} />
    </Dialog>
  )
}

export type { Page }
