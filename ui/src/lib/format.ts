import type { JobStatus, UserRef } from '../api/types'
import { currentLocale, tr, trn } from './i18n'

// Dates in the chosen locale (lib/i18n), made once per locale.
const formatters = new Map<string, Intl.DateTimeFormat | Intl.RelativeTimeFormat>()
function cached<T extends Intl.DateTimeFormat | Intl.RelativeTimeFormat>(name: string, make: (locale: string) => T): T {
  const key = currentLocale() + ':' + name
  let f = formatters.get(key) as T | undefined
  if (!f) formatters.set(key, (f = make(currentLocale())))
  return f
}
const rtf = () => cached('rtf', (l) => new Intl.RelativeTimeFormat(l, { numeric: 'auto' }))
const dateTime = () => cached('dateTime', (l) => new Intl.DateTimeFormat(l, { dateStyle: 'medium', timeStyle: 'short' }))
const dateMedium = () => cached('date', (l) => new Intl.DateTimeFormat(l, { dateStyle: 'medium' }))
const timeOnly = () => cached('time', (l) => new Intl.DateTimeFormat(l, { hour: '2-digit', minute: '2-digit' }))
const weekday = () => cached('weekday', (l) => new Intl.DateTimeFormat(l, { weekday: 'short', hour: '2-digit', minute: '2-digit' }))

export function parseDate(s: string | null | undefined): Date | null {
  if (!s) return null
  const d = new Date(s)
  return isNaN(d.getTime()) ? null : d
}

/** "12 min ago", "yesterday", "3 weeks ago". */
export function ago(s: string | null | undefined, now = Date.now()): string {
  const d = parseDate(s)
  if (!d) return '—'
  const sec = Math.round((d.getTime() - now) / 1000)
  const abs = Math.abs(sec)
  if (abs < 45) return sec <= 0 ? tr('just now') : tr('in a moment')
  if (abs < 3600) return rtf().format(Math.round(sec / 60), 'minute')
  if (abs < 86400) return rtf().format(Math.round(sec / 3600), 'hour')
  if (abs < 86400 * 7) return rtf().format(Math.round(sec / 86400), 'day')
  if (abs < 86400 * 30) return rtf().format(Math.round(sec / (86400 * 7)), 'week')
  if (abs < 86400 * 365) return rtf().format(Math.round(sec / (86400 * 30)), 'month')
  return rtf().format(Math.round(sec / (86400 * 365)), 'year')
}

export function absolute(s: string | null | undefined): string {
  const d = parseDate(s)
  return d ? dateTime().format(d) : '—'
}

/** The date alone, in the chosen locale. */
export function dateOnly(s: string | Date | null | undefined): string {
  const d = s instanceof Date ? s : parseDate(s)
  return d ? dateMedium().format(d) : '—'
}

/** A date and time, in the chosen locale. */
export function dateTimeText(s: string | Date | null | undefined): string {
  const d = s instanceof Date ? s : parseDate(s)
  return d ? dateTime().format(d) : '—'
}

/** Short form for upcoming runs: "14:00", "Thu 06:00", "12 Mar 2026, 06:00". */
export function upcoming(s: string | null | undefined, now = Date.now()): string {
  const d = parseDate(s)
  if (!d) return '—'
  const diff = d.getTime() - now
  if (diff >= 0 && diff < 86400_000 && new Date(now).getDate() === d.getDate()) return tr('today {time}', { time: timeOnly().format(d) })
  if (diff >= 0 && diff < 6 * 86400_000) return weekday().format(d)
  return dateTime().format(d)
}

/** "6m 42s", "1h 02m", "48s". */
export function duration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || isNaN(seconds)) return '—'
  const s = Math.max(0, Math.round(seconds))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`
  const h = Math.floor(m / 60)
  return `${h}h ${String(m % 60).padStart(2, '0')}m`
}

/** Elapsed seconds of a job, live while running. */
export function elapsed(job: { started?: string | null; finished?: string | null; elapsed?: number }, now = Date.now()): number | null {
  if (job.finished && job.elapsed) return job.elapsed
  const st = parseDate(job.started)
  if (!st) return null
  const end = parseDate(job.finished)?.getTime() ?? now
  return (end - st.getTime()) / 1000
}

export function userName(u?: UserRef | null): string {
  if (!u) return '—'
  const full = [u.first_name, u.last_name].filter(Boolean).join(' ')
  return full || u.username
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

/** Avatar pastels, the same in both themes; their initials are coal. */
export const avatarPalette = ['#E3C9A8', '#BFD3C4', '#CFC4E8', '#C4D7E8', '#E8C4C9', '#D8DDB8', '#C9C9C9']

/** Stable pastel for an avatar, derived from the name. */
export function avatarColor(name: string): string {
  let h = 0
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return avatarPalette[h % avatarPalette.length]
}

export type StatusKind = 'ok' | 'fail' | 'error' | 'run' | 'pending' | 'cancel' | 'none'

export function statusKind(s: JobStatus | string | null | undefined): StatusKind {
  switch (s) {
    case 'successful':
    case 'ok':
      return 'ok'
    case 'failed':
      return 'fail'
    case 'error':
    case 'missing':
      return 'error'
    case 'running':
    case 'updating':
      return 'run'
    case 'new':
    case 'pending':
    case 'waiting':
      return 'pending'
    case 'canceled':
      return 'cancel'
  }
  return 'none'
}

export function statusLabel(s: string | null | undefined): string {
  if (!s) return '—'
  const known: Record<string, string> = {
    'never updated': tr('Never run'),
    new: tr('New'),
    pending: tr('Pending'),
    waiting: tr('Waiting'),
    running: tr('Running'),
    successful: tr('Successful'),
    failed: tr('Failed'),
    error: tr('Error'),
    canceled: tr('Canceled'),
    ok: tr('Ok'),
    missing: tr('Missing'),
    updating: tr('Updating'),
    none: tr('None'),
  }
  return known[s] ?? s.charAt(0).toUpperCase() + s.slice(1)
}

/**
 * Order for lists of jobs from several templates: latest start first, with
 * jobs not started yet on top. Ordering by id would show a job above the
 * project sync it waited for, since the sync is created after the job.
 */
export const jobsNewestFirst = '-started,-id'

export const finished = (s: string) => ['successful', 'failed', 'error', 'canceled'].includes(s)
export const active = (s: string) => ['new', 'pending', 'waiting', 'running'].includes(s)

/** "3 hosts", in the chosen locale: the catalog has "{n} host" / "{n} hosts". */
export function plural(n: number, one: string, many = one + 's'): string {
  // i18n-dynamic: the checker reads the keys from plural()'s callers.
  return trn(n, `{n} ${one}`, `{n} ${many}`)
}

export function shortSha(s: string | null | undefined): string {
  return s ? s.slice(0, 7) : ''
}

/** Friendly name of a unified job/template type. */
export function typeLabel(t: string): string {
  return (
    {
      job: tr('Playbook run'),
      job_template: tr('Job template'),
      workflow_job: tr('Workflow'),
      workflow_job_template: tr('Workflow template'),
      project_update: tr('Project sync'),
      project: tr('Project'),
      inventory_update: tr('Inventory sync'),
      inventory_source: tr('Inventory source'),
      ad_hoc_command: tr('Command'),
      system_job: tr('Management job'),
      system_job_template: tr('Management job'),
      workflow_approval: tr('Approval'),
    } as Record<string, string>
  )[t] ?? t
}

/** UI route of a unified job. */
export function jobPath(j: { id: number; type: string }): string {
  return `/jobs/${j.type}/${j.id}`
}

/** UI route of a unified job template. */
export function templatePath(t: { id: number; type: string }): string {
  switch (t.type) {
    case 'job_template':
      return `/templates/job/${t.id}`
    case 'workflow_job_template':
      return `/templates/workflow/${t.id}`
    case 'project':
      return `/projects/${t.id}`
    case 'inventory_source':
      return `/inventory-sources/${t.id}`
    case 'system_job_template':
      return `/management-jobs`
  }
  return '/templates'
}

/** API path of a unified job by type. */
export function jobApi(type: string, id: number | string): string {
  const res: Record<string, string> = {
    job: 'jobs',
    workflow_job: 'workflow_jobs',
    project_update: 'project_updates',
    inventory_update: 'inventory_updates',
    ad_hoc_command: 'ad_hoc_commands',
    system_job: 'system_jobs',
    workflow_approval: 'workflow_approvals',
  }
  return `${res[type] ?? 'unified_jobs'}/${id}/`
}
