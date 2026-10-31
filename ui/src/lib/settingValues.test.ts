import { describe, expect, it } from 'vitest'
import { layoutRow } from './settingsLayout'
import {
  addItems,
  amountText,
  amountValue,
  bestUnit,
  controlKind,
  fieldValue,
  isSecret,
  mountsOf,
  mountsValue,
  pairsOf,
  pairsValue,
  recordsOf,
  recordsValue,
  searchesOf,
  searchesValue,
  withField,
  type Field,
} from './settingValues'

// The API's settings whose values are lists or objects, with their type and
// default (settings/all/, OPTIONS): those once edited as JSON.
const servers = ['', '1_', '2_', '3_', '4_', '5_']
const github = ['GITHUB', 'GITHUB_ORG', 'GITHUB_TEAM', 'GITHUB_ENTERPRISE', 'GITHUB_ENTERPRISE_ORG', 'GITHUB_ENTERPRISE_TEAM', 'GOOGLE_OAUTH2', 'AZUREAD_OAUTH2', 'SAML', '']
const structured: Record<string, [string, unknown]> = {
  ...Object.fromEntries(
    servers.flatMap((p) => [
      ...['CONNECTION_OPTIONS', 'GROUP_TYPE_PARAMS', 'ORGANIZATION_MAP', 'TEAM_MAP', 'USER_ATTR_MAP', 'USER_FLAGS_BY_GROUP'].map((f) => [`AUTH_LDAP_${p}${f}`, ['nested object', {}]]),
      ...['GROUP_SEARCH', 'USER_SEARCH'].map((f) => [`AUTH_LDAP_${p}${f}`, ['list', []]]),
    ]),
  ),
  ...Object.fromEntries(github.flatMap((p) => ['ORGANIZATION_MAP', 'TEAM_MAP'].map((f) => [`SOCIAL_AUTH_${p ? p + '_' : ''}${f}`, ['nested object', null]]))),
  AD_HOC_COMMANDS: ['list', ['command', 'shell']],
  AWX_ANSIBLE_CALLBACK_PLUGINS: ['list', []],
  AWX_ISOLATION_SHOW_PATHS: ['list', []],
  AWX_TASK_ENV: ['nested object', {}],
  CUSTOM_VENV_PATHS: ['list', []],
  GALAXY_TASK_ENV: ['nested object', { ANSIBLE_FORCE_COLOR: 'false' }],
  LOG_AGGREGATOR_LOGGERS: ['list', ['awx']],
  NAMED_URL_FORMATS: ['nested object', {}],
  NAMED_URL_GRAPH_NODES: ['nested object', {}],
  OAUTH2_PROVIDER: ['nested object', {}],
  OIDC_AUDIENCES: ['list', []],
  OIDC_MAPPING: ['list', []],
  OIDC_SCOPES: ['list', ['openid']],
  PROXY_IP_ALLOWED_LIST: ['list', []],
  REMOTE_HOST_HEADERS: ['list', ['REMOTE_ADDR']],
  SOCIAL_AUTH_GOOGLE_OAUTH2_AUTH_EXTRA_ARGUMENTS: ['nested object', {}],
  SOCIAL_AUTH_GOOGLE_OAUTH2_WHITELISTED_DOMAINS: ['list', []],
  SOCIAL_AUTH_SAML_ENABLED_IDPS: ['nested object', {}],
  SOCIAL_AUTH_SAML_EXTRA_DATA: ['list', null],
  SOCIAL_AUTH_SAML_ORGANIZATION_ATTR: ['nested object', null],
  SOCIAL_AUTH_SAML_ORG_INFO: ['nested object', {}],
  SOCIAL_AUTH_SAML_SECURITY_CONFIG: ['nested object', { requestedAuthnContext: false }],
  SOCIAL_AUTH_SAML_SP_EXTRA: ['nested object', null],
  SOCIAL_AUTH_SAML_SUPPORT_CONTACT: ['nested object', {}],
  SOCIAL_AUTH_SAML_TEAM_ATTR: ['nested object', null],
  SOCIAL_AUTH_SAML_TECHNICAL_CONTACT: ['nested object', {}],
  SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR: ['nested object', null],
}
// The API's read-only settings: OPTIONS has them under GET, not PUT.
const readOnly = (k: string) => /^(INSTALL_UUID|NAMED_URL_.*|.*_CALLBACK_URL|SOCIAL_AUTH_SAML_METADATA_URL)$/.test(k)
// The organization and team maps and their kin, edited with their own
// editors (awxMaps.ts); three free-form settings that have no effect stay JSON.
const maps = /(ORGANIZATION_MAP|TEAM_MAP|USER_FLAGS_BY_GROUP|USER_FLAGS_BY_ATTR|ORGANIZATION_ATTR|TEAM_ATTR)$/
const freeForm = new Set(['OAUTH2_PROVIDER', 'SOCIAL_AUTH_SAML_SP_EXTRA', 'SOCIAL_AUTH_SAML_EXTRA_DATA'])

const kindOf = (k: string, type: string, value?: unknown) => controlKind(k, type, layoutRow(k) ?? {}, !readOnly(k), value)

describe('the control of each setting', () => {
  it('edits no setting as JSON, but the free-form ones', () => {
    expect(Object.keys(structured)).toHaveLength(95)
    const json = Object.entries(structured)
      .filter(([k, [type, value]]) => kindOf(k, type, value) === 'json')
      .map(([k]) => k)
    expect(json.filter((k) => !freeForm.has(k))).toEqual([])
    const mapKinds = Object.keys(structured)
      .filter((k) => maps.test(k))
      .map((k) => kindOf(k, 'nested object'))
    expect(mapKinds).toHaveLength(41)
    expect(new Set(mapKinds)).toEqual(new Set(['awx-maps', 'ldap-flags', 'saml-org-attr', 'saml-team-attr', 'saml-flags']))
    expect(kindOf('NAMED_URL_FORMATS', 'nested object')).toBe('readonly')
  })

  it('picks a control by what the layout says, then by type', () => {
    expect(kindOf('AWX_TASK_ENV', 'nested object')).toBe('pairs')
    expect(kindOf('AUTH_LDAP_3_USER_SEARCH', 'list', [])).toBe('ldap-search')
    expect(kindOf('AUTH_LDAP_USER_ATTR_MAP', 'nested object')).toBe('fields')
    expect(kindOf('SOCIAL_AUTH_SAML_ENABLED_IDPS', 'nested object')).toBe('records')
    expect(kindOf('AWX_ISOLATION_SHOW_PATHS', 'list', [])).toBe('paths')
    expect(kindOf('LOG_AGGREGATOR_LOGGERS', 'list', ['awx'])).toBe('checks')
    expect(kindOf('AD_HOC_COMMANDS', 'list', ['command'])).toBe('chips')
    expect(kindOf('CUSTOM_LOGO', 'string')).toBe('image')
    expect(kindOf('CUSTOM_LOGIN_INFO', 'string')).toBe('lines')
    expect(kindOf('TOWER_URL_BASE', 'string')).toBe('text')
    expect(kindOf('MAX_FORKS', 'integer')).toBe('amount')
    expect(kindOf('ACTIVITY_STREAM_ENABLED', 'boolean')).toBe('switch')
    expect(kindOf('LOG_AGGREGATOR_LEVEL', 'choice')).toBe('choice')
    expect(kindOf('DEFAULT_EXECUTION_ENVIRONMENT', 'field')).toBe('object')
    expect(kindOf('SOCIAL_AUTH_GITHUB_CALLBACK_URL', 'string')).toBe('readonly')
    expect(kindOf('SOCIAL_AUTH_OIDC_SECRET', 'string')).toBe('secret')
    expect(kindOf('SOCIAL_AUTH_SAML_SP_PRIVATE_KEY', 'string')).toBe('secret')
    // A list whose items are not words, or not a list yet, stays JSON.
    expect(kindOf('A_NEW_SETTING', 'list', [['a', 'b']])).toBe('json')
    expect(kindOf('A_NEW_SETTING', 'list', null)).toBe('json')
  })

  it('knows the secrets by name, as the API encrypts them', () => {
    for (const k of ['AUTH_LDAP_BIND_PASSWORD', 'AUTH_LDAP_5_BIND_PASSWORD', 'LOG_AGGREGATOR_PASSWORD', 'SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_SECRET', 'SOCIAL_AUTH_SAML_SP_PRIVATE_KEY'])
      expect(isSecret(k), k).toBe(true)
    for (const k of ['SOCIAL_AUTH_GITHUB_KEY', 'SOCIAL_AUTH_SAML_SP_PUBLIC_CERT', 'LOG_AGGREGATOR_USERNAME']) expect(isSecret(k), k).toBe(false)
  })
})

describe('amounts', () => {
  it('shows a duration in the largest unit it is a whole number of', () => {
    expect(bestUnit(7200, 'duration')).toBe(3600)
    expect(bestUnit(1800, 'duration')).toBe(60)
    expect(bestUnit(90, 'duration')).toBe(1)
    expect(bestUnit(172800, 'duration')).toBe(86400)
    expect(bestUnit(0, 'duration')).toBe(60)
    expect(bestUnit(1048576, 'size')).toBe(1048576)
    expect(bestUnit(1536, 'size')).toBe(1)
    expect(amountText(7200, 3600)).toBe('2')
    expect(amountText(null, 60)).toBe('')
  })

  it('stores what is typed in the unit stored', () => {
    expect(amountValue('2', 3600)).toBe(7200)
    expect(amountValue('1.5', 60)).toBe(90)
    expect(amountValue('30')).toBe(30)
    expect(amountValue('', 60)).toBeNull()
    expect(amountValue('soon', 60)).toBeNull()
  })
})

describe('lists and objects', () => {
  it('edits variables as names and values', () => {
    const env = { ANSIBLE_FORCE_COLOR: 'false', GIT_SSH_COMMAND: 'ssh -o StrictHostKeyChecking=no' }
    expect(pairsValue(pairsOf(env))).toEqual(env)
    // Rows without a name are left out; values stay text.
    expect(pairsValue([{ key: ' HTTP_PROXY ', value: 'http://proxy:3128' }, { key: '', value: 'x' }, { key: 'N', value: '3' }])).toEqual({ HTTP_PROXY: 'http://proxy:3128', N: '3' })
    // LDAP's options keep their numbers.
    const opts = pairsOf({ OPT_NETWORK_TIMEOUT: 30, OPT_X_TLS_CACERTFILE: '/etc/ca.pem' })
    expect(opts).toEqual([
      { key: 'OPT_NETWORK_TIMEOUT', value: '30' },
      { key: 'OPT_X_TLS_CACERTFILE', value: '/etc/ca.pem' },
    ])
    expect(pairsValue(opts, true)).toEqual({ OPT_NETWORK_TIMEOUT: 30, OPT_X_TLS_CACERTFILE: '/etc/ca.pem' })
    expect(pairsOf(null)).toEqual([])
  })

  it('adds items to a list once each', () => {
    expect(addItems(['ping'], 'setup, ping\nwin_ping ')).toEqual(['ping', 'setup', 'win_ping'])
    expect(addItems([], ' , ')).toEqual([])
  })

  it('reads and writes mounts as AWX does', () => {
    expect(mountsOf(['/etc/pki/ca-trust', '/srv/data:/data:ro'])).toEqual([
      { host: '/etc/pki/ca-trust', container: '', options: '' },
      { host: '/srv/data', container: '/data', options: 'ro' },
    ])
    expect(mountsValue([{ host: '/etc/pki', container: '', options: '' }])).toEqual(['/etc/pki'])
    // Options need a container path: the host's when none is given.
    expect(mountsValue([{ host: '/etc/pki', container: '', options: 'ro,z' }])).toEqual(['/etc/pki:/etc/pki:ro,z'])
    expect(mountsValue([{ host: ' ', container: '/x', options: '' }])).toEqual([])
  })

  it('reads one LDAP search or several, and writes them back the same way', () => {
    const one = ['OU=Users,DC=example,DC=com', 'SCOPE_SUBTREE', '(uid=%(user)s)']
    expect(searchesOf(one)).toEqual([{ base: one[0], scope: one[1], filter: one[2] }])
    expect(searchesValue(searchesOf(one))).toEqual(one)
    const two = [one, ['OU=Admins,DC=example,DC=com', 'SCOPE_ONELEVEL', '(cn=%(user)s)']]
    expect(searchesValue(searchesOf(two))).toEqual(two)
    expect(searchesOf([])).toEqual([])
    expect(searchesValue([{ base: '', scope: 'SCOPE_SUBTREE', filter: '' }])).toEqual([])
  })

  it('edits an object as a form, keeping the keys it does not show', () => {
    const name: Field = { key: 'givenName', label: 'Name' }
    expect(withField({ emailAddress: 'a@example.com' }, name, 'Ann')).toEqual({ emailAddress: 'a@example.com', givenName: 'Ann' })
    expect(withField({ givenName: 'Ann', other: 1 }, name, '')).toEqual({ other: 1 })
    expect(withField(null, name, 'Ann')).toEqual({ givenName: 'Ann' })
    // SAML's authentication context is false when none is asked for.
    const ctx: Field = { key: 'requestedAuthnContext', label: 'Context', empty: false }
    expect(withField({ requestedAuthnContext: 'urn:x' }, ctx, '')).toEqual({ requestedAuthnContext: false })
    expect(fieldValue({ requestedAuthnContext: false }, ctx)).toBe('')
    expect(fieldValue({ requestedAuthnContext: ['urn:a', 'urn:b'] }, ctx)).toBe('urn:a, urn:b')
    expect(fieldValue({ wantNameId: true }, { key: 'wantNameId', label: 'NameID', kind: 'switch' })).toBe(true)
  })

  it('edits named objects, without those that have no name', () => {
    const idps = { okta: { entity_id: 'http://www.okta.com/x', url: 'https://example.okta.com/sso' } }
    expect(recordsValue(recordsOf(idps))).toEqual(idps)
    expect(recordsValue([...recordsOf(idps), { name: ' ', value: { url: 'x' } }])).toEqual(idps)
    expect(recordsOf(null)).toEqual([])
  })
})
