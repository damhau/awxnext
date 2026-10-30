// WCAG contrast: the check behind dark mode (roadmap M7-13). Text needs 4.5:1
// against what is behind it, large text (24px, or 18.66px bold) 3:1.

export type RGBA = [number, number, number, number]

export function parseColor(s: string): RGBA | null {
  const v = s.trim().toLowerCase()
  if (v === 'transparent') return [0, 0, 0, 0]
  let m = v.match(/^#([0-9a-f]{3,8})$/)
  if (m) {
    let h = m[1]
    if (h.length === 3 || h.length === 4) h = [...h].map((c) => c + c).join('')
    if (h.length !== 6 && h.length !== 8) return null
    const n = (i: number) => parseInt(h.slice(i, i + 2), 16)
    return [n(0), n(2), n(4), h.length === 8 ? n(6) / 255 : 1]
  }
  m = v.match(/^rgba?\(([^)]+)\)$/)
  if (m) {
    const p = m[1].split(/[\s,/]+/).filter(Boolean).map((x) => (x.endsWith('%') ? (parseFloat(x) / 100) * 255 : parseFloat(x)))
    if (p.length < 3 || p.some(isNaN)) return null
    const a = p.length > 3 ? (m[1].includes('%') && p[3] > 1 ? p[3] / 255 : p[3]) : 1
    return [p[0], p[1], p[2], a]
  }
  return null
}

/** fg drawn over bg. */
export function over(fg: RGBA, bg: RGBA): RGBA {
  const a = fg[3] + bg[3] * (1 - fg[3])
  if (a === 0) return [0, 0, 0, 0]
  const c = (i: number) => (fg[i] * fg[3] + bg[i] * bg[3] * (1 - fg[3])) / a
  return [c(0), c(1), c(2), a]
}

export function luminance(c: RGBA): number {
  const ch = (v: number) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * ch(c[0]) + 0.7152 * ch(c[1]) + 0.0722 * ch(c[2])
}

/** The contrast ratio of fg (composited over bg) against an opaque bg. */
export function contrast(fg: RGBA, bg: RGBA): number {
  const f = over(fg, bg)
  const [a, b] = [luminance(f), luminance(bg)].sort((x, y) => y - x)
  return (a + 0.05) / (b + 0.05)
}

/** The ratio text of this size and weight needs (WCAG 1.4.3, level AA). */
export function required(fontSizePx: number, fontWeight: number): number {
  return fontSizePx >= 24 || (fontSizePx >= 18.66 && fontWeight >= 700) ? 3 : 4.5
}

/** The custom properties of a theme, resolved: the :root block, with the
 * dark block's overrides for the dark theme. */
export function themeTokens(css: string, theme: 'light' | 'dark'): Record<string, string> {
  const block = (sel: RegExp) => {
    const m = css.match(sel)
    const out: Record<string, string> = {}
    if (!m) return out
    for (const d of m[1].replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) out[d[1]] = d[2].trim()
    return out
  }
  const raw = { ...block(/:root\s*\{([^}]*)\}/), ...(theme === 'dark' ? block(/:root\[data-theme='dark'\]\s*\{([^}]*)\}/) : {}) }
  const resolve = (v: string, depth = 0): string => (depth > 10 ? v : v.replace(/var\((--[\w-]+)\)/g, (_, k) => resolve(raw[k] ?? '', depth + 1)))
  return Object.fromEntries(Object.entries(raw).map(([k, v]) => [k, resolve(v)]))
}
