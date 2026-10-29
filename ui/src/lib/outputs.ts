/**
 * Typed outputs: what a job template's playbook publishes with set_stats
 * (docs/workflows.md). The template form edits them; the workflow editor
 * offers the outputs of earlier steps in conditions and loops.
 */

export const OUTPUT_TYPES = ['string', 'integer', 'float', 'boolean', 'list', 'object'] as const
export type OutputType = (typeof OUTPUT_TYPES)[number]

export interface OutputDecl {
  name: string
  type: OutputType
  required: boolean
}

/** The declarations to save: names trimmed, empty rows dropped. */
export function cleanOutputs(rows: OutputDecl[]): OutputDecl[] {
  return rows.map((r) => ({ ...r, name: r.name.trim() })).filter((r) => r.name !== '')
}

const IDENT = /^[A-Za-z_][A-Za-z0-9_]*$/

/** How an expression names a step's output: nodes.build.artifacts.tag, or nodes['smoke-test'].artifacts.tag. */
export function outputRef(identifier: string, output: string): string {
  const node = IDENT.test(identifier) ? `nodes.${identifier}` : `nodes['${identifier.replace(/\\/g, '\\\\').replace(/'/g, "\\'")}']`
  return `${node}.artifacts.${output}`
}

interface GraphNode {
  id: number
  identifier: string
  unified_job_template: number | null
  success_nodes: number[]
  failure_nodes: number[]
  always_nodes: number[]
}

/** The steps a link path leads from to this one, nearest first. */
export function ancestors(id: number, nodes: GraphNode[]): GraphNode[] {
  const parents = new Map<number, GraphNode[]>()
  for (const n of nodes) {
    for (const to of [...n.success_nodes, ...n.failure_nodes, ...n.always_nodes]) {
      parents.set(to, [...(parents.get(to) ?? []), n])
    }
  }
  const out: GraphNode[] = []
  const seen = new Set<number>([id])
  let level = [id]
  while (level.length) {
    const next: number[] = []
    for (const x of level) {
      for (const p of parents.get(x) ?? []) {
        if (seen.has(p.id)) continue
        seen.add(p.id)
        out.push(p)
        next.push(p.id)
      }
    }
    level = next
  }
  return out
}

export interface OutputSuggestion {
  ref: string
  step: string
  output: OutputDecl
}

/**
 * The outputs of the steps before a step, for its condition (all of them) or
 * its loop (lists only).
 */
export function outputSuggestions(id: number, nodes: GraphNode[], outputsOf: (template: number) => OutputDecl[] | undefined, listsOnly = false): OutputSuggestion[] {
  const out: OutputSuggestion[] = []
  for (const n of ancestors(id, nodes)) {
    if (!n.identifier || n.unified_job_template == null) continue
    for (const o of outputsOf(n.unified_job_template) ?? []) {
      if (listsOnly && o.type !== 'list') continue
      out.push({ ref: outputRef(n.identifier, o.name), step: n.identifier, output: o })
    }
  }
  return out
}

/** Adds a reference to a condition: alone when it is empty, after "and" otherwise. */
export function addToCondition(when: string, ref: string): string {
  const w = when.trim()
  return w ? `${w} and ${ref}` : ref
}
