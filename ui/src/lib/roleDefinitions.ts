// Role definitions (roadmap M7-17): permissions as django-ansible-base names
// them ("awx.execute_jobtemplate"), grouped by the kind of object they are
// about, with the rule the API applies: a role that grants anything on a kind
// of object (adding excepted) also lets its holders view it.
import { tr } from './i18n'

export interface Perm {
  perm: string
  action: string
  model: string
}

export function parsePerm(perm: string): Perm {
  const code = perm.includes('.') ? perm.slice(perm.indexOf('.') + 1) : perm
  const i = code.indexOf('_')
  return { perm, action: i < 0 ? code : code.slice(0, i), model: i < 0 ? '' : code.slice(i + 1) }
}

/** A content type or model, for people: "awx.jobtemplate" -> "Job template". */
export function modelLabel(model: string, many = false): string {
  const m = model.includes('.') ? model.slice(model.indexOf('.') + 1) : model
  const names: Record<string, [string, string]> = {
    organization: [tr('Organization'), tr('Organizations')],
    team: [tr('Team'), tr('Teams')],
    folder: [tr('Folder'), tr('Folders')],
    jobtemplate: [tr('Job template'), tr('Job templates')],
    workflowjobtemplate: [tr('Workflow template'), tr('Workflow templates')],
    inventory: [tr('Inventory'), tr('Inventories')],
    project: [tr('Project'), tr('Projects')],
    credential: [tr('Credential'), tr('Credentials')],
    executionenvironment: [tr('Execution environment'), tr('Execution environments')],
    notificationtemplate: [tr('Notification template'), tr('Notification templates')],
    instancegroup: [tr('Instance group'), tr('Instance groups')],
  }
  const n = names[m]
  return n ? n[many ? 1 : 0] : m
}

/** What a permission lets its holder do. */
export function actionLabel(p: Perm): string {
  const labels: Record<string, string> = {
    view: tr('View'),
    change: tr('Change'),
    delete: tr('Delete'),
    add: tr('Create'),
    use: tr('Use'),
    execute: tr('Launch'),
    update: tr('Sync'),
    adhoc: tr('Run commands'),
    approve: tr('Approve'),
    member: tr('Be a member'),
    audit: tr('Audit'),
  }
  return labels[p.action] ?? p.action
}

const actionOrder = ['view', 'use', 'execute', 'update', 'adhoc', 'approve', 'member', 'change', 'add', 'delete']

/** The allowed permissions by kind of object: the role's own type first. */
export function groupPerms(allowed: string[], contentType: string): { model: string; perms: Perm[] }[] {
  const own = parsePerm('x.view_' + (contentType.split('.').pop() ?? '')).model
  const by = new Map<string, Perm[]>()
  for (const p of allowed.map(parsePerm)) by.set(p.model, [...(by.get(p.model) ?? []), p])
  const rank = (a: string) => actionOrder.indexOf(a) < 0 ? 99 : actionOrder.indexOf(a)
  return [...by.entries()]
    .map(([model, perms]) => ({ model, perms: perms.sort((a, b) => rank(a.action) - rank(b.action)) }))
    .sort((a, b) => (a.model === own ? -1 : b.model === own ? 1 : modelLabel(a.model).localeCompare(modelLabel(b.model))))
}

const viewOf = (p: Perm) => p.perm.replace(/\.[a-z]+_/, '.view_')

/** Turns a permission on or off, keeping the role valid: granting something
 * on a kind of object turns its view on; turning view off turns off the rest
 * of that kind (adding excepted). */
export function togglePerm(selected: string[], perm: string): string[] {
  const p = parsePerm(perm)
  const on = new Set(selected)
  if (on.has(perm)) {
    on.delete(perm)
    if (p.action === 'view') for (const s of selected.map(parsePerm)) if (s.model === p.model && s.action !== 'add') on.delete(s.perm)
  } else {
    on.add(perm)
    if (p.action !== 'add' && p.action !== 'view') on.add(viewOf(p))
  }
  return [...on]
}

/** The view permissions a set lacks for what it grants. */
export function missingViews(selected: string[]): string[] {
  const on = new Set(selected)
  const out = new Set<string>()
  for (const p of selected.map(parsePerm)) if (p.action !== 'add' && p.action !== 'view' && !on.has(viewOf(p))) out.add(viewOf(p))
  return [...out]
}

/** "Launch, View" for a short summary, in the order of the picker. */
export function summary(perms: string[], contentType: string | null): string {
  const groups = groupPerms(perms, contentType ?? '')
  return groups
    .map((g) => {
      const actions = g.perms.map(actionLabel).join(', ')
      return groups.length === 1 && contentType?.endsWith(g.model) ? actions : `${modelLabel(g.model, true)}: ${actions}`
    })
    .join(' · ')
}
