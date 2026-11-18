// Catalog items in the admin UI: the item's form over its template's survey,
// and the body the API takes.
import { tr } from './i18n'

export type ItemKind = 'run_once' | 'deployment'

/** A question of the template's survey, as far as an item reads it. */
export interface SurveyQuestion {
  variable: string
  question_name: string
  question_description?: string
  type: string
  required?: boolean
  default?: unknown
  choices?: string[] | string
  choices_from?: unknown
  value_from?: unknown
}

/** What an item says of a survey question (the API's questions, by variable). */
export interface QuestionOverride {
  question_name?: string
  question_description?: string
  choices?: string[]
  filter?: string
  default?: unknown
  value?: unknown
  visible?: boolean
  hidden?: boolean
}

export type QuestionMode = 'asked' | 'hidden' | 'fixed'

/** A question in the item's form. Empty texts keep the survey's. */
export interface QuestionEdit {
  variable: string
  mode: QuestionMode
  question_name: string
  question_description: string
  /** The fixed choices requesters may pick; null: all of the survey's. */
  choices: string[] | null
  filter: string
  default: string
  value: string
  visible: boolean
}

export interface ApprovalRule {
  name: string
  condition: string
  approvers: 'team_admins' | 'team' | 'users'
  team?: number | null
  users_from?: string
  required?: number
  timeout?: number
}

export interface ConnectionLayout {
  label: string
  value: string
  copy?: boolean
}

export interface ItemAction {
  id: string
  name: string
  description?: string
  template: number | null
}

export const DEFAULT_TIMEOUT_HOURS = 7 * 24

/** The survey's fixed choices of a question, as a list. */
export function surveyChoices(q: SurveyQuestion): string[] {
  const c = q.choices
  if (Array.isArray(c)) return c.map(String)
  return (c ?? '').split('\n').filter((x) => x !== '')
}

export const hasFixedChoices = (q: SurveyQuestion) => !q.choices_from && (q.type === 'multiplechoice' || q.type === 'multiselect')

/** An answer as the form edits it: a multiselect's as lines. */
export function answerText(v: unknown): string {
  if (v == null) return ''
  if (Array.isArray(v)) return v.map(String).join('\n')
  return String(v)
}

/** The questions an item can change: those the server doesn't compute. */
export function editableQuestions(spec: SurveyQuestion[]): SurveyQuestion[] {
  return spec.filter((q) => !q.value_from)
}

/** The form's questions, in the survey's order, from the item's overrides. */
export function questionEdits(spec: SurveyQuestion[], overrides: Record<string, QuestionOverride> = {}): QuestionEdit[] {
  return editableQuestions(spec).map((q) => {
    const o = overrides[q.variable] ?? {}
    const fixed = 'value' in o
    return {
      variable: q.variable,
      mode: fixed ? 'fixed' : o.hidden ? 'hidden' : 'asked',
      question_name: o.question_name ?? '',
      question_description: o.question_description ?? '',
      choices: o.choices ?? null,
      filter: o.filter ?? '',
      default: 'default' in o ? answerText(o.default) : '',
      value: fixed ? answerText(o.value) : '',
      visible: !!o.visible,
    }
  })
}

function typed(q: SurveyQuestion, text: string): unknown {
  if (q.type === 'integer' || q.type === 'float') {
    const n = Number(text)
    return text.trim() !== '' && Number.isFinite(n) ? n : text
  }
  return text
}

/** The item's questions for the API: only what differs from the survey. */
export function questionOverrides(spec: SurveyQuestion[], rows: QuestionEdit[]): Record<string, QuestionOverride> {
  const out: Record<string, QuestionOverride> = {}
  for (const q of editableQuestions(spec)) {
    const r = rows.find((x) => x.variable === q.variable)
    if (!r) continue
    const o: QuestionOverride = {}
    if (r.question_name.trim() && r.question_name.trim() !== q.question_name) o.question_name = r.question_name.trim()
    if (r.question_description.trim() && r.question_description.trim() !== (q.question_description ?? '')) o.question_description = r.question_description.trim()
    if (r.mode === 'fixed') {
      o.value = typed(q, r.value)
      if (r.visible && q.type !== 'password') o.visible = true
    } else {
      if (r.mode === 'hidden') o.hidden = true
      const all = surveyChoices(q)
      if (r.mode === 'asked' && hasFixedChoices(q) && r.choices && r.choices.length < all.length) o.choices = all.filter((c) => r.choices!.includes(c))
      if (r.mode === 'asked' && q.choices_from && r.filter.trim()) o.filter = r.filter.trim()
      if (r.default !== '' && q.type !== 'password' && r.default !== answerText(q.default)) o.default = typed(q, r.default)
    }
    if (Object.keys(o).length) out[q.variable] = o
  }
  return out
}

/** A question's line in the item's details: what requesters get. */
export function questionSummary(q: SurveyQuestion, r: QuestionEdit): string {
  if (r.mode === 'fixed') {
    if (q.type === 'password') return tr('Fixed, never shown')
    return r.visible ? tr('Fixed to {value}, shown', { value: r.value }) : tr('Fixed to {value}, not shown', { value: r.value })
  }
  if (r.mode === 'hidden') return tr('Hidden: its default applies')
  const parts = [r.question_name ? tr('Asked as “{name}”', { name: r.question_name }) : tr('Asked')]
  if (r.choices) parts.push(tr('{n} of {all} choices', { n: r.choices.length, all: surveyChoices(q).length }))
  if (r.filter) parts.push(tr('filtered'))
  return parts.join(', ')
}

/** Days as an item's extensions take them, from "1, 3, 7". */
export function parseDays(text: string): { days: number[]; error: string | null } {
  const parts = text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  const days: number[] = []
  for (const p of parts) {
    const n = Number(p)
    if (!Number.isInteger(n) || n < 1) return { days: [], error: tr('Whole days of at least 1, separated by commas.') }
    if (days.includes(n)) return { days: [], error: tr('{n} is there twice.', { n }) }
    days.push(n)
  }
  return { days, error: null }
}

/** A number field: null when empty. */
export function optionalInt(text: string): number | null {
  return text.trim() === '' ? null : Number(text)
}

/** Who approves under a rule, in words. */
export function approverText(r: ApprovalRule, teamName?: string): string {
  if (r.approvers === 'team') return teamName ? tr('Members of {team}', { team: teamName }) : tr('Members of a team')
  if (r.approvers === 'users') return tr('The users from {expr}', { expr: r.users_from ?? '' })
  return tr('The Team Admins of the request’s team')
}

export function timeoutText(seconds: number | undefined): string {
  const s = seconds ?? DEFAULT_TIMEOUT_HOURS * 3600
  if (s % 86400 === 0) return s / 86400 === 1 ? tr('1 day') : tr('{n} days', { n: s / 86400 })
  return tr('{n} hours', { n: Math.round(s / 3600) })
}

/** Step labels as rows, and back. */
export function stepRows(labels: Record<string, string> | null | undefined): { step: string; label: string }[] {
  return Object.entries(labels ?? {}).map(([step, label]) => ({ step, label }))
}

export function stepLabels(rows: { step: string; label: string }[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const r of rows) if (r.step.trim() && r.label.trim()) out[r.step.trim()] = r.label.trim()
  return out
}

/** A suggested action id from its name: "Add disk" → add_disk. */
export function actionID(name: string): string {
  const id = name
    .toLowerCase()
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .replace(/^[^a-z]+/, '')
    .slice(0, 64)
  return ['new', 'extend', 'delete'].includes(id) ? `${id}_it` : id
}

/** Which section of the form an API field error belongs to. */
export function errorSection(field: string): 'item' | 'questions' | 'approvals' | 'deployment' | 'quotas' | 'steps' {
  if (field === 'questions') return 'questions'
  if (['approval_rules', 'freeze_exception_team', 'notification_template'].includes(field)) return 'approvals'
  if (['max_deployments', 'max_requests_per_day', 'over_quota'].includes(field)) return 'quotas'
  if (field === 'step_labels') return 'steps'
  if (['deployment_name', 'max_lifetime_days', 'default_lifetime_days', 'extension_days', 'max_extensions', 'teardown_template', 'connection', 'actions', 'expiry_waits_for_freeze'].includes(field)) return 'deployment'
  return 'item'
}

type Named = { id: number; name: string; type?: string }

/** An approval rule in the form. */
export interface RuleEdit {
  name: string
  condition: string
  approvers: ApprovalRule['approvers']
  team: Named | null
  users_from: string
  required: string
  timeout_hours: string
}

export function ruleEdit(r: ApprovalRule, team: Named | null = null): RuleEdit {
  return {
    name: r.name,
    condition: r.condition ?? '',
    approvers: r.approvers ?? 'team_admins',
    team,
    users_from: r.users_from ?? '',
    required: String(r.required ?? 1),
    timeout_hours: String(Math.round((r.timeout ?? DEFAULT_TIMEOUT_HOURS * 3600) / 3600)),
  }
}

export const newRule = (): RuleEdit => ruleEdit({ name: '', condition: '', approvers: 'team_admins' })

export function approvalRule(e: RuleEdit): ApprovalRule {
  const r: ApprovalRule = { name: e.name.trim(), condition: e.condition.trim(), approvers: e.approvers, required: Number(e.required), timeout: Math.round(Number(e.timeout_hours) * 3600) }
  if (e.approvers === 'team') r.team = e.team?.id ?? null
  if (e.approvers === 'users') r.users_from = e.users_from.trim()
  return r
}

/** An action in the form; auto: its id follows its name. */
export interface ActionEdit {
  id: string
  name: string
  description: string
  template: Named | null
  auto: boolean
}

export const actionEdit = (a: ItemAction, template: Named | null): ActionEdit => ({ id: a.id, name: a.name, description: a.description ?? '', template, auto: false })

export const newAction = (): ActionEdit => ({ id: '', name: '', description: '', template: null, auto: true })

export const itemAction = (e: ActionEdit): ItemAction => ({ id: e.id.trim(), name: e.name.trim(), description: e.description.trim(), template: e.template?.id ?? null })

/** The item's form. Numbers are kept as typed; empty is none. */
export type ItemForm = {
  name: string
  description: string
  template: Named | null
  kind: ItemKind
  icon: string
  category: string
  owner_team: Named | null
  questions: QuestionEdit[]
  rules: RuleEdit[]
  freeze_exception_team: Named | null
  notification_template: Named | null
  deployment_name: string
  /** forever: kept until its owners delete it; the lifetime fields don't apply. */
  lifetime: 'forever' | 'limited'
  default_lifetime_days: string
  max_lifetime_days: string
  extension_days: string
  max_extensions: string
  teardown_template: Named | null
  connection: ConnectionLayout[]
  actions: ActionEdit[]
  expiry_waits_for_freeze: boolean
  max_deployments: string
  max_requests_per_day: string
  over_quota: 'refuse' | 'approve'
  steps: { step: string; label: string }[]
}

/** A catalog item as the API returns it, as far as the form reads it. */
export interface CatalogItem {
  id: number
  name: string
  description: string
  unified_job_template: number
  kind: ItemKind
  icon: string
  category: string
  owner_team: number | null
  questions?: Record<string, QuestionOverride>
  approval_rules: ApprovalRule[]
  freeze_exception_team: number | null
  notification_template: number | null
  deployment_name: string
  default_lifetime_days: number | null
  max_lifetime_days: number | null
  extension_days: number[]
  max_extensions: number | null
  teardown_template: number | null
  connection: ConnectionLayout[]
  actions: ItemAction[]
  expiry_waits_for_freeze: boolean
  max_deployments: number | null
  max_requests_per_day: number | null
  over_quota: 'refuse' | 'approve'
  step_labels: Record<string, string>
}

const num = (n: number | null | undefined) => (n == null ? '' : String(n))

/** The form of a new item, or of an item, its references named by name(resource, id). */
export function itemForm(item: CatalogItem | null, name: (resource: 'teams' | 'templates' | 'notifications', id: number) => Named | null, spec: SurveyQuestion[] = []): ItemForm {
  const ref = (resource: 'teams' | 'templates' | 'notifications', id: number | null | undefined) => (id == null ? null : name(resource, id))
  return {
    name: item?.name ?? '',
    description: item?.description ?? '',
    template: ref('templates', item?.unified_job_template),
    kind: item?.kind ?? 'run_once',
    icon: item?.icon ?? '',
    category: item?.category ?? '',
    owner_team: ref('teams', item?.owner_team),
    questions: questionEdits(spec, item?.questions ?? {}),
    rules: (item?.approval_rules ?? []).map((r) => ruleEdit(r, ref('teams', r.team))),
    freeze_exception_team: ref('teams', item?.freeze_exception_team),
    notification_template: ref('notifications', item?.notification_template),
    deployment_name: item?.deployment_name ?? '',
    lifetime: item?.default_lifetime_days == null && item?.max_lifetime_days == null ? 'forever' : 'limited',
    default_lifetime_days: num(item?.default_lifetime_days),
    max_lifetime_days: num(item?.max_lifetime_days),
    extension_days: (item?.extension_days ?? []).join(', '),
    max_extensions: num(item?.max_extensions),
    teardown_template: ref('templates', item?.teardown_template),
    connection: (item?.connection ?? []).map((c) => ({ label: c.label, value: c.value, copy: c.copy !== false })),
    actions: (item?.actions ?? []).map((a) => actionEdit(a, ref('templates', a.template))),
    expiry_waits_for_freeze: !!item?.expiry_waits_for_freeze,
    max_deployments: num(item?.max_deployments),
    max_requests_per_day: num(item?.max_requests_per_day),
    over_quota: item?.over_quota ?? 'refuse',
    steps: stepRows(item?.step_labels),
  }
}

/** The ids an item's form names: its teams, templates and notification template. */
export function itemRefs(item: CatalogItem): { teams: number[]; templates: number[]; notifications: number[] } {
  const ids = (xs: (number | null | undefined)[]) => [...new Set(xs.filter((x): x is number => x != null))]
  return {
    teams: ids([item.owner_team, item.freeze_exception_team, ...item.approval_rules.map((r) => r.team)]),
    templates: ids([item.unified_job_template, item.teardown_template, ...item.actions.map((a) => a.template)]),
    notifications: ids([item.notification_template]),
  }
}

/** The API's body for the form. Questions are left out while the survey is
 * unknown, so that saving keeps the item's. A run-once item clears what only
 * a deployment has. */
export function itemBody(v: ItemForm, spec: SurveyQuestion[] | null): Record<string, unknown> {
  const dep = v.kind === 'deployment'
  const expires = dep && v.lifetime === 'limited'
  const body: Record<string, unknown> = {
    name: v.name.trim(),
    description: v.description.trim(),
    unified_job_template: v.template?.id ?? null,
    kind: v.kind,
    icon: v.icon.trim(),
    category: v.category.trim(),
    owner_team: v.owner_team?.id ?? null,
    approval_rules: v.rules.map(approvalRule),
    freeze_exception_team: v.freeze_exception_team?.id ?? null,
    notification_template: v.notification_template?.id ?? null,
    max_requests_per_day: optionalInt(v.max_requests_per_day),
    over_quota: v.over_quota,
    step_labels: stepLabels(v.steps),
    deployment_name: dep ? v.deployment_name.trim() : '',
    default_lifetime_days: expires ? optionalInt(v.default_lifetime_days) : null,
    max_lifetime_days: expires ? optionalInt(v.max_lifetime_days) : null,
    extension_days: expires ? parseDays(v.extension_days).days : [],
    max_extensions: expires ? optionalInt(v.max_extensions) : null,
    teardown_template: dep ? (v.teardown_template?.id ?? null) : null,
    connection: dep ? v.connection.filter((c) => c.label.trim() || c.value.trim()).map((c) => ({ label: c.label.trim(), value: c.value.trim(), copy: c.copy !== false })) : [],
    actions: dep ? v.actions.map(itemAction) : [],
    expiry_waits_for_freeze: expires && v.expiry_waits_for_freeze,
    max_deployments: dep ? optionalInt(v.max_deployments) : null,
  }
  if (spec) body.questions = questionOverrides(spec, v.questions)
  return body
}

/** Why a deployment that expires has no lifetime yet: it needs a default or a longest. */
export function lifetimeError(v: Pick<ItemForm, 'kind' | 'lifetime' | 'default_lifetime_days' | 'max_lifetime_days'>): string | null {
  if (v.kind !== 'deployment' || v.lifetime !== 'limited') return null
  if (optionalInt(v.default_lifetime_days) == null && optionalInt(v.max_lifetime_days) == null) return tr('Give a default or a longest lifetime.')
  return null
}

/** The survey path of a template, by its type. */
export function surveyPath(t: Named): string {
  return `${t.type === 'workflow_job_template' ? 'workflow_job_templates' : 'job_templates'}/${t.id}/`
}

export type StatusCell = 'ok' | 'failed' | 'running' | 'pending' | 'canceled' | 'none'

/** A request's status as a cell and in words. */
export function requestStatus(status: string): { cell: StatusCell; label: string } {
  switch (status) {
    case 'awaiting_approval':
      return { cell: 'pending', label: tr('Awaiting approval') }
    case 'pending':
      return { cell: 'pending', label: tr('Pending') }
    case 'running':
      return { cell: 'running', label: tr('Running') }
    case 'successful':
      return { cell: 'ok', label: tr('Successful') }
    case 'denied':
      return { cell: 'failed', label: tr('Denied') }
    case 'canceled':
      return { cell: 'canceled', label: tr('Canceled') }
    default:
      return { cell: 'failed', label: tr('Failed') }
  }
}

/** A deployment's status as a cell and in words. */
export function deploymentStatus(status: string): { cell: StatusCell; label: string } {
  switch (status) {
    case 'active':
      return { cell: 'ok', label: tr('Active') }
    case 'deleting':
      return { cell: 'running', label: tr('Being deleted') }
    case 'delete_failed':
      return { cell: 'failed', label: tr('Couldn’t be deleted') }
    default:
      return { cell: 'none', label: tr('Removed') }
  }
}

/** A request's kind in words: new, extend, delete, or the action's name. */
export function requestKind(kind: string, actions: { id: string; name: string }[] = []): string {
  if (kind === 'new') return tr('New')
  if (kind === 'extend') return tr('Extension')
  if (kind === 'delete') return tr('Deletion')
  return actions.find((a) => a.id === kind)?.name ?? kind
}
