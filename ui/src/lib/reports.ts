// The Reports page's logic: its parameters (kept in the URL and in saved
// reports), the CSV of each table, and the charts' scales.
import { asIs, tr } from './i18n'

export type Period = '7' | '30' | '90' | 'custom'
export type Interval = 'day' | 'week'

export interface ReportPeriod {
  from: string
  to: string
  days: number
  previous_from: string
  previous_to: string
  interval: Interval
  kept_since: string
  cut_short: boolean
}

export interface Outcomes {
  runs: number
  successful: number
  failed: number
  canceled: number
}

export interface Totals extends Outcomes {
  period: ReportPeriod
  success_rate: number | null
  hours: number
  host_runs: number
  hosts: number
  top_projects: { project: number; name: string | null; runs: number }[]
}

export interface TemplateRow extends Outcomes {
  template: number | null
  name: string
  success_rate: number | null
  previous_success_rate: number | null
  success_rate_change: number | null
  median_duration: number | null
  p95_duration: number | null
  flips: number
  last_failure: { job: number; finished: string; exists: boolean } | null
}

export interface TrendBucket extends Outcomes {
  start: string
  failure_rate: number | null
  median_duration: number | null
}

export interface HostsReport {
  period: ReportPeriod
  automated: { organization: number | null; name: string | null; month: string; hosts: number }[]
  most_failed: { host_name: string; host: number | null; inventory: number | null; inventory_name: string | null; runs: number; failed: number; unreachable: number }[]
}

export interface LaunchRow extends Outcomes {
  success_rate: number | null
}

export interface LaunchesReport {
  period: ReportPeriod
  by_user: (LaunchRow & { user: number | null; username: string | null })[]
  by_team: (LaunchRow & { team: number; name: string })[]
  by_launch_type: (LaunchRow & { launch_type: string })[]
}

/** The filters a report takes, each an object's ID, with the API's names. */
export const FILTERS = ['organization', 'template', 'inventory', 'project', 'label'] as const
export type FilterKey = (typeof FILTERS)[number]

export const LAUNCH_TYPES: [string, string][] = [
  ['manual', tr('Manual')],
  ['relaunch', tr('Relaunch')],
  ['callback', tr('Callback')],
  ['scheduled', tr('Scheduled')],
  ['dependency', tr('Dependency')],
  ['workflow', tr('Workflow')],
  ['webhook', tr('Webhook')],
  ['sync', tr('Sync')],
  ['scm', tr('SCM update')],
  ['portal', tr('Portal request')],
  ['event_rule', tr('Event rule')],
]

export function launchTypeLabel(t: string): string {
  return LAUNCH_TYPES.find(([v]) => v === t)?.[1] ?? t
}

const DATE = /^\d{4}-\d{2}-\d{2}$/

/** The page's state from its URL: the period, filters and table order. */
export interface ReportState {
  period: Period
  from: string
  to: string
  filters: Partial<Record<FilterKey, number>>
  launchType: string
  order: string
  interval: Interval | ''
}

export function readState(sp: URLSearchParams): ReportState {
  const from = sp.get('from') ?? ''
  const p = sp.get('period')
  const period: Period = DATE.test(from) ? 'custom' : p === '7' || p === '90' ? p : '30'
  const filters: ReportState['filters'] = {}
  for (const k of FILTERS) {
    const n = Number(sp.get(k))
    if (Number.isInteger(n) && n > 0) filters[k] = n
  }
  const lt = sp.get('launch_type') ?? ''
  const iv = sp.get('interval')
  return {
    period,
    from: period === 'custom' ? from : '',
    to: period === 'custom' && DATE.test(sp.get('to') ?? '') ? (sp.get('to') as string) : '',
    filters,
    launchType: LAUNCH_TYPES.some(([v]) => v === lt) ? lt : '',
    order: sp.get('order_by') ?? '',
    interval: iv === 'day' || iv === 'week' ? iv : '',
  }
}

/** The API parameters every report of the page shares. */
export function apiParams(s: ReportState): Record<string, string> {
  const out: Record<string, string> = {}
  if (s.period === 'custom') {
    out.from = s.from
    if (s.to) out.to = s.to
  } else out.period = s.period
  for (const k of FILTERS) if (s.filters[k]) out[k] = String(s.filters[k])
  if (s.launchType) out.launch_type = s.launchType
  return out
}

/** A saved report: the URL parameters, without the table order. */
export interface SavedReport {
  name: string
  params: Record<string, string>
}

export function savedParams(s: ReportState): Record<string, string> {
  const out = apiParams(s)
  if (s.interval) out.interval = s.interval
  return out
}

export function sameParams(a: Record<string, string>, b: Record<string, string>): boolean {
  const ka = Object.keys(a)
  return ka.length === Object.keys(b).length && ka.every((k) => a[k] === b[k])
}

/** A rate as a whole percentage. */
export function percent(rate: number | null | undefined): string {
  if (rate === null || rate === undefined) return '—'
  return `${Math.round(rate * 100)}%`
}

/** A change of rate, in percentage points, signed. */
export function points(change: number | null | undefined): string {
  if (change === null || change === undefined) return ''
  const n = Math.round(change * 100)
  if (n === 0) return tr('±0 pts')
  return `${n > 0 ? '+' : '−'}${Math.abs(n)} ${tr('pts')}`
}

export interface CsvColumn<T> {
  header: string
  value: (row: T) => string | number | boolean | null | undefined
}

// A spreadsheet runs a cell that starts with one of these as a formula.
const FORMULA = /^[=+\-@\t\r]/

function csvCell(v: string | number | boolean | null | undefined): string {
  if (v === null || v === undefined) return ''
  let s = String(v)
  if (typeof v === 'string' && FORMULA.test(s)) s = "'" + s
  return /[",\n\r]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s
}

/** A table as CSV (RFC 4180, CRLF line ends), a header row first. */
export function toCsv<T>(columns: CsvColumn<T>[], rows: T[]): string {
  const lines = [columns.map((c) => csvCell(c.header)).join(',')]
  for (const r of rows) lines.push(columns.map((c) => csvCell(c.value(r))).join(','))
  return lines.join('\r\n') + '\r\n'
}

/** Each report's columns are its API fields, so that the CSV has the API's rows and values. */
function fields<T>(...names: (keyof T & string)[]): CsvColumn<T>[] {
  return names.map((n) => ({ header: n, value: (r: T) => r[n] as string | number | boolean | null | undefined }))
}

const outcomeFields = ['runs', 'successful', 'failed', 'canceled'] as const

export const templateCsv: CsvColumn<TemplateRow>[] = [
  ...fields<TemplateRow>('template', 'name', ...outcomeFields, 'success_rate', 'previous_success_rate', 'success_rate_change', 'median_duration', 'p95_duration', 'flips'),
  { header: asIs('last_failure.job'), value: (r) => r.last_failure?.job },
  { header: asIs('last_failure.finished'), value: (r) => r.last_failure?.finished },
]

export const trendCsv = fields<TrendBucket>('start', ...outcomeFields, 'failure_rate', 'median_duration')
export const automatedCsv = fields<HostsReport['automated'][number]>('organization', 'name', 'month', 'hosts')
export const mostFailedCsv = fields<HostsReport['most_failed'][number]>('host_name', 'host', 'inventory', 'inventory_name', 'runs', 'failed', 'unreachable')
export const byUserCsv = fields<LaunchesReport['by_user'][number]>('user', 'username', ...outcomeFields, 'success_rate')
export const byTeamCsv = fields<LaunchesReport['by_team'][number]>('team', 'name', ...outcomeFields, 'success_rate')
export const byLaunchTypeCsv = fields<LaunchesReport['by_launch_type'][number]>('launch_type', ...outcomeFields, 'success_rate')

/** The CSV's file name: the report, then the period. */
export function csvName(report: string, p: ReportPeriod | undefined): string {
  return p ? `awxnext-${report}-${p.from}-to-${p.to}.csv` : `awxnext-${report}.csv`
}

/** A y axis from 0: a round top at or above max and its ticks, at most 5. */
export function niceScale(max: number): { top: number; ticks: number[] } {
  if (!(max > 0)) return { top: 1, ticks: [0, 1] }
  const raw = max / 4
  const mag = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? 10 * mag
  const top = Math.ceil(max / step) * step
  const ticks: number[] = []
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(Math.round(v * 1e6) / 1e6)
  return { top, ticks }
}

/** A y axis of seconds that steps in whole seconds, minutes or hours. */
export function durationScale(max: number): { top: number; ticks: number[] } {
  const unit = max > 2 * 3600 ? 3600 : max > 120 ? 60 : 1
  const s = niceScale(max / unit)
  return { top: s.top * unit, ticks: s.ticks.map((t) => Math.round(t * unit)) }
}

/** A bucket's label: its day, or the week it starts. */
export function bucketLabel(start: string, interval: Interval, long = false): string {
  const d = new Date(start + 'T00:00:00Z')
  const opts: Intl.DateTimeFormatOptions = { day: 'numeric', month: 'short', timeZone: 'UTC' }
  if (long) opts.year = 'numeric'
  const s = new Intl.DateTimeFormat(undefined, opts).format(d)
  return interval === 'week' && long ? tr('week of {day}', { day: s }) : s
}

/** Which bucket labels the x axis shows: every nth, so that about 8 fit. */
export function labelEvery(n: number, fit = 8): number {
  return Math.max(1, Math.ceil(n / fit))
}
