import type { JobStatus, UserRef } from '../api/types'

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const timeOnly = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' })
const weekday = new Intl.DateTimeFormat(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' })

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
  if (abs < 45) return sec <= 0 ? 'just now' : 'in a moment'
  if (abs < 3600) return rtf.format(Math.round(sec / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(sec / 3600), 'hour')
  if (abs < 86400 * 7) return rtf.format(Math.round(sec / 86400), 'day')
  if (abs < 86400 * 30) return rtf.format(Math.round(sec / (86400 * 7)), 'week')
  if (abs < 86400 * 365) return rtf.format(Math.round(sec / (86400 * 30)), 'month')
  return rtf.format(Math.round(sec / (86400 * 365)), 'year')
}

export function absolute(s: string | null | undefined): string {
  const d = parseDate(s)
  return d ? dateTime.format(d) : '—'
}

/** Short form for upcoming runs: "14:00", "Thu 06:00", "12 Mar 2026, 06:00". */
export function upcoming(s: string | null | undefined, now = Date.now()): string {
  const d = parseDate(s)
  if (!d) return '—'
  const diff = d.getTime() - now
  if (diff >= 0 && diff < 86400_000 && new Date(now).getDate() === d.getDate()) return 'today ' + timeOnly.format(d)
  if (diff >= 0 && diff < 6 * 86400_000) return weekday.format(d)
  return dateTime.format(d)
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

/** Stable pastel for an avatar, derived from the name. */
export function avatarColor(name: string): string {
  const palette = ['#E3C9A8', '#BFD3C4', '#CFC4E8', '#C4D7E8', '#E8C4C9', '#D8DDB8', '#C9C9C9']
  let h = 0
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return palette[h % palette.length]
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
  if (s === 'never updated') return 'Never run'
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/**
 * Order for lists of jobs from several templates: latest start first, with
 * jobs not started yet on top. Ordering by id would show a job above the
 * project sync it waited for, since the sync is created after the job.
 */
export const jobsNewestFirst = '-started,-id'

export const finished = (s: string) => ['successful', 'failed', 'error', 'canceled'].includes(s)
export const active = (s: string) => ['new', 'pending', 'waiting', 'running'].includes(s)

export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`
}

export function shortSha(s: string | null | undefined): string {
  return s ? s.slice(0, 7) : ''
}

/** Friendly name of a unified job/template type. */
export function typeLabel(t: string): string {
  return (
    {
      job: 'Playbook run',
      job_template: 'Job template',
      workflow_job: 'Workflow',
      workflow_job_template: 'Workflow template',
      project_update: 'Project sync',
      project: 'Project',
      inventory_update: 'Inventory sync',
      inventory_source: 'Inventory source',
      ad_hoc_command: 'Command',
      system_job: 'Management job',
      system_job_template: 'Management job',
      workflow_approval: 'Approval',
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
