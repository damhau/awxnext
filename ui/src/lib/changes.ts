import { plural, shortSha } from './format'
import { tr, trn } from './i18n'

export type Reason = 'last_success' | 'previous' | 'chosen'

export interface RunRef {
  id: number
  type: string
  status: string
  created?: string
  finished?: string | null
  reason?: Reason
}

export interface ChangeVersion {
  version: number
  created: string
  operation: string
  comment: string
  fields: string[]
  actor: { id: number; username: string } | null
}

export interface FieldChange {
  field: string
  key?: string
  before: unknown
  after: unknown
  secret?: boolean
  added?: boolean
  removed?: boolean
}

export interface ObjectChange {
  kind: string
  id?: number
  name?: string
  readable: boolean
  versions?: ChangeVersion[]
  from_version?: number
  to_version?: number
  fields?: FieldChange[]
}

export interface Commit {
  sha: string
  author: string
  date: string
  message: string
}

export interface ProjectChange extends ObjectChange {
  before: { revision: string }
  after: { revision: string }
  commits: Commit[]
  total_commits?: number
  commits_available?: boolean
  missing?: 'repository' | 'older' | 'newer'
  diverged?: boolean
  other_project?: { id: number; name: string } | null
}

export interface ImageChange extends ObjectChange {
  before: { image: string; digest: string }
  after: { image: string; digest: string }
  same_tag: boolean
}

export interface HostChange {
  added: string[]
  removed: string[]
  added_count: number
  removed_count: number
  before_count: number
  after_count: number
}

export interface TemplateRef {
  id: number
  name: string
  type: string
}

export interface NodeChange {
  identifier: string
  change: '' | 'added' | 'removed' | 'template'
  template: TemplateRef | null
  before_template: TemplateRef | null
  before_job: RunRef | null
  after_job: RunRef | null
}

/** What jobs/N/changes/ answers: how a run differs from an earlier one. */
export interface JobChanges {
  job: RunRef
  compared_with: RunRef | null
  last_success: RunRef | null
  previous: RunRef | null
  count: number
  template: ObjectChange | null
  project: ProjectChange | null
  inventory: ObjectChange | null
  credentials: ObjectChange[]
  execution_environment: ImageChange | null
  inputs: FieldChange[]
  hosts: HostChange | null
  nodes: NodeChange[]
}

export type Section = 'project' | 'template' | 'inventory' | 'credentials' | 'inputs' | 'execution_environment' | 'hosts' | 'nodes'

export interface SummaryLine {
  section: Section
  /** The id of the line's section on the Changes tab. */
  anchor: string
  label: string
  text: string
  /** When the object was last edited, if the line is an edit. */
  at?: string
  by?: string
}

const SHOWN_FIELDS = 3

export function changesPath(job: { id: number; type: string }, since?: number | string | null): string {
  const res = job.type === 'workflow_job' ? 'workflow_jobs' : 'jobs'
  return `${res}/${job.id}/changes/` + (since ? `?since=${encodeURIComponent(String(since))}` : '')
}

export function anchorOf(section: Section, id?: number): string {
  return section === 'credentials' && id ? `credential-${id}` : section
}

/** The field a change names: an extra variable or a credential input by its key. */
export function fieldName(f: FieldChange): string {
  return f.key && f.field !== 'extra_vars' ? `${f.field}.${f.key}` : (f.key ?? f.field)
}

function names(list: string[]): string {
  const uniq = [...new Set(list)]
  const shown = uniq.slice(0, SHOWN_FIELDS).join(', ')
  return uniq.length > SHOWN_FIELDS ? `${shown} +${uniq.length - SHOWN_FIELDS}` : shown
}

/** How an object was edited between the runs, as "extra_vars, limit" by whom and when. */
export function editLine(o: ObjectChange): Pick<SummaryLine, 'text' | 'at' | 'by'> {
  if (!o.readable) return { text: tr('changed (you cannot see how)') }
  const versions = o.versions ?? []
  const last = versions[versions.length - 1]
  const fields = o.fields?.length ? o.fields.map(fieldName) : versions.flatMap((v) => v.fields)
  const text = fields.length ? names(fields) : last?.comment || tr('edited')
  const people = [...new Set(versions.map((v) => v.actor?.username).filter((u): u is string => !!u))]
  const by = people.length > 1 ? trn(people.length - 1, '{name} and {n} other', '{name} and {n} others', { name: people[people.length - 1] }) : people[0]
  return { text, at: last?.created, by }
}

export function shortDigest(d: string): string {
  return d ? d.replace(/^(sha256:[0-9a-f]{4})[0-9a-f]+$/, '$1…') : ''
}

function projectLine(p: ProjectChange): string {
  const b = shortSha(p.before.revision) || tr('none')
  const a = shortSha(p.after.revision) || tr('none')
  if (p.other_project) return tr('another project: {before} → {after}', { before: p.other_project.name, after: p.name ?? '' })
  if (p.before.revision === p.after.revision) return editLine(p).text
  if (p.commits_available && p.total_commits !== undefined) return `${plural(p.total_commits, 'commit')} · ${b} → ${a}` + (p.diverged ? tr(', history rewritten') : '')
  if (p.missing === 'older') return `${b} → ${a} · ${tr('the older revision is gone')}`
  return `${b} → ${a}`
}

function imageLine(e: ImageChange): Pick<SummaryLine, 'text' | 'at' | 'by'> {
  if (e.same_tag) return { text: `${tr('same tag, new image')} · ${shortDigest(e.before.digest)} → ${shortDigest(e.after.digest)}` }
  if (e.before.image && e.after.image && e.before.image !== e.after.image) return { text: `${e.before.image} → ${e.after.image}` }
  return editLine(e)
}

/** An input's value as the tab and the summary show it. */
export function inputValue(v: unknown): string {
  if (v === null || v === undefined || v === '') return tr('(none)')
  if (Array.isArray(v)) return v.length ? v.map(inputValue).join(', ') : tr('(none)')
  if (typeof v === 'object') {
    const o = v as Record<string, unknown>
    if ('name' in o && 'id' in o) return String(o.name || `#${o.id}`)
    return JSON.stringify(v)
  }
  return String(v)
}

function inputsLine(inputs: FieldChange[]): string {
  if (inputs.length === 1 && !inputs[0].secret) {
    const i = inputs[0]
    const b = inputValue(i.before)
    const a = inputValue(i.after)
    if (b.length <= 24 && a.length <= 24) return `${fieldName(i)} ${b} → ${a}`
  }
  return names(inputs.map(fieldName))
}

function hostsLine(h: HostChange): string {
  const parts = []
  if (h.added_count) parts.push(trn(h.added_count, '{n} host added', '{n} hosts added'))
  if (h.removed_count) parts.push(parts.length ? trn(h.removed_count, '{n} removed', '{n} removed') : trn(h.removed_count, '{n} host removed', '{n} hosts removed'))
  return parts.join(', ')
}

function nodesLine(nodes: NodeChange[]): string {
  const n = (c: NodeChange['change']) => nodes.filter((x) => x.change === c).length
  const parts = []
  if (n('added')) parts.push(trn(n('added'), '{n} node added', '{n} nodes added'))
  if (n('removed')) parts.push(trn(n('removed'), '{n} node removed', '{n} nodes removed'))
  if (n('template')) parts.push(trn(n('template'), '{n} node runs another template', '{n} nodes run another template'))
  return parts.join(', ')
}

/** Every change, one line each, in the Changes tab's order. */
export function changeLines(c: JobChanges): SummaryLine[] {
  const out: SummaryLine[] = []
  const line = (section: Section, label: string, l: Pick<SummaryLine, 'text' | 'at' | 'by'>, id?: number) => out.push({ section, anchor: anchorOf(section, id), label, ...l })
  if (c.project) line('project', tr('Project'), { text: projectLine(c.project), ...(c.project.before.revision === c.project.after.revision ? editLine(c.project) : {}) })
  if (c.template) line('template', c.job.type === 'workflow_job' ? tr('Workflow') : tr('Template'), editLine(c.template))
  if (c.inventory) line('inventory', tr('Inventory'), editLine(c.inventory))
  for (const cr of c.credentials) line('credentials', tr('Credential'), { ...editLine(cr), text: `${cr.name}: ${editLine(cr).text}` }, cr.id)
  if (c.inputs.length) line('inputs', tr('Inputs'), { text: inputsLine(c.inputs) })
  if (c.execution_environment) line('execution_environment', tr('Image'), imageLine(c.execution_environment))
  if (c.hosts) line('hosts', tr('Hosts'), { text: hostsLine(c.hosts) })
  const nodes = c.nodes.filter((n) => n.change)
  if (nodes.length) line('nodes', tr('Nodes'), { text: nodesLine(nodes) })
  return out
}

export interface Summary {
  since: RunRef
  lines: SummaryLine[]
  /** How many changes in all: the "See all" link shows when more than the lines. */
  count: number
  more: boolean
  /** Set when nothing changed, to say so. */
  nothing?: string
}

/** The summary under "Last success of this template": at most three lines. */
export function summarize(c: JobChanges | undefined, max = 3): Summary | null {
  if (!c?.compared_with) return null
  const all = changeLines(c)
  const since = c.compared_with
  if (!all.length) {
    return {
      since,
      lines: [],
      count: 0,
      more: false,
      nothing:
        c.job.type === 'workflow_job'
          ? tr('Nothing changed since #{id}: same workflow, nodes and inputs.', { id: since.id })
          : tr('Nothing changed since #{id}: same template, inventory, project revision, image and inputs.', { id: since.id }),
    }
  }
  return { since, lines: all.slice(0, max), count: Math.max(c.count, all.length), more: all.length > max || c.count > max }
}

/** The tab's header: "Compared with #41, the last success". */
export function comparedWith(c: JobChanges): string {
  const r = c.compared_with
  if (!r) return ''
  if (r.id === c.last_success?.id) return tr('Compared with #{id}, the last success,', { id: r.id })
  if (r.id === c.previous?.id) return c.last_success ? tr('Compared with #{id}, the previous run,', { id: r.id }) : tr('Compared with #{id}, the previous run (no earlier success),', { id: r.id })
  return tr('Compared with #{id}, a run you chose,', { id: r.id })
}

/** The sections the Changes tab shows, in order. */
export function sections(c: JobChanges): Section[] {
  const out: Section[] = []
  if (c.project) out.push('project')
  if (c.template) out.push('template')
  if (c.inventory) out.push('inventory')
  if (c.credentials.length) out.push('credentials')
  if (c.inputs.length) out.push('inputs')
  if (c.execution_environment) out.push('execution_environment')
  if (c.hosts) out.push('hosts')
  if (c.nodes.some((n) => n.change || n.after_job)) out.push('nodes')
  return out
}

/** Whether a field's values read best as a line diff, as extra_vars do. */
export function multiline(f: FieldChange): boolean {
  const text = (v: unknown) => typeof v === 'string' && v.includes('\n')
  return !f.secret && (text(f.before) || text(f.after) || (typeof f.before === 'object' && f.before !== null && !Array.isArray(f.before)) || (typeof f.after === 'object' && f.after !== null && !Array.isArray(f.after)))
}

/** A value as diff lines: text as is, anything else as indented JSON. */
export function diffText(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'string') return v
  return JSON.stringify(v, null, 2)
}

/** Commits listed out of their total: "50 of 132 commits". */
export function commitsShown(p: ProjectChange): string | null {
  const total = p.total_commits ?? 0
  return total > p.commits.length ? tr('{shown} of {total} commits, the newest first', { shown: p.commits.length, total }) : null
}

/** Why the commits between the revisions can't be listed. */
export function commitsMissing(p: ProjectChange): string | null {
  if (p.before.revision === p.after.revision || p.commits_available || !p.readable || p.other_project) return null
  switch (p.missing) {
    case 'older':
      return tr('The older revision is gone from the repository: its history was rewritten (a force push).')
    case 'newer':
      return tr('This node’s copy of the repository lacks the newer revision: sync the project to list the commits.')
    case 'repository':
      return tr('This node has no copy of the repository: sync the project to list the commits.')
  }
  return tr('The commits are listed only for git projects.')
}
