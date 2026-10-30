import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { contrast, over, parseColor, required, themeTokens } from './contrast'
import { avatarPalette } from './format'

describe('contrast', () => {
  it('parses colours', () => {
    expect(parseColor('#fff')).toEqual([255, 255, 255, 1])
    expect(parseColor('#15171a')).toEqual([21, 23, 26, 1])
    expect(parseColor('rgba(92, 196, 120, 0.13)')).toEqual([92, 196, 120, 0.13])
    expect(parseColor('rgb(0 0 0 / 50%)')).toEqual([0, 0, 0, 0.5])
    expect(parseColor('var(--x)')).toBeNull()
  })
  it('computes WCAG ratios', () => {
    expect(contrast(parseColor('#000')!, parseColor('#fff')!)).toBeCloseTo(21, 1)
    expect(contrast(parseColor('#777')!, parseColor('#fff')!)).toBeCloseTo(4.48, 1)
    // Half-transparent black on white is a mid grey.
    expect(contrast(parseColor('rgba(0,0,0,0.5)')!, parseColor('#fff')!)).toBeCloseTo(3.95, 1)
    expect(required(13, 400)).toBe(4.5)
    expect(required(28, 700)).toBe(3)
    expect(required(19, 700)).toBe(3)
  })
})

// Every text colour on every background it is used on, in both themes.
const css = readFileSync(new URL('../styles.css', import.meta.url), 'utf8')
const text = ['--ink', '--ink-2', '--ink-3', '--muted', '--muted-2', '--accent']
const grounds = ['--ground', '--surface', '--surface-2', '--surface-3', '--hover', '--accent-soft', '--accent-wash']
const pairs: [string, string, number?][] = [
  ...text.flatMap((t) => grounds.map((g): [string, string] => [t, g])),
  ['--ink', '--lime-wash'],
  ['--ok-ink', '--ok-soft'],
  ['--ok-ink', '--surface'],
  ['--changed-ink', '--changed-soft'],
  ['--changed-ink', '--surface'],
  ['--fail-ink', '--fail-soft'],
  ['--fail-ink', '--surface'],
  ['--fail-deep', '--fail-soft'],
  ['--fail-deep', '--fail-wash'],
  ['--fail-deep', '--surface'],
  ['--ink', '--fail-soft'],
  ['--ink', '--fail-wash'],
  ['--ground', '--ink'], // selected chips and segments
  ['--on-lime', '--lime'],
  ['--on-lime', '--lime-hover'],
  ['#ffffff', '--fail-solid'],
  ['#ffffff', '--bar'],
  ['--bar-ink', '--bar'],
  ['--bar-muted', '--bar'],
  ['--bar-muted', '--bar-2'],
  ['--term-ink', '--term'],
  ['--term-ink', '--term-2'],
  ['--coal', '--paper'],
  ['--term-faint', '--term'], // line numbers
  ['--term-faint', '--term-2'],
  ['#ffffff', '--term-ok'], // task badges
  ['--coal', '--term-changed'],
  ['#ffffff', '--term-fail'],
  ['--coal', '--term-skip'],
  ['--star', '--surface', 3], // an icon
  ['--ok', '--surface', 3], // cells
  ['--fail', '--surface', 3],
]

describe.each(['light', 'dark'] as const)('the %s theme', (theme) => {
  const tokens = themeTokens(css, theme)
  const color = (t: string) => parseColor(t.startsWith('--') ? (tokens[t] ?? '') : t)
  it('defines every token in both themes', () => {
    for (const [fg, bg] of pairs) {
      expect(color(fg), `${fg} in ${theme}`).not.toBeNull()
      expect(color(bg), `${bg} in ${theme}`).not.toBeNull()
    }
  })
  it('has enough contrast for its text', () => {
    const failing = pairs
      .map(([fg, bg, need = 4.5]) => ({ fg, bg, need, ratio: contrast(color(fg)!, color(bg)!) }))
      .filter((p) => p.ratio < p.need)
      .map((p) => `${p.fg} on ${p.bg}: ${p.ratio.toFixed(2)} < ${p.need}`)
    expect(failing).toEqual([])
  })
  it('keeps every colour of the output readable on every row', () => {
    const inks = ['--term-ink', '--ansi-red', '--ansi-green', '--ansi-yellow', '--ansi-blue', '--ansi-magenta', '--ansi-cyan', '--ansi-white', '--ansi-grey']
    const rows = ['--term', '--term-fatal', '--term-hit', '--term-hover', '--term-diff-add', '--term-diff-del'].map((r) => ({ r, bg: over(color(r)!, color('--term')!) }))
    const failing: string[] = []
    for (const { r, bg } of rows) {
      for (const ink of inks) if (contrast(color(ink)!, bg) < 4.5) failing.push(`${ink} on ${r}`)
      // Line numbers, except on search hits, where they take the text colour.
      if (r !== '--term-hit' && contrast(color('--term-faint')!, bg) < 4.5) failing.push(`--term-faint on ${r}`)
    }
    expect(failing).toEqual([])
  })
  it('keeps the avatars readable', () => {
    for (const bg of avatarPalette) expect(contrast(color('--coal')!, parseColor(bg)!), bg).toBeGreaterThanOrEqual(4.5)
  })
})
