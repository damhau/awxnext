import { useQuery } from '@tanstack/react-query'
import { Fragment, useEffect, useId, useMemo, useState, type ReactNode } from 'react'
import { Link, Navigate, useLocation, useNavigate, useParams } from 'react-router'
import { api, ApiError, getAll } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { FieldMeta, OptionsMeta, Ref } from '../../api/types'
import { useSession } from '../../app/context'
import { FormError, Picker } from '../../components/form'
import { Icon } from '../../components/icons'
import { useConfirm, useLeaveGuard, useToast } from '../../components/overlay'
import { Empty, ErrorNote, Loading, PageHead, Panel } from '../../components/ui'
import { humanize } from './common'
import { grantReason, grantTarget, testerClaims, type MappedGrant } from '../../lib/mapping'
import { samlIdps, statusText, stepLabel, stepState, unsavedFor, type TestStep } from '../../lib/connectionTest'
import {
  asPattern,
  describeExpr,
  formatFlags,
  formatMaps,
  formatSamlFlags,
  formatSamlOrgAttr,
  formatSamlTeamAttr,
  grantRows,
  isPattern,
  lines,
  mapLogin,
  mapProblems,
  newOrg,
  newTeam,
  orgRoles,
  parseAttributes,
  parseFlags,
  parseMaps,
  parseSamlFlags,
  parseSamlOrgAttr,
  parseSamlTeamAttr,
  problemText,
  roleLabel,
  whoKind,
  whoKinds,
  withKind,
  type Expr,
  type MapEntry,
  type Role,
  type SamlFlag,
  type SamlFlags,
  type SamlOrgAttr,
  type SamlTeam,
  type SamlTeamAttr,
} from '../../lib/awxMaps'
import { aggregatorTarget, testOutcome } from '../../lib/logging'
import { archiveCredentialParams } from '../../lib/jobSettings'
import {
  addItems,
  amountText,
  amountValue,
  bestUnit,
  controlKind,
  ENCRYPTED,
  fieldValue,
  mountsOf,
  mountsValue,
  pairsOf,
  pairsValue,
  recordsOf,
  recordsValue,
  searchesOf,
  searchesValue,
  searchScopes,
  unitsOf,
  valueText,
  withField,
  type Field,
  type Mount,
  type NamedRecord,
  type Pair,
  type Search,
} from '../../lib/settingValues'
import {
  githubChosen,
  githubOn,
  githubVariant,
  githubVariants,
  githubWho,
  localState,
  methodStates,
  type GitHubHost,
  type GitHubVariant,
  type GitHubWho,
  type MethodState,
} from '../../lib/loginMethods'
import type { Backend } from '../../lib/sso'
import {
  categoryPage,
  changesByCategory,
  childPages,
  githubForm,
  hasNoEffect,
  inSettings,
  layoutRow,
  pageSections,
  rowAnchor,
  searchSettings,
  settingLabel,
  settingPlace,
  settingsPage,
  settingsPages,
  visibleSections,
  type Row,
  type Section,
  type SettingsPage,
} from '../../lib/settingsLayout'
import { asIs, tr, trn } from '../../lib/i18n'
import { useStored } from '../../lib/storage'
import './settings.css'

// The settings pages are drawn from the layout (lib/settingsLayout.ts): its
// pages, their sections and rows. Every setting is loaded at once, from
// settings/all/; changes are kept across pages until saved, and saving
// sends each AWX category its own changes.

type Values = Record<string, unknown>

interface AllSettings {
  meta: Record<string, FieldMeta>
  writable: Set<string>
  /** Each setting's AWX category. */
  categories: Record<string, string>
}

const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null)
const metaLabel = (k: string, m?: FieldMeta) => (m?.label && m.label !== k ? m.label : humanize(k))
const defaultOf = (m: FieldMeta) => m.default ?? (m.type === 'boolean' ? false : m.type === 'list' ? [] : '')
const atDefault = (v: unknown, m: FieldMeta) => same(v, m.default) || (v === '' && (m.default ?? '') === '')

function useAllSettings() {
  const options = useQuery({ queryKey: ['settings/all/', 'options'], queryFn: () => api.options<OptionsMeta>('settings/all/') })
  const values = useObject<Values>('settings/all/')
  const data = useMemo<AllSettings | null>(() => {
    const get = options.data?.actions?.GET
    if (!get) return null
    const categories: Record<string, string> = {}
    for (const [k, m] of Object.entries(get)) categories[k] = String(m.category_slug ?? '')
    return { meta: get, writable: new Set(Object.keys(options.data?.actions?.PUT ?? {})), categories }
  }, [options.data])
  return { data, saved: values.data, error: options.error ?? values.error }
}

export function Settings() {
  const { me } = useSession()
  const slug = useParams().category ?? 'system'
  const { hash } = useLocation()
  const { data, saved, error: loadError } = useAllSettings()
  const changed = useObject<Values>('settings/changed/')
  const [edits, setEdits] = useState<Values>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const confirm = useConfirm()
  const invalidate = useInvalidate()
  const editable = me.is_superuser
  const [showNoEffect, setShowNoEffect] = useStored('settings.noEffect', false)
  const page = settingsPage(slug)
  const loaded = !!(data && saved)
  const states = useMemo(() => (saved ? methodStates(saved) : undefined), [saved])
  // The GitHub variant the form shows, once chosen; until then, the one linked to or on.
  const [ghChoice, setGhChoice] = useState<string | null>(null)
  useEffect(() => setGhChoice(null), [hash, slug])
  // A link to a section or a setting: scrolled to once the page is drawn.
  useEffect(() => {
    if (loaded && hash) document.getElementById(decodeURIComponent(hash.slice(1)))?.scrollIntoView({ block: 'start' })
  }, [loaded, hash, slug])
  // Changes are kept from one settings page to another until saved: leaving the settings asks first.
  const dirty = saved ? Object.keys(edits).filter((k) => !same(edits[k], saved[k])) : []
  useLeaveGuard(editable ? dirty.length : 0, (path) => !inSettings(path))
  if (!page && slug !== 'changed') {
    // A former category page (/settings/authentication, /settings/github-org…).
    const moved = categoryPage(slug)
    return <Navigate replace to={moved ? `/settings/${moved.slug}${moved.section ? `#${moved.section}` : ''}` : '/settings/system'} />
  }
  const values: Values = { ...saved, ...edits }
  // The settings with no effect are hidden unless asked for, linked to, edited or in error.
  const target = decodeURIComponent(hash.slice(1))
  const { sections, hidden } = visibleSections(
    page && data ? pageSections(page, data.categories, (k) => hasNoEffect(data.meta[k]?.help_text)) : [],
    showNoEffect,
    (k) => k === target || dirty.includes(k) || !!errors[k],
    target,
  )
  const gh = slug === 'github' && saved ? (githubVariants.find((v) => v.category === ghChoice) ?? githubChosen(saved, target)) : undefined
  // A login method's page says whether it is on.
  const methodOn = page?.parent === 'signin' && saved ? (gh ? githubOn(saved, gh) : !!states?.[slug]?.on) : undefined
  const index = gh ? githubIndex() : sections
  const set = (k: string, v: unknown) => {
    setEdits((e) => ({ ...e, [k]: v }))
    if (errors[k]) setErrors(({ [k]: _, ...rest }) => rest)
  }
  const save = async () => {
    if (!data) return
    setBusy(true)
    setError(null)
    const left: Values = { ...edits }
    const failed: Record<string, string> = {}
    let other: unknown = null
    let n = 0
    for (const [cat, body] of Object.entries(changesByCategory(dirty, edits, data.categories))) {
      try {
        await api.patch(`settings/${cat}/`, body)
        for (const k of Object.keys(body)) delete left[k]
        n += Object.keys(body).length
      } catch (e) {
        const fields = e instanceof ApiError ? e.fields : {}
        if (Object.keys(fields).length) Object.assign(failed, fields)
        else other ??= e
      }
    }
    await invalidate('settings/')
    setEdits(left)
    setErrors(failed)
    setError(other)
    setBusy(false)
    if (n) toast(trn(n, 'Saved {n} setting', 'Saved {n} settings'))
    if (other || Object.keys(failed).length) toast(tr('Some changes were not saved: see the settings marked.'), 'error')
  }
  const discard = () => {
    setEdits({})
    setErrors({})
    setError(null)
  }
  // Resetting a page resets the AWX categories it shows: each is on one page only.
  const resettable = page && data ? page.categories.filter((c) => Object.keys(data.categories).some((k) => data.categories[k] === c && data.writable.has(k))) : []
  const resetCategories = async (cats: string[], ask: { title: string; body?: ReactNode; confirm: string }, done: string) => {
    if (!data) return
    if (!(await confirm({ ...ask, danger: true }))) return
    try {
      for (const c of cats) await api.del(`settings/${c}/`)
      toast(done)
    } catch (e) {
      toast((e as Error).message, 'error')
    }
    await invalidate('settings/')
    setEdits((e) => Object.fromEntries(Object.entries(e).filter(([k]) => !cats.includes(data.categories[k]))))
  }
  const resetPage = () => page && resetCategories(resettable, { title: tr('Reset every setting of {page} to its default?', { page: page.title }), confirm: tr('Reset') }, tr('Reset to defaults'))
  // Turning a login method off resets its settings; GitHub's, those of the variant shown.
  const turnOff = () => {
    if (!page || !saved) return
    const method = gh ? `${page.title} (${githubWho(saved, gh)})` : page.title
    return resetCategories(
      gh ? [gh.category] : page.categories,
      { title: tr('Turn off {method}?', { method }), body: tr('Its settings go back to their defaults, secrets included, and people can no longer sign in with it.'), confirm: tr('Turn off') },
      tr('{method} turned off', { method }),
    )
  }
  const placed = data ? dirty.map((k) => settingPlace(k, data.categories[k], hasNoEffect(data.meta[k]?.help_text))?.page) : []
  const elsewhere = settingsPages.filter((p) => p.slug !== slug && placed.includes(p.slug))
  const ctx: Ctx | null = data && saved ? { data, saved, values, set, errors, editable, page: slug, hash: hash.slice(1) } : null
  return (
    <div className="page settings">
      <SettingsNav active={slug} sections={sections} states={states} changed={changed.data ? Object.keys(changed.data).length : undefined} meta={data?.meta} />
      <div className="settings-main">
        <PageHead
          crumbs={page?.parent ? [{ label: settingsPage(page.parent)?.title ?? page.parent, to: `/settings/${page.parent}` }] : undefined}
          title={page?.title ?? tr('Changed from default')}
          badge={methodOn !== undefined && <OnBadge on={methodOn} />}
          actions={
            methodOn &&
            editable && (
              <button type="button" className="btn ghost" onClick={turnOff}>
                {tr('Turn off…')}
              </button>
            )
          }
          subtitle={page?.blurb ?? tr('Every setting that differs from its default, and where it is.')}
        >
          {index.length > 1 && (
            <nav className="s-index" aria-label={tr('On this page')}>
              {index.map((s) => (
                <a key={s.id} href={`#${s.id}`}>
                  {s.title}
                </a>
              ))}
            </nav>
          )}
        </PageHead>
        <div className={`page-body${gh ? ' steps' : ''}`}>
          {!editable && <div className="notice">{tr('Only superusers can change settings. You can review them here.')}</div>}
          {loadError ? (
            <ErrorNote error={loadError} />
          ) : !ctx ? (
            <Loading />
          ) : !page ? (
            changed.error ? <ErrorNote error={changed.error} /> : changed.data ? <ChangedSettings all={ctx.data} changed={changed.data} /> : <Loading />
          ) : gh ? (
            <GitHubForm v={gh} ctx={ctx} choose={(v) => setGhChoice(v.category)} />
          ) : (
            sections.map((s) => <SectionView key={s.id} section={s} ctx={ctx} />)
          )}
          {page && ctx && hidden > 0 && (
            <p className="s-hidden">
              {trn(hidden, '{n} AWX setting that has no effect in awxnext is hidden.', '{n} AWX settings that have no effect in awxnext are hidden.')}{' '}
              <button type="button" className="link-btn" onClick={() => setShowNoEffect(true)}>
                {trn(hidden, 'Show it', 'Show them')}
              </button>
            </p>
          )}
          {page && ctx && showNoEffect && sections.some((s) => s.noEffect) && (
            <p className="s-hidden">
              <button type="button" className="link-btn" onClick={() => setShowNoEffect(false)}>
                {tr('Hide the settings with no effect')}
              </button>
            </p>
          )}
          <FormError error={error} />
          {page && ctx && editable && resettable.length > 0 && (
            <button type="button" className="btn ghost s-reset" onClick={resetPage}>
              {tr('Reset this page to defaults')}
            </button>
          )}
        </div>
        {editable && (dirty.length > 0 || busy) && (
          <div className="s-savebar" role="region" aria-label={tr('Unsaved changes')}>
            <span className="what">
              {trn(dirty.length, '{n} change not saved', '{n} changes not saved')}
              {elsewhere.length > 0 && (
                <span className="others">
                  {' · '}
                  {tr('Also on:')}{' '}
                  {elsewhere.map((p, i) => (
                    <Fragment key={p.slug}>
                      {i ? ', ' : ''}
                      <Link to={`/settings/${p.slug}`}>{p.title}</Link>
                    </Fragment>
                  ))}
                </span>
              )}
            </span>
            <button type="button" className="btn ghost" disabled={busy} onClick={discard}>
              {tr('Discard')}
            </button>
            <button type="button" className="btn primary" disabled={busy || dirty.length === 0} onClick={save}>
              {busy ? tr('Saving…') : tr('Save')}
            </button>
          </div>
        )}
      </div>
    </div>
  )
}

/** The menu: the pages, the login methods under Sign-in, the current page's sections. */
function SettingsNav({ active, sections, states, changed, meta }: { active: string; sections: Section[]; states?: Record<string, MethodState>; changed?: number; meta?: Record<string, FieldMeta> }) {
  const [query, setQuery] = useState('')
  const link = (p: SettingsPage) => {
    const state = p.parent ? states?.[p.slug] : undefined
    return (
      <Link key={p.slug} to={`/settings/${p.slug}`} className={p.slug === active ? 'active' : ''} aria-current={p.slug === active ? 'page' : undefined}>
        {state && <span className="cell" data-size="xs" data-state={state.on ? 'ok' : 'none'} title={state.on ? tr('Enabled') : tr('Not set up')} />}
        {p.menu ?? p.title}
      </Link>
    )
  }
  return (
    <nav className="sidelist settings-nav" aria-label={tr('Settings')}>
      <SettingsSearch meta={meta} query={query} setQuery={setQuery} />
      {query.trim() === '' && (
        <>
          <div className="group">
            <div className="group-head">
              <span>{tr('Settings')}</span>
            </div>
            {settingsPages
              .filter((p) => !p.parent)
              .map((p) => {
                const children = childPages(p.slug)
                return (
                  <Fragment key={p.slug}>
                    {link(p)}
                    {children.length > 0 && <div className="sub">{children.map(link)}</div>}
                    {children.length === 0 && p.slug === active && sections.length > 1 && (
                      <div className="sub">
                        {sections.map((s) => (
                          <a key={s.id} href={`#${s.id}`}>
                            {s.title}
                          </a>
                        ))}
                      </div>
                    )}
                  </Fragment>
                )
              })}
          </div>
          <div className="group">
            <Link to="/settings/changed" className={`changed-link ${active === 'changed' ? 'active' : ''}`} aria-current={active === 'changed' ? 'page' : undefined}>
              <span className="cell" data-size="xs" data-state="changed" aria-hidden="true" />
              {tr('Changed from default')}
              {changed !== undefined && <span className="n">{changed}</span>}
            </Link>
          </div>
        </>
      )}
    </nav>
  )
}

/** The search of the settings menu: by label, key or help (lib/settingsLayout.ts).
 * The results replace the menu while it has words; Enter goes to the first. */
function SettingsSearch({ meta, query, setQuery }: { meta?: Record<string, FieldMeta>; query: string; setQuery: (q: string) => void }) {
  const nav = useNavigate()
  const found = useMemo(() => (meta ? searchSettings(query, meta, (k) => hasNoEffect(meta[k]?.help_text), (k) => metaLabel(k, meta[k])) : []), [meta, query])
  const to = (f: (typeof found)[number]) => `/settings/${f.place.page}#${f.place.anchor}`
  return (
    <>
      <label className="s-search">
        <Icon name="search" size={14} />
        <input
          className="input"
          type="search"
          data-filter
          placeholder={tr('Search settings')}
          aria-label={tr('Search settings')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && found[0]) nav(to(found[0]))
            if (e.key === 'Escape') setQuery('')
          }}
        />
        {!query && <span className="key" aria-hidden="true">/</span>}
      </label>
      {query.trim() !== '' && (
        <div className="group s-found" role="region" aria-label={tr('Settings found')}>
          <div className="group-head">
            <span>{meta ? trn(found.length, '{n} setting found', '{n} settings found') : tr('Loading…')}</span>
          </div>
          {found.slice(0, 50).map((f) => (
            <Link key={f.key} to={to(f)}>
              <span className="what">{f.label}</span>
              <span className="where">
                {f.pageTitle} › {f.sectionTitle}
                {f.by !== 'label' && <> · <span className="mono">{f.key}</span></>}
              </span>
            </Link>
          ))}
          {found.length > 50 && <p className="s-more">{tr('More settings match: add a word.')}</p>}
        </div>
      )}
    </>
  )
}

interface Ctx {
  data: AllSettings
  saved: Values
  /** The saved values with the changes not saved yet. */
  values: Values
  set: (k: string, v: unknown) => void
  errors: Record<string, string>
  editable: boolean
  /** The page shown, and the anchor linked to. */
  page: string
  hash: string
}

function SectionView({ section, ctx }: { section: Section; ctx: Ctx }) {
  const rows = (
    <div className="s-rows">
      {ctx.page === 'oidc' && section.id === 'provider' && <OIDCRedirect />}
      {section.rows.map((r) => (
        <SettingRow key={rowAnchor(r)} row={r} ctx={ctx} />
      ))}
      {section.test && ctx.editable && <ConnectionTest login={section.test} ctx={ctx} />}
      {ctx.page === 'logging' && section.id === 'aggregator' && (
        <div className="srow">
          <div>
            <div className="s-name">{tr('Send a test message')}</div>
            <p className="s-help">{tr('To the aggregator, with the saved settings.')}</p>
          </div>
          <div className="s-control">
            <div className="row wrap">
              <LoggingTest values={ctx.saved} unsaved={Object.keys(ctx.data.categories).some((k) => ctx.data.categories[k] === 'logging' && !same(ctx.values[k], ctx.saved[k]))} />
            </div>
          </div>
        </div>
      )}
    </div>
  )
  if (section.on) {
    // An LDAP server: folded while it has no URI, unless it holds the change or error linked to.
    const on = section.on(ctx.values)
    const open = on || ctx.hash === section.id || section.rows.some((r) => r.keys.some((k) => ctx.errors[k] || k === ctx.hash || !same(ctx.values[k], ctx.saved[k])))
    return (
      <details className="s-section" id={section.id} open={open}>
        <summary>
          <h2>{section.title}</h2>
          {!on && <span className="off">{tr('off')}</span>}
        </summary>
        {rows}
      </details>
    )
  }
  return (
    <section className={`s-section ${section.noEffect ? 'no-effect' : ''}`} id={section.id}>
      <h2>{section.title}</h2>
      {section.intro && <p className="s-intro">{section.intro}</p>}
      {section.children ? <LoginMethods ctx={ctx} /> : rows}
    </section>
  )
}

/** A login method on or off, in a word. */
function OnBadge({ on }: { on: boolean }) {
  return (
    <span className="badge ok-badge">
      <span className="cell" data-size="xs" data-state={on ? 'ok' : 'none'} aria-hidden="true" />
      {on ? tr('Enabled') : tr('Not set up')}
    </span>
  )
}

/** The login methods, those on first: local accounts, then a card for each, opening its page. */
function LoginMethods({ ctx }: { ctx: Ctx }) {
  const states = methodStates(ctx.saved)
  const local = localState(ctx.saved)
  const pages = childPages(ctx.page)
  const ordered = [...pages.filter((p) => states[p.slug]?.on), ...pages.filter((p) => !states[p.slug]?.on)]
  return (
    <div className="methods">
      <a className="method on" href="#local-accounts">
        <div className="m-head">
          <span className="cell" data-state="ok" aria-hidden="true" />
          <b>{tr('Local accounts')}</b>
          <span className="m-state">{tr('Enabled')}</span>
        </div>
        <p className="m-what">{local.what}</p>
        <p className="m-sub">{tr('Passwords kept by awxnext. Superusers keep theirs even when local accounts are off for others.')}</p>
      </a>
      {ordered.map((p) => {
        const s = states[p.slug] ?? { on: false }
        return (
          <Link key={p.slug} className={`method ${s.on ? 'on' : 'off'}`} to={`/settings/${p.slug}`}>
            <div className="m-head">
              <span className="cell" data-state={s.on ? 'ok' : 'none'} aria-hidden="true" />
              <b>{p.title}</b>
              <span className="m-state">{s.on ? tr('Enabled') : tr('Not set up')}</span>
            </div>
            {s.what && <p className="m-what">{s.what}</p>}
            <p className="m-sub">{s.on ? s.sub : p.blurb}</p>
            <span className="btn sm">{s.on ? tr('Edit') : tr('Set up')}</span>
          </Link>
        )
      })}
    </div>
  )
}

/** The address OIDC sends people back to: the server's, known once OIDC is on. */
function OIDCRedirect() {
  const backends = useQuery({ queryKey: ['settings/auth-backends'], queryFn: () => api.get<Record<string, Backend & { complete_url?: string }>>('auth/'), retry: false })
  const url = backends.data?.oidc?.complete_url
  return (
    <div className="srow" id="redirect-uri">
      <div>
        <div className="s-name">{tr('Redirect URI')}</div>
        <p className="s-help">{tr('Give it to the provider when you register awxnext as a client.')}</p>
      </div>
      <div className="s-control">
        {url ? <ReadOnlyValue value={url} /> : <span className="note">{tr('Shown once a provider URL and a client ID are saved: the server\'s external URL, AWXNEXT_BASE_URL, followed by /sso/complete/oidc/.')}</span>}
      </div>
    </div>
  )
}

/** The sections of the GitHub form, for the page's index. */
const githubIndex = () => [
  { id: 'who', title: tr('Who may sign in') },
  { id: 'oauth-app', title: tr('OAuth app on GitHub') },
  { id: 'maps', title: tr('Organizations and teams') },
]

/** GitHub's six AWX categories as one form: github.com or GitHub Enterprise,
 * then who may sign in. It edits the settings of the category they make. */
function GitHubForm({ v, ctx, choose }: { v: GitHubVariant; ctx: Ctx; choose: (v: GitHubVariant) => void }) {
  const form = githubForm(v)
  const others = githubVariants.filter((o) => o !== v && githubOn(ctx.saved, o))
  const hosts: [GitHubHost, string][] = [
    ['github.com', asIs('github.com')],
    ['enterprise', tr('GitHub Enterprise')],
  ]
  const whos: [GitHubWho, string, string][] = [
    ['any', tr('Any GitHub account'), tr('Anyone with an account can sign in, and gets what the maps below give.')],
    ['org', tr('Members of an organization'), tr('As in its URL: https://github.com/<organization>')],
    ['team', tr('Members of a team'), tr('The team\'s numeric ID, from GET /orgs/<org>/teams/<team>')],
  ]
  const whoKey = form.who?.keys[0]
  const rows = (rs: Row[]) => rs.map((r) => <SettingRow key={rowAnchor(r)} row={r} ctx={ctx} />)
  return (
    <>
      <section className="s-section" id="who">
        <h2>{tr('Who may sign in')}</h2>
        <div className="s-rows">
          <div className="srow">
            <div>
              <div className="s-name">{asIs('GitHub')}</div>
              <p className="s-help">{tr('github.com, or your own GitHub Enterprise Server.')}</p>
            </div>
            <div className="s-control">
              <div className="segmented" role="radiogroup" aria-label={asIs('GitHub')}>
                {hosts.map(([h, label]) => (
                  <label key={h} className={v.host === h ? 'on' : ''}>
                    <input type="radio" name="gh-host" checked={v.host === h} onChange={() => choose(githubVariant(h, v.who))} />
                    {label}
                  </label>
                ))}
              </div>
            </div>
          </div>
          {rows(form.where)}
          <div className={`srow${whoKey && !same(ctx.values[whoKey], ctx.saved[whoKey]) ? ' edited' : ''}`} id={form.who ? rowAnchor(form.who) : undefined}>
            <div>
              <div className="s-name">{tr('Accounts')}</div>
              <p className="s-help">{tr('GitHub checks membership at each login: someone who leaves the organization or team can no longer sign in.')}</p>
            </div>
            <div className="s-control">
              <div className="choice" role="radiogroup" aria-label={tr('Accounts')}>
                {whos.map(([w, label, help]) => (
                  <Fragment key={w}>
                    <label>
                      <input type="radio" name="gh-who" checked={v.who === w} onChange={() => choose(githubVariant(v.host, w))} />
                      <span>
                        {label}
                        <small>{help}</small>
                      </span>
                    </label>
                    {v.who === w && form.who && whoKey && (
                      <div className="indent">
                        <SettingControl k={whoKey} row={{ ...form.who, label }} ctx={ctx} />
                        {ctx.errors[whoKey] && <span className="err">{ctx.errors[whoKey]}</span>}
                      </div>
                    )}
                  </Fragment>
                ))}
              </div>
              <span className="saved-as">
                {tr('Saved in AWX\'s category')} <code>{v.category}</code>: <code>{v.prefix}*</code>
              </span>
              {others.length > 0 && (
                <span className="note">
                  {tr('Also set up, and offered on the login page:')}{' '}
                  {others.map((o, i) => (
                    <Fragment key={o.category}>
                      {i ? ', ' : ''}
                      <button type="button" className="link-btn" onClick={() => choose(o)}>
                        {githubWho(ctx.saved, o)}
                      </button>
                    </Fragment>
                  ))}
                </span>
              )}
            </div>
          </div>
        </div>
      </section>
      <section className="s-section" id="oauth-app">
        <h2>{tr('OAuth app on GitHub')}</h2>
        <p className="s-intro">{tr('On GitHub, under Settings → Developer settings → OAuth Apps, register an app with this callback URL, then copy its client ID and a client secret here.')}</p>
        <div className="s-rows">{rows(form.app)}</div>
      </section>
      <section className="s-section" id="maps">
        <h2>{tr('Organizations and teams')}</h2>
        <p className="s-intro">{tr('Applied at each login. GitHub uses only these maps, not the default ones: without them, people join no organization.')}</p>
        {/* The section says what the maps do: the row only names them. */}
        <div className="s-rows">{rows([{ ...form.maps, label: tr('Organization and team maps'), help: undefined }])}</div>
      </section>
    </>
  )
}

/** One row: what it sets on the left, with its AWX keys; its control on the right. */
function SettingRow({ row, ctx }: { row: Row; ctx: Ctx }) {
  const { data, values, saved, set, errors, editable } = ctx
  const keys = row.keys
  const single = keys.length === 1
  const k0 = keys[0]
  const label = row.label ?? (single ? metaLabel(k0, data.meta[k0]) : keys.map((k) => metaLabel(k, data.meta[k])).join(', '))
  const metaHelp = data.meta[k0]?.help_text
  const help = row.help ?? (single && metaHelp && metaHelp !== label ? metaHelp : undefined)
  const edited = keys.some((k) => !same(values[k], saved[k]))
  // Changed from its default: the settings that can be changed, with the changes not saved.
  const changed = keys.filter((k) => data.writable.has(k) && data.meta[k] && !atDefault(values[k], data.meta[k]))
  const errs = keys.map((k) => errors[k]).filter(Boolean)
  const defaultText = (k: string) => valueText(defaultOf(data.meta[k]), data.meta[k], row, k)
  return (
    <div className={`srow${edited ? ' edited' : ''}${row.wide ? ' wide' : ''}${ctx.hash === rowAnchor(row) ? ' target' : ''}`} id={rowAnchor(row)}>
      <div>
        <div className="s-name">
          {changed.length > 0 && <span className="cell" data-size="xs" data-state="changed" title={tr('Changed from its default')} aria-label={tr('Changed from its default')} />}
          {single && !row.control ? <label htmlFor={`s-${k0}`}>{label}</label> : label}
          {edited && <span className="unsaved">{tr('unsaved')}</span>}
        </div>
        {help && <p className="s-help">{help}</p>}
        <div className="s-meta">
          {keys.map((k) => (
            <KeyButton key={k} k={k} />
          ))}
          {changed.length > 0 && (
            <span className="s-default">
              {/* A row of several says each part's default, unless they have the same. */}
              {single || new Set(changed.map(defaultText)).size === 1 ? (
                <>
                  {tr('Default:')} <b>{defaultText(changed[0])}</b>
                </>
              ) : (
                changed.map((k, i) => (
                  <Fragment key={k}>
                    {i > 0 && ' · '}
                    {tr('Default for {part}:', { part: row.parts?.[keys.indexOf(k)] ?? metaLabel(k, data.meta[k]) })} <b>{defaultText(k)}</b>
                  </Fragment>
                ))
              )}
              {editable && (
                <>
                  {' · '}
                  <button type="button" className="link-btn" onClick={() => changed.forEach((k) => set(k, defaultOf(data.meta[k])))}>
                    {tr('Reset')}
                  </button>
                </>
              )}
            </span>
          )}
        </div>
      </div>
      <div className="s-control">
        {single ? <SettingControl k={k0} row={row} ctx={ctx} /> : row.control === 'awx-maps' ? <MapsControl row={row} ctx={ctx} /> : <Parts row={row} ctx={ctx} />}
        {row.consequence && <span className="s-consequence">{row.consequence}</span>}
        {errs.map((e, i) => (
          <span key={i} className="err">
            {e}
          </span>
        ))}
      </div>
    </div>
  )
}

/** A setting's AWX key, copied on click. */
function KeyButton({ k }: { k: string }) {
  const copy = useCopy()
  return (
    <button type="button" className="s-key" title={tr('Copy')} onClick={() => copy(k, k)}>
      {k}
    </button>
  )
}

/** The settings of a row of several, each with its part's label. The
 * row's amount unit applies to each; its control to the first. */
function Parts({ row, ctx }: { row: Row; ctx: Ctx }) {
  const meta = (k: string) => ctx.data.meta[k]
  const part = (i: number): Row => ({ ...row, keys: [row.keys[i]], control: i === 0 ? row.control : undefined })
  const label = (k: string, i: number) => row.parts?.[i] ?? metaLabel(k, meta(k))
  if (!row.unit && !row.none && row.keys.every((k) => ['integer', 'float'].includes(meta(k)?.type)))
    return (
      <div className={`amounts amounts-${row.keys.length}`}>
        {row.keys.map((k, i) => (
          <label key={k}>
            {label(k, i)}
            <SettingControl k={k} row={part(i)} ctx={ctx} />
          </label>
        ))}
      </div>
    )
  return (
    <div className="s-parts">
      {row.keys.map((k, i) =>
        meta(k)?.type === 'boolean' ? (
          <SettingControl key={k} k={k} row={{ ...part(i), states: [label(k, i), label(k, i)] }} ctx={ctx} />
        ) : (
          <div key={k} className="amount-l">
            <label htmlFor={`s-${k}`}>{label(k, i)}</label>
            <SettingControl k={k} row={part(i)} ctx={ctx} />
          </div>
        ),
      )}
    </div>
  )
}

/** The control that edits a setting, from the layout's row and its type (lib/settingValues.ts). */
function SettingControl({ k, row, ctx }: { k: string; row: Row; ctx: Ctx }) {
  const meta = ctx.data.meta[k]
  const value = ctx.values[k]
  const disabled = !ctx.editable
  const onChange = (v: unknown) => ctx.set(k, v)
  const id = `s-${k}`
  if (!meta) return null
  switch (controlKind(k, meta.type, row, ctx.data.writable.has(k), value)) {
    case 'readonly':
      return <ReadOnlyValue value={value} />
    case 'oidc-mapping':
      return <MappingEditor value={(value as Rule[]) ?? []} onChange={onChange} editable={!disabled} groupsClaim={String(ctx.values.OIDC_GROUPS_CLAIM ?? 'groups')} />
    case 'archive-credential':
      return <ObjectControl id={id} resource="credentials/" params={archiveCredentialParams(ctx.values.JOB_EVENTS_ARCHIVE_STORAGE)} value={value as number | null} onChange={onChange} disabled={disabled} />
    case 'secret':
      return <SecretControl id={id} value={value} saved={ctx.saved[k]} onChange={onChange} disabled={disabled} lines={row.lines} />
    case 'switch': {
      const on = row.invert ? !value : !!value
      const [off, yes] = row.states ?? [tr('Disabled'), tr('Enabled')]
      const turn = (v: boolean) => onChange(row.invert ? !v : v)
      if (row.segmented)
        return (
          <div className="segmented" role="radiogroup" aria-label={row.label ?? metaLabel(k, meta)}>
            {[off, yes].map((l, i) => (
              <label key={i} className={on === !!i ? 'on' : ''}>
                <input id={i ? undefined : id} type="radio" name={id} checked={on === !!i} disabled={disabled} onChange={() => turn(!!i)} />
                {l}
              </label>
            ))}
          </div>
        )
      return (
        <label className="switch">
          <input id={id} type="checkbox" checked={on} disabled={disabled} onChange={(e) => onChange(row.invert ? !e.target.checked : e.target.checked)} />
          <span className="track" aria-hidden="true" />
          {on ? yes : off}
        </label>
      )
    }
    case 'amount':
      return <AmountControl id={id} value={value} onChange={onChange} disabled={disabled} unit={row.unit} none={row.none} />
    case 'choice':
      return (
        <select id={id} className="select" disabled={disabled} value={String(value ?? '')} onChange={(e) => onChange(e.target.value === '' ? null : e.target.value)}>
          {(meta.default === null || value === null || value === undefined) && <option value="">---------</option>}
          {(row.options ?? meta.choices ?? []).map(([v, l]) => (
            <option key={String(v)} value={String(v)}>
              {l}
            </option>
          ))}
        </select>
      )
    case 'object':
      return <ObjectControl id={id} resource="execution_environments/" value={value as number | null} onChange={onChange} disabled={disabled} meta={(r) => r.image} />
    case 'chips':
      return <ChipsControl id={id} value={value} onChange={onChange} disabled={disabled} />
    case 'checks':
      return <ChecksControl value={value} options={row.options ?? []} onChange={onChange} disabled={disabled} />
    case 'pairs':
      return <PairsControl id={id} value={value} onChange={onChange} disabled={disabled} columns={row.columns ?? [tr('Name'), tr('Value')]} coerce={row.coerce} add={row.add} />
    case 'fields':
      return <FieldsControl id={id} value={value} fields={row.fields ?? []} onChange={onChange} disabled={disabled} />
    case 'ldap-flags':
      return <LdapFlagsControl id={id} value={value} onChange={onChange} disabled={disabled} />
    case 'saml-org-attr':
      return <SamlOrgAttrControl id={id} value={value} onChange={onChange} disabled={disabled} />
    case 'saml-team-attr':
      return <SamlTeamAttrControl id={id} value={value} onChange={onChange} disabled={disabled} />
    case 'saml-flags':
      return <SamlFlagsControl id={id} value={value} onChange={onChange} disabled={disabled} />
    case 'records':
      return <RecordsControl id={id} value={value} fields={row.fields ?? []} name={row.name ?? tr('Name')} add={row.add ?? tr('Add')} onChange={onChange} disabled={disabled} />
    case 'paths':
      return <PathsControl id={id} value={value} onChange={onChange} disabled={disabled} />
    case 'ldap-search':
      return <SearchControl id={id} value={value} onChange={onChange} disabled={disabled} multiple={row.multiple} />
    case 'image':
      return <ImageControl id={id} value={value} onChange={onChange} disabled={disabled} />
    case 'lines':
      return <LinesControl id={id} value={value} onChange={onChange} disabled={disabled} mono={/CERT|KEY/.test(k)} />
    case 'json':
      return <JsonControl id={id} value={value} onChange={onChange} disabled={disabled} />
  }
  return (
    <input
      id={id}
      className={`input ${/URL|PATH|HOST|CLAIM|ENDPOINT|KEY|ENTITY|_DN|URI/.test(k) ? 'mono' : ''}`}
      disabled={disabled}
      autoComplete="off"
      value={String(value ?? '')}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}

/** Text written to the clipboard, said in a toast. */
function useCopy() {
  const toast = useToast()
  return (text: string, what?: string) => navigator.clipboard?.writeText(text).then(() => toast(what ? tr('Copied {key}', { key: what }) : tr('Copied')), () => {})
}

/** A value awxnext sets, to give somewhere else: a callback URL, the installation's ID. */
function ReadOnlyValue({ value }: { value: unknown }) {
  const copy = useCopy()
  if (value === null || value === undefined || value === '') return <span className="note">{tr('Not set')}</span>
  if (typeof value === 'string' || typeof value === 'number')
    return (
      <div className="copyval">
        <code>{String(value)}</code>
        <button type="button" className="btn sm" onClick={() => copy(String(value))}>
          {tr('Copy')}
        </button>
      </div>
    )
  const entries = value && typeof value === 'object' && !Array.isArray(value) ? Object.entries(value) : []
  if (entries.length && entries.every(([, v]) => typeof v === 'string'))
    return (
      <div className="kv kv-ro">
        {entries.map(([k, v]) => (
          <Fragment key={k}>
            <code>{k}</code>
            <code>{String(v)}</code>
          </Fragment>
        ))}
      </div>
    )
  return (
    <details className="s-value">
      <summary>{tr('Show')}</summary>
      <pre>{JSON.stringify(value, null, 2)}</pre>
    </details>
  )
}

/** A secret: set or not, replaced rather than edited, since the API never returns it. */
function SecretControl({ id, value, saved, onChange, disabled, lines }: { id: string; value: unknown; saved: unknown; onChange: (v: unknown) => void; disabled: boolean; lines?: boolean }) {
  const [replacing, setReplacing] = useState(false)
  useEffect(() => {
    if (value === saved) setReplacing(false)
  }, [value, saved])
  const input = (onText: (t: string) => void, text: string, autoFocus = false) =>
    lines ? (
      <textarea id={id} className="textarea mono" rows={5} disabled={disabled} spellCheck={false} autoFocus={autoFocus} value={text} onChange={(e) => onText(e.target.value)} />
    ) : (
      <input id={id} className="input mono" type="password" autoComplete="new-password" disabled={disabled} autoFocus={autoFocus} value={text} onChange={(e) => onText(e.target.value)} />
    )
  if (saved !== ENCRYPTED) return input(onChange, String(value ?? ''))
  if (value === '')
    return (
      <div className="secret">
        <span className="set">{tr('Removed when saved')}</span>
        <button type="button" className="link-btn" onClick={() => onChange(ENCRYPTED)}>
          {tr('Undo')}
        </button>
      </div>
    )
  if (!replacing && value === ENCRYPTED)
    return (
      <div className="secret">
        <span className="set">
          <span className="cell" data-size="xs" data-state="ok" />
          {tr('Set')}
        </span>
        <button type="button" className="btn sm" disabled={disabled} onClick={() => setReplacing(true)}>
          {tr('Replace…')}
        </button>
        <button type="button" className="link-btn" disabled={disabled} onClick={() => onChange('')}>
          {tr('Remove')}
        </button>
      </div>
    )
  // Typing the new value; emptied, the one set stays.
  return (
    <div className="s-list">
      {input((t) => onChange(t === '' ? ENCRYPTED : t), value === ENCRYPTED ? '' : String(value ?? ''), true)}
      <button
        type="button"
        className="link-btn"
        onClick={() => {
          setReplacing(false)
          onChange(ENCRYPTED)
        }}
      >
        {tr('Keep the one set')}
      </button>
    </div>
  )
}

/** A number in its unit, with what stands for none ("No limit") as a checkbox. */
function AmountControl({ id, value, onChange, disabled, unit, none }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean; unit?: string; none?: [number, string] }) {
  const kind = unit === 'duration' || unit === 'size' ? unit : undefined
  const isNone = !!none && value === none[0]
  const [factor, setFactor] = useState(() => (kind ? bestUnit(value, kind) : 1))
  const [limited, setLimited] = useState(!isNone)
  const [text, setText] = useState(() => (isNone ? '' : amountText(value, factor)))
  // A value set from elsewhere (discarded, back to its default): shown in its own unit.
  useEffect(() => {
    if (none) setLimited(value !== none[0])
    if (amountValue(text, factor) === value) return
    const f = kind && (typeof value !== 'number' || value % factor !== 0) ? bestUnit(value, kind) : factor
    setFactor(f)
    setText(none && value === none[0] ? '' : amountText(value, f))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])
  const type = (t: string) => {
    setText(t)
    const v = amountValue(t, factor)
    if (t.trim() === '') onChange(none ? none[0] : null)
    else if (v !== null) onChange(v)
  }
  const off = disabled || !limited
  return (
    <div className="amount">
      <input id={id} className="input" inputMode="decimal" autoComplete="off" disabled={off} value={limited ? text : ''} onChange={(e) => type(e.target.value)} />
      {kind ? (
        <select
          className="select"
          aria-label={tr('Unit')}
          disabled={off}
          value={factor}
          onChange={(e) => {
            const f = Number(e.target.value)
            setFactor(f)
            const v = amountValue(text, f)
            if (v !== null) onChange(v)
          }}
        >
          {unitsOf(kind).map(([l, f]) => (
            <option key={f} value={f}>
              {l}
            </option>
          ))}
        </select>
      ) : (
        unit && <span className="note">{unit}</span>
      )}
      {none && (
        <label className="check">
          <input
            type="checkbox"
            checked={!limited}
            disabled={disabled}
            onChange={(e) => {
              setLimited(!e.target.checked)
              if (e.target.checked) onChange(none[0])
              else window.setTimeout(() => document.getElementById(id)?.focus())
            }}
          />
          {none[1]}
        </label>
      )}
    </div>
  )
}

/** Local rows of an editor whose value only holds complete ones (a variable
 * needs a name): reset when the value changes from elsewhere. */
function useDraft<T>(value: unknown, parse: (v: unknown) => T[], format: (rows: T[]) => unknown, onChange: (v: unknown) => void) {
  const [rows, setRows] = useState<T[]>(() => parse(value))
  useEffect(() => {
    if (!same(format(rows), value)) setRows(parse(value))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])
  const update = (next: T[]) => {
    setRows(next)
    const v = format(next)
    if (!same(v, value)) onChange(v)
  }
  return [rows, update] as const
}

/** A list of words or paths, as chips. */
function ChipsControl({
  id,
  value,
  onChange,
  disabled,
  lines,
  placeholder,
  as,
}: {
  id: string
  value: unknown
  onChange: (v: unknown) => void
  disabled: boolean
  /** One item per line: distinguished names and patterns have commas. */
  lines?: boolean
  placeholder?: string
  /** How an item typed is stored (a pattern between slashes). */
  as?: (s: string) => string
}) {
  const list = Array.isArray(value) ? value.map(String) : []
  const add = (t: string) => [...new Set(addItems(list, t, lines).map(as ?? ((x) => x)))]
  const [text, setText] = useState('')
  const [all, setAll] = useState(false)
  const shown = all ? list : list.slice(0, 12)
  const commit = () => {
    if (!text.trim()) return
    onChange(add(text))
    setText('')
  }
  return (
    <div className={`chips${lines ? ' lines' : ''}`}>
      {shown.map((x) => (
        <span key={x} className="chip">
          {x}
          {!disabled && (
            <button type="button" aria-label={tr('Remove {item}', { item: x })} onClick={() => onChange(list.filter((y) => y !== x))}>
              ×
            </button>
          )}
        </span>
      ))}
      {list.length > shown.length && (
        <button type="button" className="add" onClick={() => setAll(true)}>
          {tr('+ {n} more', { n: list.length - shown.length })}
        </button>
      )}
      {!list.length && disabled && <span className="note">{tr('None.')}</span>}
      {!disabled && (
        <input
          id={id}
          className="input chip-input"
          value={text}
          placeholder={placeholder ?? tr('Add…')}
          onChange={(e) => setText(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || (e.key === ',' && !lines)) {
              e.preventDefault()
              commit()
            }
          }}
          onPaste={(e) => {
            const t = e.clipboardData.getData('text')
            if (!(lines ? /\n/ : /[\n,]/).test(t)) return
            e.preventDefault()
            onChange(add(text + t))
            setText('')
          }}
        />
      )}
    </div>
  )
}

/** A list of known items, each one on or off, in the options' order. */
function ChecksControl({ value, options, onChange, disabled }: { value: unknown; options: [string, string][]; onChange: (v: unknown) => void; disabled: boolean }) {
  const list = Array.isArray(value) ? value.map(String) : []
  const others = list.filter((x) => !options.some(([v]) => v === x))
  const toggle = (v: string, on: boolean) => {
    const next = new Set(list)
    if (on) next.add(v)
    else next.delete(v)
    onChange([...options.map(([o]) => o).filter((o) => next.has(o)), ...others])
  }
  return (
    <div className="checks">
      {options.map(([v, l]) => (
        <label key={v} className="check">
          <input type="checkbox" checked={list.includes(v)} disabled={disabled} onChange={(e) => toggle(v, e.target.checked)} />
          <span>
            {l}
            <code>{v}</code>
          </span>
        </label>
      ))}
    </div>
  )
}

/** Names and their values: environment variables, parameters, options. */
function PairsControl({ id, value, onChange, disabled, columns, coerce, add }: {
  id: string
  value: unknown
  onChange: (v: unknown) => void
  disabled: boolean
  columns: [string, string]
  coerce?: boolean
  add?: string
}) {
  const [rows, update] = useDraft(value, pairsOf, (r) => pairsValue(r, coerce), onChange)
  const edit = (i: number, p: Partial<Pair>) => update(rows.map((r, j) => (j === i ? { ...r, ...p } : r)))
  return (
    <div className="s-list">
      {rows.length ? (
        <div className="kv">
          <span className="head">{columns[0]}</span>
          <span className="head">{columns[1]}</span>
          <span />
          {rows.map((r, i) => (
            <Fragment key={i}>
              <input id={i === 0 ? id : undefined} className="input" aria-label={columns[0]} disabled={disabled} value={r.key} onChange={(e) => edit(i, { key: e.target.value })} />
              <input className="input" aria-label={columns[1]} disabled={disabled} value={r.value} onChange={(e) => edit(i, { value: e.target.value })} />
              <button type="button" className="icon-x" aria-label={tr('Remove')} disabled={disabled} onClick={() => update(rows.filter((_, j) => j !== i))}>
                ×
              </button>
            </Fragment>
          ))}
        </div>
      ) : (
        <span className="note">{tr('None.')}</span>
      )}
      {!disabled && (
        <button type="button" className="btn sm ghost" onClick={() => update([...rows, { key: '', value: '' }])}>
          + {add ?? tr('Add')}
        </button>
      )}
    </div>
  )
}

/** An object edited as a small form; the keys it does not show are kept. */
function FieldsControl({ id, value, fields, onChange, disabled }: { id: string; value: unknown; fields: Field[]; onChange: (v: unknown) => void; disabled: boolean }) {
  return (
    <div className="s-fields">
      {fields.map((f, i) => {
        const v = fieldValue(value, f)
        const fid = i === 0 ? id : `${id}-${f.key}`
        const set = (x: string | boolean) => onChange(withField(value, f, x))
        if (f.kind === 'switch')
          return (
            <label key={f.key} className="switch">
              <input id={fid} type="checkbox" checked={v === true} disabled={disabled} onChange={(e) => set(e.target.checked)} />
              <span className="track" aria-hidden="true" />
              {f.label}
            </label>
          )
        const choices = f.choices ?? []
        return (
          <label key={f.key} className={`s-field${f.kind === 'lines' ? ' full' : ''}`}>
            <span>{f.label}</span>
            {f.kind === 'choice' ? (
              <select id={fid} className="select" disabled={disabled} value={String(v)} onChange={(e) => set(e.target.value)}>
                {!choices.some(([c]) => c === v) && <option value={String(v)}>{String(v)}</option>}
                {choices.map(([c, l]) => (
                  <option key={c} value={c}>
                    {l}
                  </option>
                ))}
              </select>
            ) : f.kind === 'lines' ? (
              <textarea id={fid} className="textarea mono" rows={4} spellCheck={false} disabled={disabled} value={String(v)} onChange={(e) => set(e.target.value)} />
            ) : (
              <input id={fid} className="input mono" autoComplete="off" disabled={disabled} placeholder={f.placeholder} value={String(v)} onChange={(e) => set(e.target.value)} />
            )}
          </label>
        )
      })}
    </div>
  )
}

/** Objects by name, each edited as a form: SAML's identity providers. */
function RecordsControl({ id, value, fields, name, add, onChange, disabled }: {
  id: string
  value: unknown
  fields: Field[]
  name: string
  add: string
  onChange: (v: unknown) => void
  disabled: boolean
}) {
  const [list, update] = useDraft(value, recordsOf, recordsValue, onChange)
  const edit = (i: number, r: Partial<NamedRecord>) => update(list.map((x, j) => (j === i ? { ...x, ...r } : x)))
  return (
    <div className="s-list">
      {list.map((r, i) => (
        <fieldset key={i} className="record">
          <div className="record-head">
            <label className="s-field">
              <span>{name}</span>
              <input id={i === 0 ? id : undefined} className="input mono" autoComplete="off" disabled={disabled} value={r.name} onChange={(e) => edit(i, { name: e.target.value })} />
            </label>
            <button type="button" className="icon-x" aria-label={tr('Remove')} disabled={disabled} onClick={() => update(list.filter((_, j) => j !== i))}>
              ×
            </button>
          </div>
          <FieldsControl id={`${id}-${i}`} value={r.value} fields={fields} onChange={(v) => edit(i, { value: v as Record<string, unknown> })} disabled={disabled} />
        </fieldset>
      ))}
      {!list.length && <span className="note">{tr('None.')}</span>}
      {!disabled && (
        <button type="button" className="btn sm ghost" onClick={() => update([...list, { name: '', value: {} }])}>
          + {add}
        </button>
      )}
    </div>
  )
}

/** Host directories mounted into job containers. */
function PathsControl({ id, value, onChange, disabled }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  const [rows, update] = useDraft(value, mountsOf, mountsValue, onChange)
  const edit = (i: number, m: Partial<Mount>) => update(rows.map((r, j) => (j === i ? { ...r, ...m } : r)))
  return (
    <div className="s-list">
      {rows.length ? (
        <div className="kv kv-paths">
          <span className="head">{tr('Host path')}</span>
          <span className="head">{tr('In the container')}</span>
          <span className="head">{tr('Options')}</span>
          <span />
          {rows.map((r, i) => (
            <Fragment key={i}>
              <input id={i === 0 ? id : undefined} className="input" aria-label={tr('Host path')} disabled={disabled} value={r.host} onChange={(e) => edit(i, { host: e.target.value })} />
              <input className="input" aria-label={tr('In the container')} placeholder={tr('Same path')} disabled={disabled} value={r.container} onChange={(e) => edit(i, { container: e.target.value })} />
              <input className="input" aria-label={tr('Options')} placeholder={asIs('ro')} disabled={disabled} value={r.options} onChange={(e) => edit(i, { options: e.target.value })} />
              <button type="button" className="icon-x" aria-label={tr('Remove')} disabled={disabled} onClick={() => update(rows.filter((_, j) => j !== i))}>
                ×
              </button>
            </Fragment>
          ))}
        </div>
      ) : (
        <span className="note">{tr('None.')}</span>
      )}
      {!disabled && (
        <button type="button" className="btn sm ghost" onClick={() => update([...rows, { host: '', container: '', options: '' }])}>
          + {tr('Add a path')}
        </button>
      )}
    </div>
  )
}

/** Removed or stays, when someone no longer matches what gave them a role. */
function RemoveChoice({ remove, onChange, disabled, label }: { remove: boolean; onChange: (v: boolean) => void; disabled: boolean; label?: string }) {
  const name = useId()
  const l = label ?? tr('When no longer matched')
  return (
    <div className="map-remove">
      <span>{l}</span>
      <div className="segmented" role="radiogroup" aria-label={l}>
        {([
          [true, tr('Removed')],
          [false, tr('Stays')],
        ] as [boolean, string][]).map(([v, t]) => (
          <label key={t} className={remove === v ? 'on' : ''}>
            <input type="radio" name={name} checked={remove === v} disabled={disabled} onChange={() => onChange(v)} />
            {t}
          </label>
        ))}
      </div>
    </div>
  )
}

/** An organization map and its team map as rows: who joins what, as what
 * (lib/awxMaps.ts). Saved in AWX's format, each map in its own setting. */
function MapsControl({ row, ctx }: { row: Row; ctx: Ctx }) {
  const [orgKey, teamKey] = row.keys
  const ldap = orgKey.startsWith('AUTH_LDAP_')
  const disabled = !ctx.editable || !row.keys.every((k) => ctx.data.writable.has(k))
  const orgV = ctx.values[orgKey]
  const teamV = ctx.values[teamKey]
  const [entries, setEntries] = useState<MapEntry[]>(() => parseMaps(orgV, teamV))
  const [open, setOpen] = useState<number | null>(null)
  // Values changed elsewhere (discarded, reset): shown again. Entries not
  // saved yet (no name) stay while the maps are the same.
  useEffect(() => {
    if (!same(formatMaps(entries, ldap), formatMaps(parseMaps(orgV, teamV), ldap))) {
      setEntries(parseMaps(orgV, teamV))
      setOpen(null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orgV, teamV])
  // An emptied map is stored as its default: null for the social maps, {} for LDAP's.
  const stored = (k: string, m: Record<string, unknown>) => (Object.keys(m).length ? m : (ctx.data.meta[k]?.default ?? {}))
  const update = (next: MapEntry[]) => {
    const [o, t] = formatMaps(next, ldap)
    const [po, pt] = formatMaps(entries, ldap)
    setEntries(next)
    if (!same(o, po)) ctx.set(orgKey, stored(orgKey, o))
    if (!same(t, pt)) ctx.set(teamKey, stored(teamKey, t))
  }
  const add = (e: MapEntry) => {
    update([...entries, e])
    setOpen(entries.length)
  }
  const problems = mapProblems(entries)
  const grants = grantRows(entries, ldap)
  return (
    <div className="map-wrap">
      {grants.length ? (
        <table className="maps">
          <thead>
            <tr>
              <th>{tr('Joins')}</th>
              <th>{tr('Who')}</th>
              <th>{tr('As')}</th>
              <th>{tr('When no longer matched')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {grants.map((g, i) => {
              const e = entries[g.entry]
              const first = i === 0 || grants[i - 1].entry !== g.entry
              const last = i === grants.length - 1 || grants[i + 1].entry !== g.entry
              const expr = g.role === null ? null : e.kind === 'team' ? e.users : e.exprs[g.role]
              const remove = g.role === null ? true : e.kind === 'team' ? e.remove : e.remove[g.role]
              const who = describeExpr(expr, ldap)
              const name = e.kind === 'organization' && e.alias ? e.alias : e.name
              return (
                <Fragment key={`${g.entry}-${g.role}`}>
                  <tr className={first ? '' : 'cont'}>
                    <td>
                      {first && (
                        <>
                          <b>{name || '—'}</b>
                          <span className="sub">
                            {e.kind === 'team'
                              ? tr('team in {org}', { org: e.organization || '?' })
                              : e.alias
                                ? tr('organization, entry {name}', { name: e.name })
                                : tr('organization')}
                          </span>
                          {problems[g.entry] && <span className="sub err">{problemText(problems[g.entry])}</span>}
                        </>
                      )}
                    </td>
                    <td className="who">
                      {who.text ?? (
                        <>
                          {who.lead && <>{who.lead} </>}
                          {who.items.map((x) => (
                            <Fragment key={x}>
                              <code>{x}</code>{' '}
                            </Fragment>
                          ))}
                        </>
                      )}
                    </td>
                    <td>{g.role === null ? '—' : roleLabel[g.role]}</td>
                    <td>{expr === null || expr === true ? '—' : remove ? tr('Removed') : tr('Stays')}</td>
                    <td>
                      {first && !disabled && (
                        <button
                          type="button"
                          className="icon-x"
                          aria-label={tr('Edit {name}', { name: name || (e.kind === 'team' ? tr('the new team') : tr('the new organization')) })}
                          aria-expanded={open === g.entry}
                          onClick={() => setOpen(open === g.entry ? null : g.entry)}
                        >
                          …
                        </button>
                      )}
                    </td>
                  </tr>
                  {last && open === g.entry && (
                    <tr className="map-edit">
                      <td colSpan={5}>
                        <MapEntryEditor
                          entry={e}
                          ldap={ldap}
                          onChange={(x) => update(entries.map((y, j) => (j === g.entry ? x : y)))}
                          onRemove={() => {
                            update(entries.filter((_, j) => j !== g.entry))
                            setOpen(null)
                          }}
                          onDone={() => setOpen(null)}
                        />
                      </td>
                    </tr>
                  )}
                </Fragment>
              )
            })}
          </tbody>
        </table>
      ) : (
        <p className="note">{ldap ? tr('None yet: LDAP users join no organization, and see nothing until someone grants them access.') : tr('None yet: people join no organization, and see nothing until someone grants them access.')}</p>
      )}
      {!disabled && (
        <div className="map-actions">
          <button type="button" className="btn sm" onClick={() => add(newOrg())}>
            {tr('Add an organization')}
          </button>
          <button type="button" className="btn sm" onClick={() => add(newTeam())}>
            {tr('Add a team')}
          </button>
        </div>
      )}
      {ctx.editable && <MapTester orgKey={orgKey} ctx={ctx} />}
    </div>
  )
}

/** Names known, offered while typing an organization or a team. */
function useNames(path: string) {
  return useQuery({ queryKey: ['settings/names', path], queryFn: () => getAll<{ name: string }>(path, {}, 2).then((l) => [...new Set(l.map((x) => x.name))]), staleTime: 60_000 }).data ?? []
}

/** One organization or team of a map: its name, then each role's people. */
function MapEntryEditor({ entry, ldap, onChange, onRemove, onDone }: {
  entry: MapEntry
  ldap: boolean
  onChange: (e: MapEntry) => void
  onRemove: () => void
  onDone: () => void
}) {
  const id = useId()
  const orgs = useNames('organizations/')
  const teams = useNames('teams/')
  const team = entry.kind === 'team'
  return (
    <div className="map-entry">
      <div className="s-fields">
        <label className="s-field">
          <span>{team ? tr('Team') : tr('Organization')}</span>
          <input
            className="input"
            autoFocus={!entry.name}
            autoComplete="off"
            list={`${id}-${team ? 'teams' : 'orgs'}`}
            value={entry.name}
            onChange={(e) => onChange({ ...entry, name: e.target.value })}
          />
        </label>
        {entry.kind === 'team' && (
          <label className="s-field">
            <span>{tr('In the organization')}</span>
            <input className="input" autoComplete="off" list={`${id}-orgs`} value={entry.organization} onChange={(e) => onChange({ ...entry, organization: e.target.value })} />
          </label>
        )}
        {entry.kind === 'organization' && entry.alias && (
          <label className="s-field">
            <span>{tr('Joins the organization')}</span>
            <input className="input" autoComplete="off" list={`${id}-orgs`} value={entry.alias} onChange={(e) => onChange({ ...entry, alias: e.target.value })} />
          </label>
        )}
        <datalist id={`${id}-orgs`}>
          {orgs.map((n) => (
            <option key={n} value={n} />
          ))}
        </datalist>
        <datalist id={`${id}-teams`}>
          {teams.map((n) => (
            <option key={n} value={n} />
          ))}
        </datalist>
      </div>
      <p className="note">{tr('An organization or team that does not exist yet is created at the first login it applies to.')}</p>
      {entry.kind === 'team' ? (
        <WhoEditor label={tr('Members')} expr={entry.users} remove={entry.remove} ldap={ldap} onChange={(users, remove) => onChange({ ...entry, users, remove })} />
      ) : (
        orgRoles(ldap).map((r) => (
          <WhoEditor
            key={r}
            label={{ users: tr('Members'), admins: tr('Admins'), auditors: tr('Auditors') }[r]}
            expr={entry.exprs[r]}
            remove={entry.remove[r]}
            ldap={ldap}
            onChange={(x, remove) => onChange({ ...entry, exprs: { ...entry.exprs, [r]: x }, remove: { ...entry.remove, [r]: remove } })}
          />
        ))
      )}
      <div className="map-actions">
        <button type="button" className="btn sm" onClick={onDone}>
          {tr('Done')}
        </button>
        <button type="button" className="btn sm ghost" onClick={onRemove}>
          {team ? tr('Remove this team') : tr('Remove this organization')}
        </button>
      </div>
    </div>
  )
}

/** Who gets a role: everyone, these people or groups, no one, or left as it is. */
function WhoEditor({ label, expr, remove, ldap, onChange }: { label: string; expr: Expr; remove: boolean; ldap: boolean; onChange: (e: Expr, remove: boolean) => void }) {
  const id = useId()
  const kind = whoKind(expr)
  const list = Array.isArray(expr) ? expr : []
  const people = list.filter((x) => !isPattern(x))
  const patterns = list.filter(isPattern)
  return (
    <fieldset className="map-role">
      <legend>{label}</legend>
      <div className="choice row" role="radiogroup" aria-label={label}>
        {whoKinds(ldap).map(([k, l]) => (
          <label key={k}>
            <input type="radio" name={id} checked={kind === k} onChange={() => onChange(withKind(expr, k), remove)} />
            <span>{l}</span>
          </label>
        ))}
      </div>
      {kind === 'list' &&
        (ldap ? (
          <div className="amount-l">
            <span>{tr('Groups, by distinguished name')}</span>
            <ChipsControl id={`${id}-dn`} lines value={list} onChange={(v) => onChange(v as string[], remove)} disabled={false} placeholder={asIs('cn=ops,ou=groups,dc=example,dc=com')} />
          </div>
        ) : (
          <>
            <div className="amount-l">
              <span>{tr('Usernames or emails')}</span>
              <ChipsControl id={`${id}-users`} value={people} onChange={(v) => onChange([...(v as string[]), ...patterns], remove)} disabled={false} />
            </div>
            <div className="amount-l">
              <span>{tr('Usernames or emails matching')}</span>
              <ChipsControl id={`${id}-re`} lines as={asPattern} value={patterns} onChange={(v) => onChange([...people, ...(v as string[])], remove)} disabled={false} placeholder={asIs('/^ops-.*@example\\.com$/i')} />
            </div>
          </>
        ))}
      {kind !== 'keep' && kind !== 'all' && <RemoveChoice remove={remove} onChange={(v) => onChange(expr, v)} disabled={false} />}
    </fieldset>
  )
}

/** LDAP's superusers and auditors: members of these groups. */
function LdapFlagsControl({ id, value, onChange, disabled }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  const f = parseFlags(value)
  return (
    <div className="s-parts">
      {([
        ['superusers', tr('Superusers: members of')],
        ['auditors', tr('System auditors: members of')],
      ] as const).map(([k, l]) => (
        <div key={k} className="amount-l">
          <label htmlFor={`${id}-${k}`}>{l}</label>
          <ChipsControl id={`${id}-${k}`} lines value={f[k]} onChange={(v) => onChange(formatFlags({ ...f, [k]: v as string[] }))} disabled={disabled} placeholder={asIs('cn=admins,ou=groups,dc=example,dc=com')} />
        </div>
      ))}
    </div>
  )
}

/** SAML's organizations from attributes: the attribute naming each role's organizations. */
function SamlOrgAttrControl({ id, value, onChange, disabled }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  const [[a], update] = useDraft(value, (v) => [parseSamlOrgAttr(v)], (r) => formatSamlOrgAttr(r[0]), onChange)
  const set = (next: Partial<SamlOrgAttr>) => update([{ ...a, ...next }])
  return (
    <div className="s-parts">
      {([
        ['users', tr('Member of the organizations in')],
        ['admins', tr('Admin of the organizations in')],
        ['auditors', tr('Auditor of the organizations in')],
      ] as [Role, string][]).map(([r, l]) => (
        <div key={r} className="amount-l">
          <label htmlFor={`${id}-${r}`}>{l}</label>
          <input
            id={`${id}-${r}`}
            className="input mono"
            autoComplete="off"
            placeholder={tr('No attribute')}
            disabled={disabled}
            value={a.attrs[r]}
            onChange={(e) => set({ attrs: { ...a.attrs, [r]: e.target.value } })}
          />
          {a.attrs[r].trim() && <RemoveChoice label={tr('When no longer named')} remove={a.remove[r]} disabled={disabled} onChange={(v) => set({ remove: { ...a.remove, [r]: v } })} />}
        </div>
      ))}
    </div>
  )
}

/** SAML's teams from an attribute: which of its values are which team. */
function SamlTeamAttrControl({ id, value, onChange, disabled }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  const [[a], update] = useDraft(value, (v) => [parseSamlTeamAttr(v)], (r) => formatSamlTeamAttr(r[0]), onChange)
  const set = (next: Partial<SamlTeamAttr>) => update([{ ...a, ...next }])
  const edit = (i: number, t: Partial<SamlTeam>) => set({ teams: a.teams.map((x, j) => (j === i ? { ...x, ...t } : x)) })
  return (
    <div className="s-list">
      <div className="amount-l">
        <label htmlFor={id}>{tr('Attribute')}</label>
        <input id={id} className="input mono" autoComplete="off" placeholder={asIs('memberOf')} disabled={disabled} value={a.attr} onChange={(e) => set({ attr: e.target.value })} />
      </div>
      {a.teams.length ? (
        <div className="kv kv-teams">
          <span className="head">{tr('Value')}</span>
          <span className="head">{tr('Team')}</span>
          <span className="head">{tr('Organization')}</span>
          <span />
          {a.teams.map((t, i) => (
            <Fragment key={i}>
              <input className="input" aria-label={tr('Value')} disabled={disabled} value={t.team} onChange={(e) => edit(i, { team: e.target.value })} />
              <input className="input" aria-label={tr('Team')} placeholder={t.team || tr('Same as the value')} disabled={disabled} value={t.alias} onChange={(e) => edit(i, { alias: e.target.value })} />
              <input className="input" aria-label={tr('Organization')} disabled={disabled} value={t.organization} onChange={(e) => edit(i, { organization: e.target.value })} />
              <button type="button" className="icon-x" aria-label={tr('Remove')} disabled={disabled} onClick={() => set({ teams: a.teams.filter((_, j) => j !== i) })}>
                ×
              </button>
            </Fragment>
          ))}
        </div>
      ) : (
        <span className="note">{tr('No team yet.')}</span>
      )}
      {!disabled && (
        <button type="button" className="btn sm ghost" onClick={() => set({ teams: [...a.teams, { team: '', alias: '', organization: '' }] })}>
          + {tr('Add a team')}
        </button>
      )}
      <RemoveChoice label={tr('When no longer in a team listed')} remove={a.remove} disabled={disabled} onChange={(remove) => set({ remove })} />
    </div>
  )
}

/** SAML's superusers and auditors: by role, or by an attribute's value. */
function SamlFlagsControl({ id, value, onChange, disabled }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  const [[f], update] = useDraft(value, (v) => [parseSamlFlags(v)], (r) => formatSamlFlags(r[0]), onChange)
  const set = (k: keyof SamlFlags, x: Partial<SamlFlag>) => update([{ ...f, [k]: { ...f[k], ...x } }])
  return (
    <div className="s-list">
      {([
        ['superuser', tr('Superusers')],
        ['auditor', tr('System auditors')],
      ] as [keyof SamlFlags, string][]).map(([k, l]) => (
        <fieldset key={k} className="record">
          <legend>{l}</legend>
          <div className="amount-l">
            <span>{tr('With one of these roles')}</span>
            <ChipsControl id={`${id}-${k}-role`} value={f[k].roles} onChange={(v) => set(k, { roles: v as string[] })} disabled={disabled} />
          </div>
          <div className="s-fields">
            <label className="s-field">
              <span>{tr('Or with the attribute')}</span>
              <input className="input mono" autoComplete="off" disabled={disabled} value={f[k].attr} onChange={(e) => set(k, { attr: e.target.value })} />
            </label>
            {f[k].attr.trim() && (
              <div className="s-field full">
                <span>{tr('Set to one of')}</span>
                <ChipsControl id={`${id}-${k}-value`} lines value={f[k].values} onChange={(v) => set(k, { values: v as string[] })} disabled={disabled} placeholder={tr('Any value')} />
              </div>
            )}
          </div>
          <RemoveChoice label={tr('Otherwise')} remove={f[k].remove} disabled={disabled} onChange={(remove) => set(k, { remove })} />
        </fieldset>
      ))}
    </div>
  )
}

const scopeLabels: Record<string, string> = { SCOPE_SUBTREE: tr('Subtree'), SCOPE_ONELEVEL: tr('One level'), SCOPE_BASE: tr('Base only') }

/** LDAP searches: a base DN, a scope and a filter; with `multiple`, several. */
function SearchControl({ id, value, onChange, disabled, multiple }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean; multiple?: boolean }) {
  const [rows, update] = useDraft(value, searchesOf, searchesValue, onChange)
  const blank: Search = { base: '', scope: 'SCOPE_SUBTREE', filter: '' }
  const shown = rows.length ? rows : [blank]
  const edit = (i: number, s: Partial<Search>) => update(shown.map((r, j) => (j === i ? { ...r, ...s } : r)))
  return (
    <div className="s-list">
      <div className="kv kv-search">
        <span className="head">{tr('Base DN')}</span>
        <span className="head">{tr('Scope')}</span>
        <span className="head">{tr('Filter')}</span>
        <span />
        {shown.map((r, i) => (
          <Fragment key={i}>
            <input id={i === 0 ? id : undefined} className="input" aria-label={tr('Base DN')} placeholder={asIs('OU=Users,DC=example,DC=com')} disabled={disabled} value={r.base} onChange={(e) => edit(i, { base: e.target.value })} />
            <select className="select" aria-label={tr('Scope')} disabled={disabled} value={r.scope} onChange={(e) => edit(i, { scope: e.target.value })}>
              {[...new Set([...searchScopes, r.scope])].map((s) => (
                <option key={s} value={s}>
                  {scopeLabels[s] ?? s}
                </option>
              ))}
            </select>
            <input className="input" aria-label={tr('Filter')} placeholder={asIs(multiple ? '(uid=%(user)s)' : '(objectClass=group)')} disabled={disabled} value={r.filter} onChange={(e) => edit(i, { filter: e.target.value })} />
            {multiple && shown.length > 1 ? (
              <button type="button" className="icon-x" aria-label={tr('Remove')} disabled={disabled} onClick={() => update(shown.filter((_, j) => j !== i))}>
                ×
              </button>
            ) : (
              <span />
            )}
          </Fragment>
        ))}
      </div>
      {multiple && !disabled && (
        <button type="button" className="btn sm ghost" onClick={() => update([...shown, blank])}>
          + {tr('Add a search')}
        </button>
      )}
    </div>
  )
}

/** An image, stored as a data URL. */
function ImageControl({ id, value, onChange, disabled }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  const src = typeof value === 'string' ? value : ''
  return (
    <div className="s-image">
      {src ? <img src={src} alt={tr('The logo')} /> : <span className="note">{tr('None.')}</span>}
      {!disabled && (
        <label className="btn sm">
          {src ? tr('Replace…') : tr('Choose an image…')}
          <input
            id={id}
            type="file"
            accept="image/gif,image/png,image/jpeg"
            hidden
            onChange={(e) => {
              const f = e.target.files?.[0]
              e.target.value = ''
              if (!f) return
              const r = new FileReader()
              r.onload = () => onChange(String(r.result))
              r.readAsDataURL(f)
            }}
          />
        </label>
      )}
      {!disabled && src && (
        <button type="button" className="link-btn" onClick={() => onChange('')}>
          {tr('Remove')}
        </button>
      )}
    </div>
  )
}

/** Text on several lines: a certificate, the login page's text. */
function LinesControl({ id, value, onChange, disabled, mono }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean; mono: boolean }) {
  const text = String(value ?? '')
  return (
    <textarea
      id={id}
      className={`textarea${mono ? ' mono' : ''}`}
      rows={Math.min(10, Math.max(3, text.split('\n').length + 1))}
      spellCheck={!mono}
      disabled={disabled}
      value={text}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}

function JsonControl({ id, value, onChange, disabled, label }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled: boolean; label?: string }) {
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
    <>
      <textarea
        id={id}
        className="textarea mono"
        rows={Math.min(12, text.split('\n').length + 1)}
        disabled={disabled}
        spellCheck={false}
        value={text}
        aria-label={label}
        aria-invalid={!!bad}
        onChange={(e) => {
          setText(e.target.value)
          try {
            onChange(JSON.parse(e.target.value))
            setBad(null)
          } catch (err) {
            setBad(tr('Not valid JSON: ') + (err as Error).message)
          }
        }}
      />
      {bad && <span className="err">{bad}</span>}
    </>
  )
}

/** A setting that holds an API object's ID: an execution environment, a credential. */
function ObjectControl({ id, resource, params, value, onChange, disabled, meta }: {
  id: string
  resource: string
  params?: Record<string, string | number>
  value: number | null
  onChange: (v: unknown) => void
  disabled: boolean
  meta?: (r: Ref) => ReactNode
}) {
  const obj = useObject<Ref>(value ? `${resource}${value}/` : null)
  if (disabled) return <span>{obj.data?.name ?? (value ? tr('#{value}', { value }) : tr('Not set'))}</span>
  return (
    <div style={{ width: '100%', maxWidth: 420 }}>
      <Picker id={id} resource={resource} params={params} placeholder={tr('Search…')} display={(r) => r.name} meta={meta} values={obj.data ? [obj.data] : []} single onChange={(v) => onChange(v[0]?.id ?? null)} />
      {value ? (
        <button type="button" className="link-btn small" onClick={() => onChange(null)}>
          {tr('Clear')}
        </button>
      ) : null}
    </div>
  )
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
      <button type="button" className="btn" disabled={busy || unsaved} title={unsaved ? tr('Save first: the test uses the saved settings.') : undefined} onClick={run}>
        {busy ? tr('Testing…') : tr('Test')}
      </button>
      <span className={`small ${outcome?.ok === false ? '' : 'faint'}`} style={outcome?.ok === false ? { color: 'var(--fail-deep)' } : undefined} role="status">
        {outcome ? outcome.text : target ? <>{tr('Sends to')} <span className="mono">{target}</span></> : values.LOG_AGGREGATOR_ENABLED ? '' : tr('External logging is off.')}
      </span>
    </>
  )
}

/** Every setting changed from its default, with a link to where it is set. */
function ChangedSettings({ all, changed }: { all: AllSettings; changed: Values }) {
  const entries = Object.entries(changed)
  if (!entries.length) return <Empty title={tr('Everything is at its default')} />
  return (
    <Panel pad={false}>
      <table className="data">
        <thead>
          <tr>
            <th scope="col">{tr('Setting')}</th>
            <th scope="col">{tr('Value')}</th>
            <th scope="col">{tr('Default')}</th>
            <th scope="col">{tr('Where')}</th>
          </tr>
        </thead>
        <tbody>
          {entries.map(([k, v]) => {
            const place = settingPlace(k, all.categories[k] ?? '', hasNoEffect(all.meta[k]?.help_text))
            const page = place && settingsPage(place.page)
            const section = page && place && (page.sections.find((s) => s.id === place.section)?.title ?? (place.section === 'no-effect' ? tr('No effect in awxnext') : tr('Other settings')))
            return (
              <tr key={k}>
                <td>
                  <div style={{ fontWeight: 500 }}>{settingLabel(k) ?? metaLabel(k, all.meta[k])}</div>
                  <div className="cell-sub mono">{k}</div>
                </td>
                <td className="s-word">{valueText(v, all.meta[k], layoutRow(k), k)}</td>
                <td className="s-word muted">{all.meta[k] ? valueText(defaultOf(all.meta[k]), all.meta[k], layoutRow(k), k) : ''}</td>
                <td>
                  {page && place && (
                    <Link to={`/settings/${page.slug}#${place.anchor}`}>
                      {page.title} › {section}
                    </Link>
                  )}
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
        {tr('At every sign-in, the groups in the token\'s')} <span className="mono">{groupsClaim}</span> {tr('claim are matched against these rules. Access granted by rules is replaced on each sign-in: if a group disappears, so does the access it gave. Access granted by hand in the UI is kept.')}
      </p>
      <div className="row">
        <span className="grow" />
        <label className="check small">
          <input type="checkbox" checked={asJson} onChange={(e) => setAsJson(e.target.checked)} />
          {tr('Edit as JSON')}
        </label>
      </div>
      {asJson ? (
        <>
          <JsonControl id="oidc-map-json" label={tr('Rules')} value={rules} onChange={(v) => onChange(Array.isArray(v) ? (v as Rule[]) : [])} disabled={!editable} />
          <span className="note">{tr('A list of rule objects.')}</span>
        </>
      ) : (
        <div className="table-wrap">
          <table className="data mapping">
            <thead>
              <tr>
                <th scope="col">{tr('When the user is in group')}</th>
                <th scope="col">{tr('Grant')}</th>
                <th scope="col">{tr('Where')}</th>
                <th scope="col" className="end">
                  <span className="sr-only">{tr('Actions')}</span>
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
                        <select className="select" style={{ width: 128, flex: '0 0 128px' }} aria-label={tr('Match')} disabled={!editable} value={regex ? 'regex' : 'exact'} onChange={(e) => update(i, e.target.value === 'regex' ? { group_regex: r.group ?? '', group: undefined } : { group: r.group_regex ?? '', group_regex: undefined })}>
                          <option value="exact">{tr('named')}</option>
                          <option value="regex">{tr('matching')}</option>
                        </select>
                        <input
                          className="input mono"
                          aria-label={tr('Group')}
                          disabled={!editable}
                          placeholder={regex ? tr('^team-(.+)$') : tr('awx-admins')}
                          value={(regex ? r.group_regex : r.group) ?? ''}
                          onChange={(e) => update(i, regex ? { group_regex: e.target.value } : { group: e.target.value })}
                        />
                      </div>
                    </td>
                    <td>
                      <select className="select" style={{ minWidth: 180 }} aria-label={tr('Grant')} disabled={!editable} value={g} onChange={(e) => setGrant(i, e.target.value as Grant)}>
                        <option value="superuser">{tr('Superuser')}</option>
                        <option value="auditor">{tr('System auditor')}</option>
                        <option value="org">{tr('Organization role')}</option>
                        <option value="team">{tr('Team membership')}</option>
                        <option value="roledef">{tr('Role definition')}</option>
                      </select>
                    </td>
                    <td>
                      {g === 'superuser' || g === 'auditor' ? (
                        <span className="faint small">{tr('Everywhere')}</span>
                      ) : (
                        <div className="row wrap" style={{ gap: 6 }}>
                          <input className="input" style={{ flex: '1 1 140px' }} aria-label={tr('Organization')} placeholder={tr('Organization')} disabled={!editable} value={r.organization ?? ''} onChange={(e) => update(i, { organization: e.target.value })} />
                          {g === 'team' && <input className="input" style={{ flex: '1 1 120px' }} aria-label={tr('Team')} placeholder={tr('Team (or $1)')} disabled={!editable} value={r.team ?? ''} onChange={(e) => update(i, { team: e.target.value })} />}
                          {g === 'roledef' ? (
                            <input className="input" style={{ flex: '1 1 160px' }} aria-label={tr('Role definition')} placeholder={tr('Organization Execute')} disabled={!editable} value={r.role_definition ?? ''} onChange={(e) => update(i, { role_definition: e.target.value })} />
                          ) : (
                            <select className="select" style={{ width: 120 }} aria-label={tr('Role')} disabled={!editable} value={r.role ?? 'member'} onChange={(e) => update(i, { role: e.target.value })}>
                              <option value="member">{tr('member')}</option>
                              <option value="admin">{tr('admin')}</option>
                              {g === 'org' && <option value="auditor">{tr('auditor')}</option>}
                            </select>
                          )}
                          <label className="check small" title={tr('Create the organization or team when it does not exist')}>
                            <input type="checkbox" disabled={!editable} checked={r.create !== false} onChange={(e) => update(i, { create: e.target.checked ? undefined : false })} />
                            {tr('create')}
                          </label>
                        </div>
                      )}
                    </td>
                    <td className="end nowrap">
                      {editable && (
                        <>
                          <button type="button" className="btn ghost sm icon" aria-label={tr('Move up')} disabled={i === 0} onClick={() => move(i, -1)}>
                            ↑
                          </button>
                          <button type="button" className="btn ghost sm icon" aria-label={tr('Move down')} disabled={i === rules.length - 1} onClick={() => move(i, 1)}>
                            ↓
                          </button>
                          <button type="button" className="btn ghost sm icon" aria-label={tr('Remove rule')} onClick={() => onChange(rules.filter((_, j) => j !== i))}>
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
          {rules.length === 0 && <Empty title={tr('No rules')}>{tr('Without rules, SSO users can sign in but get no access until granted in the UI.')}</Empty>}
          {editable && (
            <div className="table-foot">
              <button type="button" className="btn sm" onClick={() => onChange([...rules, { group: '', organization: '', role: 'member' }])}>
                <Icon name="plus" size={12} />
                {tr('Add rule')}
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

/** What a sign-in would give a user, as the dry runs answer. */
interface MapResult {
  superuser: boolean | null
  auditor: boolean | null
  grants: MappedGrant[] | null
  kept?: MappedGrant[] | null
  removed?: MappedGrant[] | null
  would_create: string[] | null
  errors?: string[] | null
}

interface DryRun extends MapResult {
  username: string
  email?: string
  groups: string[]
  groups_claim: string
}

interface MapTest extends MapResult {
  username: string
  email: string
}

const roleList = (l: MappedGrant[]) => l.map((g) => `${g.role_definition} on ${grantTarget(g)}`).join(', ')

/** A dry run's answer: flags, the roles granted, kept and removed, and what would be created. */
function MappedResult({ result, none, flagsLost = false }: { result: MapResult; none: string; flagsLost?: boolean }) {
  const lost = flagsLost ? [result.superuser === false && tr('Not a superuser: taken away if they were one.'), result.auditor === false && tr('Not a system auditor: taken away if they were one.')].filter(Boolean) : []
  return (
    <>
      {(result.superuser || result.auditor) && <div className="notice accent">{result.superuser ? tr('Becomes a superuser.') : tr('Becomes a system auditor.')}</div>}
      {lost.length ? <div className="small muted">{lost.join(' ')}</div> : null}
      {result.grants?.length ? (
        <table className="data">
          <thead>
            <tr>
              <th scope="col">{tr('Role')}</th>
              <th scope="col">{tr('On')}</th>
              <th scope="col">{tr('Because of')}</th>
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
        !result.superuser && !result.auditor && <div className="notice">{none}</div>
      )}
      {result.kept?.length ? (
        <div className="small muted">
          {tr('Left as they are (kept if the user has them from an earlier login):')} {roleList(result.kept)}
        </div>
      ) : null}
      {result.removed?.length ? (
        <div className="small muted">
          {tr('Taken away if the user has them:')} {roleList(result.removed)}
        </div>
      ) : null}
      {result.would_create?.length ? <div className="small muted">{tr('Would create:')} {result.would_create.join(', ')}</div> : null}
      {result.errors?.length ? <div className="form-error">{result.errors.join('; ')}</div> : null}
    </>
  )
}

/** What a connection test found of an LDAP user: their entry, groups, and what the maps give them. */
interface LDAPTestUser extends Partial<MapResult> {
  dn: string
  first_name?: string
  last_name?: string
  email?: string
  groups?: string[]
}

interface ConnTest {
  login: string
  ok: boolean
  steps: TestStep[]
  user?: LDAPTestUser
}

/** Tries a login method's settings as shown, unsaved changes included, against its server or provider. */
function ConnectionTest({ login, ctx }: { login: string; ctx: Ctx }) {
  const [user, setUser] = useState('')
  const [password, setPassword] = useState('')
  const [urls, setUrls] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<ConnTest | null>(null)
  const [error, setError] = useState<unknown>(null)
  const ldap = login.startsWith('ldap')
  const idps = login === 'saml' ? samlIdps(ctx.values.SOCIAL_AUTH_SAML_ENABLED_IDPS) : []
  const run = async () => {
    setBusy(true)
    setError(null)
    const body: Record<string, unknown> = { login, settings: unsavedFor(login, ctx.data.categories, ctx.values, ctx.saved) }
    if (ldap && user.trim()) Object.assign(body, { username: user.trim(), password })
    if (login === 'saml') body.metadata_urls = Object.fromEntries(Object.entries(urls).filter(([, u]) => u.trim()))
    try {
      setResult(await api.post<ConnTest>('settings/connections/test/', body))
    } catch (e) {
      setError(e)
      setResult(null)
    } finally {
      setBusy(false)
    }
  }
  const u = result?.user
  return (
    <div className="srow conn-test">
      <div>
        <div className="s-name">{tr('Test the connection')}</div>
        <p className="s-help">
          {ldap
            ? tr('Connects and binds with these settings as shown, unsaved changes included. Give a username to also find the user, with their groups and what they would get, and their password to check it.')
            : login === 'oidc'
              ? tr("Reads the provider's discovery document and signing keys, with these settings as shown, unsaved changes included.")
              : tr("Checks the service provider's certificate and key, then each identity provider's certificate, metadata and sign-in URL, with these settings as shown, unsaved changes included.")}
        </p>
      </div>
      <div className="s-control">
        <div className="stack" style={{ gap: 8 }}>
          {ldap && (
            <div className="row wrap">
              <input className="input mono" style={{ width: '12rem' }} aria-label={tr('Username')} placeholder={tr('username (optional)')} value={user} onChange={(e) => setUser(e.target.value)} />
              <input className="input mono" style={{ width: '12rem' }} type="password" autoComplete="new-password" aria-label={tr('Password')} placeholder={tr('password (optional)')} value={password} onChange={(e) => setPassword(e.target.value)} disabled={!user.trim()} />
            </div>
          )}
          {idps.map((p) => (
            <label key={p.name} className="stack" style={{ gap: 2 }}>
              <span className="small muted">{tr('Metadata URL of {name}', { name: p.name })}</span>
              <input
                className="input mono"
                value={urls[p.name] ?? ''}
                onChange={(e) => setUrls({ ...urls, [p.name]: e.target.value })}
                placeholder={p.isUrl ? tr('{url} (its entity ID)', { url: p.entityId }) : tr('Not compared without one')}
              />
            </label>
          ))}
          <div className="row wrap">
            <button type="button" className="btn" disabled={busy} onClick={run}>
              {busy ? tr('Testing…') : tr('Test')}
            </button>
            {result && (
              <span className="small" role="status">
                {result.ok ? tr('Passed: each step works.') : tr('Failed: see the step marked.')}
              </span>
            )}
          </div>
          <FormError error={error} />
          {result && (
            <ol className="test-steps">
              {result.steps.map((s, i) => (
                <li key={i}>
                  <span className="cell" data-size="xs" data-state={stepState[s.status]} role="img" aria-label={statusText(s.status)} title={statusText(s.status)} />
                  <span className="t-step">
                    {stepLabel(s.step)}
                    {s.idp ? <span className="mono"> · {s.idp}</span> : null}
                  </span>
                  <span className="t-detail">{s.detail}</span>
                </li>
              ))}
            </ol>
          )}
          {u && (
            <div className="stack" style={{ gap: 8 }}>
              <div className="small">
                <span className="mono">{u.dn}</span>
                {[u.first_name, u.last_name].filter(Boolean).length ? <span> · {[u.first_name, u.last_name].filter(Boolean).join(' ')}</span> : null}
                {u.email ? <span className="mono faint"> · {u.email}</span> : null}
              </div>
              {u.groups && (
                <div className="small">
                  {tr('Groups:')} {u.groups.length ? u.groups.map((g) => <span key={g} className="chip mono" style={{ marginRight: 4 }}>{g}</span>) : <span className="faint">{tr('none')}</span>}
                </div>
              )}
              {u.grants && <MappedResult result={u as MapResult} none={tr('Nothing matches: the user could sign in, with no access from these maps.')} flagsLost />}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

/** Tries a login's maps on a sample user, unsaved edits included, with the server's own evaluation. */
function MapTester({ orgKey, ctx }: { orgKey: string; ctx: Ctx }) {
  const [user, setUser] = useState('')
  const [email, setEmail] = useState('')
  const [more, setMore] = useState('')
  const [result, setResult] = useState<MapTest | null>(null)
  const [error, setError] = useState<unknown>(null)
  const login = mapLogin(orgKey)
  if (!login) return null
  const attrs = login.kind === 'saml' ? parseAttributes(more) : null
  const run = async () => {
    if (!user.trim()) return
    setError(null)
    const body: Record<string, unknown> = {
      login: login.login,
      username: user,
      email,
      settings: Object.fromEntries(login.keys.filter((k) => k in ctx.values).map((k) => [k, ctx.values[k]])),
    }
    if (login.kind === 'ldap') body.groups = lines(more)
    if (attrs) body.attributes = attrs.attributes
    try {
      setResult(await api.post<MapTest>('settings/maps/test/', body))
    } catch (e) {
      setError(e)
      setResult(null)
    }
  }
  return (
    <details className="map-test">
      <summary>{tr('Try with a user')}</summary>
      <div className="stack" style={{ gap: 10 }}>
        <p className="small muted" style={{ margin: 0 }}>
          {login.kind === 'ldap'
            ? tr('Nothing is saved or changed: this is what signing in would give a user in these groups, from the maps as shown, unsaved edits included.')
            : login.kind === 'saml'
              ? tr('Nothing is saved or changed: this is what signing in would give a user with these attributes, from the maps as shown, unsaved edits included.')
              : tr('Nothing is saved or changed: this is what signing in would give this user, from the maps as shown, unsaved edits included.')}
        </p>
        <div className="row" style={{ flexWrap: 'wrap' }}>
          <input className="input mono" style={{ flex: '1 1 10rem', width: 'auto' }} aria-label={tr('Username')} placeholder={tr('Username')} value={user} onChange={(e) => setUser(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && run()} />
          {login.kind !== 'ldap' && (
            <input className="input mono" style={{ flex: '1 1 12rem', width: 'auto' }} aria-label={tr('Email')} placeholder={tr('email (optional)')} value={email} onChange={(e) => setEmail(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && run()} />
          )}
        </div>
        {login.kind === 'ldap' && (
          <textarea className="textarea mono" rows={3} spellCheck={false} aria-label={tr('Groups, one distinguished name per line')} placeholder={tr('Groups, one distinguished name per line: CN=Engineering,OU=Groups,DC=example,DC=com')} value={more} onChange={(e) => setMore(e.target.value)} />
        )}
        {login.kind === 'saml' && (
          <textarea className="textarea mono" rows={3} spellCheck={false} aria-label={tr('Attributes, one value per line')} placeholder={tr('Attributes, one value per line: memberOf = Engineering')} value={more} onChange={(e) => setMore(e.target.value)} />
        )}
        {attrs?.bad.length ? <div className="small err">{tr('Not read, write name = value: {lines}', { lines: attrs.bad.join(', ') })}</div> : null}
        <div>
          <button type="button" className="btn" onClick={run} disabled={!user.trim()}>
            {tr('Try')}
          </button>
        </div>
        <FormError error={error} />
        {result && (
          <div className="stack" style={{ gap: 8 }}>
            <MappedResult result={result} none={tr('Nothing matches: the user could sign in, with no access from these maps.')} flagsLost />
          </div>
        )}
      </div>
    </details>
  )
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
    <Panel title={tr('Try the rules')} actions={<label className="check small"><input type="checkbox" checked={advanced} onChange={(e) => setAdvanced(e.target.checked)} />{tr('Paste full token claims')}</label>}>
      <div className="stack" style={{ gap: 10 }}>
        <p className="small muted" style={{ margin: 0 }}>
          {tr('Nothing is saved or changed: this shows what a user with these groups would get from the rules above, including unsaved edits, and from the saved default organization and team maps of the Sign-in page, which match the username and email.')}
        </p>
        {advanced ? (
          <textarea className="textarea mono" rows={6} spellCheck={false} placeholder={tr('{\n  "preferred_username": "alice",\n  "{groupsClaim}": ["ops", "team-payments"]\n}', { groupsClaim })} value={claimsText} onChange={(e) => setClaimsText(e.target.value)} aria-label={tr('Token claims')} />
        ) : (
          <div className="row" style={{ flexWrap: 'wrap' }}>
            <input className="input mono" style={{ flex: '1 1 10rem', width: 'auto' }} aria-label={tr('Username')} placeholder={tr('test-user')} value={user} onChange={(e) => setUser(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && run()} />
            <input className="input mono" style={{ flex: '1 1 12rem', width: 'auto' }} aria-label={tr('Email')} placeholder={tr('email (optional)')} value={email} onChange={(e) => setEmail(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && run()} />
            <input className="input mono" style={{ flex: '2 1 16rem', width: 'auto' }} aria-label={tr('Groups')} placeholder={tr('ops, team-payments, awx-admins')} value={groups} onChange={(e) => setGroups(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && run()} />
            <button type="button" className="btn" onClick={run}>
              {tr('Evaluate')}
            </button>
          </div>
        )}
        {advanced && (
          <div>
            <button type="button" className="btn" onClick={run}>
              {tr('Evaluate')}
            </button>
          </div>
        )}
        <FormError error={error} />
        {result && (
          <div className="stack" style={{ gap: 8 }}>
            <div className="small">
              <span className="mono">{result.username}</span>
              {result.email ? <span className="mono faint"> ({result.email})</span> : null} {tr('with groups')} {result.groups.length ? result.groups.map((g) => <span key={g} className="chip" style={{ marginRight: 4 }}>{g}</span>) : <span className="faint">{tr('none')}</span>}
            </div>
            <MappedResult result={result} none={tr('No rule matches: the user could sign in but would have no access.')} />
          </div>
        )}
      </div>
    </Panel>
  )
}
