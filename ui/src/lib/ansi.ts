// Minimal ANSI SGR parser for ansible output: foreground colours and bold.

export interface Segment {
  text: string
  cls: string
}

const sgr = /\x1b\[([0-9;]*)m/g

export function parseAnsi(input: string): Segment[] {
  const out: Segment[] = []
  let fg = ''
  let bold = false
  let last = 0
  const push = (text: string) => {
    if (!text) return
    const cls = [fg && `ansi-${fg}`, bold && 'ansi-bold'].filter(Boolean).join(' ')
    const prev = out[out.length - 1]
    if (prev && prev.cls === cls) prev.text += text
    else out.push({ text, cls })
  }
  for (const m of input.matchAll(sgr)) {
    push(input.slice(last, m.index))
    last = (m.index ?? 0) + m[0].length
    const codes = m[1] === '' ? [0] : m[1].split(';').map(Number)
    for (const c of codes) {
      if (c === 0) {
        fg = ''
        bold = false
      } else if (c === 1) bold = true
      else if (c === 22) bold = false
      else if (c === 39) fg = ''
      else if ((c >= 30 && c <= 37) || (c >= 90 && c <= 97)) fg = String(c)
    }
  }
  push(input.slice(last))
  return out
}

/** Text without escape sequences or carriage returns. */
export function stripAnsi(s: string): string {
  return s.replace(sgr, '').replace(/\x1b\[[0-9;]*[A-Za-z]/g, '').replace(/\r/g, '')
}
