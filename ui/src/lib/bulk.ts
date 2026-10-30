// Bulk actions on a list's selected rows: what is selected across pages,
// and what a run over them reports.

/** The rows selected, kept by key so a selection survives paging. `next` is
 * the table's new set of keys; rows not seen before come from the page. */
export function nextSelection<T, K>(picked: Map<K, T>, next: Set<K>, page: T[], key: (r: T) => K): Map<K, T> {
  const out = new Map<K, T>()
  for (const k of next) {
    const r = picked.get(k) ?? page.find((p) => key(p) === k)
    if (r !== undefined) out.set(k, r)
  }
  return out
}

/** "Select all N": offered when the whole page is selected and more match. */
export function offerSelectAll(selected: number, pageKeys: unknown[], isSelected: (k: unknown) => boolean, total: number): boolean {
  return pageKeys.length > 0 && pageKeys.every(isSelected) && total > selected
}

/** At most this many rows are selected by "Select all". */
export const MAX_SELECT_ALL = 1000

export interface BulkOutcome {
  done: number
  failed: number
  /** The first failure's message. */
  error?: string
}

export function bulkOutcome(results: PromiseSettledResult<unknown>[]): BulkOutcome {
  const failed = results.filter((r): r is PromiseRejectedResult => r.status === 'rejected')
  const reason = failed[0]?.reason
  return {
    done: results.length - failed.length,
    failed: failed.length,
    error: failed.length ? (reason instanceof Error ? reason.message : String(reason)) : undefined,
  }
}

/** The toast after a bulk action: "Delete: done for 3 projects", or
 * "Delete: 1 of 3 failed — You do not have permission…". */
export function bulkMessage(label: string, o: BulkOutcome, noun: [string, string]): string {
  const n = o.done + o.failed
  if (o.failed) return `${label}: ${o.failed} of ${n} failed — ${o.error}`
  return `${label}: done for ${n} ${n === 1 ? noun[0] : noun[1]}`
}

/** The names in a confirmation: "a, b and c", or "a, b, c and 7 more". */
export function nameList(names: string[], max = 8): string {
  if (names.length <= 1) return names.join('')
  if (names.length > max) return `${names.slice(0, max).join(', ')} and ${names.length - max} more`
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}
