// Launch policies (roadmap M12-07): what their pages compute without the
// API. The completion of conditions from the fields the server lists, how
// decisions are shown, and the examples the editor starts from.
import type { CellState } from './cells'
import { CEL_FUNCTIONS, celCompletions, nodeAt, type Completion, type SchemaNode } from './eventRules'
import { asIs, tr } from './i18n'

/** A field a condition reads, as GET launch_policies/fields/ lists it. */
export interface PolicyField {
  path: string
  type: string
  help: string
}

/** What a policy decided of one launch, as a launch check or a refusal gives it. */
export interface CheckDecision {
  id: number
  name: string
  mode: string
  outcome: string
  message: string
  error: string
}

/** A field's type as a completion node: "list of string" holds strings. */
function nodeOf(type: string): SchemaNode {
  if (type.startsWith('list of ')) return { type: 'list', items: nodeOf(type.slice(8)) }
  if (type === 'object') return { type: 'object', fields: {} }
  if (type === 'map') return { type: 'map', values: { type: 'dyn' } }
  return { type }
}

/** The fields as one tree under launch: launch.credentials[].kind is a field of the list's items. */
export function fieldsTree(fields: PolicyField[]): SchemaNode {
  const root: SchemaNode = { type: 'object', fields: {} }
  for (const f of fields) {
    const segs = f.path.split('.')
    if (segs[0] !== 'launch' || segs.length < 2) continue
    let node = root
    for (const [i, raw] of segs.slice(1).entries()) {
      const list = raw.endsWith('[]')
      const name = list ? raw.slice(0, -2) : raw
      node.fields ??= {}
      if (i === segs.length - 2) {
        node.fields[name] ??= nodeOf(f.type)
        break
      }
      node.fields[name] ??= list ? { type: 'list', items: { type: 'object', fields: {} } } : { type: 'object', fields: {} }
      node = list ? (node.fields[name].items ??= { type: 'object', fields: {} }) : node.fields[name]
    }
  }
  return root
}

const ROOTS: Completion[] = [
  { label: asIs('launch'), detail: 'object', insert: 'launch', from: 0 },
  { label: asIs('now'), detail: 'timestamp', insert: 'now', from: 0 },
  { label: asIs('window('), detail: 'function', insert: 'window(now, ', from: 0 },
  ...CEL_FUNCTIONS,
]

/** The completion of a condition: launch's fields, now, window() and CEL's functions. */
export function policyCompletions(tree: SchemaNode | null): (text: string, cursor: number) => Completion[] {
  return (text, cursor) =>
    celCompletions(text, cursor, ROOTS, (segs) => {
      if (!tree || segs[0] !== 'launch') return null
      return nodeAt(tree, segs.slice(1))
    })
}

/** The fields grouped by the object they belong to, launch's own first. */
export function fieldGroups(fields: PolicyField[]): { group: string; fields: PolicyField[] }[] {
  const groups = new Map<string, PolicyField[]>()
  for (const f of fields) {
    const segs = f.path.split('.')
    const g = segs.length > 2 ? segs.slice(0, 2).join('.').replace(/\[\]$/, '') : segs[0] === 'launch' ? 'launch' : ''
    if (!groups.has(g)) groups.set(g, [])
    groups.get(g)!.push(f)
  }
  return [...groups.entries()].map(([group, fs]) => ({ group, fields: fs }))
}

export const MODES: [string, string][] = [
  ['shadow', tr('Shadow')],
  ['enforce', tr('Enforce')],
]
export const modeLabel = (m: string) => MODES.find(([v]) => v === m)?.[1] ?? m

/** An outcome's words and cell. */
export function outcomeView(outcome: string): { label: string; state: CellState } {
  switch (outcome) {
    case 'allowed':
      return { label: tr('Allowed'), state: 'ok' }
    case 'refused':
      return { label: tr('Refused'), state: 'failed' }
    case 'would_refuse':
      return { label: tr('Would have refused'), state: 'changed' }
  }
  return { label: outcome, state: 'none' }
}

/** What a launch check means for the Launch button: the policies that
 * refuse the launch, and those in shadow mode that would. */
export function launchVerdict(decisions: CheckDecision[] | undefined): { refused: CheckDecision[]; shadow: CheckDecision[] } {
  const list = decisions ?? []
  return { refused: list.filter((d) => d.outcome === 'refused'), shadow: list.filter((d) => d.outcome === 'would_refuse') }
}

/** Why the policies refuse a launch, one line each: "name: message". */
export function refusalLines(decisions: CheckDecision[] | undefined): string[] {
  return launchVerdict(decisions).refused.map((d) => (d.message ? `${d.name}: ${d.message}` : d.name))
}

/** Where an attached object's page is. */
export function attachedPath(a: { type: string; id: number }): string {
  switch (a.type) {
    case 'organization':
      return `/organizations/${a.id}`
    case 'inventory':
      return `/inventories/${a.id}`
    case 'job_template':
      return `/templates/job/${a.id}`
    case 'workflow_job_template':
      return `/templates/workflow/${a.id}`
  }
  return ''
}

export const attachedLabel = (type: string) =>
  ({ organization: tr('Organization'), inventory: tr('Inventory'), job_template: tr('Job template'), workflow_job_template: tr('Workflow template') })[type] ?? type

export interface PolicyExample {
  name: string
  condition: string
  message: string
}

/** The examples of the operator guide, to start a policy from. */
export const POLICY_EXAMPLES: PolicyExample[] = [
  {
    name: tr('Production change window'),
    condition: asIs('launch.inventory.name == "prod"\n  && !window(now, "Mon-Thu 20:00-06:00", "Europe/Zurich")\n  && !("oncall" in launch.user.teams)'),
    message: tr('Production changes run Monday to Thursday, 20:00 to 06:00 Zurich time. Outside it, ask the on-call team.'),
  },
  {
    name: tr('A limit on large inventories'),
    condition: asIs('launch.inventory.hosts > 100 && launch.limit == ""'),
    message: tr('This inventory has more than 100 hosts: give a limit.'),
  },
  {
    name: tr('No branch override on production'),
    condition: asIs('launch.inventory.name == "prod" && launch.branch != ""'),
    message: tr('Production runs the branch of its template or project: don’t override it.'),
  },
  {
    name: tr('No high verbosity on production'),
    condition: asIs('launch.inventory.name == "prod" && launch.verbosity >= 3'),
    message: tr('Verbosity 3 and above can print secrets: not on production.'),
  },
  {
    name: tr('Images from the company registry'),
    condition: asIs('launch.image != "" && !launch.image.startsWith("registry.example.com/")'),
    message: tr('Use an execution environment from registry.example.com.'),
  },
]
