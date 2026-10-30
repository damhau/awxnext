/** The host metrics list's query: a name search, deleted hosts hidden unless
 * asked for, most recently automated first. */
export function metricsQuery(q: string, showDeleted: boolean, page: number, pageSize: number): Record<string, string | number> {
  const out: Record<string, string | number> = { order_by: '-last_automation', page, page_size: pageSize }
  if (q.trim()) out.search = q.trim()
  if (!showDeleted) out.deleted = 'false'
  return out
}

/** A summary row's month ("2026-10-01") in words: "October 2026". */
export function monthLabel(date: string): string {
  const m = /^(\d{4})-(\d{2})/.exec(date)
  if (!m) return date
  const d = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, 1))
  return d.toLocaleDateString('en-GB', { month: 'long', year: 'numeric', timeZone: 'UTC' })
}
