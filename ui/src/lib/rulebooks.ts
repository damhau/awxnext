// Rulebooks of ansible-rulebook imported into streams, connectors and rules
// (roadmap M14-09): their report, as the rulebook's page shows it.
import { parse as parseYaml, stringify as stringifyYaml } from 'yaml'
import type { CellState } from './cells'
import { tr, trn } from './i18n'

export interface ReportObject {
  type: string
  id: number
  name: string
}

export interface ActionReport {
  action: string
  status: string
  as?: string
  reason?: string
  object?: ReportObject
}

export interface RuleReport {
  name: string
  status: string
  actions: ActionReport[]
  reasons?: string[]
  notes?: string[]
}

export interface SourceReport {
  name: string
  plugin: string
  status: string
  as?: string
  reasons?: string[]
  notes?: string[]
  object?: ReportObject
}

export interface RulesetReport {
  name: string
  sources: SourceReport[]
  rules: RuleReport[]
  notes?: string[]
  streams?: ReportObject[]
}

export interface ReportSummary {
  rules: number
  translated: number
  partial: number
  not_translated: number
}

export interface RulebookReport {
  rulesets?: RulesetReport[]
  summary?: ReportSummary
}

/** The UI page of an object an import made. */
export function objectPath(o: ReportObject | undefined): string | null {
  if (!o) return null
  switch (o.type) {
    case 'event_stream':
      return `/event-streams/${o.id}`
    case 'event_connector':
      return `/event-connectors/${o.id}`
    case 'event_rule':
      return `/event-rules/${o.id}`
  }
  return null
}

/** An imported object's name without the project and rulebook it starts
 * with ("ops/web.yml: Web / Deploy" is "Web / Deploy"). */
export function shortName(name: string): string {
  const i = name.indexOf(': ')
  return i < 0 ? name : name.slice(i + 2)
}

/** How a translation status shows. */
export function statusView(status: string): { cell: CellState; label: string } {
  switch (status) {
    case 'translated':
      return { cell: 'ok', label: tr('Translated') }
    case 'partial':
      return { cell: 'changed', label: tr('Partly translated') }
    case 'not_translated':
      return { cell: 'failed', label: tr('Not translated') }
  }
  return { cell: 'empty', label: status }
}

/** One line on a report's rules. */
export function summaryText(s: ReportSummary | undefined): string {
  if (!s || !s.rules) return tr('No rules')
  const parts = [trn(s.rules, '{translated} of {n} rule translated', '{translated} of {n} rules translated', { translated: s.translated })]
  if (s.partial) parts.push(trn(s.partial, '{n} partly', '{n} partly'))
  if (s.not_translated) parts.push(trn(s.not_translated, '{n} not', '{n} not'))
  return parts.join(' · ')
}

/** Why a rule, or some of its actions, didn't translate: the rule's own
 * reasons, then each untranslated action's, named by its action. */
export function ruleReasons(r: RuleReport): string[] {
  const out = [...(r.reasons ?? [])]
  for (const a of r.actions) if (a.status !== 'translated' && a.reason) out.push(tr('{action}: {reason}', { action: a.action, reason: a.reason }))
  return out
}

/** The objects a report links to, once each, in report order. */
export function reportObjects(rep: RulebookReport | undefined): ReportObject[] {
  const seen = new Set<string>()
  const out: ReportObject[] = []
  const add = (o?: ReportObject) => {
    if (!o || seen.has(`${o.type}/${o.id}`)) return
    seen.add(`${o.type}/${o.id}`)
    out.push(o)
  }
  for (const rs of rep?.rulesets ?? []) {
    for (const o of rs.streams ?? []) add(o)
    for (const s of rs.sources) add(s.object)
    for (const r of rs.rules) for (const a of r.actions) add(a.object)
  }
  return out
}

/** What the rulebook's import did not make act yet: streams without a
 * credential, disabled connectors and rules are what an operator turns on. */
export function nextSteps(objects: { type: string; enabled?: boolean; credential?: number | null }[]): string[] {
  const streams = objects.filter((o) => o.type === 'event_stream' && !o.credential).length
  const connectors = objects.filter((o) => o.type === 'event_connector' && !o.enabled).length
  const rules = objects.filter((o) => o.type === 'event_rule' && !o.enabled).length
  const out: string[] = []
  if (streams) out.push(trn(streams, 'Give {n} stream a credential: it refuses events until then.', 'Give {n} streams a credential: they refuse events until then.'))
  if (connectors) out.push(trn(connectors, 'Enable {n} connector, with its execution environment and credentials.', 'Enable {n} connectors, with their execution environments and credentials.'))
  if (rules) out.push(trn(rules, 'Enable {n} rule in shadow mode, and read its decisions before moving it to auto.', 'Enable {n} rules in shadow mode, and read their decisions before moving them to auto.'))
  return out
}

/** A rulebook's variables from YAML or JSON: a mapping, as --vars takes. */
export function parseVariables(text: string): { vars?: Record<string, unknown>; error?: string } {
  if (!text.trim()) return { vars: {} }
  let v: unknown
  try {
    v = parseYaml(text)
  } catch (e) {
    return { error: (e as Error).message.split('\n')[0] }
  }
  if (v === null) return { vars: {} }
  if (typeof v !== 'object' || Array.isArray(v)) return { error: tr('The variables are a mapping (name: value).') }
  return { vars: v as Record<string, unknown> }
}

/** Variables as YAML for the editor, empty for none. */
export function variablesText(v: Record<string, unknown> | null | undefined): string {
  return v && Object.keys(v).length ? stringifyYaml(v) : ''
}
