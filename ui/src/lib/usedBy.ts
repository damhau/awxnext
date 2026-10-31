import { tr } from './i18n'
// What uses an object (awxnext's used_by): for its "Used by" panel and its
// delete confirmation, which says what deleting it does to each dependent.

export interface Dependent {
  type: string
  resource: string
  id: number | null
  name: string
  field: string
  /** Deleted with the object, or loses it (the reference is cleared). */
  effect: 'deleted' | 'cleared'
}

export interface UsedBy {
  count: number
  /** Dependents the user cannot see. */
  hidden: number
  results: Dependent[]
}

/** The resources with a used_by. */
export const HAS_USED_BY = new Set(['inventories', 'credentials', 'projects', 'execution_environments', 'job_templates', 'workflow_job_templates'])

const kinds: Record<string, [string, string]> = {
  job_template: [tr('job template'), tr('job templates')],
  workflow_job_template: [tr('workflow'), tr('workflows')],
  schedule: [tr('schedule'), tr('schedules')],
  inventory: [tr('inventory'), tr('inventories')],
  inventory_source: [tr('inventory source'), tr('inventory sources')],
  project: [tr('project'), tr('projects')],
  organization: [tr('organization'), tr('organizations')],
  execution_environment: [tr('execution environment'), tr('execution environments')],
  credential: [tr('credential'), tr('credentials')],
  instance_group: [tr('instance group'), tr('instance groups')],
  gitops_source: [tr('GitOps source'), tr('GitOps sources')],
  setting: [tr('setting'), tr('settings')],
}

export function kindOf(d: Dependent, n = 1): string {
  const k = kinds[d.type] ?? [d.type.replace(/_/g, ' '), d.type.replace(/_/g, ' ') + 's']
  return n === 1 ? k[0] : k[1]
}

const how: Record<string, string> = {
  inventory: tr('as its inventory'),
  node_inventory: tr('as a step’s inventory'),
  input_inventories: tr('as an input'),
  credentials: tr('as a credential'),
  credential: tr('as its credential'),
  webhook_credential: tr('as its webhook credential'),
  signature_validation_credential: tr('to check signatures'),
  node_credentials: tr('as a step’s credential'),
  input_sources: tr('to look up a field'),
  galaxy_credentials: tr('as a Galaxy credential'),
  source_project: tr('as its source'),
  unified_job_template: tr('runs it'),
  project: tr('as its project'),
  node_template: tr('runs it in a step'),
  execution_environment: tr('as its execution environment'),
  default_environment: tr('as its default execution environment'),
  node_execution_environment: tr('as a step’s execution environment'),
  DEFAULT_EXECUTION_ENVIRONMENT: tr('as the global default'),
}

/** "as its inventory", "runs it in a step" */
export function howUsed(d: Dependent): string {
  return how[d.field] ?? tr('as {field}', { field: d.field.replace(/_/g, ' ') })
}

/** Where a dependent is shown in the UI. */
export function linkTo(d: Dependent): string | null {
  const q = (p: string) => `${p}?q=${encodeURIComponent(d.name)}`
  switch (d.type) {
    case 'job_template':
      return `/templates/job/${d.id}`
    case 'workflow_job_template':
      return `/templates/workflow/${d.id}`
    case 'schedule':
      return q('/schedules')
    case 'inventory':
      return `/inventories/${d.id}`
    case 'inventory_source':
      return `/inventory-sources/${d.id}`
    case 'project':
      return `/projects/${d.id}`
    case 'organization':
      return `/organizations/${d.id}`
    case 'credential':
      return `/credentials/${d.id}`
    case 'execution_environment':
      return q('/execution-environments')
    case 'instance_group':
      return `/instance-groups/${d.id}`
    case 'gitops_source':
      return '/gitops'
    case 'setting':
      return '/settings'
  }
  return null
}

/** "2 job templates and 1 schedule" */
export function countKinds(ds: Dependent[]): string {
  const by = new Map<string, Dependent[]>()
  for (const d of ds) by.set(d.type, [...(by.get(d.type) ?? []), d])
  const parts = [...by.values()].map((g) => `${g.length} ${kindOf(g[0], g.length)}`)
  if (parts.length <= 1) return parts.join('')
  return `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}`
}

/** One entry per dependent, for a short list: a workflow using an inventory
 * as its own and as a step's is shown once. */
export function distinct(ds: Dependent[]): Dependent[] {
  const seen = new Set<string>()
  return ds.filter((d) => {
    const k = `${d.effect}/${d.type}/${d.id ?? d.name}`
    if (seen.has(k)) return false
    seen.add(k)
    return true
  })
}

/** What deleting the object does: what goes with it, and what loses it. */
export function deleteEffects(u: UsedBy): { deleted: Dependent[]; cleared: Dependent[]; hidden: number } {
  const all = distinct(u.results)
  return { deleted: all.filter((d) => d.effect === 'deleted'), cleared: all.filter((d) => d.effect === 'cleared'), hidden: u.hidden }
}

/** "2 job templates and 1 schedule", with those the user cannot see. */
export function summarize(u: UsedBy): string {
  const seen = countKinds(distinct(u.results))
  if (!u.hidden) return seen
  return seen ? `${seen}, and ${u.hidden} you cannot see` : `${u.hidden} you cannot see`
}

export function inUse(u: UsedBy | null): boolean {
  return !!u && (u.results.length > 0 || u.hidden > 0)
}
