import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, getAll } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Me, Ref } from '../../api/types'
import { usePreselectOrg, useSession } from '../../app/context'
import { PrincipalRoles, useRoleDefinitions, type Assignment } from '../../components/access/Access'
import { Check, FormError, Lookup, MultiLookup, SelectField, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, useConfirm, useToast } from '../../components/overlay'
import { DataTable } from '../../components/table'
import { Ago, Avatar, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import { absolute, userName } from '../../lib/format'
import { checkPassword, describeRules, passwordRules } from '../../lib/passwordPolicy'
import { ListPage, OneTimeSecret, useDelete } from './common'

type User = Base & Me & { last_login: string | null; is_active?: boolean; ldap_dn?: string; external_account?: string | null; auth?: unknown[] }

function Kind({ u }: { u: User }) {
  if (u.is_superuser) return <span className="badge run">Superuser</span>
  if (u.is_system_auditor) return <span className="badge">Auditor</span>
  return null
}

export function UserList() {
  const nav = useNavigate()
  return (
    <ListPage<User>
      title="Users"
      subtitle="People and service accounts. Single sign-on users appear after their first login."
      path="users/"
      order="username"
      keys={{
        superuser: { field: 'is_superuser', help: 'true/false' },
        auditor: { field: 'is_system_auditor', help: 'true/false' },
        email: { field: 'email', help: 'email address' },
      }}
      placeholder="Filter by name, username or email…  try  superuser:true"
      actions={
        <Link to="/users/new" className="btn primary">
          <Icon name="plus" size={14} />
          New user
        </Link>
      }
      onOpen={(u) => nav(`/users/${u.id}`)}
      columns={[
        {
          key: 'name',
          header: 'Person',
          sort: 'username',
          render: (u) => (
            <div className="row">
              <Avatar name={userName(u)} size="sm" />
              <div>
                <Link to={`/users/${u.id}`} className="cell-title">
                  {userName(u)}
                </Link>
                <div className="small faint">{u.username}</div>
              </div>
            </div>
          ),
        },
        { key: 'email', header: 'Email', sort: 'email', render: (u) => u.email || <span className="faint">—</span> },
        { key: 'kind', header: 'Type', render: (u) => <Kind u={u} /> },
        {
          key: 'login',
          header: 'Last sign-in',
          sort: 'last_login',
          className: 'small nowrap',
          render: (u) => (u.last_login ? <Ago at={u.last_login} /> : <span className="faint">Never</span>),
        },
      ]}
      empty={<Empty title="No users">Create local accounts here, or configure single sign-on in Settings.</Empty>}
    />
  )
}

interface UserValues extends Record<string, unknown> {
  username: string
  first_name: string
  last_name: string
  email: string
  password: string
  confirm: string
  kind: 'normal' | 'auditor' | 'superuser'
  is_active: boolean
  organization: Ref | null
}

function UserForm({ user }: { user?: User }) {
  const nav = useNavigate()
  const { me } = useSession()
  const invalidate = useInvalidate()
  const f = useForm<UserValues>({
    username: user?.username ?? '',
    first_name: user?.first_name ?? '',
    last_name: user?.last_name ?? '',
    email: user?.email ?? '',
    password: '',
    confirm: '',
    kind: user?.is_superuser ? 'superuser' : user?.is_system_auditor ? 'auditor' : 'normal',
    is_active: user?.is_active ?? true,
    organization: null,
  })
  // As in AWX, organization admins create users in an organization they
  // administer (the user joins it); superusers may leave it empty.
  const needsOrg = !user && !me.is_superuser
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), needsOrg)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const mismatch = f.values.password !== f.values.confirm
  // The password rules are readable by every user (LOCAL_PASSWORD_MIN_*).
  const authSettings = useQuery({ queryKey: ['settings', 'authentication'], queryFn: () => api.get<Record<string, unknown>>('settings/authentication/'), staleTime: 60_000 })
  const rules = passwordRules(authSettings.data)
  const weak = f.values.password ? checkPassword(f.values.password, rules) : null
  const rulesText = describeRules(rules)
  const save = async () => {
    setBusy(true)
    setError(null)
    const body: Record<string, unknown> = {
      username: f.values.username,
      first_name: f.values.first_name,
      last_name: f.values.last_name,
      email: f.values.email,
      is_active: f.values.is_active,
    }
    if (me.is_superuser) {
      body.is_superuser = f.values.kind === 'superuser'
      body.is_system_auditor = f.values.kind === 'auditor'
    }
    if (f.values.password) body.password = f.values.password
    try {
      const org = f.values.organization
      const saved = user
        ? await api.patch<User>(`users/${user.id}/`, body)
        : await api.post<User>(org ? `organizations/${org.id}/users/` : 'users/', body)
      invalidate('users', 'me')
      nav(`/users/${saved.id}`)
    } catch (e) {
      setError(e)
      if (e && typeof e === 'object' && 'fields' in e) f.setErrors((e as { fields: Record<string, string> }).fields)
    } finally {
      setBusy(false)
    }
  }
  const external = !!user?.external_account || (Array.isArray(user?.auth) && user.auth.length > 0)
  return (
    <form
      className="form"
      style={{ maxWidth: 860 }}
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput label="Username" value={f.values.username} onChange={(v) => f.set('username', v)} error={f.errors.username} autoFocus={!user} disabled={external} />
        <TextInput label="Email" optional type="email" value={f.values.email} onChange={(v) => f.set('email', v)} error={f.errors.email} />
        <TextInput label="First name" optional value={f.values.first_name} onChange={(v) => f.set('first_name', v)} />
        <TextInput label="Last name" optional value={f.values.last_name} onChange={(v) => f.set('last_name', v)} />
        {!external && (
          <>
            <TextInput
              label={user ? 'New password' : 'Password'}
              optional={!!user}
              type="password"
              value={f.values.password}
              onChange={(v) => f.set('password', v)}
              error={f.errors.password ?? weak ?? undefined}
              help={[user ? 'Leave empty to keep the current password.' : '', rulesText].filter(Boolean).join(' ') || undefined}
            />
            <TextInput label="Confirm password" type="password" optional={!!user} value={f.values.confirm} onChange={(v) => f.set('confirm', v)} error={mismatch && f.values.confirm ? 'The passwords differ.' : undefined} />
          </>
        )}
        {!user && (
          <Lookup
            label="Organization"
            optional={me.is_superuser}
            resource="organizations/"
            params={me.is_superuser ? undefined : { role_level: 'admin_role' }}
            value={f.values.organization}
            onChange={(v) => f.set('organization', v)}
            help="The user becomes a member of it."
          />
        )}
        {me.is_superuser && (
          <SelectField
            label="Type"
            value={f.values.kind}
            onChange={(v) => f.set('kind', v as UserValues['kind'])}
            options={[
              ['normal', 'Normal — access through roles'],
              ['auditor', 'System auditor — can see everything'],
              ['superuser', 'Superuser — can do everything'],
            ]}
          />
        )}
      </div>
      {user && <Check label="Active" checked={f.values.is_active} onChange={(v) => f.set('is_active', v)} help="Inactive users cannot sign in; their history is kept." />}
      {external && <div className="notice">This account signs in through single sign-on; its username and password are managed by the identity provider.</div>}
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.username.trim() || mismatch || !!weak || (!user && !f.values.password && !external) || (needsOrg && !f.values.organization)}>
          {busy ? 'Saving…' : user ? 'Save changes' : 'Create user'}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(user ? `/users/${user.id}` : '/users')}>
          Cancel
        </button>
      </div>
    </form>
  )
}

export function UserNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: 'Users', to: '/users' }, { label: 'New' }]} title="New user" />
      <div className="page-body">
        <UserForm />
      </div>
    </div>
  )
}

export function UserDetail() {
  const id = Number(useParams().id)
  const { me } = useSession()
  const user = useObject<User>(`users/${id}/`)
  const del = useDelete()
  if (user.error) return <div className="page-body"><ErrorNote error={user.error} /></div>
  if (!user.data) return <Loading />
  const u = user.data
  const base = `/users/${id}`
  const self = me.id === id
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: 'Users', to: '/users' }, { label: u.username }]}
        title={
          <span className="row" style={{ gap: 10 }}>
            <Avatar name={userName(u)} />
            {userName(u)}
          </span>
        }
        badge={<Kind u={u} />}
        subtitle={
          <>
            <span className="mono">{u.username}</span>
            {u.email ? ` · ${u.email}` : ''}
            {self ? ' · this is you' : ''}
          </>
        }
        actions={
          <>
            {u.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/edit`}>
                Edit
              </Link>
            )}
            {u.summary_fields.user_capabilities?.delete && !self && (
              <button className="btn danger" onClick={() => del({ path: `users/${id}/`, name: u.username, what: 'user', then: '/users' })}>
                Delete
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              Details
            </Tab>
            <Tab to={`${base}/teams`}>Teams</Tab>
            <Tab to={`${base}/roles`}>Roles</Tab>
            <Tab to={`${base}/tokens`}>Tokens</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route
            index
            element={
              <Panel title="Account">
                <KV
                  items={[
                    ['Username', <span className="mono">{u.username}</span>],
                    ['Name', [u.first_name, u.last_name].filter(Boolean).join(' ')],
                    ['Email', u.email],
                    ['Type', u.is_superuser ? 'Superuser' : u.is_system_auditor ? 'System auditor' : 'Normal'],
                    ['Status', u.is_active === false ? 'Inactive' : 'Active'],
                    ['Sign-in', u.external_account || (Array.isArray(u.auth) && u.auth.length) ? 'Single sign-on' : 'Local password'],
                    ['Last sign-in', u.last_login ? <span title={absolute(u.last_login)}><Ago at={u.last_login} /></span> : <span className="faint">Never</span>],
                    ['Created', <Ago at={u.created} />],
                  ]}
                />
              </Panel>
            }
          />
          <Route path="edit" element={<UserForm user={u} />} />
          <Route path="teams" element={<UserTeams user={u} />} />
          <Route path="roles" element={<PrincipalRoles kind="user" id={id} name={userName(u)} />} />
          <Route path="tokens" element={<Tokens user={u} self={self} />} />
        </Routes>
      </div>
    </div>
  )
}

function UserTeams({ user }: { user: User }) {
  const list = useQuery({
    queryKey: ['role_user_assignments/', 'user-teams', user.id],
    queryFn: () => getAll<Assignment>('role_user_assignments/', { user: user.id, content_type: 'team' }),
  })
  const roles = useRoleDefinitions()
  const [adding, setAdding] = useState(false)
  const [picked, setPicked] = useState<Ref[]>([])
  const invalidate = useInvalidate()
  const toast = useToast()
  const confirm = useConfirm()
  const member = roles.data?.find((r) => r.name === 'Team Member')
  const add = async () => {
    try {
      for (const t of picked) await api.post('role_user_assignments/', { role_definition: member?.id, user: user.id, object_id: t.id })
      invalidate('role_user_assignments', 'role_access')
      setAdding(false)
      setPicked([])
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row">
        <span className="small faint grow">{userName(user)} gets every role granted to these teams.</span>
        <button className="btn primary" onClick={() => setAdding(true)}>
          <Icon name="plus" size={14} />
          Add to teams
        </button>
      </div>
      <DataTable
        label="Teams"
        rows={list.data}
        loading={list.isPending}
        error={list.error}
        empty={<Empty title="Not in any team" />}
        columns={[
          {
            key: 'team',
            header: 'Team',
            render: (a) => (
              <Link to={`/teams/${a.object_id}`} className="cell-title">
                {a.summary_fields.content_object?.name}
              </Link>
            ),
          },
          { key: 'role', header: 'Role', render: (a) => (a.summary_fields.role_definition?.name ?? '').replace(/^Team /, '') },
          { key: 'since', header: 'Since', className: 'small faint nowrap', render: (a) => <Ago at={a.created} /> },
          {
            key: 'x',
            header: <span className="sr-only">Actions</span>,
            className: 'end',
            render: (a) => (
              <button
                className="btn sm ghost"
                onClick={async () => {
                  if (!(await confirm({ title: `Remove from ${a.summary_fields.content_object?.name}?`, confirm: 'Remove', danger: true }))) return
                  await api.del(`role_user_assignments/${a.id}/`).catch((e) => toast((e as Error).message, 'error'))
                  invalidate('role_user_assignments', 'role_access')
                }}
              >
                Remove
              </button>
            ),
          },
        ]}
      />
      {adding && (
        <Dialog
          title={`Add ${userName(user)} to teams`}
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
          <MultiLookup label="Teams" resource="teams/" values={picked} onChange={setPicked} meta={(r) => r.summary_fields?.organization?.name} full />
        </Dialog>
      )}
    </div>
  )
}

type Token = Base & { scope: string; expires: string; token: string; user: number }

function Tokens({ user, self }: { user: User; self: boolean }) {
  const tokens = useList<Token>(`users/${user.id}/personal_tokens/`, { order_by: '-created', page_size: 50 })
  const [creating, setCreating] = useState(false)
  const [desc, setDesc] = useState('')
  const [scope, setScope] = useState('write')
  const [created, setCreated] = useState<Token | null>(null)
  const [error, setError] = useState<unknown>(null)
  const invalidate = useInvalidate()
  const del = useDelete()
  const create = async () => {
    setError(null)
    try {
      const t = await api.post<Token>('tokens/', { description: desc, scope })
      setCreated(t)
      setDesc('')
      invalidate(`users/${user.id}/personal_tokens`)
    } catch (e) {
      setError(e)
    }
  }
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row">
        <span className="small faint grow">Personal access tokens authenticate scripts and CI as {self ? 'you' : userName(user)}: send <span className="mono">Authorization: Bearer &lt;token&gt;</span>.</span>
        {self && (
          <button className="btn primary" onClick={() => setCreating(true)}>
            <Icon name="plus" size={14} />
            New token
          </button>
        )}
      </div>
      <DataTable
        label="Tokens"
        rows={tokens.data?.results}
        loading={tokens.isPending}
        error={tokens.error}
        empty={<Empty title="No tokens">{self ? 'Create one for scripts that call the API.' : 'Only the user can create tokens for themselves.'}</Empty>}
        columns={[
          { key: 'desc', header: 'Description', render: (t) => t.description || <span className="faint">No description</span> },
          { key: 'scope', header: 'Scope', render: (t) => <span className="tag">{t.scope}</span> },
          { key: 'created', header: 'Created', className: 'small nowrap', render: (t) => <Ago at={t.created} /> },
          {
            key: 'x',
            header: <span className="sr-only">Actions</span>,
            className: 'end',
            render: (t) =>
              t.summary_fields.user_capabilities?.delete !== false && (
                <button className="btn sm ghost" onClick={() => del({ path: `tokens/${t.id}/`, name: t.description || `token ${t.id}`, what: 'token', invalidate: [`users/${user.id}/personal_tokens`], body: <p style={{ margin: 0 }}>Scripts using it stop working immediately.</p> })}>
                  Revoke
                </button>
              ),
          },
        ]}
      />
      {creating && (
        <Dialog
          title={created ? 'Token created' : 'New personal token'}
          onClose={() => {
            setCreating(false)
            setCreated(null)
          }}
          footer={
            created ? (
              <button
                className="btn primary"
                onClick={() => {
                  setCreating(false)
                  setCreated(null)
                }}
              >
                Done
              </button>
            ) : (
              <>
                <button className="btn" onClick={() => setCreating(false)}>
                  Cancel
                </button>
                <button className="btn primary" onClick={create}>
                  Create token
                </button>
              </>
            )
          }
        >
          {created ? (
            <OneTimeSecret value={created.token} note="Copy the token now. It is shown only once; afterwards only its description is visible." />
          ) : (
            <>
              <TextInput label="Description" value={desc} onChange={setDesc} placeholder="e.g. GitLab CI deploy pipeline" autoFocus />
              <SelectField
                label="Scope"
                value={scope}
                onChange={setScope}
                options={[
                  ['write', 'Write — everything you can do'],
                  ['read', 'Read — view only'],
                ]}
              />
              <FormError error={error} />
            </>
          )}
        </Dialog>
      )}
    </div>
  )
}
