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
