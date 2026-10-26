import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams, useSearchParams } from 'react-router'
import { api, getAll } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { useOrg } from '../../app/context'
import { AccessTab, PrincipalRoles, useRoleDefinitions, type Assignment } from '../../components/access/Access'
import { FormError, Lookup, MultiLookup, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, useConfirm, useToast } from '../../components/overlay'
import { DataTable } from '../../components/table'
import { Ago, Avatar, Empty, ErrorNote, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { userName } from '../../lib/format'
import { ListPage, useDelete } from './common'

type Team = Base & { organization: number }

export function TeamList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Team>
      title="Teams"
      subtitle="Grant roles to a team once; every member gets them."
      path="teams/"
      params={scope}
      keys={{ org: { field: 'organization__name', help: 'organization name' } }}
      placeholder="Filter teams…  try  org:Acme*"
      actions={
        <Link to="/teams/new" className="btn primary">
          <Icon name="plus" size={14} />
          New team
        </Link>
      }
      onOpen={(t) => nav(`/teams/${t.id}`)}
      columns={[
        {
          key: 'name',
          header: 'Team',
          sort: 'name',
          render: (t) => (
            <div className="row">
              <Avatar name={t.name} size="sm" />
              <div>
                <Link to={`/teams/${t.id}`} className="cell-title">
                  {t.name}
                </Link>
                {t.description && <div className="small faint">{t.description}</div>}
              </div>
            </div>
          ),
        },
        {
          key: 'org',
          header: 'Organization',
          render: (t) => (t.summary_fields.organization ? <Link to={`/organizations/${t.organization}`}>{t.summary_fields.organization.name}</Link> : '—'),
        },
        { key: 'modified', header: 'Changed', sort: 'modified', className: 'small faint nowrap', render: (t) => <Ago at={t.modified} /> },
      ]}
      empty={<Empty title="No teams yet">Teams make access manageable: grant roles to the team, then add people.</Empty>}
    />
  )
}

function TeamForm({ team }: { team?: Team }) {
  const nav = useNavigate()
  const [sp] = useSearchParams()
  const { org } = useOrg()
  const presetOrg = sp.get('organization')
  const orgObj = useObject<Ref>(!team && presetOrg ? `organizations/${presetOrg}/` : null)
  const f = useForm<{ name: string; description: string; organization: Ref | null }>({
    name: team?.name ?? '',
    description: team?.description ?? '',
    organization: team?.summary_fields.organization ?? (presetOrg ? null : org),
  })
  const organization = f.values.organization ?? (orgObj.data && !team ? orgObj.data : null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const body = { name: f.values.name, description: f.values.description, organization: organization?.id }
      const saved = team ? await api.patch<Team>(`teams/${team.id}/`, body) : await api.post<Team>('teams/', body)
      invalidate('teams')
      nav(`/teams/${saved.id}`)
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
      style={{ maxWidth: 760 }}
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput label="Name" value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!team} />
        <Lookup label="Organization" resource="organizations/" value={organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} />
        <TextArea label="Description" optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !organization}>
          {busy ? 'Saving…' : team ? 'Save changes' : 'Create team'}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(team ? `/teams/${team.id}` : '/teams')}>
          Cancel
        </button>
      </div>
    </form>
  )
}

export function TeamNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: 'Teams', to: '/teams' }, { label: 'New' }]} title="New team" />
      <div className="page-body">
        <TeamForm />
      </div>
    </div>
  )
}

/** Holders of "Team Member" on a team: people and nested teams. */
function useTeamMembers(id: number) {
  return useQuery({
    queryKey: ['role_user_assignments/', 'team-members', id],
    queryFn: async () => {
      const [users, teams] = await Promise.all([
        getAll<Assignment>('role_user_assignments/', { content_type: 'team', object_id: id }),
        getAll<Assignment>('role_team_assignments/', { content_type: 'team', object_id: id }),
      ])
      return { users, teams }
    },
  })
}

export function TeamDetail() {
  const id = Number(useParams().id)
  const team = useObject<Team>(`teams/${id}/`)
  const members = useTeamMembers(id)
  const del = useDelete()
  if (team.error) return <div className="page-body"><ErrorNote error={team.error} /></div>
  if (!team.data) return <Loading />
  const t = team.data
  const base = `/teams/${id}`
  const memberCount = (members.data?.users.filter((a) => a.summary_fields.role_definition?.name === 'Team Member').length ?? 0) + (members.data?.teams.filter((a) => a.summary_fields.role_definition?.name === 'Team Member').length ?? 0)
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: 'Teams', to: '/teams' }, { label: t.name }]}
        title={t.name}
        subtitle={
          <>
            Team in <Link to={`/organizations/${t.organization}`}>{t.summary_fields.organization?.name}</Link>
            {t.description ? ` · ${t.description}` : ''}
          </>
        }
        actions={
          <>
            {t.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/edit`}>
                Edit
              </Link>
            )}
            {t.summary_fields.user_capabilities?.delete && (
              <button className="btn danger" onClick={() => del({ path: `teams/${id}/`, name: t.name, what: 'team', then: '/teams', body: <p style={{ margin: 0 }}>Members lose every role granted through this team.</p> })}>
                Delete
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end n={memberCount || undefined}>
              Members
            </Tab>
            <Tab to={`${base}/roles`}>Roles</Tab>
            <Tab to={`${base}/parents`}>Member of</Tab>
            <Tab to={`${base}/access`}>Access</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route index element={<TeamMembers team={t} />} />
          <Route path="edit" element={<TeamForm team={t} />} />
          <Route path="roles" element={<PrincipalRoles kind="team" id={id} name={t.name} />} />
          <Route path="parents" element={<TeamParents team={t} />} />
          <Route path="access" element={<AccessTab resource="teams" id={id} name={t.name} />} />
        </Routes>
      </div>
    </div>
  )
}

function TeamMembers({ team }: { team: Team }) {
  const members = useTeamMembers(team.id)
  const [adding, setAdding] = useState(false)
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  type Row = { key: string; kind: 'user' | 'team'; a: Assignment; name: string; sub: string; id: number; role: string }
  const rows: Row[] = [
    ...(members.data?.users ?? []).map((a) => ({
      key: 'u' + a.id,
      kind: 'user' as const,
      a,
      id: a.summary_fields.user?.id ?? 0,
      name: userName(a.summary_fields.user),
      sub: a.summary_fields.user?.username ?? '',
      role: (a.summary_fields.role_definition?.name ?? '').replace(/^Team /, ''),
    })),
    ...(members.data?.teams ?? []).map((a) => ({
      key: 't' + a.id,
      kind: 'team' as const,
      a,
      id: a.summary_fields.team?.id ?? 0,
      name: a.summary_fields.team?.name ?? '',
      sub: 'Team — all its members',
      role: (a.summary_fields.role_definition?.name ?? '').replace(/^Team /, ''),
    })),
  ].sort((x, y) => x.role.localeCompare(y.role) || x.name.localeCompare(y.name))
  const remove = async (r: Row) => {
    if (!(await confirm({ title: `Remove ${r.name}?`, body: <p style={{ margin: 0 }}>{r.name} will no longer be {r.role === 'Member' ? 'a member of' : `${r.role.toLowerCase()} on`} {team.name}.</p>, confirm: 'Remove', danger: true }))) return
    try {
      await api.del(`role_${r.kind}_assignments/${r.a.id}/`)
      invalidate('role_user_assignments', 'role_team_assignments', 'role_access')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row">
        <span className="small faint grow">Members inherit every role granted to {team.name}. Team admins can add and remove members.</span>
        <button className="btn primary" onClick={() => setAdding(true)}>
          <Icon name="plus" size={14} />
          Add members
        </button>
      </div>
      <DataTable
        label="Members"
        rows={rows}
        rowKey={(r) => r.key}
        loading={members.isPending}
        error={members.error}
        empty={<Empty title="Nobody is in this team yet" action={<button className="btn" onClick={() => setAdding(true)}>Add members</button>} />}
        columns={[
          {
            key: 'who',
            header: 'Member',
            render: (r) => (
              <div className="row">
                <Avatar name={r.name} size="sm" />
                <div>
                  <Link to={r.kind === 'user' ? `/users/${r.id}` : `/teams/${r.id}`} className="cell-title">
                    {r.name}
                  </Link>
                  <div className="small faint">{r.sub}</div>
                </div>
              </div>
            ),
          },
          { key: 'role', header: 'Role', render: (r) => (r.role === 'Member' ? 'Member' : <span className="tag">{r.role}</span>) },
          { key: 'since', header: 'Since', className: 'small faint nowrap', render: (r) => <Ago at={r.a.created} /> },
          {
            key: 'x',
            header: <span className="sr-only">Actions</span>,
            className: 'end',
            render: (r) =>
              r.a.summary_fields.user_capabilities?.delete !== false && (
                <button className="btn sm ghost" onClick={() => remove(r)}>
                  Remove
                </button>
              ),
          },
        ]}
      />
      {adding && <AddTeamMembers team={team} onClose={() => setAdding(false)} />}
    </div>
  )
}

function AddTeamMembers({ team, onClose }: { team: Team; onClose: () => void }) {
  const roles = useRoleDefinitions()
  const [users, setUsers] = useState<Ref[]>([])
  const [teams, setTeams] = useState<Ref[]>([])
  const [admin, setAdmin] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const toast = useToast()
  const role = roles.data?.find((r) => r.name === (admin ? 'Team Admin' : 'Team Member'))
  const submit = async () => {
    if (!role) return
    setBusy(true)
    try {
      for (const u of users) await api.post('role_user_assignments/', { role_definition: role.id, user: u.id, object_id: team.id })
      for (const t of teams) await api.post('role_team_assignments/', { role_definition: role.id, team: t.id, object_id: team.id })
      invalidate('role_user_assignments', 'role_team_assignments', 'role_access')
      toast(`Added ${users.length + teams.length} to ${team.name}`)
      onClose()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={`Add to ${team.name}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !role || users.length + teams.length === 0} onClick={submit}>
            Add
          </button>
        </>
      }
    >
      <MultiLookup
        label="People"
        resource="users/"
        order="username"
        values={users}
        onChange={setUsers}
        display={(r) => [r.first_name, r.last_name].filter(Boolean).join(' ') || r.username}
        meta={(r) => r.username}
        full
      />
      <MultiLookup
        label="Teams"
        optional
        resource="teams/"
        params={{ not__id: team.id }}
        values={teams}
        onChange={setTeams}
        meta={(r) => r.summary_fields?.organization?.name}
        help="Members of a nested team are members of this team too."
        full
      />
      <label className="check">
        <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} />
        <span>
          Make them team admins <span className="faint">— can manage membership</span>
        </span>
      </label>
      <FormError error={error} />
    </Dialog>
  )
}

/** Teams this team is a member of (nested teams). */
function TeamParents({ team }: { team: Team }) {
  const parents = useQuery({
    queryKey: ['role_team_assignments/', 'parents', team.id],
    queryFn: () => getAll<Assignment>('role_team_assignments/', { team: team.id, content_type: 'team' }),
  })
  const roles = useRoleDefinitions()
  const [adding, setAdding] = useState(false)
  const [picked, setPicked] = useState<Ref[]>([])
  const invalidate = useInvalidate()
  const toast = useToast()
  const member = roles.data?.find((r) => r.name === 'Team Member')
  const add = async () => {
    try {
      for (const p of picked) await api.post('role_team_assignments/', { role_definition: member?.id, team: team.id, object_id: p.id })
      invalidate('role_team_assignments', 'role_access')
      setAdding(false)
      setPicked([])
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  const rows = (parents.data ?? []).filter((a) => a.summary_fields.role_definition?.name === 'Team Member')
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row">
        <span className="small faint grow">Everyone in {team.name} is also a member of these teams and gets their roles.</span>
        <button className="btn" onClick={() => setAdding(true)}>
          <Icon name="plus" size={14} />
          Join a team
        </button>
      </div>
      <Panel pad={false}>
        {rows.length ? (
          <ul className="list-plain" style={{ padding: 14 }}>
            {rows.map((a) => (
              <li key={a.id}>
                <Link to={`/teams/${a.object_id}`}>{a.summary_fields.content_object?.name}</Link>
                <button
                  className="btn sm ghost"
                  onClick={async () => {
                    await api.del(`role_team_assignments/${a.id}/`).catch((e) => toast((e as Error).message, 'error'))
                    invalidate('role_team_assignments', 'role_access')
                  }}
                >
                  Leave
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <Empty title="Not nested in any team">{parents.isPending ? 'Loading…' : 'This team only has the roles granted to it directly.'}</Empty>
        )}
      </Panel>
      {adding && (
        <Dialog
          title={`Make ${team.name} a member of…`}
          onClose={() => setAdding(false)}
          footer={
            <>
              <button className="btn" onClick={() => setAdding(false)}>
                Cancel
              </button>
              <button className="btn primary" disabled={!picked.length || !member} onClick={add}>
                Add
              </button>
            </>
          }
        >
          <MultiLookup label="Teams" resource="teams/" params={{ not__id: team.id }} values={picked} onChange={setPicked} meta={(r) => r.summary_fields?.organization?.name} full />
        </Dialog>
      )}
    </div>
  )
}
