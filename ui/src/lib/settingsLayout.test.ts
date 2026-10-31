import { describe, expect, it } from 'vitest'
import { categoryPage, changesByCategory, childPages, hasNoEffect, pageSections, placements, settingLabel, settingPlace, settingsPage, settingsPages } from './settingsLayout'

// AWX's settings categories, as the API lists them at settings/: they do
// not change, so AWX's clients keep working.
const awxCategories = [
  'system',
  'jobs',
  'authentication',
  'oidc',
  'ldap',
  'saml',
  'github',
  'github-org',
  'github-team',
  'github-enterprise',
  'github-enterprise-org',
  'github-enterprise-team',
  'google-oauth2',
  'azuread-oauth2',
  'logging',
  'ui',
  'named-url',
]

describe('the settings layout', () => {
  it('shows each AWX category on one page', () => {
    for (const c of awxCategories) expect(settingsPages.filter((p) => p.categories.includes(c)).map((p) => p.slug), c).toHaveLength(1)
    expect(settingsPages.flatMap((p) => p.categories).sort()).toEqual([...awxCategories].sort())
  })

  it('places each setting once, in rows that hold together', () => {
    const seen = new Set<string>()
    for (const p of settingsPages) {
      const ids = p.sections.map((s) => s.id)
      expect(new Set(ids).size, p.slug).toBe(ids.length)
      for (const s of p.sections)
        for (const r of s.rows) {
          expect(r.keys.length).toBeGreaterThan(0)
          if (r.parts) expect(r.parts, r.keys.join()).toHaveLength(r.keys.length)
          if (r.keys.length > 1) expect(r.label, r.keys.join()).toBeTruthy()
          for (const k of r.keys) {
            expect(seen.has(k), k).toBe(false)
            seen.add(k)
          }
        }
    }
    expect(placements().size).toBe(seen.size)
    expect(new Set(settingsPages.map((p) => p.slug)).size).toBe(settingsPages.length)
  })

  it('has a menu of pages, with the login methods under Sign-in', () => {
    expect(settingsPages.filter((p) => !p.parent).map((p) => p.slug)).toEqual(['system', 'signin', 'jobs', 'logging', 'ui'])
    expect(childPages('signin').map((p) => p.slug)).toEqual(['oidc', 'ldap', 'saml', 'github', 'google-oauth2', 'azuread-oauth2'])
    for (const p of settingsPages) if (p.parent) expect(settingsPage(p.parent), p.slug).toBeDefined()
  })

  it('reaches every setting: those it does not place are added to their category\'s page', () => {
    const jobs = settingsPage('jobs')!
    const categories = { DEFAULT_JOB_TIMEOUT: 'jobs', MAX_FORKS: 'jobs', A_NEW_JOBS_SETTING: 'jobs', AN_OLD_ONE: 'jobs', TOWER_URL_BASE: 'system' }
    const sections = pageSections(jobs, categories, (k) => k === 'AN_OLD_ONE')
    const keys = (id: string) => sections.find((s) => s.id === id)?.rows.flatMap((r) => r.keys)
    // Sections without any of the API's settings are left out.
    expect(sections.map((s) => s.id)).toEqual(['limits', 'other', 'no-effect'])
    expect(keys('limits')).toEqual(['DEFAULT_JOB_TIMEOUT', 'MAX_FORKS'])
    expect(keys('other')).toEqual(['A_NEW_JOBS_SETTING'])
    expect(keys('no-effect')).toEqual(['AN_OLD_ONE'])
  })

  it('keeps the parts of a row the API has', () => {
    const sections = pageSections(settingsPage('jobs')!, { DEFAULT_INVENTORY_UPDATE_TIMEOUT: 'jobs' })
    expect(sections[0].rows[0]).toMatchObject({ keys: ['DEFAULT_INVENTORY_UPDATE_TIMEOUT'], parts: ['Inventory syncs'], label: 'Sync timeouts' })
  })

  it('keeps the login methods on Sign-in', () => {
    const sections = pageSections(settingsPage('signin')!, { SESSION_COOKIE_AGE: 'authentication' })
    expect(sections.map((s) => s.id)).toEqual(['login-methods', 'sessions'])
  })

  it('folds an LDAP server without a URI', () => {
    const [first, second] = settingsPage('ldap')!.sections
    expect(first.on).toBeUndefined()
    expect(second.on!({ AUTH_LDAP_1_SERVER_URI: '' })).toBe(false)
    expect(second.on!({ AUTH_LDAP_1_SERVER_URI: 'ldaps://dc1.example.com' })).toBe(true)
    expect(second.rows.map((r) => r.keys[0])).toContain('AUTH_LDAP_1_BIND_PASSWORD')
  })

  it('sends a former category page to where its settings are', () => {
    expect(categoryPage('authentication')).toEqual({ slug: 'signin' })
    expect(categoryPage('github-enterprise-org')).toEqual({ slug: 'github', section: 'github-enterprise-org' })
    expect(categoryPage('github')).toEqual({ slug: 'github' })
    expect(categoryPage('named-url')).toEqual({ slug: 'system', section: 'named-url' })
    expect(categoryPage('oidc')).toEqual({ slug: 'oidc' })
    expect(categoryPage('jobs')).toEqual({ slug: 'jobs' })
    expect(categoryPage('nope')).toBeUndefined()
  })

  it('says where a setting is', () => {
    expect(settingPlace('DEFAULT_PROJECT_UPDATE_TIMEOUT', 'jobs')).toEqual({ page: 'jobs', section: 'limits', anchor: 'DEFAULT_PROJECT_UPDATE_TIMEOUT' })
    expect(settingPlace('DEFAULT_INVENTORY_UPDATE_TIMEOUT', 'jobs')).toEqual({ page: 'jobs', section: 'limits', anchor: 'DEFAULT_PROJECT_UPDATE_TIMEOUT' })
    expect(settingPlace('SOCIAL_AUTH_GITHUB_TEAM_ID', 'github-team')).toEqual({ page: 'github', section: 'github-team', anchor: 'SOCIAL_AUTH_GITHUB_TEAM_ID' })
    expect(settingPlace('A_NEW_SETTING', 'logging')).toEqual({ page: 'logging', section: 'other', anchor: 'A_NEW_SETTING' })
    expect(settingPlace('AN_OLD_ONE', 'ui', true)).toEqual({ page: 'ui', section: 'no-effect', anchor: 'AN_OLD_ONE' })
    expect(settingPlace('A_NEW_SETTING', 'nope')).toBeUndefined()
  })

  it('labels a setting as its row does', () => {
    expect(settingLabel('MAX_FORKS')).toBe('Forks per job, at most')
    expect(settingLabel('DEFAULT_PROJECT_UPDATE_TIMEOUT')).toBe('Sync timeouts: Project syncs')
    expect(settingLabel('TOWER_URL_BASE')).toBeUndefined()
    expect(settingLabel('A_NEW_SETTING')).toBeUndefined()
  })

  it('saves changes to the categories they belong to', () => {
    const values = { MAX_FORKS: 50, TOWER_URL_BASE: 'https://awx.example.com', SCHEDULE_MAX_JOBS: 5 }
    const categories = { MAX_FORKS: 'jobs', TOWER_URL_BASE: 'system', SCHEDULE_MAX_JOBS: 'jobs' }
    expect(changesByCategory(['MAX_FORKS', 'TOWER_URL_BASE', 'SCHEDULE_MAX_JOBS', 'GONE'], values, categories)).toEqual({
      jobs: { MAX_FORKS: 50, SCHEDULE_MAX_JOBS: 5 },
      system: { TOWER_URL_BASE: 'https://awx.example.com' },
    })
  })

  it('knows the settings that have no effect from their help', () => {
    expect(hasNoEffect('No effect in awxnext: it has no Pendo integration.')).toBe(true)
    expect(hasNoEffect('Maximum number of forks.')).toBe(false)
    expect(hasNoEffect(undefined)).toBe(false)
  })
})
