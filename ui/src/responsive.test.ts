import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// The layout's breakpoints (roadmap M7-14): 900px and below, the navigation
// is a drawer; 600px and below, the phone layout. Every stylesheet uses the
// same two, so pages change layout together.
const read = (p: string) => readFileSync(new URL(p, import.meta.url), 'utf8')
const sheets = ['./styles.css', './pages/admin/admin.css', './pages/automation/automation.css', './pages/resources/resources.css']

describe('responsive layout', () => {
  it('lets phones use their width', () => {
    expect(read('../index.html')).toContain('<meta name="viewport" content="width=device-width, initial-scale=1" />')
  })
  it('uses only the two breakpoints', () => {
    for (const s of sheets) {
      for (const m of read(s).matchAll(/@media\s*\(([^)]*)\)/g)) {
        const q = m[1].replace(/\s+/g, ' ').trim()
        if (/^(prefers-|pointer|hover)/.test(q)) continue
        expect(['max-width: 900px', 'max-width: 600px'], `${s}: @media (${q})`).toContain(q)
      }
    }
  })
  it('has a phone layout for the stacked tables and the drawer', () => {
    const css = read('./styles.css')
    const phone = css.slice(css.indexOf('@media (max-width: 600px)'))
    expect(phone).toContain('table.data.stack-sm')
    expect(css.slice(css.indexOf('@media (max-width: 900px)'))).toContain('.nav.open')
  })
})
