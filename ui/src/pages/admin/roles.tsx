// Role definitions (roadmap M7-17): the built-in roles, and custom ones a
// superuser makes from the permissions a kind of object allows. Custom roles
// are granted from the access tabs like the built-in ones.
import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { api } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Base } from '../../api/types'
import { useSession } from '../../app/context'
import type { RoleDef } from '../../components/access/Access'
import { FormError, Segmented, SelectField, TextInput, useForm } from '../../components/form'
import { Dialog, Menu, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, PageHead } from '../../components/ui'
import { plural } from '../../lib/format'
import { tr } from '../../lib/i18n'
import { actionLabel, groupPerms, missingViews, modelLabel, summary, togglePerm } from '../../lib/roleDefinitions'
import { applyErrors, FilterBox, KindTag, useDelete } from '../resources/common'

type Role = RoleDef & Base

const show = [
  ['all', tr('All')],
  ['custom', tr('Custom')],
  ['managed', tr('Built-in')],
] as const

function useRoleMetadata() {
  return useQuery({
    queryKey: ['role_metadata/'],
    queryFn: () => api.get<{ allowed_permissions: Record<string, string[]> }>('role_metadata/'),
    staleTime: 10 * 60_000,
  })
}

export function RoleDefinitionList() {
  const ls = useListState({ order: 'name' })
  const { me } = useSession()
  const [only, setOnly] = useState<(typeof show)[number][0]>('all')
  const params = only === 'custom' ? { managed: 'false' } : only === 'managed' ? { managed: 'true' } : {}
  const list = useList<Role>('role_definitions/', { search: ls.q || undefined, ...params, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const [open, setOpen] = useState<null | { role?: Role; copy?: boolean }>(null)
  const del = useDelete('role_definitions')
  const cols: Column<Role>[] = [
    {
      key: 'name',
      header: tr('Role'),
      sort: 'name',
      render: (r) => (
        <div>
          <span className="row" style={{ gap: 6 }}>
            <button className="link-btn cell-title" onClick={() => setOpen({ role: r })}>
              {r.name}
            </button>
            {r.managed ? <KindTag>{tr('built-in')}</KindTag> : <KindTag>{tr('custom')}</KindTag>}
          </span>
          {r.description && <div className="faint small">{r.description}</div>}
        </div>
      ),
    },
    { key: 'type', header: tr('Applies to'), render: (r) => <span className="small">{r.content_type ? modelLabel(r.content_type) : tr('Everything')}</span> },
    {
      key: 'perms',
      header: tr('Permissions'),
      render: (r) => (
        <span className="small muted" title={r.permissions.join('\n')}>
          {r.permissions.length > 6 ? plural(r.permissions.length, 'permission') : summary(r.permissions, r.content_type)}
        </span>
      ),
    },
    // Built-in roles are rewritten at every start: their date says nothing.
    { key: 'modified', header: tr('Edited'), sort: 'modified', render: (r) => (r.managed ? <span className="faint small">—</span> : <span className="small faint nowrap"><Ago at={r.modified} /></span>) },
    {
      key: 'menu',
      header: <span className="sr-only">{tr('Actions')}</span>,
      className: 'end',
      render: (r) =>
        me?.is_superuser ? (
          <Menu label={tr('Actions for {name}', { name: r.name })} className="btn sm icon ghost">
            {!r.managed && <button onClick={() => setOpen({ role: r })}>{tr('Edit…')}</button>}
            <button onClick={() => setOpen({ role: r, copy: true })}>{tr('Copy as a custom role…')}</button>
            {!r.managed && (
              <>
                <hr />
                <button className="danger" onClick={() => del(r, 'role', <p style={{ margin: 0 }}>{tr('Everyone who holds it, directly or through a team, loses what it gives. This cannot be undone.')}</p>)}>
                  {tr('Delete…')}
                </button>
              </>
            )}
          </Menu>
        ) : null,
    },
  ]
  return (
    <div className="page">
      <PageHead
        title={tr('Roles')}
        subtitle={tr('Sets of permissions granted on an object, or on an organization or folder for everything in it. Built-in roles are fixed; custom roles hold the permissions you pick.')}
        bordered={false}
        actions={
          me?.is_superuser ? (
            <button className="btn primary" onClick={() => setOpen({})}>
              {tr('New role')}
            </button>
          ) : undefined
        }
      />
      <div className="page-body">
        <div className="row wrap" style={{ gap: 8 }}>
          <FilterBox value={ls.q} onApply={ls.setQ} placeholder={tr('name…')} />
          <Segmented legend={<span className="sr-only">{tr('Show')}</span>} value={only} onChange={setOnly} options={show.map(([k, label]) => [k, label])} />
        </div>
        <DataTable
          label={tr('Roles')}
          rows={list.data?.results}
          columns={cols}
          columnsKey="role_definitions"
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(r) => setOpen({ role: r })}
          empty={<Empty title={ls.q || only !== 'all' ? tr('No role matches') : tr('No roles')} />}
          footer={
            <div className="table-foot">
              <span>{list.data ? plural(list.data.count, 'role') : ''}</span>
              <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
      {open && <RoleDialog role={open.role} copy={open.copy} onClose={() => setOpen(null)} />}
    </div>
  )
}

function RoleDialog({ role, copy, onClose }: { role?: Role; copy?: boolean; onClose: () => void }) {
  const { me } = useSession()
  const editing = role && !copy ? role : undefined
  const editable = !!me?.is_superuser && (!editing || !editing.managed)
  const meta = useRoleMetadata()
  const holders = useList<Base>(editing ? 'role_user_assignments/' : '', { role_definition: editing?.id, page_size: 1 }, { enabled: !!editing })
  const teams = useList<Base>(editing ? 'role_team_assignments/' : '', { role_definition: editing?.id, page_size: 1 }, { enabled: !!editing })
  const held = (holders.data?.count ?? 0) + (teams.data?.count ?? 0)
  const f = useForm({
    name: copy && role ? tr('{name} (copy)', { name: role.name }) : (role?.name ?? ''),
    description: role?.description ?? '',
    content_type: role?.content_type ?? 'awx.jobtemplate',
  })
  const [perms, setPerms] = useState<string[]>(role?.permissions ?? ['awx.view_jobtemplate'])
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const toast = useToast()
  const v = f.values
  const types = Object.keys(meta.data?.allowed_permissions ?? {}).sort((a, b) => modelLabel(a).localeCompare(modelLabel(b)))
  const allowed = meta.data?.allowed_permissions[v.content_type] ?? []
  const groups = groupPerms(allowed, v.content_type)
  const valid = perms.filter((p) => allowed.includes(p))
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    const body = { name: v.name, description: v.description, content_type: v.content_type, permissions: valid }
    try {
      const saved = editing ? await api.patch<Role>(`role_definitions/${editing.id}/`, body) : await api.post<Role>('role_definitions/', body)
      invalidate('role_definitions')
      toast(editing ? tr('Saved {name}', { name: saved.name }) : tr('Created {name}', { name: saved.name }))
      onClose()
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  const title = !role ? tr('New role') : copy ? tr('New role from {name}', { name: role.name }) : editable ? tr('Edit {name}', { name: role.name }) : role.name
  return (
    <Dialog
      title={title}
      onClose={onClose}
      wide
      footer={
        editable ? (
          <>
            <button className="btn" type="button" onClick={onClose}>
              {tr('Cancel')}
            </button>
            <button className="btn primary" form="role-form" disabled={busy || !v.name.trim() || !valid.length || missingViews(valid).length > 0}>
              {busy ? tr('Saving…') : editing ? tr('Save') : tr('Create role')}
            </button>
          </>
        ) : (
          <button className="btn" type="button" onClick={onClose}>
            {tr('Close')}
          </button>
        )
      }
    >
      <form id="role-form" className="form" onSubmit={submit}>
        {role?.managed && !copy && <p className="muted" style={{ marginTop: 0 }}>{tr('A built-in role: its permissions are fixed. Copy it as a custom role to change them.')}</p>}
        {editing && held > 0 && editable && (
          <div className="notice small" role="note">
            {tr('Held by {n}: what you change here applies to them at once.', { n: plural(held, 'assignment') })}
          </div>
        )}
        <div className="form-grid">
          <TextInput label={tr('Name')} value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} disabled={!editable} />
          <SelectField
            label={tr('Applies to')}
            value={v.content_type}
            onChange={(x) => {
              f.set('content_type', x)
              const next = meta.data?.allowed_permissions[x] ?? []
              setPerms((p) => p.filter((q) => next.includes(q)))
            }}
            options={types.map((t) => [t, modelLabel(t)])}
            help={editing ? tr('Fixed once the role exists: its assignments are on objects of this kind.') : tr('Granted on one of these, or on an organization or folder for every object in it.')}
            error={f.errors.content_type}
            disabled={!editable || !!editing}
          />
          <TextInput label={tr('Description')} value={v.description} onChange={(x) => f.set('description', x)} optional full disabled={!editable} />
        </div>
        <fieldset className="form-section perms" disabled={!editable}>
          <legend>
            {tr('Permissions')} <span className="faint small">· {plural(valid.length, 'permission')}</span>
          </legend>
          {meta.isPending ? (
            <p className="muted small">{tr('Loading…')}</p>
          ) : (
            groups.map((g) => (
              <div key={g.model} className="perm-group">
                <div className="perm-model">{modelLabel(g.model, true)}</div>
                <div className="perm-list">
                  {g.perms.map((p) => (
                    <label key={p.perm} className="check" title={p.perm}>
                      <input type="checkbox" checked={perms.includes(p.perm)} onChange={() => setPerms((s) => togglePerm(s, p.perm))} />
                      {actionLabel(p)}
                    </label>
                  ))}
                </div>
              </div>
            ))
          )}
          <p className="help small" style={{ margin: 0 }}>
            {tr('Granting anything on a kind of object also lets it be seen, so View turns on with it.')}
          </p>
        </fieldset>
        <FormError error={error} />
      </form>
    </Dialog>
  )
}
