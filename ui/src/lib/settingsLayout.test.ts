import { describe, expect, it } from 'vitest'
import { categoryPage, changesByCategory, childPages, githubForm, hasNoEffect, inSettings, pageSections, placements, searchSettings, settingLabel, settingPlace, settingsPage, settingsPages, visibleSections, type SettingInfo } from './settingsLayout'
import { githubVariantOf, githubVariants } from './loginMethods'

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
    expect(settingLabel('TOWER_URL_BASE')).toBe('Service URL')
    expect(settingLabel('AUTH_LDAP_3_BIND_DN')).toBe('Bind DN')
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

  it('labels every setting it places in plain words, not with AWX\'s label', () => {
    for (const p of settingsPages)
      for (const s of p.sections)
        for (const r of s.rows) {
          expect(r.label, r.keys.join()).toBeTruthy()
          if (r.keys.length > 1) expect(r.parts?.every(Boolean), r.keys.join()).toBe(true)
          // AWX's labels name the login method or start with "Enable": the page already says which.
          expect(r.label, r.keys.join()).not.toMatch(/^(LDAP|SAML|OIDC|GitHub|Google|Azure|Logging|Enable)\b|OAuth2/)
        }
  })

  it('hides the settings with no effect unless asked for', () => {
    const jobs = settingsPage('jobs')!
    const categories = { DEFAULT_JOB_TIMEOUT: 'jobs', AN_OLD_ONE: 'jobs', ANOTHER: 'jobs' }
    const sections = pageSections(jobs, categories, (k) => k !== 'DEFAULT_JOB_TIMEOUT')
    const ids = (v: ReturnType<typeof visibleSections>) => [v.sections.map((s) => s.id), v.hidden]
    const none = () => false
    expect(ids(visibleSections(sections, false, none))).toEqual([['limits'], 2])
    expect(ids(visibleSections(sections, true, none))).toEqual([['limits', 'no-effect'], 0])
    // Shown when one of them is linked to, edited or in error, or their section linked to.
    expect(ids(visibleSections(sections, false, (k) => k === 'ANOTHER'))).toEqual([['limits', 'no-effect'], 0])
    expect(ids(visibleSections(sections, false, none, 'no-effect'))).toEqual([['limits', 'no-effect'], 0])
    expect(ids(visibleSections(sections, false, none, 'limits'))).toEqual([['limits'], 2])
  })

  it('puts every jobs setting in a section named for its purpose', () => {
    // The API's 39 jobs settings; the last four have no effect in awxnext.
    const jobs = [
      ...['AD_HOC_COMMANDS', 'ALLOW_ANSIBLE_VARS_AT_LAUNCH', 'ALLOW_JINJA_IN_EXTRA_VARS', 'ANSIBLE_FACT_CACHE_TIMEOUT', 'AWX_ANSIBLE_CALLBACK_PLUGINS', 'AWX_COLLECTIONS_ENABLED'],
      ...['AWX_ISOLATION_SHOW_PATHS', 'AWX_MOUNT_ISOLATED_PATHS_ON_K8S', 'AWX_ROLES_ENABLED', 'AWX_RUNNER_KEEPALIVE_SECONDS', 'AWX_SHOW_PLAYBOOK_LINKS', 'AWX_TASK_ENV'],
      ...['BULK_HOST_MAX_CREATE', 'BULK_HOST_MAX_DELETE', 'BULK_JOB_MAX_LAUNCH', 'DEFAULT_INVENTORY_UPDATE_TIMEOUT', 'DEFAULT_JOB_IDLE_TIMEOUT', 'DEFAULT_JOB_TIMEOUT'],
      ...['DEFAULT_PROJECT_UPDATE_TIMEOUT', 'GALAXY_IGNORE_CERTS', 'GALAXY_TASK_ENV', 'IGNORE_UNPROMPTED_LAUNCH_FIELDS', 'JOB_EVENTS_ARCHIVE_BUCKET', 'JOB_EVENTS_ARCHIVE_CREDENTIAL'],
      ...['JOB_EVENTS_ARCHIVE_ENDPOINT', 'JOB_EVENTS_ARCHIVE_PREFIX', 'JOB_EVENTS_ARCHIVE_REGION', 'JOB_EVENTS_ARCHIVE_STORAGE', 'JOB_EVENTS_HOT_DAYS', 'JOB_EVENT_MAX_RATE'],
      ...['MAX_FORKS', 'MAX_WEBSOCKET_EVENT_RATE', 'PROJECT_ALLOW_FILE_URLS', 'SCHEDULE_MAX_JOBS', 'STDOUT_MAX_BYTES_DISPLAY'],
    ]
    const noEffect = ['AWX_COLLECTIONS_ENABLED_ON_PROJECT_SYNC', 'AWX_ISOLATION_BASE_PATH', 'EVENT_STDOUT_MAX_BYTES_DISPLAY', 'PROJECT_UPDATE_VVV']
    const categories = Object.fromEntries([...jobs, ...noEffect].map((k) => [k, 'jobs']))
    expect(Object.keys(categories)).toHaveLength(39)
    const sections = pageSections(settingsPage('jobs')!, categories, (k) => noEffect.includes(k))
    expect(sections.map((s) => s.id)).toEqual(['launch-safety', 'limits', 'content', 'execution', 'output', 'no-effect'])
    expect(sections.flatMap((s) => s.rows.flatMap((r) => r.keys)).sort()).toEqual(Object.keys(categories).sort())
  })

  it('says what each launch safety choice lets someone do', () => {
    const safety = settingsPage('jobs')!.sections.find((s) => s.id === 'launch-safety')!
    expect(safety.rows.filter((r) => !r.consequence).map((r) => r.keys[0])).toEqual(['AD_HOC_COMMANDS'])
    // Jinja's choices in plain words, the default first.
    expect(safety.rows.find((r) => r.keys[0] === 'ALLOW_JINJA_IN_EXTRA_VARS')?.options?.map(([v]) => v)).toEqual(['template', 'never', 'always'])
  })

  it('draws each of GitHub\'s six categories as one form, with every setting of it', () => {
    const page = settingsPage('github')!
    for (const v of githubVariants) {
      const form = githubForm(v)
      const keys = [...form.where, ...(form.who ? [form.who] : []), ...form.app, form.maps].flatMap((r) => r.keys)
      const section = page.sections.find((s) => s.id === v.category)!
      expect(keys.sort()).toEqual(section.rows.flatMap((r) => r.keys).sort())
      expect(keys.every((k) => k.startsWith(v.prefix) && githubVariantOf(k) === v)).toBe(true)
    }
    // The callback URL comes first in the OAuth app step: GitHub asks for it before it gives a client ID.
    expect(githubForm(githubVariants[1]).app.map((r) => r.keys[0])).toEqual(['SOCIAL_AUTH_GITHUB_ORG_CALLBACK_URL', 'SOCIAL_AUTH_GITHUB_ORG_KEY', 'SOCIAL_AUTH_GITHUB_ORG_SECRET'])
  })

  it('ends the SAML service provider with what to give the identity providers', () => {
    const sp = settingsPage('saml')!.sections.find((s) => s.id === 'service-provider')!
    expect(sp.rows.slice(-2).map((r) => r.keys[0])).toEqual(['SOCIAL_AUTH_SAML_METADATA_URL', 'SOCIAL_AUTH_SAML_CALLBACK_URL'])
  })

  it('knows the settings that have no effect from their help', () => {
    expect(hasNoEffect('No effect in awxnext: it has no Pendo integration.')).toBe(true)
    expect(hasNoEffect('Maximum number of forks.')).toBe(false)
    expect(hasNoEffect(undefined)).toBe(false)
  })
})

describe('searching the settings', () => {
  // As the API's OPTIONS gives them: AWX's labels and help, each setting's category.
  const info: Record<string, SettingInfo> = {
    DEFAULT_JOB_TIMEOUT: { label: 'Default Job Timeout', help_text: 'Maximum time in seconds to allow jobs to run.', category_slug: 'jobs' },
    DEFAULT_PROJECT_UPDATE_TIMEOUT: { label: 'Default Project Update Timeout', category_slug: 'jobs' },
    DISABLE_LOCAL_AUTH: { label: 'Disable the built-in authentication system', category_slug: 'authentication' },
    SESSION_COOKIE_AGE: { label: 'Idle Time Force Log Out', category_slug: 'authentication' },
    AWX_UNPLACED_THING: { label: 'Délai of something', help_text: 'Waits for the timeout.', category_slug: 'jobs' },
    AWX_OLD_TIMEOUT: { label: 'Old timeout', help_text: 'No effect in awxnext: jobs run elsewhere.', category_slug: 'jobs' },
  }
  const none = (k: string) => hasNoEffect(info[k]?.help_text)
  const keys = (q: string) => searchSettings(q, info, none).map((f) => `${f.key}:${f.by}`)

  it('finds by label first, then by key, then by help, those with no effect last', () => {
    expect(keys('timeout')).toEqual(['DEFAULT_JOB_TIMEOUT:label', 'DEFAULT_PROJECT_UPDATE_TIMEOUT:label', 'AWX_OLD_TIMEOUT:label', 'AWX_UNPLACED_THING:help'])
    // Every word, anywhere in the label, the key or the help; case and accents aside.
    expect(keys('TIMEOUT jobs run')).toEqual(['AWX_OLD_TIMEOUT:help'])
    expect(keys('delai')).toEqual(['AWX_UNPLACED_THING:label'])
    expect(keys('timeout nowhere')).toEqual([])
    expect(keys('  ')).toEqual([])
  })

  it('finds a setting by its key, or its key\'s words', () => {
    expect(keys('DISABLE_LOCAL_AUTH')).toEqual(['DISABLE_LOCAL_AUTH:key'])
    expect(keys('cookie age')).toEqual(['SESSION_COOKIE_AGE:key'])
  })

  it('says where each one is, with the layout\'s label', () => {
    const [job, sync] = searchSettings('timeout', info, none)
    expect(job).toMatchObject({ label: 'Job timeout', place: { page: 'jobs', section: 'limits', anchor: 'DEFAULT_JOB_TIMEOUT' }, pageTitle: 'Jobs', sectionTitle: 'Limits and timeouts' })
    expect(sync).toMatchObject({ label: 'Sync timeouts: Project syncs', place: { anchor: 'DEFAULT_PROJECT_UPDATE_TIMEOUT' } })
    const [auth] = searchSettings('disable local', info, none)
    expect(auth.label).toBe('Password login for everyone')
    expect(auth.place).toEqual(settingPlace('DISABLE_LOCAL_AUTH', 'authentication'))
    // The settings the layout does not place are where their page lists them.
    expect(searchSettings('delai', info, none)[0]).toMatchObject({ place: { page: 'jobs', section: 'other', anchor: 'AWX_UNPLACED_THING' }, sectionTitle: 'Other settings' })
    expect(searchSettings('old', info, none)[0].place.section).toBe('no-effect')
  })
})

describe('leaving the settings', () => {
  it('is going to a page that is not a settings page', () => {
    expect(inSettings('/settings')).toBe(true)
    expect(inSettings('/settings/jobs')).toBe(true)
    expect(inSettings('/settings-other')).toBe(false)
    expect(inSettings('/jobs')).toBe(false)
  })
})
