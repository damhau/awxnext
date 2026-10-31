// Translations (roadmap M7-16). Strings are keyed by their English text,
// gettext style: tr('Delete {name}?', { name }) looks the text up in the
// locale's catalog (src/locales/<locale>.json) and falls back on the
// English. Plurals go through trn(), dates and numbers through Intl with the
// chosen locale. (Not t(): components name their template variables t.)
// The locale is chosen once per page load (<html lang> and every Intl
// formatter follow it); changing it reloads.
import { load, save } from './storage'

export type Locale = 'en' | 'fr'

/** A translation: the text, or plural forms by Intl.PluralRules category. */
export type Entry = string | Partial<Record<Intl.LDMLPluralRule, string>>
export type Catalog = Record<string, Entry>

/** Placeholder values; null and undefined fill as empty. */
export type Vars = Record<string, string | number | null | undefined>

/** Text that is deliberately not translated (names, code, syntax). */
export const asIs = <T extends string>(text: T): T => text

/** Each language named in itself. */
export const LOCALES: { value: Locale; label: string }[] = [
  { value: 'en', label: asIs('English') },
  { value: 'fr', label: asIs('Français') },
]

const loaders: Record<Exclude<Locale, 'en'>, () => Promise<Catalog>> = {
  fr: () => import('../locales/fr.json').then((m) => m.default as Catalog),
}

let locale: Locale = 'en'
let catalog: Catalog = {}

/** The chosen locale: the user's choice in this browser, else the first of
 * the browser's languages we have, else English. */
export function resolveLocale(choice: string | null | undefined, languages: readonly string[]): Locale {
  const ok = (l: string): Locale | null => (LOCALES.some((x) => x.value === l) ? (l as Locale) : null)
  if (choice && choice !== 'system' && ok(choice)) return ok(choice)!
  for (const l of languages) {
    const base = ok(l.toLowerCase().split('-')[0])
    if (base) return base
  }
  return 'en'
}

/** Loads the locale's catalog; called before the app renders. */
export async function initI18n(): Promise<void> {
  const want = resolveLocale(load<string>('lang', 'system'), typeof navigator !== 'undefined' ? navigator.languages ?? [navigator.language] : [])
  try {
    catalog = want === 'en' ? {} : await loaders[want]()
    locale = want
  } catch {
    catalog = {}
    locale = 'en'
  }
  document.documentElement.lang = locale
}

export function currentLocale(): Locale {
  return locale
}

/** The language choice in this browser: 'system' or a locale. */
export function languageChoice(): string {
  return load<string>('lang', 'system')
}

export function setLanguage(choice: 'system' | Locale) {
  save('lang', choice)
  window.location.reload()
}

/** For tests: use this locale and catalog. */
export function setCatalogForTests(l: Locale, c: Catalog) {
  locale = l
  catalog = c
}

function fill(text: string, vars?: Vars): string {
  if (!vars) return text
  return text.replace(/\{(\w+)\}/g, (m, k) => {
    if (!(k in vars)) return m
    const v = vars[k]
    return v === null || v === undefined ? '' : typeof v === 'number' ? number(v) : v
  })
}

/** The text in the current locale, with {placeholders} filled. */
export function tr(text: string, vars?: Vars): string {
  const e = catalog[text]
  return fill(typeof e === 'string' && e ? e : text, vars)
}

/** A count and its noun: trn(n, '{n} host', '{n} hosts'). The catalog keys
 * plural forms by the English plural. */
export function trn(n: number, one: string, other: string, vars?: Vars): string {
  const e = catalog[other]
  const rule = new Intl.PluralRules(locale).select(n)
  let text = n === 1 ? one : other
  if (e && typeof e === 'object') text = e[rule] ?? e.other ?? text
  else if (typeof e === 'string' && e) text = e
  return fill(text, { n, ...vars })
}


export function number(n: number, opts?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(locale, opts).format(n)
}
