import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { NavLink, useParams } from 'react-router'
import { api, type Page } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { FieldMeta, OptionsMeta, Ref } from '../../api/types'
import { useSession } from '../../app/context'
import { Field, FormError, Lookup } from '../../components/form'
import { Icon } from '../../components/icons'
import { useConfirm, useToast } from '../../components/overlay'
import { Empty, ErrorNote, Loading, PageHead, Panel } from '../../components/ui'
import { humanize } from './common'
import { grantReason, grantTarget, testerClaims, type MappedGrant } from '../../lib/mapping'
import { ldapPrefix, ldapServer, ldapServerOn, ldapServerOrder } from '../../lib/ldapSettings'
import { aggregatorTarget, loggingSection, loggingSections, testOutcome } from '../../lib/logging'

interface Category {
  slug: string
  name: string
}

const blurbs: Record<string, string> = {
  system: 'Base URL, activity stream, proxies and the default execution environment.',
  jobs: 'Job defaults, timeouts, ad hoc modules, Galaxy and isolation.',
  authentication: 'Sessions, basic auth and local accounts.',
  oidc: 'Single sign-on with an OpenID Connect provider, and how its groups map to access.',
  ldap: 'Up to six LDAP servers, tried in order before local accounts, and how their groups map to access.',
  saml: 'Single sign-on with SAML identity providers: the service provider awxnext is, the providers, and how their attributes map to access.',
  github: 'Login with GitHub accounts.',
  'github-org': 'Login with GitHub accounts that are members of an organization.',
  'github-team': 'Login with GitHub accounts that are members of a team.',
  'github-enterprise': 'Login with GitHub Enterprise accounts.',
  'github-enterprise-org': 'Login with GitHub Enterprise accounts that are members of an organization.',
  'github-enterprise-team': 'Login with GitHub Enterprise accounts that are members of a team.',
  'google-oauth2': 'Login with Google accounts, optionally of some domains only.',
  'azuread-oauth2': 'Login with Microsoft Entra ID (Azure AD) accounts.',
  logging: 'Shipping logs to an external aggregator.',
  ui: 'Login page and live updates.',
  changed: 'Every setting that differs from its default.',
}

const order = ['system', 'jobs', 'authentication', 'oidc', 'ldap', 'saml', 'github', 'github-org', 'github-team', 'github-enterprise', 'github-enterprise-org', 'github-enterprise-team', 'google-oauth2', 'azuread-oauth2', 'logging', 'ui', 'changed']

const isSecret = (key: string) => /SECRET|PASSWORD/.test(key)

function SettingsNav({ cats, active }: { cats: Category[]; active: string }) {
  return (
    <nav className="sidelist" aria-label="Settings categories">
      <div className="group">
        <div className="group-head">
          <span>Settings</span>
        </div>
        {cats.map((c) => (
          <NavLink key={c.slug} to={`/settings/${c.slug}`} className={c.slug === active ? 'active' : ''}>
            {c.slug === 'oidc' ? 'Single sign-on (OIDC)' : c.name}
          </NavLink>
        ))}
      </div>
    </nav>
  )
}

export function Settings() {
  const { me } = useSession()
  const cat = useParams().category ?? 'system'
  const cats = useQuery({ queryKey: ['settings/', 'cats'], queryFn: () => api.get<Page<Category>>('settings/') })
  const list = (cats.data?.results ?? []).filter((c) => c.slug !== 'all').sort((a, b) => order.indexOf(a.slug) - order.indexOf(b.slug))
  const current = list.find((c) => c.slug === cat)
  return (
    <div className="page" style={{ flexDirection: 'row' }}>
      <SettingsNav cats={list} active={cat} />
      <div className="grow" style={{ display: 'flex', flexDirection: 'column' }}>
        <PageHead title={cat === 'oidc' ? 'Single sign-on' : (current?.name ?? 'Settings')} subtitle={blurbs[cat]} />
        <div className="page-body">
          {!me.is_superuser && <div className="notice">Only superusers can change settings. You can review them here.</div>}
          {cat === 'changed' ? <ChangedSettings /> : <CategoryForm key={cat} cat={cat} editable={me.is_superuser} />}
        </div>
      </div>
    </div>
  )
}

type Values = Record<string, unknown>

function useCategory(cat: string) {
  const meta = useQuery({ queryKey: [`settings/${cat}/`, 'options'], queryFn: () => api.options<OptionsMeta>(`settings/${cat}/`) })
  const values = useObject<Values>(`settings/${cat}/`)
  return { meta, values }
}

/** Groups related settings under readable headings. */
function sectionOf(cat: string, key: string): string {
  if (cat === 'oidc') return key.startsWith('SOCIAL_AUTH_') ? 'Provider' : key === 'OIDC_MAPPING' ? 'Group mapping' : 'Claims and tokens'
  if (cat === 'authentication') return key.endsWith('_MAP') ? 'Organization and team maps' : key.startsWith('LOCAL_PASSWORD_') ? 'Local passwords' : 'Sessions and accounts'
  if (cat === 'ldap') return ldapServer(key)
  if (cat === 'logging') return loggingSection(key)
  if (cat === 'saml') return /_MAP$|_ATTR$|FLAGS/.test(key) ? 'Organizations, teams and flags' : /IDPS|SECURITY|EXTRA_DATA/.test(key) ? 'Identity providers' : 'Service provider'
  if (cat === 'jobs') {
    if (/TIMEOUT/.test(key)) return 'Timeouts'
    if (/GALAXY|COLLECTIONS|ROLES/.test(key)) return 'Content'
    if (/ISOLATION|MOUNT|TASK_ENV|CALLBACK/.test(key)) return 'Execution'
    return 'General'
  }
  return ''
}

const sectionOrder: Record<string, string[]> = {
  oidc: ['Provider', 'Claims and tokens', 'Group mapping'],
  authentication: ['Sessions and accounts', 'Local passwords', 'Organization and team maps'],
  ldap: ldapServerOrder,
  saml: ['Service provider', 'Identity providers', 'Organizations, teams and flags'],
  jobs: ['General', 'Timeouts', 'Content', 'Execution'],
  logging: loggingSections,
}

/** AWX's logging test: a message sent with the saved settings. */
function LoggingTest({ values, unsaved }: { values: Values; unsaved: boolean }) {
  const [busy, setBusy] = useState(false)
  const [outcome, setOutcome] = useState<{ ok: boolean; text: string } | null>(null)
  const target = values.LOG_AGGREGATOR_ENABLED ? aggregatorTarget(values) : null
  const run = async () => {
    setBusy(true)
    try {
      await api.post('settings/logging/test/')
      setOutcome(testOutcome(null))
    } catch (e) {
      setOutcome(testOutcome(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <button className="btn" disabled={busy || unsaved} title={unsaved ? 'Save first: the test uses the saved settings.' : undefined} onClick={run}>
        {busy ? 'Testing…' : 'Test'}
      </button>
      <span className={`small ${outcome?.ok === false ? '' : 'faint'}`} style={outcome?.ok === false ? { color: 'var(--fail-deep)' } : undefined} role="status">
        {outcome ? outcome.text : target ? <>Sends to <span className="mono">{target}</span></> : values.LOG_AGGREGATOR_ENABLED ? '' : 'External logging is off.'}
      </span>
    </>
  )
}

function CategoryForm({ cat, editable }: { cat: string; editable: boolean }) {
  const { meta, values } = useCategory(cat)
  const [draft, setDraft] = useState<Values>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const confirm = useConfirm()
  const invalidate = useInvalidate()
  useEffect(() => {
    if (values.data) setDraft(values.data)
  }, [values.data])
  const fields = useMemo(() => {
    const put = meta.data?.actions?.PUT ?? {}
    const get = meta.data?.actions?.GET ?? {}
    return Object.entries(get)
      .map(([k, m]) => ({ key: k, meta: m, writable: editable && k in put }))
      .sort((a, b) => a.key.localeCompare(b.key))
  }, [meta.data, editable])
  if (meta.error || values.error) return <ErrorNote error={meta.error ?? values.error} />
  if (!meta.data || !values.data) return <Loading />
  const dirty = fields.filter((f) => f.writable && JSON.stringify(draft[f.key]) !== JSON.stringify(values.data![f.key])).map((f) => f.key)
  const save = async () => {
    setBusy(true)
    setError(null)
    setErrors({})
    const body: Values = {}
    for (const k of dirty) body[k] = draft[k]
    try {
      await api.patch(`settings/${cat}/`, body)
      invalidate(`settings/`)
      toast(`Saved ${dirty.length} setting${dirty.length === 1 ? '' : 's'}`)
    } catch (e) {
      setError(e)
      if (e && typeof e === 'object' && 'fields' in e) setErrors((e as { fields: Record<string, string> }).fields)
    } finally {
      setBusy(false)
    }
  }
  const resetAll = async () => {
    if (!(await confirm({ title: `Reset all ${cat} settings to their defaults?`, confirm: 'Reset', danger: true }))) return
    try {
      await api.del(`settings/${cat}/`)
      invalidate('settings/')
      toast('Reset to defaults')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  const sections = new Map<string, typeof fields>()
  for (const s of sectionOrder[cat] ?? []) sections.set(s, [])
  for (const f of fields) {
    const s = sectionOf(cat, f.key)
    sections.set(s, [...(sections.get(s) ?? []), f])
  }
  for (const [k, v] of sections) if (!v.length) sections.delete(k)
  const set = (k: string, v: unknown) => {
    setDraft((d) => ({ ...d, [k]: v }))
    if (errors[k]) setErrors((e) => ({ ...e, [k]: '' }))
  }
  return (
    <div className="stack settings-form" style={{ gap: 20 }}>
      {[...sections.entries()].map(([title, list]) => {
        const fieldsGrid = (
          <div className="form-grid">
            {list.map((f) => (
              <SettingField
                key={f.key}
                k={f.key}
                meta={f.meta}
                value={draft[f.key]}
                saved={values.data![f.key]}
                onChange={(v) => set(f.key, v)}
                disabled={!f.writable}
                error={errors[f.key]}
                prefix={cat === 'ldap' ? ldapPrefix(f.key) : undefined}
              />
            ))}
          </div>
        )
        if (cat === 'ldap') {
          const on = ldapServerOn(draft, title)
          return (
            <details key={title} className="form-section" open={on || title === 'Default server' || list.some((f) => errors[f.key])}>
              <summary>
                <h2 style={{ display: 'inline' }}>{title}</h2> <span className="faint small">{on ? '' : 'off'}</span>
              </summary>
              {fieldsGrid}
            </details>
          )
        }
        return (
        <section key={title || 'main'} className="form-section">
          {title && <h2>{title}</h2>}
          {title === 'Group mapping' ? (
            <MappingEditor value={(draft.OIDC_MAPPING as Rule[]) ?? []} onChange={(v) => set('OIDC_MAPPING', v)} editable={editable} error={errors.OIDC_MAPPING} groupsClaim={String(draft.OIDC_GROUPS_CLAIM ?? 'groups')} />
          ) : (
            <div className="form-grid">
              {list.map((f) => (
                <SettingField
                  key={f.key}
                  k={f.key}
                  meta={f.meta}
                  value={draft[f.key]}
                  saved={values.data![f.key]}
                  onChange={(v) => set(f.key, v)}
                  disabled={!f.writable}
                  error={errors[f.key]}
                  prefix={cat === 'oidc' ? (f.key.startsWith('SOCIAL_AUTH_OIDC_') ? 'SOCIAL_AUTH_OIDC_' : 'OIDC_') : undefined}
                />
              ))}
            </div>
          )}
        </section>
        )
      })}
      <FormError error={error} />
      {editable && (
        <div className="form-actions settings-actions">
          <button className="btn primary" disabled={busy || dirty.length === 0} onClick={save}>
            {busy ? 'Saving…' : dirty.length ? `Save ${dirty.length} change${dirty.length === 1 ? '' : 's'}` : 'No changes'}
          </button>
          {dirty.length > 0 && (
            <button className="btn ghost" onClick={() => setDraft(values.data!)}>
              Discard
            </button>
          )}
          {cat === 'logging' && <LoggingTest values={values.data} unsaved={dirty.length > 0} />}
          <button className="btn ghost" style={{ marginLeft: 'auto' }} onClick={resetAll}>
            Reset category to defaults
          </button>
        </div>
      )}
    </div>
  )
}

function SettingField({
  k,
  meta,
  value,
  saved,
  onChange,
  disabled,
  error,
  prefix,
}: {
  k: string
  meta: FieldMeta
  value: unknown
  saved: unknown
  onChange: (v: unknown) => void
  disabled: boolean
  error?: string
  prefix?: string
}) {
  const label = meta.label && meta.label !== k ? meta.label : humanize(k, prefix)
  const isDefault = JSON.stringify(value) === JSON.stringify(meta.default ?? null) || (value === '' && (meta.default ?? '') === '')
  const changed = JSON.stringify(value) !== JSON.stringify(saved)
  const help: ReactNode = (
    <>
      {meta.help_text && meta.help_text !== label ? `${meta.help_text} ` : ''}
      <span className="mono">{k}</span>
      {!isDefault && !disabled && (
        <>
          {' · '}
          <button type="button" className="link-btn small" onClick={() => onChange(meta.default ?? (meta.type === 'boolean' ? false : meta.type === 'list' ? [] : ''))}>
            restore default
          </button>
        </>
      )}
      {changed && <span className="tag" style={{ marginLeft: 6 }}>unsaved</span>}
    </>
  )
  const id = `s-${k}`
  switch (meta.type) {
    case 'boolean':
      return (
        <div className="field">
          <label className="check">
            <input id={id} type="checkbox" checked={!!value} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
            <span style={{ fontWeight: 500 }}>{label}</span>
          </label>
          <span className="help" style={{ marginLeft: 22 }}>
            {help}
          </span>
          {error && <span className="err">{error}</span>}
        </div>
      )
    case 'integer':
    case 'float':
      return (
        <Field label={label} help={help} error={error} htmlFor={id}>
          <input id={id} className="input mono" type="number" disabled={disabled} value={value === null || value === undefined ? '' : String(value)} onChange={(e) => onChange(e.target.value === '' ? null : Number(e.target.value))} />
        </Field>
      )
    case 'choice':
      return (
        <Field label={label} help={help} error={error} htmlFor={id}>
          <select id={id} className="select" disabled={disabled} value={String(value ?? '')} onChange={(e) => onChange(e.target.value === '' ? null : e.target.value)}>
            {(meta.default === null || value === null || value === undefined) && <option value="">---------</option>}
            {(meta.choices ?? []).map(([v, l]) => (
              <option key={String(v)} value={String(v)}>
                {l}
              </option>
            ))}
          </select>
        </Field>
      )
    case 'list':
      // LDAP searches are [base, scope, filter], or a list of them.
      if (/^AUTH_LDAP_(\d_)?(USER|GROUP)_SEARCH$/.test(k)) return <JsonField id={id} label={label} help={help} error={error} value={value} onChange={onChange} disabled={disabled} />
      return <ListField id={id} label={label} help={help} error={error} value={value} onChange={onChange} disabled={disabled} />
    case 'nested object':
    case 'dict':
      return <JsonField id={id} label={label} help={help} error={error} value={value} onChange={onChange} disabled={disabled} />
    case 'field':
      return <EEField label={label} help={help} value={value as number | null} onChange={onChange} disabled={disabled} />
  }
  return (
    <Field label={label} help={help} error={error} htmlFor={id}>
      <input
        id={id}
        className={`input ${/URL|PATH|HOST|CLAIM|ENDPOINT|KEY/.test(k) ? 'mono' : ''}`}
        type={isSecret(k) ? 'password' : 'text'}
        disabled={disabled}
        autoComplete="off"
        value={String(value ?? '')}
        onChange={(e) => onChange(e.target.value)}
      />
    </Field>
  )
}

/** Lists of strings edited one per line; lists of objects as JSON. */
function ListField({ id, label, help, error, value, onChange, disabled }: { id: string; label: string; help: ReactNode; error?: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  const list = Array.isArray(value) ? value : []
  const simple = list.every((x) => typeof x === 'string' || typeof x === 'number')
  if (!simple) return <JsonField id={id} label={label} help={help} error={error} value={value} onChange={onChange} disabled={disabled} />
  return (
    <Field label={label} help={<>One per line. {help}</>} error={error} htmlFor={id}>
      <textarea
        id={id}
        className="textarea mono"
        rows={Math.min(8, Math.max(2, list.length + 1))}
        disabled={disabled}
        value={list.join('\n')}
        onChange={(e) =>
          onChange(
            e.target.value
              .split('\n')
              .map((s) => s.trim())
              .filter((s, i, all) => s !== '' || i < all.length - 1)
              .filter(Boolean),
          )
        }
      />
    </Field>
  )
}

function JsonField({ id, label, help, error, value, onChange, disabled }: { id: string; label: string; help: ReactNode; error?: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  const [text, setText] = useState(() => JSON.stringify(value ?? {}, null, 2))
  const [bad, setBad] = useState<string | null>(null)
  useEffect(() => {
    try {
      if (JSON.stringify(JSON.parse(text)) !== JSON.stringify(value ?? {})) setText(JSON.stringify(value ?? {}, null, 2))
    } catch {
      /* keep the user's text while it is invalid */
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])
  return (
    <Field label={label} help={help} error={error ?? bad ?? undefined} htmlFor={id} full>
      <textarea
        id={id}
        className="textarea mono"
        rows={Math.min(12, text.split('\n').length + 1)}
        disabled={disabled}
        spellCheck={false}
        value={text}
        aria-invalid={!!bad}
        onChange={(e) => {
          setText(e.target.value)
          try {
            onChange(JSON.parse(e.target.value))
            setBad(null)
          } catch (err) {
            setBad('Not valid JSON: ' + (err as Error).message)
          }
        }}
      />
    </Field>
  )
}

function EEField({ label, help, value, onChange, disabled }: { label: string; help: ReactNode; value: number | null; onChange: (v: unknown) => void; disabled: boolean }) {
  const ee = useObject<Ref>(value ? `execution_environments/${value}/` : null)
  if (disabled)
    return (
      <Field label={label} help={help}>
        <span>{ee.data?.name ?? (value ? `#${value}` : 'Not set')}</span>
      </Field>
    )
  return <Lookup label={label} help={help} optional resource="execution_environments/" value={ee.data ?? null} onChange={(r) => onChange(r?.id ?? null)} meta={(r) => r.image} />
}

function ChangedSettings() {
  const { meta, values } = useCategory('changed')
  if (meta.error || values.error) return <ErrorNote error={meta.error ?? values.error} />
  if (!values.data) return <Loading />
  const entries = Object.entries(values.data)
  if (!entries.length) return <Empty title="Everything is at its default" />
  return (
    <Panel pad={false}>
      <table className="data">
        <thead>
          <tr>
            <th scope="col">Setting</th>
            <th scope="col">Value</th>
            <th scope="col">Category</th>
          </tr>
        </thead>
        <tbody>
          {entries.map(([k, v]) => {
            const m = meta.data?.actions?.GET?.[k]
            return (
              <tr key={k}>
                <td>
                  <div style={{ fontWeight: 500 }}>{m?.label && m.label !== k ? m.label : humanize(k)}</div>
                  <div className="cell-sub">{k}</div>
                </td>
                <td className="mono small" style={{ maxWidth: 520, overflowWrap: 'anywhere' }}>
                  {isSecret(k) ? '••••••' : JSON.stringify(v)}
                </td>
                <td>
                  <NavLink to={`/settings/${m?.category_slug ?? m?.category ?? ''}`}>{m?.category ?? ''}</NavLink>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </Panel>
  )
}

// ---------- OIDC group mapping ----------

export interface Rule {
  group?: string
  group_regex?: string
  superuser?: boolean
  auditor?: boolean
  organization?: string
  team?: string
  role?: string
  role_definition?: string
  create?: boolean
}

type Grant = 'superuser' | 'auditor' | 'org' | 'team' | 'roledef'

function grantOf(r: Rule): Grant {
  if (r.superuser) return 'superuser'
  if (r.auditor) return 'auditor'
  if (r.team !== undefined) return 'team'
  if (r.role_definition !== undefined) return 'roledef'
  return 'org'
}

function MappingEditor({ value, onChange, editable, error, groupsClaim }: { value: Rule[]; onChange: (v: Rule[]) => void; editable: boolean; error?: string; groupsClaim: string }) {
  const [asJson, setAsJson] = useState(false)
  const rules = Array.isArray(value) ? value : []
  const update = (i: number, patch: Partial<Rule>) => onChange(rules.map((r, j) => (j === i ? clean({ ...r, ...patch }) : r)))
  const setGrant = (i: number, g: Grant) => {
    const r = rules[i]
    const base: Rule = { group: r.group, group_regex: r.group_regex }
    if (g === 'superuser') base.superuser = true
    if (g === 'auditor') base.auditor = true
    if (g === 'org') Object.assign(base, { organization: r.organization ?? '', role: r.role && ['member', 'admin', 'auditor'].includes(r.role) ? r.role : 'member' })
    if (g === 'team') Object.assign(base, { organization: r.organization ?? '', team: r.team ?? '', role: r.role === 'admin' ? 'admin' : 'member' })
    if (g === 'roledef') Object.assign(base, { organization: r.organization ?? '', role_definition: r.role_definition ?? '' })
    onChange(rules.map((x, j) => (j === i ? clean(base) : x)))
  }
  const move = (i: number, d: number) => {
    const n = [...rules]
    const [x] = n.splice(i, 1)
    n.splice(i + d, 0, x)
    onChange(n)
  }
  return (
    <div className="stack" style={{ gap: 12 }}>
      <p className="muted" style={{ margin: 0 }}>
        At every sign-in, the groups in the token's <span className="mono">{groupsClaim}</span> claim are matched against these rules. Access granted by rules is replaced on each sign-in: if a group disappears, so does the access it gave. Access granted by hand in the UI is kept.
      </p>
      <div className="row">
        <span className="grow" />
        <label className="check small">
          <input type="checkbox" checked={asJson} onChange={(e) => setAsJson(e.target.checked)} />
          Edit as JSON
        </label>
      </div>
      {asJson ? (
        <JsonField id="oidc-map-json" label="Rules" help="A list of rule objects." error={error} value={rules} onChange={(v) => onChange(Array.isArray(v) ? (v as Rule[]) : [])} disabled={!editable} />
      ) : (
        <div className="table-wrap">
          <table className="data mapping">
            <thead>
              <tr>
                <th scope="col">When the user is in group</th>
                <th scope="col">Grant</th>
                <th scope="col">Where</th>
                <th scope="col" className="end">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r, i) => {
                const g = grantOf(r)
                const regex = r.group_regex !== undefined
                return (
                  <tr key={i}>
                    <td>
                      <div className="row" style={{ gap: 6 }}>
                        <select className="select" style={{ width: 128, flex: '0 0 128px' }} aria-label="Match" disabled={!editable} value={regex ? 'regex' : 'exact'} onChange={(e) => update(i, e.target.value === 'regex' ? { group_regex: r.group ?? '', group: undefined } : { group: r.group_regex ?? '', group_regex: undefined })}>
                          <option value="exact">named</option>
                          <option value="regex">matching</option>
                        </select>
                        <input
                          className="input mono"
                          aria-label="Group"
                          disabled={!editable}
                          placeholder={regex ? '^team-(.+)$' : 'awx-admins'}
                          value={(regex ? r.group_regex : r.group) ?? ''}
                          onChange={(e) => update(i, regex ? { group_regex: e.target.value } : { group: e.target.value })}
                        />
                      </div>
                    </td>
                    <td>
                      <select className="select" style={{ minWidth: 180 }} aria-label="Grant" disabled={!editable} value={g} onChange={(e) => setGrant(i, e.target.value as Grant)}>
                        <option value="superuser">Superuser</option>
                        <option value="auditor">System auditor</option>
                        <option value="org">Organization role</option>
                        <option value="team">Team membership</option>
                        <option value="roledef">Role definition</option>
                      </select>
                    </td>
                    <td>
                      {g === 'superuser' || g === 'auditor' ? (
                        <span className="faint small">Everywhere</span>
                      ) : (
                        <div className="row wrap" style={{ gap: 6 }}>
                          <input className="input" style={{ flex: '1 1 140px' }} aria-label="Organization" placeholder="Organization" disabled={!editable} value={r.organization ?? ''} onChange={(e) => update(i, { organization: e.target.value })} />
                          {g === 'team' && <input className="input" style={{ flex: '1 1 120px' }} aria-label="Team" placeholder="Team (or $1)" disabled={!editable} value={r.team ?? ''} onChange={(e) => update(i, { team: e.target.value })} />}
                          {g === 'roledef' ? (
                            <input className="input" style={{ flex: '1 1 160px' }} aria-label="Role definition" placeholder="Organization Execute" disabled={!editable} value={r.role_definition ?? ''} onChange={(e) => update(i, { role_definition: e.target.value })} />
                          ) : (
                            <select className="select" style={{ width: 120 }} aria-label="Role" disabled={!editable} value={r.role ?? 'member'} onChange={(e) => update(i, { role: e.target.value })}>
                              <option value="member">member</option>
                              <option value="admin">admin</option>
                              {g === 'org' && <option value="auditor">auditor</option>}
                            </select>
                          )}
                          <label className="check small" title="Create the organization or team when it does not exist">
                            <input type="checkbox" disabled={!editable} checked={r.create !== false} onChange={(e) => update(i, { create: e.target.checked ? undefined : false })} />
                            create
                          </label>
                        </div>
                      )}
                    </td>
                    <td className="end nowrap">
                      {editable && (
                        <>
                          <button type="button" className="btn ghost sm icon" aria-label="Move up" disabled={i === 0} onClick={() => move(i, -1)}>
                            ↑
                          </button>
                          <button type="button" className="btn ghost sm icon" aria-label="Move down" disabled={i === rules.length - 1} onClick={() => move(i, 1)}>
                            ↓
                          </button>
                          <button type="button" className="btn ghost sm icon" aria-label="Remove rule" onClick={() => onChange(rules.filter((_, j) => j !== i))}>
                            <Icon name="x" size={12} />
                          </button>
                        </>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          {rules.length === 0 && <Empty title="No rules">Without rules, SSO users can sign in but get no access until granted in the UI.</Empty>}
          {editable && (
            <div className="table-foot">
              <button type="button" className="btn sm" onClick={() => onChange([...rules, { group: '', organization: '', role: 'member' }])}>
                <Icon name="plus" size={12} />
                Add rule
              </button>
              {error && <span className="fail-text">{error}</span>}
            </div>
          )}
        </div>
      )}
      <MappingTester rules={rules} groupsClaim={groupsClaim} />
    </div>
  )
}

function clean(r: Rule): Rule {
  const out: Rule = {}
  for (const [k, v] of Object.entries(r)) if (v !== undefined && v !== false) (out as Record<string, unknown>)[k] = v
  if (r.create === false) out.create = false
  return out
}

interface DryRun {
  username: string
  email?: string
  groups: string[]
  groups_claim: string
  superuser: boolean | null
  auditor: boolean | null
  grants: MappedGrant[] | null
  kept?: MappedGrant[] | null
  would_create: string[] | null
  errors?: string[] | null
}

/** Evaluates the unsaved rules against sample token claims. */
function MappingTester({ rules, groupsClaim }: { rules: Rule[]; groupsClaim: string }) {
  const [groups, setGroups] = useState('')
  const [user, setUser] = useState('')
  const [email, setEmail] = useState('')
  const [claimsText, setClaimsText] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const [result, setResult] = useState<DryRun | null>(null)
  const [error, setError] = useState<unknown>(null)
  const run = async () => {
    setError(null)
    try {
      let claims: Record<string, unknown>
      if (advanced) claims = JSON.parse(claimsText || '{}')
      else claims = testerClaims(groupsClaim, user, email, groups)
      setResult(await api.post<DryRun>('settings/oidc/test/', { claims, mapping: rules }))
    } catch (e) {
      setError(e)
      setResult(null)
    }
  }
  return (
    <Panel title="Try the rules" actions={<label className="check small"><input type="checkbox" checked={advanced} onChange={(e) => setAdvanced(e.target.checked)} />Paste full token claims</label>}>
      <div className="stack" style={{ gap: 10 }}>
        <p className="small muted" style={{ margin: 0 }}>
          Nothing is saved or changed: this shows what a user with these groups would get from the rules above, including unsaved edits, and from the saved organization and team maps of the Authentication settings, which match the username and email.
        </p>
        {advanced ? (
          <textarea className="textarea mono" rows={6} spellCheck={false} placeholder={`{\n  "preferred_username": "alice",\n  "${groupsClaim}": ["ops", "team-payments"]\n}`} value={claimsText} onChange={(e) => setClaimsText(e.target.value)} aria-label="Token claims" />
        ) : (
          <div className="row" style={{ flexWrap: 'wrap' }}>
            <input className="input mono" style={{ flex: '1 1 10rem', width: 'auto' }} aria-label="Username" placeholder="test-user" value={user} onChange={(e) => setUser(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && run()} />
            <input className="input mono" style={{ flex: '1 1 12rem', width: 'auto' }} aria-label="Email" placeholder="email (optional)" value={email} onChange={(e) => setEmail(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && run()} />
            <input className="input mono" style={{ flex: '2 1 16rem', width: 'auto' }} aria-label="Groups" placeholder="ops, team-payments, awx-admins" value={groups} onChange={(e) => setGroups(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && run()} />
            <button type="button" className="btn" onClick={run}>
              Evaluate
            </button>
          </div>
        )}
        {advanced && (
          <div>
            <button type="button" className="btn" onClick={run}>
              Evaluate
            </button>
          </div>
        )}
        <FormError error={error} />
        {result && (
          <div className="stack" style={{ gap: 8 }}>
            <div className="small">
              <span className="mono">{result.username}</span>
              {result.email ? <span className="mono faint"> ({result.email})</span> : null} with groups {result.groups.length ? result.groups.map((g) => <span key={g} className="chip" style={{ marginRight: 4 }}>{g}</span>) : <span className="faint">none</span>}
            </div>
            {(result.superuser || result.auditor) && <div className="notice accent">{result.superuser ? 'Becomes a superuser.' : 'Becomes a system auditor.'}</div>}
            {result.grants?.length ? (
              <table className="data">
                <thead>
                  <tr>
                    <th scope="col">Role</th>
                    <th scope="col">On</th>
                    <th scope="col">Because of</th>
                  </tr>
                </thead>
                <tbody>
                  {result.grants.map((g, i) => (
                    <tr key={i}>
                      <td>{g.role_definition}</td>
                      <td>{grantTarget(g)}</td>
                      <td className="mono small">{grantReason(g)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              !result.superuser && !result.auditor && <div className="notice">No rule matches: the user could sign in but would have no access.</div>
            )}
            {result.kept?.length ? (
              <div className="small muted">
                Left as they are (kept if the user has them from an earlier login): {result.kept.map((g) => `${g.role_definition} on ${grantTarget(g)}`).join(', ')}
              </div>
            ) : null}
            {result.would_create?.length ? <div className="small muted">Would create: {result.would_create.join(', ')}</div> : null}
            {result.errors?.length ? <div className="form-error">{result.errors.join('; ')}</div> : null}
          </div>
        )}
      </div>
    </Panel>
  )
}
