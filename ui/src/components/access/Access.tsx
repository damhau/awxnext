// Access tab: who holds which role on one object (users and teams), granted
// directly or inherited from the object's organization and folders, with
// grant and revoke. Built on the django-ansible-base style endpoints
// (role_definitions, role_user_assignments, role_team_assignments).
import { useQuery } from '@tanstack/react-query'
import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { api, getAll } from '../../api/client'
import { useInvalidate } from '../../api/hooks'
import type { Any, Ref } from '../../api/types'
import { useSession } from '../../app/context'
import { FormError, MultiLookup } from '../form'
import { Icon } from '../icons'
import { Dialog, useConfirm, useToast } from '../overlay'
import { Ago, Avatar, Empty, Loading } from '../ui'
import './access.css'

/** API resource name -> RBAC content type. */
const contentTypes: Record<string, string> = {
  organizations: 'organization',
  teams: 'team',
  credentials: 'credential',
  projects: 'project',
  inventories: 'inventory',
  constructed_inventories: 'inventory',
  job_templates: 'jobtemplate',
  workflow_job_templates: 'workflowjobtemplate',
  notification_templates: 'notificationtemplate',
  execution_environments: 'executionenvironment',
  folders: 'folder',
  instance_groups: 'instancegroup',
}

export function contentTypeOf(resource: string): string {
  return contentTypes[resource] ?? resource.replace(/_/g, '').replace(/s$/, '')
}

/** Strips the "awx."/"shared." prefix of DAB content types. */
export function bareType(ct: string | null | undefined): string {
  if (!ct) return ''
  const i = ct.lastIndexOf('.')
  return i >= 0 ? ct.slice(i + 1) : ct
}

const typeLabels: Record<string, string> = {
  organization: 'Organization',
  team: 'Team',
  credential: 'Credential',
  project: 'Project',
  inventory: 'Inventory',
  jobtemplate: 'Job template',
  workflowjobtemplate: 'Workflow template',
  notificationtemplate: 'Notification template',
  executionenvironment: 'Execution environment',
  folder: 'Folder',
  instancegroup: 'Instance group',
}

export function typeName(ct: string | null | undefined): string {
  const t = bareType(ct)
  return t ? (typeLabels[t] ?? t) : 'System'
}

/** UI route of an object identified by content type and id. */
export function objectPath(ct: string | null | undefined, id: number | string): string | null {
  switch (bareType(ct)) {
    case 'organization':
      return `/organizations/${id}`
    case 'team':
      return `/teams/${id}`
    case 'credential':
      return `/credentials/${id}`
    case 'project':
      return `/projects/${id}`
    case 'inventory':
      return `/inventories/${id}`
    case 'jobtemplate':
      return `/templates/job/${id}`
    case 'workflowjobtemplate':
      return `/templates/workflow/${id}`
    case 'notificationtemplate':
      return `/notifications/${id}`
    case 'executionenvironment':
      return `/execution-environments/${id}`
    case 'folder':
      return `/templates?folder=${id}`
    case 'instancegroup':
      return `/instance-groups/${id}`
  }
  return null
}

/** Resources a role can be granted on, for the principal-centric grant dialog. */
export const grantable: { resource: string; ct: string; label: string }[] = [
  { resource: 'organizations', ct: 'organization', label: 'Organization' },
  { resource: 'folders', ct: 'folder', label: 'Folder' },
  { resource: 'job_templates', ct: 'jobtemplate', label: 'Job template' },
  { resource: 'workflow_job_templates', ct: 'workflowjobtemplate', label: 'Workflow template' },
  { resource: 'inventories', ct: 'inventory', label: 'Inventory' },
  { resource: 'projects', ct: 'project', label: 'Project' },
  { resource: 'credentials', ct: 'credential', label: 'Credential' },
  { resource: 'teams', ct: 'team', label: 'Team' },
  { resource: 'execution_environments', ct: 'executionenvironment', label: 'Execution environment' },
  { resource: 'notification_templates', ct: 'notificationtemplate', label: 'Notification template' },
  { resource: 'instance_groups', ct: 'instancegroup', label: 'Instance group' },
]

export interface RoleDef {
  id: number
  name: string
  description: string
  content_type: string | null
  permissions: string[]
  managed: boolean
}

export interface Assignment {
  id: number
  content_type: string | null
  object_id: string | null
  role_definition: number
  user?: number
  team?: number
  created: string
  summary_fields: {
    content_object?: { id: number; name: string }
    role_definition?: { id: number; name: string; description: string }
    user?: { id: number; username: string; first_name?: string; last_name?: string }
    team?: { id: number; name: string }
    created_by?: { id: number; username: string; first_name?: string; last_name?: string }
    user_capabilities?: { delete?: boolean }
  }
}

export function useRoleDefinitions() {
  return useQuery({
    queryKey: ['role_definitions/', 'all'],
    queryFn: () => getAll<RoleDef>('role_definitions/', { order_by: 'id' }),
    staleTime: 5 * 60_000,
  })
}

/** Short role name: "JobTemplate Execute" -> "Execute" when the object type is implied. */
export function roleShort(name: string, ct?: string | null): string {
  const t = bareType(ct)
  const prefixes: Record<string, string[]> = {
    jobtemplate: ['JobTemplate '],
    workflowjobtemplate: ['WorkflowJobTemplate '],
    inventory: ['Inventory '],
    project: ['Project '],
    credential: ['Credential '],
    team: ['Team '],
    organization: ['Organization '],
    notificationtemplate: ['NotificationTemplate '],
    executionenvironment: ['ExecutionEnvironment '],
    folder: ['Folder '],
    instancegroup: ['InstanceGroup '],
  }
  for (const p of prefixes[t] ?? []) if (name.startsWith(p)) return name.slice(p.length)
  return name
}

function personName(u?: { username: string; first_name?: string; last_name?: string }): string {
  if (!u) return '—'
  return [u.first_name, u.last_name].filter(Boolean).join(' ') || u.username
}

interface Row {
  key: string
  who: 'user' | 'team'
  id: number
  name: string
  sub?: string
  role: string
  roleDesc?: string
  via: ReactNode
  inherited: boolean
  assignment?: Assignment
}

/** Assignments relevant to an object: direct, on its organization, and on its folder chain. */
async function loadAccess(resource: string, id: number) {
  const ct = contentTypeOf(resource)
  const obj = await api.get<Any>(`${resource}/${id}/`)
  const direct = (kind: 'user' | 'team') => getAll<Assignment>(`role_${kind}_assignments/`, { content_type: ct, object_id: id })
  const scopes: { ct: string; id: number; label: string }[] = []
  const orgId: number | null = ct === 'organization' ? null : (obj.organization ?? null)
  if (orgId) scopes.push({ ct: 'organization', id: orgId, label: obj.summary_fields?.organization?.name ?? `organization ${orgId}` })
  // Folder chain, nearest first.
  let folder: number | null = ct === 'folder' ? (obj.parent ?? null) : (obj.folder ?? null)
  for (let depth = 0; folder && depth < 12; depth++) {
    const f = await api.get<Any>(`folders/${folder}/`).catch(() => null)
    if (!f) break
    scopes.push({ ct: 'folder', id: f.id, label: f.name })
    folder = f.parent ?? null
  }
  const inherited = (kind: 'user' | 'team') =>
    Promise.all(scopes.map((s) => getAll<Assignment>(`role_${kind}_assignments/`, { content_type: s.ct, object_id: s.id }).then((list) => list.map((a) => ({ a, s })))))
  const [du, dt, iu, it] = await Promise.all([direct('user'), direct('team'), inherited('user'), inherited('team')])
  return { obj, ct, direct: [...du, ...dt], inherited: [...iu.flat(), ...it.flat()], scopes }
}

export function AccessTab({ resource, id, name }: { resource: string; id: number; name: string }) {
  const { me } = useSession()
  const ct = contentTypeOf(resource)
  const roles = useRoleDefinitions()
  const access = useQuery({ queryKey: [`role_access/${resource}/${id}`], queryFn: () => loadAccess(resource, id) })
  const [granting, setGranting] = useState(false)
  const [filter, setFilter] = useState<'all' | 'direct' | 'inherited'>('all')
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()

  const byId = useMemo(() => new Map((roles.data ?? []).map((r) => [r.id, r])), [roles.data])
  // Inherited assignments only matter when the role carries a permission on this object type.
  const relevant = (rd: RoleDef | undefined) => !!rd && (bareType(rd.content_type) === ct || rd.permissions.some((p) => p.endsWith('_' + ct)))

  const rows: Row[] = useMemo(() => {
    if (!access.data) return []
    const out: Row[] = []
    const principal = (a: Assignment) =>
      a.summary_fields.user
        ? { who: 'user' as const, id: a.summary_fields.user.id, name: personName(a.summary_fields.user), sub: a.summary_fields.user.username }
        : { who: 'team' as const, id: a.summary_fields.team?.id ?? a.team ?? 0, name: a.summary_fields.team?.name ?? `team ${a.team}`, sub: undefined }
    for (const a of access.data.direct) {
      const rd = byId.get(a.role_definition)
      out.push({
        key: 'd' + a.id + (a.user ? 'u' : 't'),
        ...principal(a),
        role: roleShort(a.summary_fields.role_definition?.name ?? rd?.name ?? '?', ct),
        roleDesc: rd?.description,
        via: 'This ' + typeName(ct).toLowerCase(),
        inherited: false,
        assignment: a,
      })
    }
    for (const { a, s } of access.data.inherited) {
      const rd = byId.get(a.role_definition)
      if (!relevant(rd)) continue
      const path = objectPath(s.ct, s.id)
      out.push({
        key: 'i' + a.id + (a.user ? 'u' : 't') + s.ct,
        ...principal(a),
        role: a.summary_fields.role_definition?.name ?? rd?.name ?? '?',
        roleDesc: rd?.description,
        via: (
          <>
            {typeName(s.ct)} {path ? <Link to={path}>{s.label}</Link> : s.label}
          </>
        ),
        inherited: true,
        assignment: a,
      })
    }
    return out.sort((x, y) => Number(x.inherited) - Number(y.inherited) || x.who.localeCompare(y.who) || x.name.localeCompare(y.name))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [access.data, byId, ct])

  const shown = rows.filter((r) => filter === 'all' || (filter === 'direct' ? !r.inherited : r.inherited))
  const canGrant = me.is_superuser || !!access.data?.obj?.summary_fields?.user_capabilities?.edit

  const revoke = async (r: Row) => {
    if (!r.assignment) return
    const ok = await confirm({
      title: `Remove ${r.role} from ${r.name}?`,
      body: (
        <p style={{ margin: 0 }}>
          {r.who === 'team' ? 'Members of the team' : r.name} will lose the {r.role} role on {r.inherited ? 'the inherited scope' : name}. Other roles they hold are not affected.
        </p>
      ),
      confirm: 'Remove access',
      danger: true,
    })
    if (!ok) return
    try {
      await api.del(`role_${r.who}_assignments/${r.assignment.id}/`)
      toast(`Removed ${r.role} from ${r.name}`)
      invalidate(`role_access/${resource}/${id}`, 'role_user_assignments', 'role_team_assignments')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }

  const direct = rows.filter((r) => !r.inherited).length
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row wrap">
        <div className="segmented" role="radiogroup" aria-label="Show">
          {(
            [
              ['all', `All ${rows.length}`],
              ['direct', `Direct ${direct}`],
              ['inherited', `Inherited ${rows.length - direct}`],
            ] as const
          ).map(([v, l]) => (
            <label key={v} className={filter === v ? 'on' : ''}>
              <input type="radio" name="access-filter" checked={filter === v} onChange={() => setFilter(v)} />
              {l}
            </label>
          ))}
        </div>
        <span className="small faint grow">
          Superusers can do everything and are not listed. Team roles apply to every member of the team.
        </span>
        {canGrant && (
          <button className="btn primary" onClick={() => setGranting(true)}>
            <Icon name="plus" size={14} />
            Grant access
          </button>
        )}
      </div>
      <div className="table-wrap">
        <table className="data">
          <thead>
            <tr>
              <th scope="col">Who</th>
              <th scope="col">Role</th>
              <th scope="col">Granted on</th>
              <th scope="col">Granted</th>
              <th scope="col" className="end">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <tr key={r.key}>
                <td>
                  <div className="row">
                    <Avatar name={r.name} size="sm" />
                    <div className="grow">
                      <Link to={r.who === 'user' ? `/users/${r.id}` : `/teams/${r.id}`} className="cell-title">
                        {r.name}
                      </Link>
                      <div className="small faint">{r.who === 'team' ? 'Team' : r.sub}</div>
                    </div>
                  </div>
                </td>
                <td>
                  <span title={r.roleDesc}>{r.role}</span>
                </td>
                <td className="small">{r.via}</td>
                <td className="small faint nowrap">
                  {r.assignment?.created ? <Ago at={r.assignment.created} /> : '—'}
                  {r.assignment?.summary_fields.created_by && <> · {personName(r.assignment.summary_fields.created_by)}</>}
                </td>
                <td className="end">
                  {!r.inherited && r.assignment?.summary_fields.user_capabilities?.delete !== false && (
                    <button className="btn sm ghost" onClick={() => revoke(r)} aria-label={`Remove ${r.role} from ${r.name}`}>
                      Remove
                    </button>
                  )}
                  {r.inherited && <span className="small faint">inherited</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {access.isPending && <Loading rows={4} />}
        {access.error && <div style={{ padding: 12 }}><FormError error={access.error} /></div>}
        {access.data && shown.length === 0 && (
          <Empty title={filter === 'inherited' ? 'No inherited access' : 'Nobody has been granted access yet'}>
            {filter === 'inherited' ? 'Roles on the organization or folders containing this object would show here.' : 'Grant a role to users or teams to let them use this.'}
          </Empty>
        )}
      </div>
      {granting && (
        <GrantDialog
          target={{ resource, ct, id, name }}
          onClose={() => setGranting(false)}
          onDone={() => {
            setGranting(false)
            invalidate(`role_access/${resource}/${id}`, 'role_user_assignments', 'role_team_assignments')
          }}
        />
      )}
    </div>
  )
}

/** Object-centric grant: pick users/teams and roles for one object. */
export function GrantDialog({
  target,
  onClose,
  onDone,
  fixedPrincipal,
}: {
  target: { resource: string; ct: string; id: number; name: string }
  onClose: () => void
  onDone: () => void
  fixedPrincipal?: { kind: 'user' | 'team'; ref: Ref }
}) {
  const roles = useRoleDefinitions()
  const [kind, setKind] = useState<'user' | 'team'>(fixedPrincipal?.kind ?? 'user')
  const [users, setUsers] = useState<Ref[]>(fixedPrincipal?.kind === 'user' ? [fixedPrincipal.ref] : [])
  const [teams, setTeams] = useState<Ref[]>(fixedPrincipal?.kind === 'team' ? [fixedPrincipal.ref] : [])
  const [picked, setPicked] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const toast = useToast()
  const options = (roles.data ?? []).filter((r) => bareType(r.content_type) === target.ct)
  const principals = kind === 'user' ? users : teams
  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      let n = 0
      for (const p of principals) {
        for (const rd of picked) {
          await api.post(`role_${kind}_assignments/`, { role_definition: rd, [kind]: p.id, object_id: target.id }).catch((e) => {
            // An existing identical assignment is not an error for the user.
            if (!/already|exists|unique/i.test((e as Error).message)) throw e
          })
          n++
        }
      }
      toast(`Granted ${n} role${n === 1 ? '' : 's'} on ${target.name}`)
      onDone()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={`Grant access to ${target.name}`}
      onClose={onClose}
      wide
      footer={
        <>
          <span className="small faint" style={{ marginRight: 'auto' }}>
            {principals.length && picked.size ? `${principals.length * picked.size} assignment${principals.length * picked.size === 1 ? '' : 's'} will be created` : ''}
          </span>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !principals.length || !picked.size} onClick={submit}>
            {busy ? 'Granting…' : 'Grant'}
          </button>
        </>
      }
    >
      {!fixedPrincipal && (
        <div className="segmented" role="radiogroup" aria-label="Grant to">
          {(['user', 'team'] as const).map((k) => (
            <label key={k} className={kind === k ? 'on' : ''}>
              <input type="radio" name="grant-kind" checked={kind === k} onChange={() => setKind(k)} />
              {k === 'user' ? 'Users' : 'Teams'}
            </label>
          ))}
        </div>
      )}
      {!fixedPrincipal &&
        (kind === 'user' ? (
          <MultiLookup
            label="Users"
            resource="users/"
            order="username"
            values={users}
            onChange={setUsers}
            display={(r) => [r.first_name, r.last_name].filter(Boolean).join(' ') || r.username}
            meta={(r) => r.username}
            full
          />
        ) : (
          <MultiLookup label="Teams" resource="teams/" values={teams} onChange={setTeams} meta={(r) => r.summary_fields?.organization?.name} full />
        ))}
      <fieldset>
        <legend>Roles</legend>
        {roles.isPending && <Loading rows={3} />}
        <div className="stack" style={{ gap: 6 }}>
          {options.map((r) => (
            <label key={r.id} className="role-option">
              <input
                type="checkbox"
                checked={picked.has(r.id)}
                onChange={(e) => {
                  const n = new Set(picked)
                  if (e.target.checked) n.add(r.id)
                  else n.delete(r.id)
                  setPicked(n)
                }}
              />
              <span className="grow">
                <span style={{ fontWeight: 500 }}>{roleShort(r.name, target.ct)}</span>
                {!r.managed && <span className="tag" style={{ marginLeft: 6 }}>custom</span>}
                <span className="small faint" style={{ display: 'block' }}>
                  {r.description || summarizePermissions(r.permissions)}
                </span>
              </span>
            </label>
          ))}
          {roles.data && options.length === 0 && <p className="muted" style={{ margin: 0 }}>No roles apply to this kind of object.</p>}
        </div>
      </fieldset>
      <FormError error={error} />
    </Dialog>
  )
}

/** "view, change, use project" style summary of a permission list. */
export function summarizePermissions(perms: string[]): string {
  const verbs = new Map<string, string[]>()
  for (const p of perms) {
    const [verb, ...rest] = p.replace(/^[a-z]+\./, '').split('_')
    const obj = rest.join('_')
    verbs.set(obj, [...(verbs.get(obj) ?? []), verb])
  }
  return [...verbs.entries()].map(([o, v]) => `${v.join(', ')} ${typeName(o).toLowerCase()}`).join('; ')
}

/** Roles held by one user or team across all objects, with revoke and grant. */
export function PrincipalRoles({ kind, id, name }: { kind: 'user' | 'team'; id: number; name: string }) {
  const list = useQuery({
    queryKey: [`role_${kind}_assignments/`, kind, id],
    queryFn: () => getAll<Assignment>(`role_${kind}_assignments/`, { [kind]: id, order_by: 'content_type' }),
  })
  const roles = useRoleDefinitions()
  const byId = useMemo(() => new Map((roles.data ?? []).map((r) => [r.id, r])), [roles.data])
  const [grant, setGrant] = useState(false)
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const revoke = async (a: Assignment) => {
    const role = a.summary_fields.role_definition?.name ?? 'role'
    const what = a.summary_fields.content_object?.name ?? 'the system'
    if (!(await confirm({ title: `Remove ${role}?`, body: <p style={{ margin: 0 }}>{name} will lose {role} on {what}.</p>, confirm: 'Remove', danger: true }))) return
    try {
      await api.del(`role_${kind}_assignments/${a.id}/`)
      invalidate(`role_${kind}_assignments`, 'role_access')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  const rows = list.data ?? []
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row">
        <span className="small faint grow">
          {kind === 'user' ? 'Direct grants only. Roles of the teams this user belongs to also apply.' : 'Every member of this team holds these roles.'}
        </span>
        <button className="btn primary" onClick={() => setGrant(true)}>
          <Icon name="plus" size={14} />
          Grant a role
        </button>
      </div>
      <div className="table-wrap">
        <table className="data">
          <thead>
            <tr>
              <th scope="col">Role</th>
              <th scope="col">On</th>
              <th scope="col">Type</th>
              <th scope="col">Granted</th>
              <th scope="col" className="end">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => {
              const path = a.object_id ? objectPath(a.content_type, a.object_id) : null
              const rd = byId.get(a.role_definition)
              return (
                <tr key={a.id}>
                  <td>
                    <span title={rd?.description}>{roleShort(a.summary_fields.role_definition?.name ?? rd?.name ?? '?', a.content_type)}</span>
                  </td>
                  <td>
                    {a.object_id ? (
                      path ? (
                        <Link to={path} className="cell-title">
                          {a.summary_fields.content_object?.name ?? `#${a.object_id}`}
                        </Link>
                      ) : (
                        (a.summary_fields.content_object?.name ?? `#${a.object_id}`)
                      )
                    ) : (
                      <span className="muted">Everything (system-wide)</span>
                    )}
                  </td>
                  <td className="small faint">{typeName(a.content_type)}</td>
                  <td className="small faint nowrap">
                    <Ago at={a.created} />
                  </td>
                  <td className="end">
                    {a.summary_fields.user_capabilities?.delete !== false && (
                      <button className="btn sm ghost" onClick={() => revoke(a)}>
                        Remove
                      </button>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
        {list.isPending && <Loading rows={4} />}
        {list.data && rows.length === 0 && <Empty title="No roles yet">{name} has no direct role assignments.</Empty>}
      </div>
      {grant && (
        <PrincipalGrantDialog
          kind={kind}
          principal={{ id, name }}
          onClose={() => setGrant(false)}
          onDone={() => {
            setGrant(false)
            invalidate(`role_${kind}_assignments`, 'role_access')
          }}
        />
      )}
    </div>
  )
}

/** Principal-centric grant: pick an object type, objects and roles for one user or team. */
function PrincipalGrantDialog({ kind, principal, onClose, onDone }: { kind: 'user' | 'team'; principal: { id: number; name: string }; onClose: () => void; onDone: () => void }) {
  const roles = useRoleDefinitions()
  const [type, setType] = useState(grantable[0])
  const [objects, setObjects] = useState<Ref[]>([])
  const [picked, setPicked] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const toast = useToast()
  const options = (roles.data ?? []).filter((r) => bareType(r.content_type) === type.ct)
  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      for (const o of objects) for (const rd of picked) await api.post(`role_${kind}_assignments/`, { role_definition: rd, [kind]: principal.id, object_id: o.id })
      toast(`Granted to ${principal.name}`)
      onDone()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={`Grant a role to ${principal.name}`}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !objects.length || !picked.size} onClick={submit}>
            {busy ? 'Granting…' : 'Grant'}
          </button>
        </>
      }
    >
      <div className="field">
        <label htmlFor="grant-type">On</label>
        <select
          id="grant-type"
          className="select"
          value={type.resource}
          onChange={(e) => {
            setType(grantable.find((g) => g.resource === e.target.value) ?? grantable[0])
            setObjects([])
            setPicked(new Set())
          }}
        >
          {grantable.map((g) => (
            <option key={g.resource} value={g.resource}>
              {g.label}
            </option>
          ))}
        </select>
      </div>
      <MultiLookup
        key={type.resource}
        label={`${type.label}s`}
        resource={`${type.resource}/`}
        values={objects}
        onChange={setObjects}
        meta={(r) => r.summary_fields?.organization?.name}
        full
      />
      <fieldset>
        <legend>Roles</legend>
        <div className="stack" style={{ gap: 6 }}>
          {options.map((r) => (
            <label key={r.id} className="role-option">
              <input
                type="checkbox"
                checked={picked.has(r.id)}
                onChange={(e) => {
                  const n = new Set(picked)
                  if (e.target.checked) n.add(r.id)
                  else n.delete(r.id)
                  setPicked(n)
                }}
              />
              <span className="grow">
                <span style={{ fontWeight: 500 }}>{roleShort(r.name, type.ct)}</span>
                <span className="small faint" style={{ display: 'block' }}>
                  {r.description || summarizePermissions(r.permissions)}
                </span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      <FormError error={error} />
    </Dialog>
  )
}
