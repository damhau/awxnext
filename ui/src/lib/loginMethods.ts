import { tr, trn } from './i18n'

// The login methods of the Sign-in overview: whether each is on, as the
// server decides it (internal/auth), and a line on how it is set up. And
// GitHub's six AWX categories, drawn as one form with two choices.

type Values = Record<string, unknown>

const filled = (v: unknown) => typeof v === 'string' && v.trim() !== ''
const count = (v: unknown) => (Array.isArray(v) ? v.length : v && typeof v === 'object' ? Object.keys(v).length : 0)

export interface MethodState {
  on: boolean
  /** How it is set up, in a word or a URL. */
  what?: string
  sub?: string
}

export type GitHubHost = 'github.com' | 'enterprise'
export type GitHubWho = 'any' | 'org' | 'team'

export interface GitHubVariant {
  /** The AWX category, also the login's name in its URLs. */
  category: string
  prefix: string
  host: GitHubHost
  who: GitHubWho
}

export const githubVariants: GitHubVariant[] = [
  { category: 'github', prefix: 'SOCIAL_AUTH_GITHUB_', host: 'github.com', who: 'any' },
  { category: 'github-org', prefix: 'SOCIAL_AUTH_GITHUB_ORG_', host: 'github.com', who: 'org' },
  { category: 'github-team', prefix: 'SOCIAL_AUTH_GITHUB_TEAM_', host: 'github.com', who: 'team' },
  { category: 'github-enterprise', prefix: 'SOCIAL_AUTH_GITHUB_ENTERPRISE_', host: 'enterprise', who: 'any' },
  { category: 'github-enterprise-org', prefix: 'SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_', host: 'enterprise', who: 'org' },
  { category: 'github-enterprise-team', prefix: 'SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_', host: 'enterprise', who: 'team' },
]

export const githubVariant = (host: GitHubHost, who: GitHubWho): GitHubVariant => githubVariants.find((v) => v.host === host && v.who === who)!

// The fields of a GitHub variant. Prefixes alone are ambiguous:
// SOCIAL_AUTH_GITHUB_TEAM_MAP is github's team map, not a github-team setting.
const GITHUB_FIELDS = ['CALLBACK_URL', 'URL', 'API_URL', 'KEY', 'SECRET', 'NAME', 'ID', 'ORGANIZATION_MAP', 'TEAM_MAP']

/** The variant a setting belongs to: its prefix followed by one of the fields. */
export function githubVariantOf(key: string): GitHubVariant | undefined {
  return githubVariants.find((v) => key.startsWith(v.prefix) && GITHUB_FIELDS.includes(key.slice(v.prefix.length)))
}

/** An OAuth2 login is on when its client ID and secret are set. A set secret reads back as $encrypted$. */
const oauthOn = (values: Values, prefix: string) => filled(values[`${prefix}KEY`]) && filled(values[`${prefix}SECRET`])

export const githubOn = (values: Values, v: GitHubVariant) => oauthOn(values, v.prefix)

/** The variant the GitHub form shows: the one a link points into, else the
 * first that is on, else github.com for any account. */
export function githubChosen(values: Values, key = ''): GitHubVariant {
  return githubVariantOf(key) ?? githubVariants.find((v) => githubOn(values, v)) ?? githubVariants[0]
}

/** A URL without its scheme or trailing slash, as a card shows it. */
export function bareURL(url: unknown): string {
  return String(url ?? '')
    .trim()
    .replace(/^[a-z][a-z0-9+.-]*:\/\//i, '')
    .replace(/\/+$/, '')
}

/** An LDAP server's hosts, from its URIs (several, separated by spaces or commas). */
export function ldapHosts(uri: unknown): string[] {
  return String(uri ?? '')
    .split(/[\s,]+/)
    .map((u) => bareURL(u).replace(/[:/].*$/, ''))
    .filter(Boolean)
}

/** Who a GitHub login lets in, in words. */
export function githubWho(values: Values, v: GitHubVariant): string {
  const where = v.host === 'github.com' ? 'github.com' : bareURL(values[`${v.prefix}URL`]) || tr('GitHub Enterprise')
  const who =
    v.who === 'any'
      ? tr('any account')
      : v.who === 'org'
        ? tr('members of {org}', { org: String(values[`${v.prefix}NAME`] ?? '') || '?' })
        : tr('members of team {id}', { id: String(values[`${v.prefix}ID`] ?? '') || '?' })
  return `${where} · ${who}`
}

/** What a login's own maps do. These logins use only their own maps, even when empty. */
function ownMaps(values: Values, prefix: string): string {
  const n = count(values[`${prefix}ORGANIZATION_MAP`])
  return n ? trn(n, 'Its map places people in {n} organization', 'Its maps place people in {n} organizations') : tr('No map: people join no organization')
}

const LDAP_PREFIXES = ['', '1_', '2_', '3_', '4_', '5_']

/** Each login method's state, by its settings page's slug. */
export function methodStates(values: Values): Record<string, MethodState> {
  const out: Record<string, MethodState> = {}
  const rules = count(values.OIDC_MAPPING)
  out.oidc = filled(values.SOCIAL_AUTH_OIDC_OIDC_ENDPOINT) && filled(values.SOCIAL_AUTH_OIDC_KEY)
    ? {
        on: true,
        what: bareURL(values.SOCIAL_AUTH_OIDC_OIDC_ENDPOINT),
        sub: [trn(rules, '{n} mapping rule', '{n} mapping rules'), values.OIDC_ALLOW_BEARER ? tr('provider tokens accepted in API calls') : ''].filter(Boolean).join(' · '),
      }
    : { on: false }
  const servers = LDAP_PREFIXES.filter((p) => filled(values[`AUTH_LDAP_${p}SERVER_URI`]))
  out.ldap = servers.length
    ? {
        on: true,
        what: [...new Set(servers.flatMap((p) => ldapHosts(values[`AUTH_LDAP_${p}SERVER_URI`])))].join(', '),
        sub: trn(servers.length, '{n} server of 6', '{n} servers of 6, tried in order'),
      }
    : { on: false }
  const idps = values.SOCIAL_AUTH_SAML_ENABLED_IDPS && typeof values.SOCIAL_AUTH_SAML_ENABLED_IDPS === 'object' ? Object.keys(values.SOCIAL_AUTH_SAML_ENABLED_IDPS) : []
  out.saml = idps.length ? { on: true, what: idps.join(', '), sub: trn(idps.length, '{n} identity provider', '{n} identity providers') } : { on: false }
  const github = githubVariants.filter((v) => githubOn(values, v))
  out.github = github.length
    ? {
        on: true,
        what: githubWho(values, github[0]),
        sub: github.length > 1 ? trn(github.length - 1, 'And {n} other GitHub login', 'And {n} other GitHub logins') : ownMaps(values, github[0].prefix),
      }
    : { on: false }
  const domains = Array.isArray(values.SOCIAL_AUTH_GOOGLE_OAUTH2_WHITELISTED_DOMAINS) ? values.SOCIAL_AUTH_GOOGLE_OAUTH2_WHITELISTED_DOMAINS.map(String) : []
  out['google-oauth2'] = oauthOn(values, 'SOCIAL_AUTH_GOOGLE_OAUTH2_')
    ? { on: true, what: domains.length ? domains.join(', ') : tr('Any Google account'), sub: ownMaps(values, 'SOCIAL_AUTH_GOOGLE_OAUTH2_') }
    : { on: false }
  out['azuread-oauth2'] = oauthOn(values, 'SOCIAL_AUTH_AZUREAD_OAUTH2_')
    ? { on: true, what: String(values.SOCIAL_AUTH_AZUREAD_OAUTH2_KEY).trim(), sub: ownMaps(values, 'SOCIAL_AUTH_AZUREAD_OAUTH2_') }
    : { on: false }
  return out
}

/** Local accounts: on for everyone, or for superusers only. */
export function localState(values: Values): MethodState {
  return { on: true, what: values.DISABLE_LOCAL_AUTH ? tr('Superusers only') : tr('Everyone with a password') }
}
