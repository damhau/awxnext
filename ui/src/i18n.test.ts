// Translations (roadmap M7-16), checked in CI: every display string goes
// through tr()/trn() (or asIs() when it must stay as it is), and every
// catalog has every string, with the same {placeholders}.
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { resolveLocale, setCatalogForTests, tr, trn, type Catalog } from './lib/i18n'

const root = new URL('.', import.meta.url).pathname
const sources: string[] = []
const walk = (d: string) => {
  for (const f of readdirSync(d)) {
    const p = join(d, f)
    if (statSync(p).isDirectory()) walk(p)
    else if (/\.tsx?$/.test(f) && !/\.test\./.test(f) && !p.endsWith('lib/i18n.ts')) sources.push(p)
  }
}
walk(root)

// Where display text appears: these JSX attributes, these object properties,
// these calls' first argument, [label, value] pairs given to items=, and the
// text of JSX elements other than code.
const DISPLAY_ATTRS = new Set(['label', 'help', 'title', 'aria-label', 'placeholder', 'subtitle', 'note', 'empty', 'legend', 'alt', 'confirm'])
const DISPLAY_PROPS = new Set(['header', 'label', 'what', 'confirm', 'body', 'help', 'title', 'hint', 'placeholder', 'subtitle', 'note', 'reason', 'msg', 'group', 'meta', 'empty'])
const DISPLAY_CALLS = new Set(['toast', 'Error', 'setError', 'setErr'])
const CODE_TAGS = new Set(['code', 'kbd', 'pre', 'samp'])
const WRAPPERS = new Set(['tr', 'trn', 'asIs'])
const letters = /[A-Za-z]{2,}/
// A capitalised word, a space and more: a sentence or label anywhere in code.
const sentence = /^[A-Z][a-z’']+[,:]?\s+[\w“‘(]/

const tagOf = (el: ts.Node | undefined) => (el && ts.isJsxElement(el) ? el.openingElement.tagName.getText() : '')

function inWrapper(n: ts.Node): boolean {
  for (let p = n.parent; p; p = p.parent) {
    if (ts.isCallExpression(p) && WRAPPERS.has(p.expression.getText())) return true
    if (ts.isJsxElement(p) || ts.isBlock(p) || ts.isSourceFile(p)) return false
  }
  return false
}

function displayAnchor(node: ts.Node): string | null {
  let n = node
  for (;;) {
    const p = n.parent
    if (!p) return null
    if (ts.isParenthesizedExpression(p) || ts.isAsExpression(p)) n = p
    else if (ts.isConditionalExpression(p) && p.condition !== n) n = p
    else if (ts.isBinaryExpression(p) && [ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken, ts.SyntaxKind.AmpersandAmpersandToken].includes(p.operatorToken.kind) && (p.right === n || p.operatorToken.kind !== ts.SyntaxKind.AmpersandAmpersandToken)) n = p
    else if (ts.isJsxExpression(p)) {
      const q = p.parent
      if (ts.isJsxAttribute(q)) return DISPLAY_ATTRS.has(q.name.getText()) ? 'attribute ' + q.name.getText() : null
      if (ts.isJsxElement(q) || ts.isJsxFragment(q)) return CODE_TAGS.has(tagOf(q)) ? null : 'JSX child'
      return null
    } else if (ts.isJsxAttribute(p)) return DISPLAY_ATTRS.has(p.name.getText()) ? 'attribute ' + p.name.getText() : null
    else if (ts.isPropertyAssignment(p) && p.initializer === n) return DISPLAY_PROPS.has(p.name.getText()) ? 'property ' + p.name.getText() : null
    else if ((ts.isCallExpression(p) || ts.isNewExpression(p)) && p.arguments?.[0] === n) return DISPLAY_CALLS.has(p.expression.getText().split('.').pop()!) ? 'call' : null
    else if (ts.isArrayLiteralExpression(p) && p.elements.length === 2 && ts.isArrayLiteralExpression(p.parent)) {
      let a: ts.Node | undefined = p.parent
      while (a && !ts.isJsxAttribute(a) && !ts.isSourceFile(a)) a = a.parent
      return a && ts.isJsxAttribute(a) && a.name.getText() === 'items' ? 'label pair' : null
    } else return null
  }
}

const label = /^[A-Z][a-z]/
/** The label of a [value, 'Label'] or [key, 'Label', path] tuple, or of a
 * ['Label', <value>] pair. */
function optionLabel(n: ts.StringLiteral): string | null {
  const p = n.parent
  if (!ts.isArrayLiteralExpression(p) || !label.test(n.text)) return null
  const inList = ts.isArrayLiteralExpression(p.parent) || ts.isConditionalExpression(p.parent)
  if (inList && (p.elements.length === 2 || p.elements.length === 3) && p.elements[1] === n) return 'option label'
  if (inList && p.elements.length === 2 && p.elements[0] === n && !ts.isStringLiteral(p.elements[1])) return 'label pair'
  return null
}
/** A value of a label map: an object whose values are mostly capitalised strings. */
function mapLabel(n: ts.StringLiteral): string | null {
  const p = n.parent
  if (!ts.isPropertyAssignment(p) || p.initializer !== n || !label.test(n.text) || !ts.isObjectLiteralExpression(p.parent)) return null
  const values = p.parent.properties.map((q) => (ts.isPropertyAssignment(q) ? q.initializer : null))
  const labels = values.filter((v) => v && (ts.isStringLiteral(v) ? label.test(v.text) : ts.isCallExpression(v) && v.expression.getText() === 'tr')).length
  return values.length >= 3 && labels * 2 >= values.length ? 'label map' : null
}

/** Display strings not translated, and the catalog keys used. */
function scan() {
  const untranslated: string[] = []
  const keys = new Map<string, string>()
  for (const f of sources) {
    const src = readFileSync(f, 'utf8')
    const sf = ts.createSourceFile(f, src, ts.ScriptTarget.Latest, true, f.endsWith('x') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
    const at = (n: ts.Node) => `${relative(root, f)}:${sf.getLineAndCharacterOfPosition(n.getStart()).line + 1}`
    const lit = (n: ts.Node) => (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) ? n.text : null)
    const visit = (n: ts.Node) => {
      if (ts.isJsxText(n) && letters.test(n.text) && !CODE_TAGS.has(tagOf(n.parent))) untranslated.push(`${at(n)} text "${n.text.trim().slice(0, 60)}"`)
      if ((ts.isStringLiteral(n) || ts.isTemplateExpression(n) || ts.isNoSubstitutionTemplateLiteral(n)) && !inWrapper(n)) {
        const text = ts.isTemplateExpression(n) ? n.getText() : (n as ts.StringLiteral).text
        if (letters.test(text) && !ts.isImportDeclaration(n.parent) && !ts.isExternalModuleReference(n.parent) && !ts.isLiteralTypeNode(n.parent)) {
          const a = displayAnchor(n) ?? (ts.isStringLiteral(n) ? (optionLabel(n) ?? mapLabel(n)) : null)
          if (a) untranslated.push(`${at(n)} ${a} ${JSON.stringify(text.slice(0, 60))}`)
          else if (ts.isStringLiteral(n) && sentence.test(text) && !ts.isPropertyAssignment(n.parent) === !ts.isPropertyAssignment(n.parent)) untranslated.push(`${at(n)} sentence ${JSON.stringify(text.slice(0, 60))}`)
        }
      }
      // A call marked "i18n-dynamic" on the line above builds its keys from its callers'.
      const dynamic = (c: ts.Node) => (src.split('\n')[sf.getLineAndCharacterOfPosition(c.getStart()).line - 1] ?? '').includes('i18n-dynamic')
      if (ts.isCallExpression(n) && !dynamic(n)) {
        const name = n.expression.getText()
        const [a0, a1, a2] = n.arguments
        if (name === 'tr' && a0) {
          const k = lit(a0)
          if (k === null) untranslated.push(`${at(n)} tr() of a computed string: ${a0.getText().slice(0, 60)}`)
          else keys.set(k, at(n))
        }
        if (name === 'trn' && a2) {
          const one = lit(a1)
          const other = lit(a2)
          if (one === null || other === null) untranslated.push(`${at(n)} trn() of computed strings`)
          else keys.set(other, at(n)), keys.set('#one ' + other, one)
        }
        if (name === 'plural' && a1) {
          const one = lit(a1)
          const many = a2 ? lit(a2) : one === null ? null : one + 's'
          if (one !== null && many !== null) keys.set(`{n} ${many}`, at(n)), keys.set(`#one {n} ${many}`, `{n} ${one}`)
        }
      }
      // Nouns handed to the bulk bar, pluralised there: ['project', 'projects'].
      if (ts.isArrayLiteralExpression(n) && n.elements.length === 2 && n.elements.every((e) => ts.isStringLiteral(e))) {
        const p = n.parent
        const isNoun = (ts.isJsxExpression(p) && ts.isJsxAttribute(p.parent) && p.parent.name.getText() === 'noun') || (ts.isCallExpression(p) && p.expression.getText().startsWith('useBulk'))
        if (isNoun) {
          const [one, many] = n.elements.map((e) => (e as ts.StringLiteral).text)
          keys.set(`{n} ${many}`, at(n))
          keys.set(`#one {n} ${many}`, `{n} ${one}`)
        }
      }
      ts.forEachChild(n, visit)
    }
    visit(sf)
  }
  return { untranslated, keys }
}

const { untranslated, keys } = scan()
const catalogs: Record<string, Catalog> = { fr: JSON.parse(readFileSync(join(root, 'locales/fr.json'), 'utf8')) }
// I18N_REPORT=<dir> writes the lists, for working through them.
if (process.env.I18N_REPORT) {
  writeFileSync(join(process.env.I18N_REPORT, 'untranslated.txt'), untranslated.join('\n') + '\n')
  const out: Record<string, string | { one: string; other: string }> = {}
  for (const [k, v] of keys) if (!k.startsWith('#one ')) out[k] = keys.has('#one ' + k) ? { one: keys.get('#one ' + k)!, other: k } : v
  writeFileSync(join(process.env.I18N_REPORT, 'keys.json'), JSON.stringify(out, null, 1))
}
const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(',')

describe('translations', () => {
  it('translate every display string', () => {
    expect(untranslated).toEqual([])
  })
  describe.each(Object.keys(catalogs))('the %s catalog', (locale) => {
    const cat = catalogs[locale]
    const used = [...keys.keys()].filter((k) => !k.startsWith('#one '))
    it('has every string', () => {
      expect(used.filter((k) => !cat[k]).map((k) => `${keys.get(k)} ${JSON.stringify(k)}`)).toEqual([])
    })
    it('keeps each string’s placeholders', () => {
      const bad = used.flatMap((k) => {
        const e = cat[k]
        const forms = typeof e === 'string' ? [e] : e ? Object.values(e) : []
        // A plural form may leave out {n} ("one schedule").
        const withoutN = (x: string) => placeholders(x).split(',').filter((p) => p !== 'n').join(',')
        return forms.filter((f) => f && placeholders(f) !== placeholders(k) && !(typeof e === 'object' && withoutN(f) === withoutN(k))).map((f) => `${JSON.stringify(k)} → ${JSON.stringify(f)}`)
      })
      expect(bad).toEqual([])
    })
    it('has no string the UI no longer uses', () => {
      expect(Object.keys(cat).filter((k) => !keys.has(k))).toEqual([])
    })
  })
})

describe('the runtime', () => {
  it('picks the locale: the choice, else the browser, else English', () => {
    expect(resolveLocale('fr', ['en-US'])).toBe('fr')
    expect(resolveLocale('system', ['fr-CH', 'en'])).toBe('fr')
    expect(resolveLocale(null, ['de-DE', 'en-GB'])).toBe('en')
    expect(resolveLocale('xx', ['ja'])).toBe('en')
  })
  it('translates, fills placeholders and pluralises by the locale’s rules', () => {
    setCatalogForTests('fr', { 'Delete {name}?': 'Supprimer {name} ?', '{n} hosts': { one: '{n} hôte', other: '{n} hôtes' } })
    expect(tr('Delete {name}?', { name: 'web' })).toBe('Supprimer web ?')
    expect(tr('Not in the catalog {x}', { x: 1 })).toBe('Not in the catalog 1')
    // French: 0 and 1 are singular.
    expect(trn(0, '{n} host', '{n} hosts')).toBe('0 hôte')
    expect(trn(1, '{n} host', '{n} hosts')).toBe('1 hôte')
    expect(trn(1200, '{n} host', '{n} hosts')).toBe('1 200 hôtes')
    setCatalogForTests('en', {})
    expect(trn(0, '{n} host', '{n} hosts')).toBe('0 hosts')
    expect(trn(1, '{n} host', '{n} hosts')).toBe('1 host')
  })
})
