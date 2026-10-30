// Line diff (LCS) for small texts such as variables and snapshots.

export type DiffLine = { kind: 'ctx' | 'add' | 'del'; text: string; a?: number; b?: number }

export function diffLines(before: string, after: string): DiffLine[] {
  const a = before === '' ? [] : before.replace(/\n$/, '').split('\n')
  const b = after === '' ? [] : after.replace(/\n$/, '').split('\n')
  const n = a.length
  const m = b.length
  // lcs[i][j] = LCS length of a[i:] and b[j:]
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && a[i] === b[j]) {
      out.push({ kind: 'ctx', text: a[i], a: i + 1, b: j + 1 })
      i++
      j++
    } else if (i < n && (j >= m || lcs[i + 1][j] >= lcs[i][j + 1])) {
      out.push({ kind: 'del', text: a[i], a: i + 1 })
      i++
    } else {
      out.push({ kind: 'add', text: b[j], b: j + 1 })
      j++
    }
  }
  return out
}

/** A file a task changed, from its result's `diff` (Ansible's diff mode). */
export interface TaskDiff {
  /** The file, or what the module changed. */
  path: string
  before: string
  after: string
  /** A module's own diff text, when it gives no before and after. */
  prepared?: string
  /** The loop item, for a task with a loop. */
  item?: string
}

const text = (v: unknown): string => {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string') return v
  // Modules such as file report states: one key per line.
  if (typeof v === 'object')
    return Object.entries(v as Record<string, unknown>)
      .map(([k, x]) => `${k}: ${typeof x === 'string' ? x : JSON.stringify(x)}`)
      .join('\n')
  return String(v)
}

function one(d: Record<string, unknown>, res: Record<string, unknown>, item?: string): TaskDiff | null {
  const prepared = typeof d.prepared === 'string' ? d.prepared : undefined
  const before = text(d.before)
  const after = text(d.after)
  if (!prepared && before === after) return null
  // The destination is in before_header; after_header is often the
  // module's temporary source.
  const path = String(d.before_header ?? res.dest ?? res.path ?? d.after_header ?? '')
  return { path, before, after, prepared, item }
}

/** The diffs in a task result: `res.diff` (one or a list), and each loop item's. */
export function taskDiffs(res: unknown): TaskDiff[] {
  if (!res || typeof res !== 'object') return []
  const r = res as Record<string, unknown>
  const out: TaskDiff[] = []
  const add = (diff: unknown, from: Record<string, unknown>, item?: string) => {
    for (const d of Array.isArray(diff) ? diff : diff ? [diff] : []) {
      if (d && typeof d === 'object') {
        const x = one(d as Record<string, unknown>, from, item)
        if (x) out.push(x)
      }
    }
  }
  add(r.diff, r)
  if (Array.isArray(r.results))
    for (const it of r.results as Record<string, unknown>[]) {
      if (it && typeof it === 'object' && it.diff) add(it.diff, it, it.item === undefined ? undefined : text(it.item))
    }
  return out
}

/** Comparing in the browser is quadratic: beyond this, the output's diff is shown instead. */
export const MAX_DIFF_CELLS = 4_000_000

export function tooLargeToCompare(before: string, after: string): boolean {
  const n = (s: string) => (s ? s.split('\n').length : 0)
  return n(before) * n(after) > MAX_DIFF_CELLS
}

/** A unified diff's line: its header, a hunk, an added or removed line. */
export function unifiedKind(line: string): 'head' | 'hunk' | 'add' | 'del' | 'ctx' {
  if (line.startsWith('+++') || line.startsWith('---')) return 'head'
  if (line.startsWith('@@')) return 'hunk'
  if (line.startsWith('+')) return 'add'
  if (line.startsWith('-')) return 'del'
  return 'ctx'
}

export type HunkLine = DiffLine | { kind: 'skip'; n: number }

/** The changed lines with `context` lines around them; unchanged runs
 * between are folded into a count. */
export function hunks(lines: DiffLine[], context = 3): HunkLine[] {
  const keep = lines.map(() => false)
  lines.forEach((l, i) => {
    if (l.kind === 'ctx') return
    for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) keep[k] = true
  })
  const out: HunkLine[] = []
  let skipped = 0
  lines.forEach((l, i) => {
    if (keep[i]) {
      if (skipped) out.push({ kind: 'skip', n: skipped })
      skipped = 0
      out.push(l)
    } else skipped++
  })
  if (skipped) out.push({ kind: 'skip', n: skipped })
  return out
}
