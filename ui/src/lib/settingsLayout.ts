import { asIs, tr } from './i18n'
import type { GitHubVariant } from './loginMethods'
import type { Field, Kind, UnitKind } from './settingValues'

// The settings pages, laid out by what an admin sets up rather than by AWX's
// categories: every setting the API has is placed here, once, on a page, in
// a section, in order, with its label and the control that edits it. The
// API's categories and keys do not change; a page saves its changes to the
// categories they belong to.

export interface Row {
  /** The settings the row edits: one, or several edited together. */
  keys: string[]
  /** What the row sets, in plain words; the API's label when absent. */
  label?: string
  /** What it does; the API's help text when absent. */
  help?: string
  /** In a row of several settings, each one's own label, in the keys' order. */
  parts?: string[]
  /** How the setting is edited, when its type alone does not say (see
   * settingValues.ts). In a row of several, the first setting's. */
  control?: Kind
  /** An amount's unit: durations (stored in seconds) and sizes (in bytes)
   * are shown in a unit chosen from theirs; other words follow the number. */
  unit?: UnitKind | string
  /** The amount that means none, and what it is called ("No limit"). */
  none?: [number, string]
  /** A list's possible items, or a choice's, in order: each one's value and
   * label. A choice's labels replace the API's. */
  options?: [string, string][]
  /** Text on several lines: a certificate, the login page's text. */
  lines?: boolean
  /** Names and values: the two columns' headings; whether numbers and
   * true or false are stored as such (coerce) rather than as text. */
  columns?: [string, string]
  coerce?: boolean
  /** An object edited as a form, or each of a set of named objects. */
  fields?: Field[]
  /** What a named object's name is ("Language"). */
  name?: string
  /** The words of the button that adds an item, a variable, a record. */
  add?: string
  /** LDAP searches: several may be given. */
  multiple?: boolean
  /** A switch's words for off and on. */
  states?: [string, string]
  /** A switch drawn as its two states side by side, neither the default look. */
  segmented?: boolean
  /** What the riskier value lets someone do, under the control. */
  consequence?: string
  /** The switch is on when the setting is false (DISABLE_LOCAL_AUTH). */
  invert?: boolean
  /** The control takes the row's whole width, under its label. */
  wide?: boolean
}

export interface Section {
  /** Its anchor in the page. */
  id: string
  title: string
  intro?: string
  rows: Row[]
  /** Settings kept for AWX's clients, which do nothing in awxnext. */
  noEffect?: boolean
  /** Folded while this is false (an LDAP server without a URI). */
  on?: (values: Record<string, unknown>) => boolean
  /** Lists the pages under this one (the login methods under Sign-in). */
  children?: boolean
  /** Ends with the connection test of this login: ldap, ldap1… ldap5, oidc or saml. */
  test?: string
}

export interface SettingsPage {
  slug: string
  title: string
  /** The menu's name, when shorter than the title. */
  menu?: string
  blurb: string
  /** The page it is under in the menu: a login method under Sign-in. */
  parent?: string
  /** The AWX categories it shows: a setting this file does not place lands on its category's page. */
  categories: string[]
  sections: Section[]
}

const row = (key: string, more: Partial<Row> = {}): Row => ({ keys: [key], ...more })
const filled = (v: unknown) => typeof v === 'string' && v.trim() !== ''
/** An organization map and its team map, edited together as entries (awxMaps.ts). */
const mapsRow = (org: string, team: string, help?: string): Row => ({ keys: [org, team], label: tr('Organizations and teams'), parts: [tr('Organization map'), tr('Team map')], control: 'awx-maps', wide: true, ...(help ? { help } : {}) })
const socialMapsHelp = tr('Applied at each login: people join an organization or a team when they match, by username or email.')
const envVars: Partial<Row> = { control: 'pairs', columns: [tr('Variable'), tr('Value')], add: tr('Add a variable') }

// AWX's six LDAP servers: AUTH_LDAP_* is the default one, AUTH_LDAP_1_* to
// AUTH_LDAP_5_* the others, tried in order. Fields in the order they are set up.
const ldapFields = [
  'SERVER_URI',
  'START_TLS',
  'BIND_DN',
  'BIND_PASSWORD',
  'USER_SEARCH',
  'USER_DN_TEMPLATE',
  'USER_ATTR_MAP',
  'GROUP_SEARCH',
  'GROUP_TYPE',
  'GROUP_TYPE_PARAMS',
  'REQUIRE_GROUP',
  'DENY_GROUP',
  'USER_FLAGS_BY_GROUP',
  'ORGANIZATION_MAP',
  'TEAM_MAP',
  'CONNECTION_OPTIONS',
]

// Each LDAP field's label, and how those that are not text are edited.
const ldapLabels: Record<string, string> = {
  SERVER_URI: tr('Server URI'),
  START_TLS: tr('StartTLS'),
  BIND_DN: tr('Bind DN'),
  BIND_PASSWORD: tr('Bind password'),
  USER_SEARCH: tr('User search'),
  USER_DN_TEMPLATE: tr('User DN template'),
  USER_ATTR_MAP: tr('User attributes'),
  GROUP_SEARCH: tr('Group search'),
  GROUP_TYPE: tr('Group type'),
  GROUP_TYPE_PARAMS: tr('Group type parameters'),
  REQUIRE_GROUP: tr('Group required to log in'),
  DENY_GROUP: tr('Group refused'),
  USER_FLAGS_BY_GROUP: tr('Superusers and auditors by group'),
  ORGANIZATION_MAP: tr('Organization map'),
  TEAM_MAP: tr('Team map'),
  CONNECTION_OPTIONS: tr('Connection options'),
}

const ldapControls: Record<string, Partial<Row>> = {
  USER_FLAGS_BY_GROUP: { control: 'ldap-flags', help: tr('Set at each login: members of one of the groups get the flag, the others lose it.') },
  USER_SEARCH: {
    control: 'ldap-search',
    multiple: true,
    help: tr('Where users are found: a base DN, a scope, and a filter in which %(user)s is the name typed. With several searches, exactly one must find the user.'),
  },
  USER_ATTR_MAP: {
    control: 'fields',
    help: tr('The LDAP attributes the user\'s name and email are read from, at each login.'),
    fields: [
      { key: 'first_name', label: tr('First name'), placeholder: asIs('givenName') },
      { key: 'last_name', label: tr('Last name'), placeholder: asIs('sn') },
      { key: 'email', label: tr('Email'), placeholder: asIs('mail') },
    ],
  },
  GROUP_SEARCH: { control: 'ldap-search', help: tr('Where the user\'s groups are. Organization and team maps only see the groups it finds.') },
  GROUP_TYPE_PARAMS: { control: 'pairs', columns: [tr('Parameter'), tr('Value')], add: tr('Add a parameter'), help: tr('Parameters of the group type, such as member_attr and name_attr.') },
  CONNECTION_OPTIONS: {
    control: 'pairs',
    columns: [tr('Option'), tr('Value')],
    coerce: true,
    add: tr('Add an option'),
    help: tr('python-ldap options. Used: OPT_NETWORK_TIMEOUT (seconds), OPT_X_TLS_REQUIRE_CERT (0: the server\'s certificate is not checked) and OPT_X_TLS_CACERTFILE. Referrals are never followed.'),
  },
}

const ldapSections: Section[] = ['', '1_', '2_', '3_', '4_', '5_'].map((p, n) => ({
  id: n ? `server-${n}` : 'default-server',
  title: n ? tr('Server {n}', { n }) : tr('Default server'),
  on: n ? (v) => filled(v[`AUTH_LDAP_${p}SERVER_URI`]) : undefined,
  test: n ? `ldap${n}` : 'ldap',
  rows: ldapFields
    .filter((f) => f !== 'TEAM_MAP')
    .map((f) =>
      f === 'ORGANIZATION_MAP'
        ? mapsRow(`AUTH_LDAP_${p}ORGANIZATION_MAP`, `AUTH_LDAP_${p}TEAM_MAP`, tr('Applied at each login: people join an organization or a team when they are members of its groups.'))
        : row(`AUTH_LDAP_${p}${f}`, { label: ldapLabels[f], ...ldapControls[f] }),
    ),
}))

// GitHub's six AWX categories: github.com or GitHub Enterprise, then any
// account, the members of an organization, or the members of a team.
const githubVariants: [string, string, string, string[]][] = [
  ['github', 'GITHUB_', tr('github.com: any account'), []],
  ['github-org', 'GITHUB_ORG_', tr('github.com: members of an organization'), ['NAME']],
  ['github-team', 'GITHUB_TEAM_', tr('github.com: members of a team'), ['ID']],
  ['github-enterprise', 'GITHUB_ENTERPRISE_', tr('GitHub Enterprise: any account'), ['URL', 'API_URL']],
  ['github-enterprise-org', 'GITHUB_ENTERPRISE_ORG_', tr('GitHub Enterprise: members of an organization'), ['URL', 'API_URL', 'NAME']],
  ['github-enterprise-team', 'GITHUB_ENTERPRISE_TEAM_', tr('GitHub Enterprise: members of a team'), ['URL', 'API_URL', 'ID']],
]

// The fields of the OAuth2 logins, in their providers' words.
const oauthLabels: Record<string, string> = {
  CALLBACK_URL: tr('Callback URL'),
  URL: tr('Server URL'),
  API_URL: tr('API URL'),
  KEY: tr('Client ID'),
  SECRET: tr('Client secret'),
  NAME: tr('Organization'),
  ID: tr('Team ID'),
  ORGANIZATION_MAP: tr('Organization map'),
  TEAM_MAP: tr('Team map'),
}
const oauthRow = (p: string, f: string, more: Partial<Row> = {}) => row(`SOCIAL_AUTH_${p}${f}`, { label: oauthLabels[f], ...more })
const oauthMaps = (p: string) => mapsRow(`SOCIAL_AUTH_${p}ORGANIZATION_MAP`, `SOCIAL_AUTH_${p}TEAM_MAP`, socialMapsHelp)

const githubSections: Section[] = githubVariants.map(([slug, p, title, extra]) => {
  const where = extra.filter((f) => f.endsWith('URL'))
  const who = extra.filter((f) => !f.endsWith('URL'))
  return {
    id: slug,
    title,
    rows: [...['CALLBACK_URL', ...where, 'KEY', 'SECRET', ...who].map((f) => oauthRow(p, f)), oauthMaps(p)],
  }
})

/** The rows of the GitHub form for one of its six variants: where the
 * Enterprise server is, the organization or team people must be in, the
 * OAuth app, then the maps. */
export function githubForm(v: GitHubVariant): { where: Row[]; who?: Row; app: Row[]; maps: Row } {
  const p = v.prefix.replace(/^SOCIAL_AUTH_/, '')
  return {
    where: v.host === 'enterprise' ? [oauthRow(p, 'URL'), oauthRow(p, 'API_URL')] : [],
    who: v.who === 'org' ? oauthRow(p, 'NAME') : v.who === 'team' ? oauthRow(p, 'ID') : undefined,
    app: [oauthRow(p, 'CALLBACK_URL', { label: tr('Authorization callback URL'), help: tr('Made from the service URL, on the System page.') }), oauthRow(p, 'KEY'), oauthRow(p, 'SECRET')],
    maps: oauthMaps(p),
  }
}

const oauth = (p: string, extra: Row[] = []): Section[] => [
  {
    id: 'provider',
    title: tr('Provider'),
    // Google and Microsoft call the callback URL the redirect URI.
    rows: [oauthRow(p, 'CALLBACK_URL', { label: tr('Redirect URI') }), oauthRow(p, 'KEY'), oauthRow(p, 'SECRET'), ...extra],
  },
  { id: 'maps', title: tr('Organizations and teams'), rows: [oauthMaps(p)] },
]

export const settingsPages: SettingsPage[] = [
  {
    slug: 'system',
    title: tr('System'),
    blurb: tr('The service\'s address, the activity stream, what organization admins may do, and proxies.'),
    categories: ['system', 'named-url'],
    sections: [
      { id: 'service', title: tr('Service'), rows: [
          row('TOWER_URL_BASE', { label: tr('Service URL'), help: tr('The address people reach awxnext at: links in notifications, callback URLs and SAML\'s entity ID start with it.') }),
          row('DEFAULT_EXECUTION_ENVIRONMENT', { label: tr('Default execution environment') }),
          row('INSTALL_UUID', { label: tr('Installation ID') }),
        ],
      },
      { id: 'activity-stream', title: tr('Activity stream'), rows: [row('ACTIVITY_STREAM_ENABLED', { label: tr('Record the activity stream') }), row('ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC', { label: tr('Also record inventory syncs') })] },
      { id: 'organization-admins', title: tr('Organization admins'), rows: [row('ORG_ADMINS_CAN_SEE_ALL_USERS', { label: tr('Organization admins see every user') }), row('MANAGE_ORGANIZATION_AUTH', { label: tr('Organization admins manage users and teams') })] },
      {
        id: 'proxies',
        title: tr('Behind a proxy'),
        rows: [
          row('REMOTE_HOST_HEADERS', { label: tr('Headers that carry the client\'s address') }),
          row('PROXY_IP_ALLOWED_LIST', { label: tr('Proxies trusted to set them') }),
        ],
      },
      { id: 'named-url', title: tr('Named URLs'), rows: [row('NAMED_URL_FORMATS', { label: tr('Named URL formats') }), row('NAMED_URL_GRAPH_NODES', { label: tr('Named URL graph') })] },
    ],
  },
  {
    slug: 'signin',
    title: tr('Sign-in'),
    blurb: tr('How people log in, how long their sessions last, and what local passwords must contain.'),
    categories: ['authentication'],
    sections: [
      {
        id: 'login-methods',
        title: tr('Login methods'),
        intro: tr('Each method on is offered on the login page. People who sign in through one are created on their first login and placed in organizations and teams by its maps.'),
        rows: [],
        children: true,
      },
      {
        id: 'sessions',
        title: tr('Sessions'),
        rows: [
          row('SESSION_COOKIE_AGE', { label: tr('Sign out after inactivity'), help: tr('A session ends after this long without activity: a minute at least.'), unit: 'duration' }),
          row('SESSIONS_PER_USER', { label: tr('Sessions per person'), help: tr('Signing in once more than this ends the person\'s oldest session.'), none: [-1, tr('No limit')] }),
          row('AUTH_BASIC_ENABLED', {
            label: tr('Username and password in API calls'),
            help: tr('HTTP Basic authentication. The awx CLI and the awx.awx collection use it when given a username and password; tokens work either way.'),
            states: [tr('Refused'), tr('Accepted')],
          }),
          row('LOGIN_REDIRECT_OVERRIDE', { label: tr('Where signed-out people are sent'), help: tr('Instead of the login page, for example straight to your single sign-on.') }),
        ],
      },
      {
        id: 'local-accounts',
        title: tr('Local accounts'),
        rows: [
          row('DISABLE_LOCAL_AUTH', {
            label: tr('Password login for everyone'),
            help: tr('Off: only superusers sign in with a password, to get in when single sign-on is down. Everyone else uses a login method.'),
            invert: true,
          }),
          {
            keys: ['LOCAL_PASSWORD_MIN_LENGTH', 'LOCAL_PASSWORD_MIN_DIGITS', 'LOCAL_PASSWORD_MIN_UPPER', 'LOCAL_PASSWORD_MIN_SPECIAL'],
            label: tr('Passwords must have at least'),
            help: tr('Checked when a password is set or changed. Existing passwords are not affected. 0: no minimum.'),
            parts: [tr('Characters'), tr('Digits'), tr('Uppercase'), tr('Symbols')],
          },
        ],
      },
      {
        id: 'default-maps',
        title: tr('Default organization and team maps'),
        intro: tr('Where OpenID Connect logins place people, by username or email, besides its group mapping. GitHub, Google, Microsoft Entra ID and SAML use maps of their own, and LDAP too.'),
        rows: [mapsRow('SOCIAL_AUTH_ORGANIZATION_MAP', 'SOCIAL_AUTH_TEAM_MAP', socialMapsHelp)],
      },
    ],
  },
  {
    slug: 'oidc',
    title: tr('OpenID Connect'),
    blurb: tr('Single sign-on with an OpenID Connect provider, and how its groups map to access.'),
    parent: 'signin',
    categories: ['oidc'],
    sections: [
      {
        id: 'provider',
        title: tr('Provider'),
        test: 'oidc',
        rows: [
          row('SOCIAL_AUTH_OIDC_OIDC_ENDPOINT', { label: tr('Provider URL') }),
          row('SOCIAL_AUTH_OIDC_KEY', { label: tr('Client ID') }),
          row('SOCIAL_AUTH_OIDC_SECRET', { label: tr('Client secret') }),
          row('SOCIAL_AUTH_OIDC_VERIFY_SSL', { label: tr('Provider\'s certificate'), states: [tr('Not checked'), tr('Checked')] }),
        ],
      },
      {
        id: 'claims',
        title: tr('Claims and tokens'),
        rows: [
          row('OIDC_SCOPES', { label: tr('Scopes') }),
          row('OIDC_USERNAME_CLAIM', { label: tr('Username claim') }),
          row('OIDC_GROUPS_CLAIM', { label: tr('Groups claim') }),
          row('OIDC_LINK_EXISTING_USERS', { label: tr('Link existing users') }),
          row('OIDC_ALLOW_BEARER', { label: tr('Provider tokens in API calls'), states: [tr('Refused'), tr('Accepted')] }),
          row('OIDC_AUDIENCES', { label: tr('Accepted token audiences') }),
        ],
      },
      { id: 'group-mapping', title: tr('Group mapping'), rows: [row('OIDC_MAPPING', { label: tr('Rules'), control: 'oidc-mapping', wide: true })] },
    ],
  },
  {
    slug: 'ldap',
    title: tr('LDAP'),
    blurb: tr('Up to six LDAP servers, tried in order before local accounts, and how their groups map to access.'),
    parent: 'signin',
    categories: ['ldap'],
    sections: ldapSections,
  },
  {
    slug: 'saml',
    title: tr('SAML'),
    blurb: tr('Single sign-on with SAML identity providers: the service provider awxnext is, the providers, and how their attributes map to access.'),
    parent: 'signin',
    categories: ['saml'],
    sections: [
      {
        id: 'service-provider',
        title: tr('Service provider'),
        rows: [
          row('SOCIAL_AUTH_SAML_SP_ENTITY_ID', { label: tr('Entity ID') }),
          row('SOCIAL_AUTH_SAML_SP_PUBLIC_CERT', { label: tr('Certificate'), lines: true }),
          row('SOCIAL_AUTH_SAML_SP_PRIVATE_KEY', { label: tr('Private key'), lines: true }),
          row('SOCIAL_AUTH_SAML_ORG_INFO', {
            label: tr('Organization'),
            control: 'records',
            help: tr('Your organization, as the service provider\'s metadata names it, by language: en-US at least.'),
            name: tr('Language'),
            add: tr('Add a language'),
            fields: [
              { key: 'name', label: tr('Name') },
              { key: 'displayname', label: tr('Display name') },
              { key: 'url', label: tr('URL') },
            ],
          }),
          ...[
            ['TECHNICAL_CONTACT', tr('Technical contact')],
            ['SUPPORT_CONTACT', tr('Support contact')],
          ].map(([f, label]) =>
            row(`SOCIAL_AUTH_SAML_${f}`, {
              label,
              control: 'fields',
              help: tr('Published in the service provider\'s metadata.'),
              fields: [
                { key: 'givenName', label: tr('Name') },
                { key: 'emailAddress', label: tr('Email') },
              ],
            }),
          ),
          // What to give the identity providers, once the rest is saved.
          row('SOCIAL_AUTH_SAML_METADATA_URL', { label: tr('Metadata URL'), help: tr('Give it to each identity provider: it holds the entity ID, the ACS URL and the certificate above.') }),
          row('SOCIAL_AUTH_SAML_CALLBACK_URL', { label: tr('Assertion consumer service (ACS) URL'), help: tr('Where identity providers send people back, for one that asks for it rather than the metadata.') }),
        ],
      },
      {
        id: 'identity-providers',
        title: tr('Identity providers'),
        test: 'saml',
        rows: [
          row('SOCIAL_AUTH_SAML_ENABLED_IDPS', {
            label: tr('Identity providers'),
            control: 'records',
            wide: true,
            help: tr('Each identity provider, by a name of your choice: its entity ID, its single sign-on URL and its signing certificate, and the attributes users are read from. Empty attributes are the standard ones.'),
            name: tr('Name'),
            add: tr('Add an identity provider'),
            fields: [
              { key: 'entity_id', label: tr('Entity ID') },
              { key: 'url', label: tr('Single sign-on URL') },
              { key: 'x509cert', label: tr('Signing certificate'), kind: 'lines' },
              { key: 'attr_user_permanent_id', label: tr('User ID attribute'), placeholder: asIs('name_id') },
              { key: 'attr_username', label: tr('Username attribute') },
              { key: 'attr_email', label: tr('Email attribute') },
              { key: 'attr_first_name', label: tr('First name attribute') },
              { key: 'attr_last_name', label: tr('Last name attribute') },
            ],
          }),
          row('SOCIAL_AUTH_SAML_SECURITY_CONFIG', {
            label: tr('Signatures and requirements'),
            control: 'fields',
            help: tr('How requests are signed, and what responses must hold. A response or its assertion must always be signed.'),
            fields: [
              { key: 'authnRequestsSigned', label: tr('Sign authentication requests'), kind: 'switch' },
              {
                key: 'signatureAlgorithm',
                label: tr('Signature algorithm'),
                kind: 'choice',
                choices: [
                  ['', tr('RSA-SHA256 (default)')],
                  ['http://www.w3.org/2000/09/xmldsig#rsa-sha1', 'RSA-SHA1'],
                  ['http://www.w3.org/2001/04/xmldsig-more#rsa-sha256', 'RSA-SHA256'],
                  ['http://www.w3.org/2001/04/xmldsig-more#rsa-sha384', 'RSA-SHA384'],
                  ['http://www.w3.org/2001/04/xmldsig-more#rsa-sha512', 'RSA-SHA512'],
                ],
              },
              { key: 'wantMessagesSigned', label: tr('Responses must be signed'), kind: 'switch' },
              { key: 'wantAssertionsSigned', label: tr('Assertions must be signed'), kind: 'switch' },
              { key: 'wantAssertionsEncrypted', label: tr('Assertions must be encrypted'), kind: 'switch' },
              { key: 'wantNameId', label: tr('A NameID is required'), kind: 'switch' },
              { key: 'requestedAuthnContext', label: tr('Authentication context asked for'), placeholder: tr('None'), empty: false },
              {
                key: 'requestedAuthnContextComparison',
                label: tr('Compared as'),
                kind: 'choice',
                choices: [
                  ['', tr('exact (default)')],
                  ['exact', 'exact'],
                  ['minimum', 'minimum'],
                  ['maximum', 'maximum'],
                  ['better', 'better'],
                ],
              },
            ],
          }),
        ],
      },
      {
        id: 'maps',
        title: tr('Organizations, teams and flags'),
        rows: [
          mapsRow('SOCIAL_AUTH_SAML_ORGANIZATION_MAP', 'SOCIAL_AUTH_SAML_TEAM_MAP', socialMapsHelp),
          row('SOCIAL_AUTH_SAML_ORGANIZATION_ATTR', {
            label: tr('Organizations from attributes'),
            control: 'saml-org-attr',
            help: tr('The identity provider names the organizations: each value of the attribute is an organization the user joins.'),
          }),
          row('SOCIAL_AUTH_SAML_TEAM_ATTR', {
            label: tr('Teams from an attribute'),
            control: 'saml-team-attr',
            wide: true,
            help: tr('The identity provider names the teams: each value of the attribute listed below makes the user a member of that team.'),
          }),
          row('SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR', {
            label: tr('Superusers and auditors by attribute'),
            control: 'saml-flags',
            help: tr('Set at each login, from a role the user has or an attribute\'s value.'),
          }),
        ],
      },
    ],
  },
  {
    slug: 'github',
    title: tr('GitHub'),
    blurb: tr('Login with GitHub or GitHub Enterprise accounts: any account, the members of an organization, or the members of a team.'),
    parent: 'signin',
    categories: githubVariants.map(([slug]) => slug),
    sections: githubSections,
  },
  {
    slug: 'google-oauth2',
    title: tr('Google'),
    blurb: tr('Login with Google accounts, optionally of some domains only.'),
    parent: 'signin',
    categories: ['google-oauth2'],
    sections: oauth('GOOGLE_OAUTH2_', [
      row('SOCIAL_AUTH_GOOGLE_OAUTH2_WHITELISTED_DOMAINS', { label: tr('Allowed email domains') }),
      row('SOCIAL_AUTH_GOOGLE_OAUTH2_AUTH_EXTRA_ARGUMENTS', {
        label: tr('Extra authorization parameters'),
        control: 'pairs',
        columns: [tr('Parameter'), tr('Value')],
        add: tr('Add a parameter'),
        help: tr('Added to the authorization request, such as hd with a domain to offer only the accounts of a Google Workspace domain.'),
      }),
    ]),
  },
  {
    slug: 'azuread-oauth2',
    title: tr('Microsoft Entra ID'),
    blurb: tr('Login with Microsoft Entra ID (Azure AD) accounts.'),
    parent: 'signin',
    categories: ['azuread-oauth2'],
    sections: oauth('AZUREAD_OAUTH2_'),
  },
  {
    slug: 'jobs',
    title: tr('Jobs'),
    blurb: tr('What launchers may change, what secret lookups may reach, limits and timeouts, and where jobs get their content and keep their output.'),
    categories: ['jobs'],
    sections: [
      {
        id: 'launch-safety',
        title: tr('Launch safety'),
        intro: tr('What someone who may launch a template, but not change it, can make the job do. The defaults keep a launcher to the template\'s own playbook.'),
        rows: [
          row('ALLOW_ANSIBLE_VARS_AT_LAUNCH', {
            label: tr('Variables named ansible_* at launch'),
            help: tr('In extra variables, survey answers, schedules and ad hoc commands. Variables on templates, inventories and hosts are not affected.'),
            states: [tr('Refused'), tr('Allowed')],
            segmented: true,
            consequence: tr('Allowed lets a launcher change how Ansible connects and escalates, for example ansible_ssh_common_args or ansible_become_exe, and so run other commands than the playbook\'s.'),
          }),
          row('IGNORE_UNPROMPTED_LAUNCH_FIELDS', {
            label: tr('Launch fields the template does not prompt for'),
            help: tr('A limit, an inventory, extra variables… sent at launch when the template does not ask for them.'),
            states: [tr('Refused'), tr('Ignored, as in AWX')],
            segmented: true,
            consequence: tr('Ignored: the job starts without them, and the response lists them in ignored_fields. For clients written for AWX that send every field.'),
          }),
          row('ALLOW_JINJA_IN_EXTRA_VARS', {
            label: tr('Jinja in extra variables'),
            help: tr('Whether {{ … }} in extra variables is evaluated by Ansible.'),
            options: [
              ['template', tr('Only in the template\'s own variables')],
              ['never', tr('Never')],
              ['always', tr('Also in a launcher\'s variables')],
            ],
            consequence: tr('A launcher\'s Jinja runs where the playbook runs, and its lookups can read files and run commands there.'),
          }),
          row('AD_HOC_COMMANDS', {
            label: tr('Modules for ad hoc commands'),
            help: tr('The modules people with the run commands permission may use. command and shell run anything.'),
          }),
          row('PROJECT_ALLOW_FILE_URLS', {
            label: tr('Projects from the server\'s own files'),
            help: tr('Git projects with a file:// URL or a local path.'),
            states: [tr('Not allowed'), tr('Allowed')],
            consequence: tr('Allowed lets anyone who may create a project check out any directory the server can read. Only for single-tenant installs you trust.'),
          }),
          row('AWX_SHOW_PLAYBOOK_LINKS', {
            label: tr('Symbolic links in projects'),
            help: tr('When awxnext lists a project\'s playbooks and inventory files.'),
            states: [tr('Inside the project only'), tr('Followed anywhere, as in AWX')],
            segmented: true,
            consequence: tr('Followed anywhere lets a project link to files elsewhere on the server and have them listed.'),
          }),
        ],
      },
      {
        id: 'secret-lookups',
        title: tr('Secret lookups'),
        intro: tr('Credentials of an external type, such as HashiCorp Vault or Azure Key Vault, fetch secrets from the address the credential gives, when a job starts and when someone tests them. Anyone who may create a credential chooses that address.'),
        rows: [
          row('CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS', {
            label: tr('Hosts that may receive awxnext\'s own identity'),
            help: tr('Vault\'s Kubernetes login sends awxnext\'s service account token; Azure Key Vault without a client secret, its workload or managed identity. One host per entry: vault.example.com:8200, *.vault.azure.net, or http://vault.vault.svc:8200 for plain HTTP. Empty: never.'),
            consequence: tr('Anyone who may create a credential can then read, on these hosts, whatever awxnext\'s identity can read there.'),
          }),
          row('CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES', {
            label: tr('Local addresses lookups may reach'),
            help: tr('Loopback and link-local addresses and cloud metadata services are refused: they are awxnext\'s own host. One per entry: an address, a network, or an address and port, such as 127.0.0.1:8200 for a Vault agent beside awxnext.'),
            consequence: tr('Anyone who may create a credential can then make awxnext send requests to these addresses.'),
          }),
        ],
      },
      {
        id: 'rvav-sources',
        title: tr('rvav sources'),
        intro: tr('awxnext reads rvav inventory sources itself, from the address their credential gives. Anyone who may create a credential chooses that address.'),
        rows: [
          row('INVENTORY_SOURCE_ALLOWED_LOCAL_ADDRESSES', {
            label: tr('Local addresses rvav sources may reach'),
            help: tr('Loopback and link-local addresses and cloud metadata services are refused: they are awxnext\'s own host. One per entry: an address, a network, or an address and port, such as 127.0.0.1:8080 for an rvav beside awxnext.'),
            consequence: tr('Anyone who may create a credential can then make awxnext send requests to these addresses.'),
          }),
        ],
      },
      {
        id: 'limits',
        title: tr('Limits and timeouts'),
        rows: [
          row('DEFAULT_JOB_TIMEOUT', { label: tr('Job timeout'), help: tr('A job still running after this long is canceled. Templates may set their own.'), unit: 'duration', none: [0, tr('No limit')] }),
          row('DEFAULT_JOB_IDLE_TIMEOUT', { label: tr('Idle timeout'), help: tr('A job that prints nothing for this long is canceled.'), unit: 'duration', none: [0, tr('No limit')] }),
          {
            keys: ['DEFAULT_PROJECT_UPDATE_TIMEOUT', 'DEFAULT_INVENTORY_UPDATE_TIMEOUT'],
            label: tr('Sync timeouts'),
            help: tr('For project and inventory syncs whose project or source sets none.'),
            parts: [tr('Project syncs'), tr('Inventory syncs')],
            unit: 'duration',
            none: [0, tr('No limit')],
          },
          row('ANSIBLE_FACT_CACHE_TIMEOUT', {
            label: tr('How long gathered facts stay valid'),
            help: tr('For templates that use the fact cache: older facts are gathered again.'),
            unit: 'duration',
            none: [0, tr('Always valid')],
          }),
          row('MAX_FORKS', {
            label: tr('Forks per job, at most'),
            help: tr('The most forks a template, a launch, a schedule, a workflow step or an ad hoc command may ask for.'),
            none: [0, tr('No limit')],
          }),
          row('SCHEDULE_MAX_JOBS', { label: tr('Waiting jobs per schedule') }),
          {
            keys: ['BULK_HOST_MAX_CREATE', 'BULK_HOST_MAX_DELETE', 'BULK_JOB_MAX_LAUNCH'],
            label: tr('Bulk API, per request'),
            help: tr('The most a single call to bulk/ may do.'),
            parts: [tr('Hosts created'), tr('Hosts deleted'), tr('Jobs launched')],
          },
        ],
      },
      {
        id: 'content',
        title: tr('Content'),
        intro: tr('Roles and collections a project\'s requirements.yml lists are installed when a job starts.'),
        rows: [
          { keys: ['AWX_ROLES_ENABLED', 'AWX_COLLECTIONS_ENABLED'], label: tr('Install from requirements files'), parts: [tr('Roles'), tr('Collections')] },
          row('GALAXY_IGNORE_CERTS', {
            label: tr('Galaxy server certificates'),
            help: tr('Checked by default. Skip the check only for a server with a self-signed certificate.'),
            states: [tr('Not checked'), tr('Checked')],
            invert: true,
          }),
          row('GALAXY_TASK_ENV', { label: tr('Environment of installs'), help: tr('Variables set while roles and collections are installed.'), ...envVars }),
        ],
      },
      {
        id: 'execution',
        title: tr('Execution'),
        rows: [
          row('AWX_TASK_ENV', { label: tr('Environment of jobs'), help: tr('Variables added to the environment of every job.'), ...envVars }),
          {
            keys: ['AWX_ISOLATION_SHOW_PATHS', 'AWX_MOUNT_ISOLATED_PATHS_ON_K8S'],
            label: tr('Host paths mounted into jobs'),
            help: tr('Directories of the host made visible inside execution environment containers. Options are those of a container volume, such as ro or z.'),
            parts: [tr('Paths'), tr('Also in container group pods')],
            control: 'paths',
          },
          row('AWX_RUNNER_KEEPALIVE_SECONDS', {
            label: tr('Keepalive for quiet remote jobs'),
            help: tr('After this long without output, a job on a remote node or in a container group sends a keepalive, so that idle connections are not cut.'),
            unit: 'duration',
            none: [0, tr('Never')],
          }),
          row('AWX_ANSIBLE_CALLBACK_PLUGINS', { label: tr('Extra callback plugins'), help: tr('Paths searched for callback plugins, in addition to the execution environment\'s.') }),
        ],
      },
      {
        id: 'output',
        title: tr('Output and events'),
        rows: [
          row('JOB_EVENT_MAX_RATE', {
            label: tr('Events stored per second, per job'),
            help: tr('Beyond it, consecutive plain output lines are folded into one, so that a very verbose job cannot starve the others.'),
            unit: tr('events per second'),
            none: [0, tr('No limit')],
          }),
          row('MAX_WEBSOCKET_EVENT_RATE', {
            label: tr('Live events per second, per browser'),
            help: tr('The job page fetches the rest from the API.'),
            unit: tr('events per second'),
            none: [0, tr('No limit')],
          }),
          row('STDOUT_MAX_BYTES_DISPLAY', {
            label: tr('Largest output the API returns inline'),
            help: tr('Larger output, asked for at a job\'s stdout/, is only offered as a download. The job page is not limited by it.'),
            unit: 'size',
          }),
          {
            keys: ['JOB_EVENTS_HOT_DAYS', 'JOB_EVENTS_ARCHIVE_STORAGE'],
            label: tr('Event archives'),
            help: tr('After this many days in the database, a job\'s events are compressed into an archive, kept here.'),
            parts: [tr('In the database first'), tr('Archives kept in')],
            unit: tr('days'),
          },
          row('JOB_EVENTS_ARCHIVE_BUCKET', { label: tr('Archive bucket or container') }),
          row('JOB_EVENTS_ARCHIVE_ENDPOINT', { label: tr('Archive endpoint') }),
          row('JOB_EVENTS_ARCHIVE_REGION', { label: tr('Archive region') }),
          row('JOB_EVENTS_ARCHIVE_PREFIX', { label: tr('Archive key prefix') }),
          row('JOB_EVENTS_ARCHIVE_CREDENTIAL', { label: tr('Archive credential'), control: 'archive-credential' }),
        ],
      },
    ],
  },
  {
    slug: 'logging',
    title: tr('Logging'),
    blurb: tr('Shipping logs to an external aggregator.'),
    categories: ['logging'],
    sections: [
      {
        id: 'aggregator',
        title: tr('Aggregator'),
        rows: [
          row('LOG_AGGREGATOR_ENABLED', { label: tr('Send logs to the aggregator'), help: tr('Off: nothing is sent, whatever the rest says.') }),
          row('LOG_AGGREGATOR_TYPE', { label: tr('Aggregator') }),
          row('LOG_AGGREGATOR_PROTOCOL', { label: tr('Protocol') }),
          row('LOG_AGGREGATOR_HOST', { label: tr('Host or URL') }),
          row('LOG_AGGREGATOR_PORT', { label: tr('Port'), help: tr('When the host or URL gives none.') }),
          row('LOG_AGGREGATOR_VERIFY_CERT', {
            label: tr('Aggregator\'s certificate'),
            help: tr('Over HTTPS. Skip the check only for an aggregator with a self-signed certificate.'),
            states: [tr('Not checked'), tr('Checked')],
          }),
          row('LOG_AGGREGATOR_TCP_TIMEOUT', {
            label: tr('Connection timeout'),
            help: tr('How long a connection to the aggregator may take, over HTTPS or TCP, and the wait between retries.'), unit: 'duration' }),
          row('LOG_AGGREGATOR_USERNAME', { label: tr('Username'), help: tr('Over HTTPS or HTTP, when the aggregator asks for one.') }),
          row('LOG_AGGREGATOR_PASSWORD', { label: tr('Password or token') }),
        ],
      },
      {
        id: 'messages',
        title: tr('Messages'),
        rows: [
          row('LOG_AGGREGATOR_LOGGERS', {
            label: tr('Logs sent'),
            help: tr('What is sent to the aggregator.'),
            options: [
              ['awx', tr('Service logs')],
              ['activity_stream', tr('Activity stream records')],
              ['job_events', tr('Job events')],
              ['system_tracking', tr('Facts gathered by jobs')],
              ['broadcast_websocket', tr('Websocket broadcasts: none in awxnext')],
            ],
          }),
          row('LOG_AGGREGATOR_LEVEL', { label: tr('Least severe level sent') }),
          row('LOG_AGGREGATOR_INDIVIDUAL_FACTS', { label: tr('One message per fact') }),
          row('LOG_AGGREGATOR_TOWER_UUID', { label: tr('Cluster identifier') }),
        ],
      },
      { id: 'queue', title: tr('Queue'), rows: [
          row('LOG_AGGREGATOR_ACTION_QUEUE_SIZE', { label: tr('Messages waiting, at most'), unit: tr('messages') }),
          row('LOG_AGGREGATOR_RSYSLOGD_DEBUG', { label: tr('Log each delivery') }),
        ],
      },
    ],
  },
  {
    slug: 'ui',
    title: tr('Login page and UI'),
    blurb: tr('What the login page shows, and how job output loads.'),
    categories: ['ui'],
    sections: [
      {
        id: 'login-page',
        title: tr('Login page'),
        rows: [
          row('CUSTOM_LOGO', { label: tr('Logo'), control: 'image', help: tr('Shown on the login page: a GIF, PNG or JPEG image.') }),
          row('CUSTOM_LOGIN_INFO', { label: tr('Text on the login page'), lines: true }),
        ],
      },
      {
        id: 'job-output',
        title: tr('Job output'),
        rows: [
          row('UI_LIVE_UPDATES_ENABLED', { label: tr('Live updates') }),
          row('MAX_UI_JOB_EVENTS', {
            label: tr('Events the job page loads at once'),
            help: tr('More can be loaded on demand, or the output downloaded.'),
            unit: tr('events'),
            none: [0, tr('All')],
          }),
        ],
      },
    ],
  },
]

/** The page of a slug of the menu. */
export function settingsPage(slug: string): SettingsPage | undefined {
  return settingsPages.find((p) => p.slug === slug)
}

/** Where an AWX category's settings now are: its page, and its section on
 * GitHub's page. For links to the former category pages. */
export function categoryPage(category: string): { slug: string; section?: string } | undefined {
  const page = settingsPages.find((p) => p.categories.includes(category))
  if (!page) return undefined
  return page.slug === category || page.categories.length === 1 ? { slug: page.slug } : { slug: page.slug, section: page.sections.some((s) => s.id === category) ? category : undefined }
}

/** Where a setting is: its page, its section, and its row's anchor. */
export interface Place {
  page: string
  section: string
  anchor: string
}

/** A row's anchor in its page: its first setting. */
export const rowAnchor = (r: Row) => r.keys[0]

/** Where each setting is placed. */
export function placements(): Map<string, Place> {
  const out = new Map<string, Place>()
  for (const p of settingsPages) for (const s of p.sections) for (const r of s.rows) for (const k of r.keys) out.set(k, { page: p.slug, section: s.id, anchor: rowAnchor(r) })
  return out
}

/** Where a setting is, from its key and AWX category: where the layout
 * places it, else on its category's page, under "Other settings" or with
 * the settings that have no effect. */
export function settingPlace(key: string, category: string, noEffect = false): Place | undefined {
  const placed = placements().get(key)
  if (placed) return placed
  const page = settingsPages.find((p) => p.categories.includes(category))
  return page && { page: page.slug, section: noEffect ? 'no-effect' : 'other', anchor: key }
}

/** The row a setting is placed in. */
export function layoutRow(key: string): Row | undefined {
  for (const p of settingsPages) for (const s of p.sections) for (const r of s.rows) if (r.keys.includes(key)) return r
  return undefined
}

/** The layout's label for a setting, if it gives one: a row's label, with
 * the setting's part in a row of several ("Sync timeouts: Project syncs"). */
export function settingLabel(key: string): string | undefined {
  for (const p of settingsPages)
    for (const s of p.sections)
      for (const r of s.rows) {
        const i = r.keys.indexOf(key)
        if (i < 0) continue
        if (r.parts?.[i]) return r.label ? `${r.label}: ${r.parts[i]}` : r.parts[i]
        return r.keys.length === 1 ? r.label : undefined
      }
  return undefined
}

/** AWX's settings that do nothing in awxnext: the API's help says so first. */
export const hasNoEffect = (help: string | undefined) => /^No effect in awxnext\b/.test(help ?? '')

/**
 * A page's sections with the settings the API has (`categories`: each
 * key's AWX category): rows lose the keys it does not have, and the
 * settings of the page's categories the layout does not place are added,
 * so every setting can be reached: those with no effect in awxnext (`noEffect`)
 * in a last section of their own, the others under "Other settings".
 */
export function pageSections(page: SettingsPage, categories: Record<string, string>, noEffect: (key: string) => boolean = () => false): Section[] {
  const placed = placements()
  const sections: Section[] = []
  for (const s of page.sections) {
    const rows = s.rows.flatMap((r) => {
      const keep = r.keys.map((k, i) => [k, r.parts?.[i]] as const).filter(([k]) => k in categories)
      if (!keep.length) return []
      if (keep.length === r.keys.length) return [r]
      return [{ ...r, keys: keep.map(([k]) => k), parts: r.parts && keep.map(([, p]) => p ?? '') }]
    })
    if (rows.length || s.children) sections.push({ ...s, rows })
  }
  const unplaced = Object.keys(categories)
    .filter((k) => page.categories.includes(categories[k]) && !placed.has(k))
    .sort()
  const other = unplaced.filter((k) => !noEffect(k))
  const none = unplaced.filter(noEffect)
  if (other.length) sections.push({ id: 'other', title: tr('Other settings'), rows: other.map((k) => row(k)) })
  if (none.length)
    sections.push({
      id: 'no-effect',
      title: tr('No effect in awxnext'),
      intro: tr('Kept so that AWX\'s clients can read and write them: changing them does nothing.'),
      noEffect: true,
      rows: none.map((k) => row(k)),
    })
  return sections
}

/**
 * The sections shown: the settings with no effect in awxnext only when asked
 * for (`show`), or when one of them is wanted (linked to, edited, in error),
 * or their section is linked to (`hash`); and how many settings are hidden.
 */
export function visibleSections(sections: Section[], show: boolean, wanted: (key: string) => boolean, hash = ''): { sections: Section[]; hidden: number } {
  let hidden = 0
  const shown = sections.filter((s) => {
    if (!s.noEffect || show || hash === s.id || s.rows.some((r) => r.keys.some(wanted))) return true
    hidden += s.rows.reduce((n, r) => n + r.keys.length, 0)
    return false
  })
  return { sections: shown, hidden }
}

/** The pages under a page in the menu. */
export function childPages(slug: string): SettingsPage[] {
  return settingsPages.filter((p) => p.parent === slug)
}

/** Changed settings, by the AWX category they are saved to. */
export function changesByCategory(keys: string[], values: Record<string, unknown>, categories: Record<string, string>): Record<string, Record<string, unknown>> {
  const out: Record<string, Record<string, unknown>> = {}
  for (const k of keys) {
    const c = categories[k]
    if (!c) continue
    ;(out[c] ??= {})[k] = values[k]
  }
  return out
}

/** A setting found by a search: its label, where it is, and what matched. */
export interface Found {
  key: string
  label: string
  place: Place
  /** Its page's title, and its section's. */
  pageTitle: string
  sectionTitle: string
  /** Found by its label, by its key, or only by its help. */
  by: 'label' | 'key' | 'help'
}

/** What a search reads of a setting, from the API's OPTIONS. */
export interface SettingInfo {
  label?: string
  help_text?: string
  category_slug?: string
}

// Searches ignore case and accents: "delai" finds "Délai".
const fold = (s: string) =>
  s
    .toLowerCase()
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')

/**
 * The settings with every word searched for in their label, their key or
 * their help: those found by their label first, then by their key, then by
 * their help, each in the menu's order, the settings with no effect in
 * awxnext last. A key's words count apart: "job timeout" finds
 * DEFAULT_JOB_TIMEOUT. `labelOf` names a setting the layout does not.
 */
export function searchSettings(query: string, info: Record<string, SettingInfo>, noEffect: (key: string) => boolean = () => false, labelOf?: (key: string) => string): Found[] {
  const words = fold(query).split(/\s+/).filter(Boolean)
  if (!words.length) return []
  const categories = Object.fromEntries(Object.entries(info).map(([k, m]) => [k, String(m.category_slug ?? '')]))
  const has = (text: string) => words.every((w) => text.includes(w))
  const rank = { label: 0, key: 1, help: 2 }
  const found: (Found & { none: boolean })[] = []
  for (const p of settingsPages)
    for (const s of pageSections(p, categories, noEffect))
      for (const r of s.rows)
        for (const k of r.keys) {
          const m = info[k]
          const label = settingLabel(k) ?? labelOf?.(k) ?? (m?.label || k)
          const l = fold(label)
          const key = `${l} ${fold(k)} ${fold(k.replace(/_/g, ' '))}`
          const by = has(l) ? 'label' : has(key) ? 'key' : has(`${key} ${fold(r.help ?? m?.help_text ?? '')}`) ? 'help' : null
          if (by) found.push({ key: k, label, place: { page: p.slug, section: s.id, anchor: rowAnchor(r) }, pageTitle: p.title, sectionTitle: s.title, by, none: noEffect(k) })
        }
  return found.sort((a, b) => rank[a.by] - rank[b.by] || Number(a.none) - Number(b.none)).map(({ none: _, ...f }) => f)
}

/** Whether a path is one of the settings pages: their changes are kept
 * from one to another until saved, and lost on leaving them. */
export const inSettings = (path: string) => path === '/settings' || path.startsWith('/settings/')
