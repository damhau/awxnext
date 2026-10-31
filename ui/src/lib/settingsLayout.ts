import { tr } from './i18n'

// The settings pages, laid out by what an admin sets up rather than by AWX's
// categories: every setting the API has is placed here, once, on a page, in
// a section, in order, with its label and the control that edits it. The
// API's categories and keys do not change; a page saves its changes to the
// categories they belong to.

/** How a setting is edited, when its type alone does not say. */
export type Control = 'json' | 'oidc-mapping' | 'archive-credential'

export interface Row {
  /** The settings the row edits: one, or several edited together. */
  keys: string[]
  /** What the row sets, in plain words; the API's label when absent. */
  label?: string
  /** What it does; the API's help text when absent. */
  help?: string
  /** In a row of several settings, each one's own label, in the keys' order. */
  parts?: string[]
  control?: Control
  /** A switch's words for off and on. */
  states?: [string, string]
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

const ldapSections: Section[] = ['', '1_', '2_', '3_', '4_', '5_'].map((p, n) => ({
  id: n ? `server-${n}` : 'default-server',
  title: n ? tr('Server {n}', { n }) : tr('Default server'),
  on: n ? (v) => filled(v[`AUTH_LDAP_${p}SERVER_URI`]) : undefined,
  // LDAP searches are [base, scope, filter], or a list of them.
  rows: ldapFields.map((f) => row(`AUTH_LDAP_${p}${f}`, f.endsWith('_SEARCH') ? { control: 'json' } : {})),
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

const githubSections: Section[] = githubVariants.map(([slug, p, title, extra]) => {
  const where = extra.filter((f) => f.endsWith('URL'))
  const who = extra.filter((f) => !f.endsWith('URL'))
  return {
    id: slug,
    title,
    rows: ['CALLBACK_URL', ...where, 'KEY', 'SECRET', ...who, 'ORGANIZATION_MAP', 'TEAM_MAP'].map((f) => row(`SOCIAL_AUTH_${p}${f}`)),
  }
})

const oauth = (p: string, extra: string[] = []): Section[] => [
  { id: 'provider', title: tr('Provider'), rows: ['CALLBACK_URL', 'KEY', 'SECRET', ...extra].map((f) => row(`SOCIAL_AUTH_${p}${f}`)) },
  { id: 'maps', title: tr('Organizations and teams'), rows: ['ORGANIZATION_MAP', 'TEAM_MAP'].map((f) => row(`SOCIAL_AUTH_${p}${f}`)) },
]

export const settingsPages: SettingsPage[] = [
  {
    slug: 'system',
    title: tr('System'),
    blurb: tr('The service\'s address, the activity stream, what organization admins may do, and proxies.'),
    categories: ['system', 'named-url'],
    sections: [
      { id: 'service', title: tr('Service'), rows: [row('TOWER_URL_BASE'), row('DEFAULT_EXECUTION_ENVIRONMENT'), row('INSTALL_UUID', { label: tr('Installation ID') })] },
      { id: 'activity-stream', title: tr('Activity stream'), rows: [row('ACTIVITY_STREAM_ENABLED'), row('ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC')] },
      { id: 'organization-admins', title: tr('Organization admins'), rows: [row('ORG_ADMINS_CAN_SEE_ALL_USERS'), row('MANAGE_ORGANIZATION_AUTH')] },
      {
        id: 'proxies',
        title: tr('Behind a proxy'),
        rows: [
          row('REMOTE_HOST_HEADERS', { label: tr('Headers that carry the client\'s address') }),
          row('PROXY_IP_ALLOWED_LIST', { label: tr('Proxies trusted to set them') }),
        ],
      },
      { id: 'named-url', title: tr('Named URLs'), rows: [row('NAMED_URL_FORMATS'), row('NAMED_URL_GRAPH_NODES')] },
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
          row('SESSION_COOKIE_AGE', { label: tr('Sign out after inactivity'), help: tr('Seconds without activity after which a session ends. At least 60.') }),
          row('SESSIONS_PER_USER', { label: tr('Sessions per person'), help: tr('Signing in once more than this ends the person\'s oldest session. -1: no limit.') }),
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
        rows: [row('SOCIAL_AUTH_ORGANIZATION_MAP'), row('SOCIAL_AUTH_TEAM_MAP')],
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
      { id: 'provider', title: tr('Provider'), rows: ['OIDC_ENDPOINT', 'KEY', 'SECRET', 'VERIFY_SSL'].map((f) => row(`SOCIAL_AUTH_OIDC_${f}`)) },
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
      { id: 'group-mapping', title: tr('Group mapping'), rows: [row('OIDC_MAPPING', { control: 'oidc-mapping', wide: true })] },
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
        rows: ['SP_ENTITY_ID', 'CALLBACK_URL', 'METADATA_URL', 'SP_PUBLIC_CERT', 'SP_PRIVATE_KEY', 'ORG_INFO', 'TECHNICAL_CONTACT', 'SUPPORT_CONTACT'].map((f) => row(`SOCIAL_AUTH_SAML_${f}`)),
      },
      { id: 'identity-providers', title: tr('Identity providers'), rows: [row('SOCIAL_AUTH_SAML_ENABLED_IDPS'), row('SOCIAL_AUTH_SAML_SECURITY_CONFIG')] },
      {
        id: 'maps',
        title: tr('Organizations, teams and flags'),
        rows: ['ORGANIZATION_MAP', 'TEAM_MAP', 'ORGANIZATION_ATTR', 'TEAM_ATTR', 'USER_FLAGS_BY_ATTR'].map((f) => row(`SOCIAL_AUTH_SAML_${f}`)),
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
    sections: oauth('GOOGLE_OAUTH2_', ['WHITELISTED_DOMAINS', 'AUTH_EXTRA_ARGUMENTS']),
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
    blurb: tr('What launchers may change, limits and timeouts, and where jobs get their content and keep their output.'),
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
          }),
          row('IGNORE_UNPROMPTED_LAUNCH_FIELDS', {
            label: tr('Launch fields the template does not prompt for'),
            help: tr('A limit, an inventory, extra variables… sent at launch when the template does not ask for them.'),
            states: [tr('Refused'), tr('Ignored, as in AWX')],
          }),
          row('ALLOW_JINJA_IN_EXTRA_VARS', { label: tr('Jinja in extra variables'), help: tr('Whether {{ … }} in extra variables is evaluated by Ansible.') }),
          row('AD_HOC_COMMANDS', {
            label: tr('Modules for ad hoc commands'),
            help: tr('The modules people with the run commands permission may use. command and shell run anything.'),
          }),
          row('PROJECT_ALLOW_FILE_URLS', {
            label: tr('Projects from the server\'s own files'),
            help: tr('Git projects with a file:// URL or a local path.'),
            states: [tr('Not allowed'), tr('Allowed')],
          }),
          row('AWX_SHOW_PLAYBOOK_LINKS', {
            label: tr('Symbolic links in projects'),
            help: tr('When awxnext lists a project\'s playbooks and inventory files.'),
            states: [tr('Inside the project only'), tr('Followed anywhere, as in AWX')],
          }),
        ],
      },
      {
        id: 'limits',
        title: tr('Limits and timeouts'),
        rows: [
          row('DEFAULT_JOB_TIMEOUT', { label: tr('Job timeout'), help: tr('Seconds after which a job still running is canceled. Templates may set their own. 0: no limit.') }),
          row('DEFAULT_JOB_IDLE_TIMEOUT', { label: tr('Idle timeout'), help: tr('Seconds without output after which a job is canceled. 0: no limit.') }),
          {
            keys: ['DEFAULT_PROJECT_UPDATE_TIMEOUT', 'DEFAULT_INVENTORY_UPDATE_TIMEOUT'],
            label: tr('Sync timeouts'),
            help: tr('Seconds, for project and inventory syncs whose project or source sets none. 0: no limit.'),
            parts: [tr('Project syncs'), tr('Inventory syncs')],
          },
          row('ANSIBLE_FACT_CACHE_TIMEOUT', {
            label: tr('How long gathered facts stay valid'),
            help: tr('Seconds, for templates that use the fact cache: older facts are gathered again. 0: always valid.'),
          }),
          row('MAX_FORKS', { label: tr('Forks per job, at most') }),
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
          row('GALAXY_TASK_ENV', { label: tr('Environment of installs'), help: tr('Variables set while roles and collections are installed.') }),
        ],
      },
      {
        id: 'execution',
        title: tr('Execution'),
        rows: [
          row('AWX_TASK_ENV', { label: tr('Environment of jobs'), help: tr('Variables added to the environment of every job.') }),
          {
            keys: ['AWX_ISOLATION_SHOW_PATHS', 'AWX_MOUNT_ISOLATED_PATHS_ON_K8S'],
            label: tr('Host paths mounted into jobs'),
            help: tr('Directories of the host made visible inside execution environment containers, one per line: HOST-DIR[:CONTAINER-DIR[:OPTIONS]].'),
            parts: [tr('Paths'), tr('Also in container group pods')],
          },
          row('AWX_RUNNER_KEEPALIVE_SECONDS', { label: tr('Keepalive for quiet remote jobs') }),
          row('AWX_ANSIBLE_CALLBACK_PLUGINS', { label: tr('Extra callback plugins'), help: tr('Paths searched for callback plugins, in addition to the execution environment\'s.') }),
        ],
      },
      {
        id: 'output',
        title: tr('Output and events'),
        rows: [
          row('JOB_EVENT_MAX_RATE', { label: tr('Events stored per second, per job') }),
          row('MAX_WEBSOCKET_EVENT_RATE', { label: tr('Live events per second, per browser') }),
          row('STDOUT_MAX_BYTES_DISPLAY', {
            label: tr('Largest output the API returns inline'),
            help: tr('Bytes. Larger output, asked for at a job\'s stdout/, is only offered as a download. The job page is not limited by it.'),
          }),
          {
            keys: ['JOB_EVENTS_HOT_DAYS', 'JOB_EVENTS_ARCHIVE_STORAGE'],
            label: tr('Event archives'),
            help: tr('After this many days in the database, a job\'s events are compressed into an archive, kept here.'),
            parts: [tr('Days in the database first'), tr('Archives kept in')],
          },
          row('JOB_EVENTS_ARCHIVE_BUCKET'),
          row('JOB_EVENTS_ARCHIVE_ENDPOINT'),
          row('JOB_EVENTS_ARCHIVE_REGION'),
          row('JOB_EVENTS_ARCHIVE_PREFIX'),
          row('JOB_EVENTS_ARCHIVE_CREDENTIAL', { control: 'archive-credential' }),
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
        rows: ['ENABLED', 'TYPE', 'PROTOCOL', 'HOST', 'PORT', 'VERIFY_CERT', 'TCP_TIMEOUT', 'USERNAME', 'PASSWORD'].map((f) => row(`LOG_AGGREGATOR_${f}`)),
      },
      { id: 'messages', title: tr('Messages'), rows: ['LOGGERS', 'LEVEL', 'INDIVIDUAL_FACTS', 'TOWER_UUID'].map((f) => row(`LOG_AGGREGATOR_${f}`)) },
      { id: 'queue', title: tr('Queue'), rows: [row('LOG_AGGREGATOR_ACTION_QUEUE_SIZE'), row('LOG_AGGREGATOR_RSYSLOGD_DEBUG')] },
    ],
  },
  {
    slug: 'ui',
    title: tr('Login page and UI'),
    blurb: tr('What the login page shows, and how job output loads.'),
    categories: ['ui'],
    sections: [
      { id: 'login-page', title: tr('Login page'), rows: [row('CUSTOM_LOGO'), row('CUSTOM_LOGIN_INFO')] },
      { id: 'job-output', title: tr('Job output'), rows: [row('UI_LIVE_UPDATES_ENABLED'), row('MAX_UI_JOB_EVENTS')] },
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
