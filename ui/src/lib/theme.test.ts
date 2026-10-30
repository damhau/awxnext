import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolveTheme } from './theme'

describe('theme', () => {
  it('follows the system unless a theme is chosen', () => {
    expect(resolveTheme('system', true)).toBe('dark')
    expect(resolveTheme('system', false)).toBe('light')
    expect(resolveTheme('light', true)).toBe('light')
    expect(resolveTheme('dark', false)).toBe('dark')
    expect(resolveTheme('bogus', true)).toBe('dark')
  })
  it('is applied before the first paint, from the same stored key', () => {
    const early = readFileSync(new URL('../../public/theme.js', import.meta.url), 'utf8')
    const index = readFileSync(new URL('../../index.html', import.meta.url), 'utf8')
    expect(early).toContain("localStorage.getItem('awxnext:theme')")
    expect(index.indexOf('<script src="/theme.js"></script>')).toBeGreaterThan(-1)
    expect(index.indexOf('/theme.js')).toBeLessThan(index.indexOf('/src/main.tsx'))
  })
})
