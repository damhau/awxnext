// AWX's organization and team maps, edited as entries rather than JSON, and
// saved back in AWX's format (internal/auth/awxmap.go reads them):
//
//   {"Default": {"users": true}, "Ops": {"admins": ["alice", "/^ops-/i"], "remove_users": false}}
//   {"Web": {"organization": "Ops", "users": "/^web-/", "remove": true}}
//
// An expression is null (memberships stay as they are), true (everyone),
// false (no one), or strings: usernames or emails, or /patterns/flags; in
// LDAP's maps, group DNs. Removal when no longer matched is the default.
// Also LDAP's user flags by group, and SAML's attribute maps.

import { tr } from './i18n'

export type Expr = null | boolean | string[]
export type Role = 'users' | 'admins' | 'auditors'

export interface OrgEntry {
  kind: 'organization'
  name: string
  /** organization_alias: the organization joined, if not the entry's name. Social maps only. */
  alias: string
  exprs: Record<Role, Expr>
  /** Whether a role is removed when no longer matched. */
  remove: Record<Role, boolean>
}

export interface TeamEntry {
  kind: 'team'
  name: string
  organization: string
  users: Expr
  remove: boolean
}

export type MapEntry = OrgEntry | TeamEntry

// DRF's spellings, which AWX's map fields accept.
const TRUE = new Set(['t', 'T', 'y', 'Y', 'yes', 'Yes', 'YES', 'true', 'True', 'TRUE', 'on', 'On', 'ON', '1'])
const FALSE = new Set(['f', 'F', 'n', 'N', 'no', 'No', 'NO', 'false', 'False', 'FALSE', 'off', 'Off', 'OFF', '0'])
const NULL = new Set(['null', 'Null', 'NULL', ''])

const isObject = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)

export function parseExpr(v: unknown): Expr {
  if (v === null || v === undefined) return null
  if (typeof v === 'boolean') return v
  if (typeof v === 'number') return v === 1 ? true : v === 0 ? false : [String(v)]
  if (typeof v === 'string') return TRUE.has(v) ? true : FALSE.has(v) ? false : NULL.has(v) ? null : [v]
  if (Array.isArray(v)) return v.map(String)
  return null
}

/** One string alone, as AWX's examples write it; several as a list. */
export function formatExpr(e: Expr): unknown {
  return Array.isArray(e) && e.length === 1 ? e[0] : e
}

function optBool(v: unknown): boolean {
  if (typeof v === 'boolean') return v
  if (typeof v === 'string') return !FALSE.has(v)
  if (typeof v === 'number') return v !== 0
  return true
}

export const orgRoles = (ldap: boolean): Role[] => (ldap ? ['users', 'admins', 'auditors'] : ['users', 'admins'])

/** The entries of an organization map and a team map, in their order. */
export function parseMaps(orgMap: unknown, teamMap: unknown): MapEntry[] {
  const out: MapEntry[] = []
  for (const [name, v] of Object.entries(isObject(orgMap) ? orgMap : {})) {
    const e = isObject(v) ? v : {}
    out.push({
      kind: 'organization',
      name,
      alias: typeof e.organization_alias === 'string' && !NULL.has(e.organization_alias) ? e.organization_alias : '',
      exprs: { users: parseExpr(e.users), admins: parseExpr(e.admins), auditors: parseExpr(e.auditors) },
      remove: { users: optBool(e.remove_users), admins: optBool(e.remove_admins), auditors: optBool(e.remove_auditors) },
    })
  }
  for (const [name, v] of Object.entries(isObject(teamMap) ? teamMap : {})) {
    const e = isObject(v) ? v : {}
    out.push({ kind: 'team', name, organization: typeof e.organization === 'string' ? e.organization : '', users: parseExpr(e.users), remove: optBool(e.remove) })
  }
  return out
}

/** The organization map and the team map of the entries. An expression left
 * as it is and a remove option at its default are left out; entries without
 * a name are not saved yet. */
export function formatMaps(entries: MapEntry[], ldap: boolean): [Record<string, unknown>, Record<string, unknown>] {
  const orgs: Record<string, unknown> = {}
  const teams: Record<string, unknown> = {}
  for (const e of entries) {
    const name = e.name.trim()
    if (!name) continue
    if (e.kind === 'organization') {
      const o: Record<string, unknown> = {}
      for (const r of orgRoles(ldap)) {
        if (e.exprs[r] !== null) o[r] = formatExpr(e.exprs[r])
        if (!e.remove[r]) o[`remove_${r}`] = false
      }
      if (!ldap && e.alias.trim()) o.organization_alias = e.alias.trim()
      orgs[name] = o
    } else {
      const t: Record<string, unknown> = { organization: e.organization.trim() }
      if (e.users !== null) t.users = formatExpr(e.users)
      if (!e.remove) t.remove = false
      teams[name] = t
    }
  }
  return [orgs, teams]
}

/** What stops the entries from being saved, by entry. */
export function mapProblems(entries: MapEntry[]): Record<number, string> {
  const out: Record<number, string> = {}
  const seen = new Map<string, number>()
  entries.forEach((e, i) => {
    const name = e.name.trim()
    const key = `${e.kind}:${name}`
    if (!name) out[i] = e.kind === 'team' ? 'name-team' : 'name-org'
    else if (seen.has(key)) out[i] = 'duplicate'
    else if (e.kind === 'team' && !e.organization.trim()) out[i] = 'organization'
    seen.set(key, i)
  })
  return out
}

/** A row of the table: an entry and one of its roles, or none when every one is left as it is. */
export interface GrantRow {
  entry: number
  role: Role | null
}

export function grantRows(entries: MapEntry[], ldap: boolean): GrantRow[] {
  const out: GrantRow[] = []
  entries.forEach((e, i) => {
    const roles = e.kind === 'team' ? (e.users !== null ? (['users'] as Role[]) : []) : orgRoles(ldap).filter((r) => e.exprs[r] !== null)
    if (!roles.length) out.push({ entry: i, role: null })
    for (const r of roles) out.push({ entry: i, role: r })
  })
  return out
}

/** A /pattern/flags item, as opposed to a username or email. */
export const isPattern = (s: string) => /^\/(.*)\/([im]+)?$/.test(s)

export const newOrg = (name = ''): OrgEntry => ({
  kind: 'organization',
  name,
  alias: '',
  exprs: { users: true, admins: null, auditors: null },
  remove: { users: true, admins: true, auditors: true },
})

export const newTeam = (): TeamEntry => ({ kind: 'team', name: '', organization: '', users: true, remove: true })

// LDAP's user flags by group: superusers and auditors, each one or more group DNs.

export interface UserFlags {
  superusers: string[]
  auditors: string[]
}

const strings = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : typeof v === 'string' && v.trim() ? [v] : [])

export function parseFlags(v: unknown): UserFlags {
  const o = isObject(v) ? v : {}
  return { superusers: strings(o.is_superuser), auditors: strings(o.is_system_auditor) }
}

export function formatFlags(f: UserFlags): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  if (f.superusers.length) out.is_superuser = formatExpr(f.superusers)
  if (f.auditors.length) out.is_system_auditor = formatExpr(f.auditors)
  return out
}

// SAML's organizations from attributes: an attribute per role, each
// listing the organizations its value names.

export interface SamlOrgAttr {
  attrs: Record<Role, string>
  remove: Record<Role, boolean>
}

const SAML_ORG_KEYS: Record<Role, [string, string]> = {
  users: ['saml_attr', 'remove'],
  admins: ['saml_admin_attr', 'remove_admins'],
  auditors: ['saml_auditor_attr', 'remove_auditors'],
}

export function parseSamlOrgAttr(v: unknown): SamlOrgAttr {
  const o = isObject(v) ? v : {}
  const out: SamlOrgAttr = { attrs: { users: '', admins: '', auditors: '' }, remove: { users: true, admins: true, auditors: true } }
  for (const [r, [attr, remove]] of Object.entries(SAML_ORG_KEYS) as [Role, [string, string]][]) {
    out.attrs[r] = typeof o[attr] === 'string' ? (o[attr] as string) : ''
    out.remove[r] = optBool(o[remove])
  }
  return out
}

export function formatSamlOrgAttr(a: SamlOrgAttr): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [r, [attr, remove]] of Object.entries(SAML_ORG_KEYS) as [Role, [string, string]][]) {
    if (a.attrs[r].trim()) out[attr] = a.attrs[r].trim()
    if (!a.remove[r]) out[remove] = false
  }
  return out
}

// SAML's teams from an attribute: the attribute, then which of its values
// are which team, in which organization.

export interface SamlTeam {
  team: string
  alias: string
  organization: string
}

export interface SamlTeamAttr {
  attr: string
  remove: boolean
  teams: SamlTeam[]
}

export function parseSamlTeamAttr(v: unknown): SamlTeamAttr {
  const o = isObject(v) ? v : {}
  const list = Array.isArray(o.team_org_map) ? o.team_org_map : []
  return {
    attr: typeof o.saml_attr === 'string' ? o.saml_attr : '',
    remove: optBool(o.remove),
    teams: list.map((t) => {
      const e = isObject(t) ? t : {}
      return { team: String(e.team ?? ''), alias: String(e.team_alias ?? ''), organization: String(e.organization ?? '') }
    }),
  }
}

export function formatSamlTeamAttr(a: SamlTeamAttr): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  if (a.attr.trim()) out.saml_attr = a.attr.trim()
  if (!a.remove) out.remove = false
  const teams = a.teams
    .filter((t) => t.team.trim())
    .map((t) => ({ team: t.team.trim(), ...(t.alias.trim() ? { team_alias: t.alias.trim() } : {}), organization: t.organization.trim() }))
  if (teams.length) out.team_org_map = teams
  return out
}

// SAML's superusers and auditors by attribute (internal/auth/saml.go,
// samlFlag): a role in the Role attribute, or an attribute, with one of the
// values if any are given. The flag is taken away otherwise, unless remove
// is false.

export interface SamlFlag {
  attr: string
  values: string[]
  roles: string[]
  remove: boolean
}

export type SamlFlags = Record<'superuser' | 'auditor', SamlFlag>

const SAML_FLAG_PREFIX = { superuser: ['is_superuser', 'remove_superusers'], auditor: ['is_system_auditor', 'remove_system_auditors'] } as const

export function parseSamlFlags(v: unknown): SamlFlags {
  const o = isObject(v) ? v : {}
  const flag = (k: 'superuser' | 'auditor'): SamlFlag => {
    const [p, remove] = SAML_FLAG_PREFIX[k]
    return { attr: typeof o[`${p}_attr`] === 'string' ? (o[`${p}_attr`] as string) : '', values: strings(o[`${p}_value`]), roles: strings(o[`${p}_role`]), remove: optBool(o[remove]) }
  }
  return { superuser: flag('superuser'), auditor: flag('auditor') }
}

export function formatSamlFlags(f: SamlFlags): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const k of ['superuser', 'auditor'] as const) {
    const [p, remove] = SAML_FLAG_PREFIX[k]
    const x = f[k]
    if (x.attr.trim()) out[`${p}_attr`] = x.attr.trim()
    if (x.values.length) out[`${p}_value`] = x.values
    if (x.roles.length) out[`${p}_role`] = x.roles
    if (!x.remove) out[remove] = false
  }
  return out
}

// How the editor and the table say an expression.

/** Everyone, these people (or groups), no one, or left as it is. */
export type WhoKind = 'all' | 'list' | 'none' | 'keep'

export const whoKind = (e: Expr): WhoKind => (e === null ? 'keep' : e === true ? 'all' : e === false ? 'none' : 'list')

export function withKind(e: Expr, k: WhoKind): Expr {
  if (k === whoKind(e)) return e
  return k === 'all' ? true : k === 'none' ? false : k === 'keep' ? null : []
}

export function whoKinds(ldap: boolean): [WhoKind, string][] {
  return [
    ['all', ldap ? tr('Every LDAP user') : tr('Everyone who signs in')],
    ['list', ldap ? tr('Members of these groups') : tr('These people')],
    ['none', tr('No one')],
    ['keep', tr('Not changed by the map')],
  ]
}

/** An expression in words for the table: a sentence, or a lead and the items to show as code. */
export function describeExpr(e: Expr, ldap: boolean): { text?: string; lead?: string; items: string[] } {
  if (e === null) return { text: tr('Not changed by the map'), items: [] }
  if (e === true) return { text: ldap ? tr('Every LDAP user') : tr('Everyone who signs in'), items: [] }
  if (e === false || !e.length) return { text: tr('No one'), items: [] }
  if (ldap) return { lead: tr('Members of'), items: e }
  return { lead: e.every(isPattern) ? tr('Usernames or emails matching') : undefined, items: e }
}

export const roleLabel: Record<Role, string> = { users: tr('Member'), admins: tr('Administrator'), auditors: tr('Auditor') }

/** What a problem of mapProblems means. */
export function problemText(p: string): string {
  switch (p) {
    case 'name-org':
      return tr('Not saved until the organization has a name.')
    case 'name-team':
      return tr('Not saved until the team has a name.')
    case 'duplicate':
      return tr('Two entries have this name: only the last one is saved.')
    case 'organization':
      return tr('Which organization is the team in?')
  }
  return p
}

/** A pattern as AWX writes it: /pattern/flags. */
export const asPattern = (s: string) => (isPattern(s) ? s : `/${s}/`)
