// A collection's README: CommonMark's common blocks and inlines, parsed into
// a tree the page renders as React elements. No HTML is passed through:
// the README is the uploader's text.

export type Inline =
  | { t: 'text'; text: string }
  | { t: 'code'; text: string }
  | { t: 'em' | 'strong' | 'del'; children: Inline[] }
  | { t: 'link'; href: string; children: Inline[] }
  | { t: 'br' }

export type Block =
  | { t: 'heading'; level: number; children: Inline[] }
  | { t: 'para'; children: Inline[] }
  | { t: 'code'; lang: string; text: string }
  | { t: 'list'; ordered: boolean; start: number; items: Block[][] }
  | { t: 'quote'; children: Block[] }
  | { t: 'table'; head: Inline[][]; rows: Inline[][][] }
  | { t: 'rule' }

/** A link's target when it is safe to follow: http(s), mailto, or an
 * anchor. */
export function safeHref(href: string): string | null {
  const h = href.trim()
  if (/^(https?:|mailto:)/i.test(h) || h.startsWith('#')) return h
  return null
}

const fence = /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)/
const heading = /^ {0,3}(#{1,6})(?:\s+(.*?))?\s*#*\s*$/
const rule = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/
const bullet = /^( {0,3})([-*+]|\d{1,9}[.)])(\s+|$)/
const tableSep = /^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$/

const blank = (l: string) => l.trim() === ''

export function parseMarkdown(src: string): Block[] {
  return blocks(src.replace(/\r\n?/g, '\n').replace(/\t/g, '    ').split('\n'))
}

function blocks(lines: string[]): Block[] {
  const out: Block[] = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    if (blank(line)) {
      i++
      continue
    }
    let m: RegExpMatchArray | null
    if ((m = line.match(fence))) {
      const close = new RegExp('^ {0,3}' + m[1][0] + '{' + m[1].length + ',}\\s*$')
      const body: string[] = []
      i++
      while (i < lines.length && !close.test(lines[i])) body.push(lines[i++])
      i++
      out.push({ t: 'code', lang: m[2], text: body.join('\n') })
      continue
    }
    if ((m = line.match(heading))) {
      out.push({ t: 'heading', level: m[1].length, children: inlines(m[2] ?? '') })
      i++
      continue
    }
    if (rule.test(line)) {
      out.push({ t: 'rule' })
      i++
      continue
    }
    if (/^ {4}/.test(line)) {
      const body: string[] = []
      while (i < lines.length && (/^ {4}/.test(lines[i]) || (blank(lines[i]) && /^ {4}/.test(lines[i + 1] ?? '')))) body.push(lines[i++].slice(4))
      out.push({ t: 'code', lang: '', text: body.join('\n') })
      continue
    }
    if (/^ {0,3}>/.test(line)) {
      const body: string[] = []
      while (i < lines.length && /^ {0,3}>/.test(lines[i])) body.push(lines[i++].replace(/^ {0,3}> ?/, ''))
      out.push({ t: 'quote', children: blocks(body) })
      continue
    }
    if ((m = line.match(bullet))) {
      const ordered = /\d/.test(m[2])
      const start = ordered ? parseInt(m[2], 10) : 1
      const items: Block[][] = []
      while (i < lines.length) {
        const im = lines[i].match(bullet)
        if (!im || /\d/.test(im[2]) !== ordered) break
        const indent = im[0].length
        const body = [lines[i].slice(indent)]
        i++
        while (i < lines.length) {
          const l = lines[i]
          if (blank(l)) {
            const next = lines[i + 1] ?? ''
            if (/^\s/.test(next) && !blank(next)) {
              body.push('')
              i++
              continue
            }
            break
          }
          if (bullet.test(l) && l.search(/\S/) < indent) break
          if (l.search(/\S/) < indent && (fence.test(l) || heading.test(l) || rule.test(l))) break
          body.push(l.slice(Math.min(indent, l.search(/\S/))))
          i++
        }
        items.push(blocks(body))
        while (i < lines.length && blank(lines[i]) && (lines[i + 1] ?? '').match(bullet)) i++
      }
      out.push({ t: 'list', ordered, start, items })
      continue
    }
    if (line.includes('|') && tableSep.test(lines[i + 1] ?? '')) {
      const head = cells(line)
      i += 2
      const rows: Inline[][][] = []
      while (i < lines.length && !blank(lines[i]) && lines[i].includes('|')) rows.push(cells(lines[i++]))
      out.push({ t: 'table', head, rows })
      continue
    }
    const para: string[] = []
    while (i < lines.length && !blank(lines[i]) && !fence.test(lines[i]) && !heading.test(lines[i]) && !/^ {0,3}>/.test(lines[i]) && !(para.length && bullet.test(lines[i]))) {
      if (para.length && /^ {0,3}(=+|-+)\s*$/.test(lines[i])) {
        out.push({ t: 'heading', level: lines[i].trim()[0] === '=' ? 1 : 2, children: inlines(para.join('\n')) })
        para.length = 0
        i++
        break
      }
      if (rule.test(lines[i]) && !para.length) break
      para.push(lines[i++])
    }
    if (para.length) out.push({ t: 'para', children: inlines(para.join('\n')) })
  }
  return out
}

function cells(line: string): Inline[][] {
  let s = line.trim()
  if (s.startsWith('|')) s = s.slice(1)
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1)
  return s.split(/(?<!\\)\|/).map((c) => inlines(c.trim().replace(/\\\|/g, '|')))
}

/** Text with backslash escapes, code spans, emphasis, links and line
 * breaks. Images become links to their source, named by their text. */
export function inlines(src: string): Inline[] {
  const out: Inline[] = []
  let text = ''
  const flush = () => {
    if (text) out.push({ t: 'text', text })
    text = ''
  }
  let i = 0
  while (i < src.length) {
    const c = src[i]
    if (c === '\\' && i + 1 < src.length && /[!-/:-@[-`{-~]/.test(src[i + 1])) {
      text += src[i + 1]
      i += 2
      continue
    }
    if (c === '\\' && src[i + 1] === '\n') {
      flush()
      out.push({ t: 'br' })
      i += 2
      continue
    }
    if (c === '`') {
      const ticks = src.slice(i).match(/^`+/)![0]
      const end = src.indexOf(ticks, i + ticks.length)
      if (end > 0) {
        flush()
        out.push({ t: 'code', text: src.slice(i + ticks.length, end).replace(/\n/g, ' ').replace(/^ (.*) $/, '$1') })
        i = end + ticks.length
        continue
      }
    }
    if (c === '!' && src[i + 1] === '[') {
      const l = link(src, i + 1)
      if (l) {
        flush()
        const href = safeHref(l.href)
        const children = l.label ? inlines(l.label) : [{ t: 'text' as const, text: l.href }]
        out.push(href ? { t: 'link', href, children } : { t: 'em', children })
        i = l.end
        continue
      }
    }
    if (c === '[') {
      const l = link(src, i)
      if (l) {
        flush()
        const href = safeHref(l.href)
        const children = inlines(l.label)
        if (href) out.push({ t: 'link', href, children })
        else out.push(...children)
        i = l.end
        continue
      }
    }
    if (c === '<') {
      const m = src.slice(i).match(/^<((?:https?|mailto):[^\s<>]+)>/i)
      if (m) {
        flush()
        out.push({ t: 'link', href: m[1], children: [{ t: 'text', text: m[1].replace(/^mailto:/i, '') }] })
        i += m[0].length
        continue
      }
    }
    if (c === 'h' && /^https?:\/\//.test(src.slice(i)) && (i === 0 || /[\s(]/.test(src[i - 1]))) {
      const m = src.slice(i).match(/^https?:\/\/[^\s<>]*[^\s<>.,;:!?)'"]/)
      if (m) {
        flush()
        out.push({ t: 'link', href: m[0], children: [{ t: 'text', text: m[0] }] })
        i += m[0].length
        continue
      }
    }
    if ((c === '*' || c === '_' || c === '~') && src[i + 1] !== ' ') {
      const run = src.slice(i).match(c === '~' ? /^~~/ : c === '*' ? /^\*{1,2}/ : /^_{1,2}/)
      if (run && !(c === '_' && /\w/.test(src[i - 1] ?? ''))) {
        const d = run[0]
        let end = src.indexOf(d, i + d.length)
        while (end > 0 && (src[end - 1] === ' ' || src[end - 1] === '\\')) end = src.indexOf(d, end + 1)
        if (end > i + d.length && !(d.length === 1 && src[end + 1] === d)) {
          flush()
          const children = inlines(src.slice(i + d.length, end))
          out.push({ t: c === '~' ? 'del' : d.length === 2 ? 'strong' : 'em', children })
          i = end + d.length
          continue
        }
      }
    }
    if (c === '\n') {
      if (text.endsWith('  ')) {
        text = text.replace(/ +$/, '')
        flush()
        out.push({ t: 'br' })
      } else text += ' '
      i++
      continue
    }
    text += c
    i++
  }
  flush()
  return out
}

/** [label](href "title") at i. */
function link(src: string, i: number): { label: string; href: string; end: number } | null {
  let depth = 0
  let j = i
  for (; j < src.length; j++) {
    if (src[j] === '\\') {
      j++
      continue
    }
    if (src[j] === '[') depth++
    if (src[j] === ']' && --depth === 0) break
  }
  if (j >= src.length || src[j + 1] !== '(') return null
  const close = src.indexOf(')', j + 2)
  if (close < 0) return null
  const target = src.slice(j + 2, close).trim()
  const href = target.match(/^<([^>]*)>|^(\S*)/)!
  return { label: src.slice(i + 1, j), href: href[1] ?? href[2], end: close + 1 }
}

/** The plain text of inlines, for a heading's anchor. */
export function plainText(xs: Inline[]): string {
  return xs.map((x) => (x.t === 'text' || x.t === 'code' ? x.text : x.t === 'br' ? ' ' : 'children' in x ? plainText(x.children) : '')).join('')
}

/** GitHub's heading anchors: lower case, punctuation dropped, spaces as
 * dashes. */
export function slug(s: string): string {
  return s
    .toLowerCase()
    .trim()
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .replace(/\s/g, '-')
}
