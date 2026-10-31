import { describe, expect, it } from 'vitest'
import {
  asPattern,
  lines,
  mapLogin,
  parseAttributes,
  describeExpr,
  formatFlags,
  formatMaps,
  formatSamlFlags,
  formatSamlOrgAttr,
  formatSamlTeamAttr,
  grantRows,
  mapProblems,
  newOrg,
  newTeam,
  parseExpr,
  parseFlags,
  parseMaps,
  parseSamlFlags,
  parseSamlOrgAttr,
  parseSamlTeamAttr,
  whoKind,
  withKind,
  type MapEntry,
} from './awxMaps'
import { addItems } from './settingValues'

// AWX's own examples (awx/sso/conf.py).
const awxOrgMap = {
  Default: { users: true },
  'Test Org': { admins: ['admin@example.com'], users: true },
  'Test Org 2': { admins: ['admin@example.com', '/^tower-[^@]+?@.*$/'], remove_admins: true, users: '/^[^@].*?@example\\.com$/i', remove_users: true },
}
const awxTeamMap = {
  'My Team': { organization: 'Test Org', users: ['/^[^@]+?@test\\.example\\.com$/'], remove: true },
  'Other Team': { organization: 'Test Org 2', users: ['/^[^@]+?@test\\.example\\.com$/'], remove: false },
}

// The keys internal/auth/awxmap.go accepts, as AWX does.
const socialOrgKeys = ['admins', 'users', 'remove_admins', 'remove_users', 'organization_alias']
const ldapOrgKeys = ['admins', 'auditors', 'users', 'remove_admins', 'remove_auditors', 'remove_users']
const teamKeys = ['organization', 'users', 'remove']
const keysOf = (m: Record<string, unknown>) => [...new Set(Object.values(m).flatMap((e) => Object.keys(e as object)))]

describe('organization and team maps', () => {
  it('read AWX\'s maps and write them back the same, defaults left out', () => {
    const entries = parseMaps(awxOrgMap, awxTeamMap)
    expect(entries.map((e) => `${e.kind}:${e.name}`)).toEqual(['organization:Default', 'organization:Test Org', 'organization:Test Org 2', 'team:My Team', 'team:Other Team'])
    const [orgs, teams] = formatMaps(entries, false)
    expect(orgs).toEqual({
      Default: { users: true },
      'Test Org': { admins: 'admin@example.com', users: true },
      'Test Org 2': { admins: ['admin@example.com', '/^tower-[^@]+?@.*$/'], users: '/^[^@].*?@example\\.com$/i' },
    })
    expect(teams).toEqual({
      'My Team': { organization: 'Test Org', users: '/^[^@]+?@test\\.example\\.com$/' },
      'Other Team': { organization: 'Test Org 2', users: '/^[^@]+?@test\\.example\\.com$/', remove: false },
    })
    // Read again, the same entries: nothing lost.
    expect(parseMaps(orgs, teams)).toEqual(entries)
    expect(keysOf(orgs).every((k) => socialOrgKeys.includes(k))).toBe(true)
    expect(keysOf(teams).every((k) => teamKeys.includes(k))).toBe(true)
  })

  it('read the spellings AWX accepts', () => {
    expect(parseExpr('True')).toBe(true)
    expect(parseExpr('off')).toBe(false)
    expect(parseExpr('null')).toBe(null)
    expect(parseExpr(undefined)).toBe(null)
    expect(parseExpr(1)).toBe(true)
    expect(parseExpr('alice')).toEqual(['alice'])
    const [e] = parseMaps({ Ops: { users: 'yes', remove_users: 'False', organization_alias: 'Operations' } }, null)
    expect(e).toMatchObject({ alias: 'Operations', exprs: { users: true, admins: null }, remove: { users: false, admins: true } })
  })

  it('keep LDAP\'s auditors, and never write an alias LDAP refuses', () => {
    const ldap = { 'LDAP Org': { admins: 'cn=admins,ou=groups,dc=example,dc=com', auditors: 'cn=audit,ou=groups,dc=example,dc=com', users: true, remove_auditors: false } }
    const entries = parseMaps(ldap, {})
    const [orgs] = formatMaps(entries, true)
    expect(orgs).toEqual(ldap)
    expect(keysOf(orgs).every((k) => ldapOrgKeys.includes(k))).toBe(true)
    const [social] = formatMaps(entries, false)
    expect(social['LDAP Org']).not.toHaveProperty('auditors')
    const withAlias = { ...newOrg('X'), alias: 'Y' }
    expect(formatMaps([withAlias], true)[0]).toEqual({ X: { users: true } })
    expect(formatMaps([withAlias], false)[0]).toEqual({ X: { users: true, organization_alias: 'Y' } })
  })

  it('save entries only once named, and say what is missing', () => {
    const entries: MapEntry[] = [newOrg(), { ...newTeam(), name: 'Web' }, newOrg('Default'), newOrg('Default'), newTeam()]
    expect(mapProblems(entries)).toEqual({ 0: 'name-org', 1: 'organization', 3: 'duplicate', 4: 'name-team' })
    const [orgs, teams] = formatMaps(entries, false)
    expect(Object.keys(orgs)).toEqual(['Default'])
    // The server says so too: a team's organization is required.
    expect(teams).toEqual({ Web: { organization: '', users: true } })
  })

  it('draw a row per role given, and one for an entry that changes nothing', () => {
    const entries = parseMaps({ Default: { users: true, admins: ['alice', 'bob'] }, Quiet: {} }, { Web: { organization: 'Default', users: null } })
    expect(grantRows(entries, false)).toEqual([
      { entry: 0, role: 'users' },
      { entry: 0, role: 'admins' },
      { entry: 1, role: null },
      { entry: 2, role: null },
    ])
  })

  it('say who matches in words', () => {
    expect(describeExpr(true, false).text).toBe('Everyone who signs in')
    expect(describeExpr(true, true).text).toBe('Every LDAP user')
    expect(describeExpr([], false).text).toBe('No one')
    expect(describeExpr(null, false).text).toBe('Not changed by the map')
    expect(describeExpr(['/^web-/'], false)).toEqual({ lead: 'Usernames or emails matching', items: ['/^web-/'] })
    expect(describeExpr(['alice', '/^web-/'], false)).toEqual({ lead: undefined, items: ['alice', '/^web-/'] })
    expect(describeExpr(['cn=ops,dc=example'], true)).toEqual({ lead: 'Members of', items: ['cn=ops,dc=example'] })
  })

  it('switch who gets a role, keeping the list while it stays a list', () => {
    expect(whoKind(['a'])).toBe('list')
    expect(withKind(['a'], 'list')).toEqual(['a'])
    expect(withKind(['a'], 'all')).toBe(true)
    expect(withKind(true, 'list')).toEqual([])
    expect(withKind(true, 'keep')).toBe(null)
    expect(withKind(null, 'none')).toBe(false)
  })

  it('take patterns between slashes, and distinguished names one per line', () => {
    expect(asPattern('^web-')).toBe('/^web-/')
    expect(asPattern('/^web-/i')).toBe('/^web-/i')
    expect(addItems([], 'cn=a,dc=x\ncn=b,dc=x', true)).toEqual(['cn=a,dc=x', 'cn=b,dc=x'])
    expect(addItems([], 'alice, bob')).toEqual(['alice', 'bob'])
  })
})

describe('LDAP user flags', () => {
  it('read and write AWX\'s flags by group', () => {
    const v = { is_superuser: ['cn=admins,dc=example'], is_system_auditor: 'cn=audit,dc=example' }
    const f = parseFlags(v)
    expect(f).toEqual({ superusers: ['cn=admins,dc=example'], auditors: ['cn=audit,dc=example'] })
    expect(formatFlags(f)).toEqual({ is_superuser: 'cn=admins,dc=example', is_system_auditor: 'cn=audit,dc=example' })
    expect(formatFlags({ superusers: [], auditors: [] })).toEqual({})
  })
})

describe('SAML attribute maps', () => {
  it('read and write organizations from attributes', () => {
    const v = { saml_attr: 'organization', saml_admin_attr: 'organization_admin', saml_auditor_attr: 'organization_auditor', remove: true, remove_admins: false }
    const a = parseSamlOrgAttr(v)
    expect(a.remove).toEqual({ users: true, admins: false, auditors: true })
    expect(formatSamlOrgAttr(a)).toEqual({ saml_attr: 'organization', saml_admin_attr: 'organization_admin', saml_auditor_attr: 'organization_auditor', remove_admins: false })
  })

  it('read and write teams from an attribute', () => {
    const v = {
      saml_attr: 'team',
      remove: false,
      team_org_map: [
        { team: 'Marketing', organization: 'Red Hat' },
        { team: 'Engineering', team_alias: 'Eng', organization: 'Ansible' },
      ],
    }
    const a = parseSamlTeamAttr(v)
    expect(formatSamlTeamAttr(a)).toEqual(v)
    // A row with no value is not saved yet.
    expect(formatSamlTeamAttr({ ...a, teams: [...a.teams, { team: '', alias: '', organization: 'X' }] })).toEqual(v)
  })

  it('read and write flags by role or attribute, removed unless said otherwise as the server does', () => {
    const v = { is_superuser_role: ['awx_admins'], is_superuser_attr: 'blueGroups', is_superuser_value: ['cn=My-Sys-Admins,ou=memberlist'], is_system_auditor_attr: 'auditor', remove_system_auditors: false }
    const f = parseSamlFlags(v)
    expect(f.superuser).toEqual({ attr: 'blueGroups', values: ['cn=My-Sys-Admins,ou=memberlist'], roles: ['awx_admins'], remove: true })
    expect(f.auditor.remove).toBe(false)
    expect(formatSamlFlags(f)).toEqual(v)
    expect(formatSamlFlags(parseSamlFlags({}))).toEqual({})
  })
})

describe('trying the maps', () => {
  it('finds the login of each organization map, with the settings it reads', () => {
    expect(mapLogin('SOCIAL_AUTH_ORGANIZATION_MAP')).toEqual({ login: 'default', kind: 'social', keys: ['SOCIAL_AUTH_ORGANIZATION_MAP', 'SOCIAL_AUTH_TEAM_MAP'] })
    expect(mapLogin('SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_ORGANIZATION_MAP')?.login).toBe('github-enterprise-team')
    expect(mapLogin('SOCIAL_AUTH_GITHUB_ORGANIZATION_MAP')?.keys).toEqual(['SOCIAL_AUTH_GITHUB_ORGANIZATION_MAP', 'SOCIAL_AUTH_GITHUB_TEAM_MAP'])
    expect(mapLogin('SOCIAL_AUTH_AZUREAD_OAUTH2_ORGANIZATION_MAP')?.login).toBe('azuread-oauth2')
    expect(mapLogin('SOCIAL_AUTH_SAML_ORGANIZATION_MAP')).toEqual({
      login: 'saml',
      kind: 'saml',
      keys: ['SOCIAL_AUTH_SAML_ORGANIZATION_MAP', 'SOCIAL_AUTH_SAML_TEAM_MAP', 'SOCIAL_AUTH_SAML_ORGANIZATION_ATTR', 'SOCIAL_AUTH_SAML_TEAM_ATTR', 'SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR'],
    })
    expect(mapLogin('AUTH_LDAP_ORGANIZATION_MAP')).toEqual({ login: 'ldap', kind: 'ldap', keys: ['AUTH_LDAP_ORGANIZATION_MAP', 'AUTH_LDAP_TEAM_MAP', 'AUTH_LDAP_USER_FLAGS_BY_GROUP'] })
    expect(mapLogin('AUTH_LDAP_3_ORGANIZATION_MAP')?.keys).toEqual(['AUTH_LDAP_3_ORGANIZATION_MAP', 'AUTH_LDAP_3_TEAM_MAP', 'AUTH_LDAP_3_USER_FLAGS_BY_GROUP'])
    expect(mapLogin('AUTH_LDAP_3_ORGANIZATION_MAP')?.login).toBe('ldap3')
  })

  it('knows no other map', () => {
    expect(mapLogin('SOCIAL_AUTH_TEAM_MAP')).toBeNull()
    expect(mapLogin('SOCIAL_AUTH_GITLAB_ORGANIZATION_MAP')).toBeNull()
    expect(mapLogin('AUTH_LDAP_6_ORGANIZATION_MAP')).toBeNull()
    expect(mapLogin('SOCIAL_AUTH_toString_ORGANIZATION_MAP')).toBeNull()
  })

  it('reads groups one distinguished name per line', () => {
    expect(lines(' CN=Eng, OU=Groups,DC=example,DC=com \n\n cn=ops,dc=example,dc=com')).toEqual(['CN=Eng, OU=Groups,DC=example,DC=com', 'cn=ops,dc=example,dc=com'])
  })

  it('reads SAML attributes one value per line', () => {
    expect(
      parseAttributes('memberOf = CN=Eng,DC=example,DC=com\nmemberOf=ops\nurn:oid:0.9.2342.19200300.100.1.3 = alice@example.com\nRole = \njust a line\n= value'),
    ).toEqual({
      attributes: { memberOf: ['CN=Eng,DC=example,DC=com', 'ops'], 'urn:oid:0.9.2342.19200300.100.1.3': ['alice@example.com'], Role: [''] },
      bad: ['just a line', '= value'],
    })
  })
})
