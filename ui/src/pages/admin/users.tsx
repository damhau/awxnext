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
import { BulkDelete } from '../../components/BulkBar'
import { tr } from '../../lib/i18n'

type User = Base & Me & { last_login: string | null; is_active?: boolean; ldap_dn?: string; external_account?: string | null; auth?: unknown[] }

function Kind({ u }: { u: User }) {
  if (u.is_superuser) return <span className="badge run">{tr('Superuser')}</span>
  if (u.is_system_auditor) return <span className="badge">{tr('Auditor')}</span>
  return null
}

export function UserList() {
  const nav = useNavigate()
  return (
    <ListPage<User>
      title={tr('Users')}
      subtitle={tr('People and service accounts. Single sign-on users appear after their first login.')}
      path="users/"
      order="username"
      keys={{
        superuser: { field: 'is_superuser', help: tr('true/false') },
        auditor: { field: 'is_system_auditor', help: tr('true/false') },
        email: { field: 'email', help: tr('email address') },
      }}
      placeholder={tr('Filter by name, username or email…  try  superuser:true')}
      actions={
        <Link to="/users/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New user')}
        </Link>
      }
      onOpen={(u) => nav(`/users/${u.id}`)}
      bulk={(sel) => <BulkDelete sel={sel} resource="users" noun={['user', 'users']} name={(u) => u.username} note={tr('Their job history stays, under their name; their tokens stop working.')} />}
      columns={[
        {
          key: 'name',
          header: tr('Person'),
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
        { key: 'email', header: tr('Email'), sort: 'email', render: (u) => u.email || <span className="faint">—</span> },
        { key: 'kind', header: tr('Type'), render: (u) => <Kind u={u} /> },
        {
          key: 'login',
          header: tr('Last sign-in'),
          sort: 'last_login',
          className: 'small nowrap',
          render: (u) => (u.last_login ? <Ago at={u.last_login} /> : <span className="faint">{tr('Never')}</span>),
        },
      ]}
      empty={<Empty title={tr('No users')}>{tr('Create local accounts here, or configure single sign-on in Settings.')}</Empty>}
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
        <TextInput label={tr('Username')} value={f.values.username} onChange={(v) => f.set('username', v)} error={f.errors.username} autoFocus={!user} disabled={external} />
        <TextInput label={tr('Email')} optional type="email" value={f.values.email} onChange={(v) => f.set('email', v)} error={f.errors.email} />
        <TextInput label={tr('First name')} optional value={f.values.first_name} onChange={(v) => f.set('first_name', v)} />
        <TextInput label={tr('Last name')} optional value={f.values.last_name} onChange={(v) => f.set('last_name', v)} />
        {!external && (
          <>
            <TextInput
              label={user ? tr('New password') : tr('Password')}
              optional={!!user}
              type="password"
              value={f.values.password}
              onChange={(v) => f.set('password', v)}
              error={f.errors.password ?? weak ?? undefined}
              help={[user ? tr('Leave empty to keep the current password.') : '', rulesText].filter(Boolean).join(' ') || undefined}
            />
            <TextInput label={tr('Confirm password')} type="password" optional={!!user} value={f.values.confirm} onChange={(v) => f.set('confirm', v)} error={mismatch && f.values.confirm ? tr('The passwords differ.') : undefined} />
          </>
        )}
        {!user && (
          <Lookup
            label={tr('Organization')}
            optional={me.is_superuser}
            resource="organizations/"
            params={me.is_superuser ? undefined : { role_level: 'admin_role' }}
            value={f.values.organization}
            onChange={(v) => f.set('organization', v)}
            help={tr('The user becomes a member of it.')}
          />
        )}
        {me.is_superuser && (
          <SelectField
            label={tr('Type')}
            value={f.values.kind}
            onChange={(v) => f.set('kind', v as UserValues['kind'])}
            options={[
              ['normal', tr('Normal — access through roles')],
              ['auditor', tr('System auditor — can see everything')],
              ['superuser', tr('Superuser — can do everything')],
            ]}
          />
        )}
      </div>
      {user && <Check label={tr('Active')} checked={f.values.is_active} onChange={(v) => f.set('is_active', v)} help={tr('Inactive users cannot sign in; their history is kept.')} />}
      {external && <div className="notice">{tr('This account signs in through single sign-on; its username and password are managed by the identity provider.')}</div>}
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.username.trim() || mismatch || !!weak || (!user && !f.values.password && !external) || (needsOrg && !f.values.organization)}>
          {busy ? tr('Saving…') : user ? tr('Save changes') : tr('Create user')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(user ? `/users/${user.id}` : '/users')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}

export function UserNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Users'), to: '/users' }, { label: tr('New') }]} title={tr('New user')} />
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
        crumbs={[{ label: tr('Users'), to: '/users' }, { label: u.username }]}
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
            {u.email ? tr(' · {email}', { email: u.email }) : ''}
            {self ? tr(' · this is you') : ''}
          </>
        }
        actions={
          <>
            {u.summary_fields.user_capabilities?.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {u.summary_fields.user_capabilities?.delete && !self && (
              <button className="btn danger" onClick={() => del({ path: `users/${id}/`, name: u.username, what: tr('user'), then: '/users' })}>
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
            <Tab to={`${base}/teams`}>{tr('Teams')}</Tab>
            <Tab to={`${base}/roles`}>{tr('Roles')}</Tab>
            <Tab to={`${base}/tokens`}>{tr('Tokens')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route
            index
            element={
              <Panel title={tr('Account')}>
                <KV
                  items={[
                    [tr('Username'), <span className="mono">{u.username}</span>],
                    [tr('Name'), [u.first_name, u.last_name].filter(Boolean).join(' ')],
                    [tr('Email'), u.email],
                    [tr('Type'), u.is_superuser ? tr('Superuser') : u.is_system_auditor ? tr('System auditor') : tr('Normal')],
                    [tr('Status'), u.is_active === false ? tr('Inactive') : tr('Active')],
                    [tr('Sign-in'), u.external_account || (Array.isArray(u.auth) && u.auth.length) ? tr('Single sign-on') : tr('Local password')],
                    [tr('Last sign-in'), u.last_login ? <span title={absolute(u.last_login)}><Ago at={u.last_login} /></span> : <span className="faint">{tr('Never')}</span>],
                    [tr('Created'), <Ago at={u.created} />],
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
  const member = roles.data?.find((r) => r.name === tr('Team Member'))
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
        <span className="small faint grow">{userName(user)} {tr('gets every role granted to these teams.')}</span>
        <button className="btn primary" onClick={() => setAdding(true)}>
          <Icon name="plus" size={14} />
          {tr('Add to teams')}
        </button>
      </div>
      <DataTable
        label={tr('Teams')}
        rows={list.data}
        loading={list.isPending}
        error={list.error}
        empty={<Empty title={tr('Not in any team')} />}
        columns={[
          {
            key: 'team',
            header: tr('Team'),
            render: (a) => (
              <Link to={`/teams/${a.object_id}`} className="cell-title">
                {a.summary_fields.content_object?.name}
              </Link>
            ),
          },
          { key: 'role', header: tr('Role'), render: (a) => (a.summary_fields.role_definition?.name ?? '').replace(/^Team /, '') },
          { key: 'since', header: tr('Since'), className: 'small faint nowrap', render: (a) => <Ago at={a.created} /> },
          {
            key: 'x',
            header: <span className="sr-only">{tr('Actions')}</span>,
            className: 'end',
            render: (a) => (
              <button
                className="btn sm ghost"
                onClick={async () => {
                  if (!(await confirm({ title: tr('Remove from {name}?', { name: a.summary_fields.content_object?.name }), confirm: tr('Remove'), danger: true }))) return
                  await api.del(`role_user_assignments/${a.id}/`).catch((e) => toast((e as Error).message, 'error'))
                  invalidate('role_user_assignments', 'role_access')
                }}
              >
                {tr('Remove')}
              </button>
            ),
          },
        ]}
      />
      {adding && (
        <Dialog
          title={tr('Add {user} to teams', { user: userName(user) })}
          onClose={() => setAdding(false)}
          footer={
            <>
              <button className="btn" onClick={() => setAdding(false)}>
                {tr('Cancel')}
              </button>
              <button className="btn primary" disabled={!picked.length || !member} onClick={add}>
                {tr('Add')}
              </button>
            </>
          }
        >
          <MultiLookup label={tr('Teams')} resource="teams/" values={picked} onChange={setPicked} meta={(r) => r.summary_fields?.organization?.name} full />
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
        <span className="small faint grow">{tr('Personal access tokens authenticate scripts and CI as')} {self ? tr('you') : userName(user)}{tr(': send')} <span className="mono">{tr('Authorization: Bearer <token>')}</span>.</span>
        {self && (
          <button className="btn primary" onClick={() => setCreating(true)}>
            <Icon name="plus" size={14} />
            {tr('New token')}
          </button>
        )}
      </div>
      <DataTable
        label={tr('Tokens')}
        rows={tokens.data?.results}
        loading={tokens.isPending}
        error={tokens.error}
        empty={<Empty title={tr('No tokens')}>{self ? tr('Create one for scripts that call the API.') : tr('Only the user can create tokens for themselves.')}</Empty>}
        columns={[
          { key: 'desc', header: tr('Description'), render: (t) => t.description || <span className="faint">{tr('No description')}</span> },
          { key: 'scope', header: tr('Scope'), render: (t) => <span className="tag">{t.scope}</span> },
          { key: 'created', header: tr('Created'), className: 'small nowrap', render: (t) => <Ago at={t.created} /> },
          {
            key: 'x',
            header: <span className="sr-only">{tr('Actions')}</span>,
            className: 'end',
            render: (t) =>
              t.summary_fields.user_capabilities?.delete !== false && (
                <button className="btn sm ghost" onClick={() => del({ path: `tokens/${t.id}/`, name: t.description || `token ${t.id}`, what: tr('token'), invalidate: [`users/${user.id}/personal_tokens`], body: <p style={{ margin: 0 }}>{tr('Scripts using it stop working immediately.')}</p> })}>
                  {tr('Revoke')}
                </button>
              ),
          },
        ]}
      />
      {creating && (
        <Dialog
          title={created ? tr('Token created') : tr('New personal token')}
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
                {tr('Done')}
              </button>
            ) : (
              <>
                <button className="btn" onClick={() => setCreating(false)}>
                  {tr('Cancel')}
                </button>
                <button className="btn primary" onClick={create}>
                  {tr('Create token')}
                </button>
              </>
            )
          }
        >
          {created ? (
            <OneTimeSecret value={created.token} note={tr('Copy the token now. It is shown only once; afterwards only its description is visible.')} />
          ) : (
            <>
              <TextInput label={tr('Description')} value={desc} onChange={setDesc} placeholder={tr('e.g. GitLab CI deploy pipeline')} autoFocus />
              <SelectField
                label={tr('Scope')}
                value={scope}
                onChange={setScope}
                options={[
                  ['write', tr('Write — everything you can do')],
                  ['read', tr('Read — view only')],
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
