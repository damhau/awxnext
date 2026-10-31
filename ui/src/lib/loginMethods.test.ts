import { describe, expect, it } from 'vitest'
import { bareURL, githubChosen, githubVariant, githubVariantOf, githubVariants, githubWho, ldapHosts, localState, methodStates } from './loginMethods'

describe('login methods', () => {
  it('are all off with the defaults', () => {
    const states = methodStates({ SOCIAL_AUTH_GITHUB_KEY: '', SOCIAL_AUTH_GITHUB_SECRET: '', SOCIAL_AUTH_SAML_ENABLED_IDPS: {}, AUTH_LDAP_SERVER_URI: '' })
    expect(Object.entries(states).filter(([, s]) => s.on)).toEqual([])
    expect(Object.keys(states).sort()).toEqual(['azuread-oauth2', 'github', 'google-oauth2', 'ldap', 'oidc', 'saml'])
  })

  it('are on as the server decides it', () => {
    // OIDC: a provider URL and a client ID (internal/auth/oidc.go, Enabled).
    expect(methodStates({ SOCIAL_AUTH_OIDC_OIDC_ENDPOINT: 'https://sso.example/realms/ops' }).oidc.on).toBe(false)
    const oidc = methodStates({ SOCIAL_AUTH_OIDC_OIDC_ENDPOINT: 'https://sso.example/realms/ops/', SOCIAL_AUTH_OIDC_KEY: 'awx', OIDC_MAPPING: [{}, {}, {}], OIDC_ALLOW_BEARER: true }).oidc
    expect(oidc).toEqual({ on: true, what: 'sso.example/realms/ops', sub: '3 mapping rules · provider tokens accepted in API calls' })
    // OAuth2 logins: a client ID and a secret, which reads back as $encrypted$.
    expect(methodStates({ SOCIAL_AUTH_GOOGLE_OAUTH2_KEY: 'id', SOCIAL_AUTH_GOOGLE_OAUTH2_SECRET: '' })['google-oauth2'].on).toBe(false)
    expect(methodStates({ SOCIAL_AUTH_GOOGLE_OAUTH2_KEY: ' ', SOCIAL_AUTH_GOOGLE_OAUTH2_SECRET: '$encrypted$' })['google-oauth2'].on).toBe(false)
    const google = methodStates({ SOCIAL_AUTH_GOOGLE_OAUTH2_KEY: 'id', SOCIAL_AUTH_GOOGLE_OAUTH2_SECRET: '$encrypted$', SOCIAL_AUTH_GOOGLE_OAUTH2_WHITELISTED_DOMAINS: ['example.com'] })
    expect(google['google-oauth2']).toEqual({ on: true, what: 'example.com', sub: 'No map: people join no organization' })
    // SAML: an identity provider at least.
    expect(methodStates({ SOCIAL_AUTH_SAML_ENABLED_IDPS: { okta: {}, adfs: {} } }).saml).toEqual({ on: true, what: 'okta, adfs', sub: '2 identity providers' })
  })

  it('count and name the LDAP servers', () => {
    const ldap = methodStates({ AUTH_LDAP_SERVER_URI: 'ldaps://ldap1.example:636 ldap://ldap2.example', AUTH_LDAP_3_SERVER_URI: 'ldap://ad.example,ldap://ldap1.example' }).ldap
    expect(ldap).toEqual({ on: true, what: 'ldap1.example, ldap2.example, ad.example', sub: '2 servers of 6, tried in order' })
    expect(methodStates({ AUTH_LDAP_2_SERVER_URI: 'ldap://one.example' }).ldap.sub).toBe('1 server of 6')
    expect(ldapHosts('')).toEqual([])
  })

  it('say what an OAuth2 login\'s own maps do', () => {
    const v = { SOCIAL_AUTH_AZUREAD_OAUTH2_KEY: 'client', SOCIAL_AUTH_AZUREAD_OAUTH2_SECRET: '$encrypted$', SOCIAL_AUTH_AZUREAD_OAUTH2_ORGANIZATION_MAP: { Default: { users: true } } }
    expect(methodStates(v)['azuread-oauth2']).toEqual({ on: true, what: 'client', sub: 'Its map places people in 1 organization' })
  })

  it('know local accounts for everyone or superusers only', () => {
    expect(localState({ DISABLE_LOCAL_AUTH: false }).what).toBe('Everyone with a password')
    expect(localState({ DISABLE_LOCAL_AUTH: true }).what).toBe('Superusers only')
  })

  it('show URLs without their scheme', () => {
    expect(bareURL('https://sso.example/realms/x/')).toBe('sso.example/realms/x')
    expect(bareURL(null)).toBe('')
  })
})

describe('GitHub', () => {
  it('has a variant for each of its six AWX categories', () => {
    expect(githubVariants.map((v) => v.category)).toEqual(['github', 'github-org', 'github-team', 'github-enterprise', 'github-enterprise-org', 'github-enterprise-team'])
    for (const v of githubVariants) {
      expect(githubVariant(v.host, v.who)).toBe(v)
      expect(v.prefix).toBe(`SOCIAL_AUTH_${v.category.toUpperCase().replace(/-/g, '_')}_`)
    }
  })

  it('finds the variant of a setting by its prefix and field', () => {
    expect(githubVariantOf('SOCIAL_AUTH_GITHUB_KEY')?.category).toBe('github')
    expect(githubVariantOf('SOCIAL_AUTH_GITHUB_ORG_NAME')?.category).toBe('github-org')
    expect(githubVariantOf('SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_ID')?.category).toBe('github-enterprise-team')
    // Not github-team's MAP, nor github-enterprise-team's.
    expect(githubVariantOf('SOCIAL_AUTH_GITHUB_TEAM_MAP')?.category).toBe('github')
    expect(githubVariantOf('SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_MAP')?.category).toBe('github-enterprise')
    expect(githubVariantOf('SOCIAL_AUTH_GITHUB_TEAM_TEAM_MAP')?.category).toBe('github-team')
    expect(githubVariantOf('SOCIAL_AUTH_GOOGLE_OAUTH2_KEY')).toBeUndefined()
  })

  it('shows the variant linked to, else the first on, else github.com for any account', () => {
    const on = { SOCIAL_AUTH_GITHUB_TEAM_KEY: 'id', SOCIAL_AUTH_GITHUB_TEAM_SECRET: '$encrypted$', SOCIAL_AUTH_GITHUB_ENTERPRISE_KEY: 'id', SOCIAL_AUTH_GITHUB_ENTERPRISE_SECRET: '$encrypted$' }
    expect(githubChosen({}).category).toBe('github')
    expect(githubChosen(on).category).toBe('github-team')
    expect(githubChosen(on, 'SOCIAL_AUTH_GITHUB_ORG_NAME').category).toBe('github-org')
    expect(githubChosen(on, 'maps').category).toBe('github-team')
  })

  it('says who a login lets in, and counts the others', () => {
    const v = {
      SOCIAL_AUTH_GITHUB_ORG_KEY: 'id',
      SOCIAL_AUTH_GITHUB_ORG_SECRET: '$encrypted$',
      SOCIAL_AUTH_GITHUB_ORG_NAME: 'acme',
      SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_KEY: 'id',
      SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_SECRET: '$encrypted$',
      SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_URL: 'https://git.corp.example/',
      SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_ID: '42',
    }
    expect(methodStates(v).github).toEqual({ on: true, what: 'github.com · members of acme', sub: 'And 1 other GitHub login' })
    expect(githubWho(v, githubVariant('enterprise', 'team'))).toBe('git.corp.example · members of team 42')
    expect(githubWho({}, githubVariant('enterprise', 'any'))).toBe('GitHub Enterprise · any account')
  })
})
