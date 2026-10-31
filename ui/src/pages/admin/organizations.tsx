import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, getAll } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { useSession } from '../../app/context'
import { AccessTab, roleShort, useRoleDefinitions, type Assignment } from '../../components/access/Access'
import { FormError, Lookup, MultiLookup, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, useConfirm, useToast } from '../../components/overlay'
import { DataTable } from '../../components/table'
import { Ago, Avatar, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { userName } from '../../lib/format'
import { ListPage, useDelete } from './common'
import { NotificationsTab } from '../../components/NotificationsTab'
import { tr, trn } from '../../lib/i18n'

type Org = Base & { max_hosts: number; default_environment: number | null }

export function OrganizationList() {
  const nav = useNavigate()
  const { me } = useSession()
  return (
    <ListPage<Org>
      title={tr('Organizations')}
      subtitle={tr('Top-level boundaries for content, people and quotas.')}
      path="organizations/"
      placeholder={tr('Filter organizations…')}
      actions={
        me.is_superuser && (
          <Link to="/organizations/new" className="btn primary">
            <Icon name="plus" size={14} />
            {tr('New organization')}
          </Link>
        )
      }
      onOpen={(o) => nav(`/organizations/${o.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Organization'),
          sort: 'name',
          render: (o) => (
            <div>
              <Link to={`/organizations/${o.id}`} className="cell-title">
                {o.name}
              </Link>
              {o.description && <div className="small faint">{o.description}</div>}
            </div>
          ),
        },
        ...(['teams', 'users', 'job_templates', 'inventories', 'projects'] as const).map((k) => ({
          key: k,
          header: { teams: tr('Teams'), users: tr('Members'), job_templates: tr('Templates'), inventories: tr('Inventories'), projects: tr('Projects') }[k],
          className: 'num',
          render: (o: Org) => o.summary_fields.related_field_counts?.[k] ?? 0,
        })),
        { key: 'modified', header: tr('Changed'), sort: 'modified', className: 'small faint nowrap', render: (o) => <Ago at={o.modified} /> },
      ]}
      empty={<Empty title={tr('No organizations')}>{tr('Create one to start grouping templates, inventories and teams.')}</Empty>}
    />
  )
}

interface OrgValues extends Record<string, unknown> {
  name: string
  description: string
  max_hosts: string
  default_environment: Ref | null
  galaxy: Ref[]
  groups: Ref[]
}

/** Replaces the members of an association sublist, keeping order when it matters. */
export async function syncLinks(path: string, before: number[], after: number[], ordered = false) {
  const same = before.length === after.length && before.every((x, i) => x === after[i])
  if (same) return
  if (ordered) {
    for (const id of before) await api.post(path, { id, disassociate: true })
    for (const id of after) await api.post(path, { id })
    return
  }
  for (const id of before.filter((x) => !after.includes(x))) await api.post(path, { id, disassociate: true })
  for (const id of after.filter((x) => !before.includes(x))) await api.post(path, { id })
}

function OrgForm({ org, galaxy, groups, ee }: { org?: Org; galaxy?: Ref[]; groups?: Ref[]; ee?: Ref | null }) {
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const toast = useToast()
  const f = useForm<OrgValues>({
    name: org?.name ?? '',
    description: org?.description ?? '',
    max_hosts: String(org?.max_hosts ?? 0),
    default_environment: ee ?? null,
    galaxy: galaxy ?? [],
    groups: groups ?? [],
  })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    setError(null)
    const body = {
      name: f.values.name,
      description: f.values.description,
      max_hosts: Number(f.values.max_hosts) || 0,
      default_environment: f.values.default_environment?.id ?? null,
    }
    try {
      const saved = org ? await api.patch<Org>(`organizations/${org.id}/`, body) : await api.post<Org>('organizations/', body)
      await syncLinks(`organizations/${saved.id}/galaxy_credentials/`, (galaxy ?? []).map((g) => g.id), f.values.galaxy.map((g) => g.id), true)
      await syncLinks(`organizations/${saved.id}/instance_groups/`, (groups ?? []).map((g) => g.id), f.values.groups.map((g) => g.id), true)
      invalidate('organizations')
      toast(org ? tr('Saved') : tr('Created {name}', { name: saved.name }))
      nav(`/organizations/${saved.id}`)
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
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput label={tr('Name')} value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!org} />
        <TextInput
          label={tr('Host limit')}
          type="number"
          value={f.values.max_hosts}
          onChange={(v) => f.set('max_hosts', v)}
          error={f.errors.max_hosts}
          help={tr('Hosts this organization may manage. 0 means no limit.')}
        />
        <TextArea label={tr('Description')} optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
        <Lookup
          label={tr('Default execution environment')}
          optional
          resource="execution_environments/"
          value={f.values.default_environment}
          onChange={(v) => f.set('default_environment', v)}
          meta={(r) => r.image}
          help={tr('Used by templates and projects of this organization that do not pick one.')}
          error={f.errors.default_environment}
        />
        <MultiLookup
          label={tr('Galaxy credentials')}
          optional
          resource="credentials/"
          params={{ credential_type__kind: 'galaxy' }}
          values={f.values.galaxy}
          onChange={(v) => f.set('galaxy', v)}
          help={tr('Content sources for roles and collections, tried in this order.')}
        />
        <MultiLookup
          label={tr('Instance groups')}
          optional
          resource="instance_groups/"
          values={f.values.groups}
          onChange={(v) => f.set('groups', v)}
          help={tr('Preferred capacity for this organization\'s jobs, in order.')}
        />
      </div>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim()}>
          {busy ? tr('Saving…') : org ? tr('Save changes') : tr('Create organization')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(org ? `/organizations/${org.id}` : '/organizations')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}

export function OrganizationNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Organizations'), to: '/organizations' }, { label: tr('New') }]} title={tr('New organization')} />
      <div className="page-body" style={{ maxWidth: 980 }}>
        <OrgForm />
      </div>
    </div>
  )
}

export function OrganizationDetail() {
  const id = Number(useParams().id)
  const org = useObject<Org>(`organizations/${id}/`)
  const galaxy = useList<Ref>(`organizations/${id}/galaxy_credentials/`, { page_size: 50 })
  const groups = useList<Ref>(`organizations/${id}/instance_groups/`, { page_size: 50 })
  const ee = useObject<Ref>(org.data?.default_environment ? `execution_environments/${org.data.default_environment}/` : null)
  const del = useDelete()
  if (org.error) return <div className="page-body"><ErrorNote error={org.error} /></div>
  if (!org.data) return <Loading />
  const o = org.data
  const counts = o.summary_fields.related_field_counts ?? {}
  const base = `/organizations/${id}`
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Organizations'), to: '/organizations' }, { label: o.name }]}
        title={o.name}
        subtitle={o.description || undefined}
        actions={
          <>
            {o.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {o.summary_fields.user_capabilities?.delete && (
              <button
                className="btn danger"
                onClick={() =>
                  del({
                    path: `organizations/${id}/`,
                    name: o.name,
                    what: tr('organization'),
                    then: '/organizations',
                    body: <p style={{ margin: 0 }}>{tr('Its teams, inventories, projects, templates and credentials are deleted with it.')}</p>,
                  })
                }
              >
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
            <Tab to={`${base}/members`} n={counts.users}>
              {tr('Members')}
            </Tab>
            <Tab to={`${base}/teams`} n={counts.teams}>
              {tr('Teams')}
            </Tab>
            <Tab to={`${base}/notifications`}>{tr('Notifications')}</Tab>
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
                  <Panel title={tr('Settings')}>
                    <KV
                      items={[
                        [tr('Host limit'), o.max_hosts ? o.max_hosts : tr('No limit')],
                        [tr('Default environment'), ee.data ? <Link to={`/execution-environments/${ee.data.id}`}>{ee.data.name}</Link> : <span className="faint">{tr('Global default')}</span>],
                        [
                          tr('Galaxy credentials'),
                          galaxy.data?.results.length ? galaxy.data.results.map((g, i) => <span key={g.id}>{i > 0 && ', '}<Link to={`/credentials/${g.id}`}>{g.name}</Link></span>) : <span className="faint">{tr('None')}</span>,
                        ],
                        [
                          tr('Instance groups'),
                          groups.data?.results.length ? groups.data.results.map((g, i) => <span key={g.id}>{i > 0 && ' → '}<Link to={`/instance-groups/${g.id}`}>{g.name}</Link></span>) : <span className="faint">{tr('Any available')}</span>,
                        ],
                        [tr('Created'), <><Ago at={o.created} /> {tr('by')} {userName(o.summary_fields.created_by)}</>],
                        [tr('Changed'), <><Ago at={o.modified} /> {tr('by')} {userName(o.summary_fields.modified_by)}</>],
                      ]}
                    />
                  </Panel>
                </div>
                <div className="narrow">
                  <Panel title={tr('Contents')}>
                    <ul className="list-plain">
                      {(
                        [
                          ['job_templates', tr('Job templates'), `/templates?org=${id}`],
                          ['inventories', tr('Inventories'), '/inventories'],
                          ['projects', tr('Projects'), '/projects'],
                          ['teams', tr('Teams'), `${base}/teams`],
                          ['users', tr('Members'), `${base}/members`],
                        ] as const
                      ).map(([k, l, to]) => (
                        <li key={k}>
                          <Link to={to}>{l}</Link>
                          <span className="mono">{counts[k] ?? 0}</span>
                        </li>
                      ))}
                    </ul>
                  </Panel>
                </div>
              </div>
            }
          />
          <Route
            path="edit"
            element={
              galaxy.data && groups.data && (!o.default_environment || ee.data) ? (
                <div style={{ maxWidth: 980 }}>
                  <OrgForm org={o} galaxy={galaxy.data.results} groups={groups.data.results} ee={ee.data ?? null} />
                </div>
              ) : (
                <Loading />
              )
            }
          />
          <Route path="members" element={<OrgMembers org={o} />} />
          <Route path="teams" element={<OrgTeams org={o} />} />
          <Route path="notifications" element={<NotificationsTab resource="organizations" id={o.id} approvals editable={!!o.summary_fields.user_capabilities?.edit} />} />
          <Route path="access" element={<AccessTab resource="organizations" id={id} name={o.name} />} />
        </Routes>
      </div>
    </div>
  )
}

/** Users holding a role on the organization, one row per user. */
function OrgMembers({ org }: { org: Org }) {
  const roles = useRoleDefinitions()
  const assignments = useQuery({
    queryKey: ['role_user_assignments/', 'org', org.id],
    queryFn: () => getAll<Assignment>('role_user_assignments/', { content_type: 'organization', object_id: org.id }),
  })
  const [adding, setAdding] = useState(false)
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const people = useMemo(() => {
    const by = new Map<number, { user: NonNullable<Assignment['summary_fields']['user']>; list: Assignment[] }>()
    for (const a of assignments.data ?? []) {
      const u = a.summary_fields.user
      if (!u) continue
      const e = by.get(u.id) ?? { user: u, list: [] }
      e.list.push(a)
      by.set(u.id, e)
    }
    return [...by.values()].sort((a, b) => userName(a.user).localeCompare(userName(b.user)))
  }, [assignments.data])
  const remove = async (p: (typeof people)[number]) => {
    if (!(await confirm({ title: tr('Remove {user} from {name}?', { user: userName(p.user), name: org.name }), body: <p style={{ margin: 0 }}>{tr('All of their organization roles are removed. Roles on individual objects stay.')}</p>, confirm: tr('Remove member'), danger: true }))) return
    try {
      for (const a of p.list) await api.del(`role_user_assignments/${a.id}/`)
      invalidate('role_user_assignments', 'organizations', 'role_access')
      toast(tr('Removed {user}', { user: userName(p.user) }))
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row">
        <span className="small faint grow">{tr('People with a role on the whole organization. Members can see it; admins manage everything in it.')}</span>
        <button className="btn primary" onClick={() => setAdding(true)}>
          <Icon name="plus" size={14} />
          {tr('Add members')}
        </button>
      </div>
      <DataTable
        label={tr('Members')}
        rows={people}
        rowKey={(p) => p.user.id}
        loading={assignments.isPending || roles.isPending}
        error={assignments.error}
        empty={<Empty title={tr('No members yet')}>{tr('Add people, or grant roles to teams under Access.')}</Empty>}
        columns={[
          {
            key: 'who',
            header: tr('Person'),
            render: (p) => (
              <div className="row">
                <Avatar name={userName(p.user)} size="sm" />
                <div>
                  <Link to={`/users/${p.user.id}`} className="cell-title">
                    {userName(p.user)}
                  </Link>
                  <div className="small faint">{p.user.username}</div>
                </div>
              </div>
            ),
          },
          {
            key: 'roles',
            header: tr('Roles'),
            render: (p) => (
              <div className="row wrap" style={{ gap: 4 }}>
                {p.list.map((a) => (
                  <span key={a.id} className="tag">
                    {roleShort(a.summary_fields.role_definition?.name ?? '', 'organization')}
                  </span>
                ))}
              </div>
            ),
          },
          {
            key: 'x',
            header: <span className="sr-only">{tr('Actions')}</span>,
            className: 'end',
            render: (p) => (
              <button className="btn sm ghost" onClick={() => remove(p)}>
                {tr('Remove')}
              </button>
            ),
          },
        ]}
      />
      {adding && <AddMembers org={org} onClose={() => setAdding(false)} />}
    </div>
  )
}

function AddMembers({ org, onClose }: { org: Org; onClose: () => void }) {
  const roles = useRoleDefinitions()
  const [users, setUsers] = useState<Ref[]>([])
  const options = (roles.data ?? []).filter((r) => r.content_type?.endsWith('organization'))
  const [role, setRole] = useState<number | null>(null)
  const member = options.find((r) => r.name === tr('Organization Member'))
  const chosen = role ?? member?.id ?? null
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const toast = useToast()
  const submit = async () => {
    setBusy(true)
    try {
      for (const u of users) await api.post('role_user_assignments/', { role_definition: chosen, user: u.id, object_id: org.id })
      invalidate('role_user_assignments', 'organizations', 'role_access')
      toast(trn(users.length, 'Added {n} person to {name}', 'Added {n} people to {name}', { name: org.name }))
      onClose()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={tr('Add members to {name}', { name: org.name })}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={busy || !users.length || !chosen} onClick={submit}>
            {tr('Add')}
          </button>
        </>
      }
    >
      <MultiLookup
        label={tr('People')}
        resource="users/"
        order="username"
        values={users}
        onChange={setUsers}
        display={(r) => [r.first_name, r.last_name].filter(Boolean).join(' ') || r.username}
        meta={(r) => r.username}
        full
      />
      <div className="field">
        <label htmlFor="org-role">{tr('Role')}</label>
        <select id="org-role" className="select" value={chosen ?? ''} onChange={(e) => setRole(Number(e.target.value))}>
          {options.map((r) => (
            <option key={r.id} value={r.id}>
              {roleShort(r.name, 'organization')} — {r.description}
            </option>
          ))}
        </select>
      </div>
      <FormError error={error} />
    </Dialog>
  )
}

function OrgTeams({ org }: { org: Org }) {
  const teams = useList<Base>('teams/', { organization: org.id, order_by: 'name', page_size: 100 })
  const nav = useNavigate()
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row">
        <span className="small faint grow">{tr('Teams group people so roles can be granted once.')}</span>
        <Link className="btn primary" to={`/teams/new?organization=${org.id}`}>
          <Icon name="plus" size={14} />
          {tr('New team')}
        </Link>
      </div>
      <DataTable
        label={tr('Teams')}
        rows={teams.data?.results}
        loading={teams.isPending}
        error={teams.error}
        onOpen={(t) => nav(`/teams/${t.id}`)}
        empty={<Empty title={tr('No teams in this organization')} />}
        columns={[
          {
            key: 'name',
            header: tr('Team'),
            render: (t) => (
              <div>
                <Link to={`/teams/${t.id}`} className="cell-title">
                  {t.name}
                </Link>
                {t.description && <div className="small faint">{t.description}</div>}
              </div>
            ),
          },
          { key: 'mod', header: tr('Changed'), className: 'small faint nowrap', render: (t) => <Ago at={t.modified} /> },
        ]}
      />
    </div>
  )
}
