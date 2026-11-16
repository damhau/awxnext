// Event connectors (roadmap M14-06): an ansible.eda source plugin run where
// jobs run, posting its events to a stream. The page's labels, its source
// presets and the parsing of its arguments and filters.
import { parse as parseYaml, stringify as stringifyYaml } from 'yaml'
import type { CellState } from './cells'
import { asIs, tr, trn } from './i18n'

export type ConnectorStats = {
  state?: string
  sent?: number
  buffered?: number
  dropped?: number
  rejected?: number
  retries?: number
  unreachable?: boolean
  last_error?: string
}

const STATUSES: Record<string, [string, CellState]> = {
  pending: [tr('Pending'), 'pending'],
  starting: [tr('Starting'), 'pending'],
  running: [tr('Running'), 'running'],
  restarting: [tr('Restarting'), 'changed'],
  stopped: [tr('Stopped'), 'empty'],
  completed: [tr('Completed'), 'ok'],
  failed: [tr('Failed'), 'failed'],
}

export const connectorStatus = (s: string): string => STATUSES[s]?.[0] ?? s
export const connectorCell = (s: string): CellState => STATUSES[s]?.[1] ?? 'empty'

/** A run's status: a connector's, or lost when its node was. */
export function runStatus(s: string): [string, CellState] {
  if (s === 'lost') return [tr('Node lost'), 'unreachable']
  return [connectorStatus(s), connectorCell(s)]
}

export const RESTART_POLICIES: [string, string][] = [
  ['on-failure', tr('On failure')],
  ['always', tr('Always')],
  ['never', tr('Never')],
]

/** "Restarts on failure after 5 s, the delay doubling, at most 3 times in a row." */
export function restartText(policy: string, delay: number, max: number): string {
  if (policy === 'never') return tr('Never restarts: it stays completed or failed.')
  const when = policy === 'always' ? tr('Restarts whenever it stops') : tr('Restarts when it fails')
  const limit = max > 0 ? trn(max, 'at most {n} time in a row', 'at most {n} times in a row') : tr('with no limit')
  return tr('{when}, after {delay} s, the delay doubling each time, {limit}.', { when, delay, limit })
}

/** "120 sent · 30 buffered · awxnext unreachable" */
export function statsText(s: ConnectorStats | null | undefined): string {
  if (!s || s.sent === undefined) return ''
  const parts = [trn(s.sent, '{n} sent', '{n} sent')]
  if (s.buffered) parts.push(trn(s.buffered, '{n} buffered', '{n} buffered'))
  if (s.dropped) parts.push(trn(s.dropped, '{n} dropped', '{n} dropped'))
  if (s.rejected) parts.push(trn(s.rejected, '{n} rejected', '{n} rejected'))
  if (s.unreachable) parts.push(tr('awxnext unreachable'))
  return parts.join(' · ')
}

const pluginName = /^[A-Za-z0-9_]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$/

/** Why a plugin's name is wrong, or null. */
export function sourceError(name: string): string | null {
  if (!name.trim()) return null
  return pluginName.test(name.trim()) ? null : tr('Give the plugin’s full name: collection namespace, collection and plugin, such as ansible.eda.kafka.')
}

/** Starting arguments for the usual ansible.eda sources. */
export const SOURCE_PRESETS: { source: string; label: string; args: Record<string, unknown> }[] = [
  { source: 'ansible.eda.kafka', label: asIs('Kafka'), args: { host: 'kafka.example.com', port: 9092, topic: 'events', group_id: 'awxnext', offset: 'latest' } },
  { source: 'ansible.eda.aws_sqs_queue', label: asIs('AWS SQS'), args: { name: 'events', region: 'eu-west-1' } },
  { source: 'ansible.eda.azure_service_bus', label: asIs('Azure Service Bus'), args: { conn_str: '', queue_name: 'events' } },
  { source: 'ansible.eda.pg_listener', label: asIs('PostgreSQL LISTEN'), args: { dsn: '', channels: ['events'] } },
  { source: 'ansible.eda.url_check', label: tr('URL check'), args: { urls: ['https://example.com/health'], delay: 60 } },
  { source: 'ansible.eda.journald', label: asIs('journald'), args: { match: 'ALL' } },
  { source: 'ansible.eda.range', label: tr('Range (a test)'), args: { limit: 5 } },
  { source: 'ansible.eda.generic', label: tr('Generic (a test)'), args: { payload: [{ message: 'hello' }], loop_count: 1 } },
]

export const presetArgs = (source: string) => SOURCE_PRESETS.find((p) => p.source === source)?.args

export type Filter = Record<string, Record<string, unknown> | null>

/** The source's arguments from YAML or JSON: a mapping. */
export function parseArgs(text: string): { args?: Record<string, unknown>; error?: string } {
  if (!text.trim()) return { args: {} }
  let v: unknown
  try {
    v = parseYaml(text)
  } catch (e) {
    return { error: (e as Error).message.split('\n')[0] }
  }
  if (v === null) return { args: {} }
  if (typeof v !== 'object' || Array.isArray(v)) return { error: tr('The arguments are a mapping (name: value).') }
  return { args: v as Record<string, unknown> }
}

/** Filters from YAML or JSON, as a rulebook writes them: a list of
 * mappings of one plugin's full name to its arguments. */
export function parseFilters(text: string): { filters?: Filter[]; error?: string } {
  if (!text.trim()) return { filters: [] }
  let v: unknown
  try {
    v = parseYaml(text)
  } catch (e) {
    return { error: (e as Error).message.split('\n')[0] }
  }
  if (v === null) return { filters: [] }
  if (!Array.isArray(v)) return { error: tr('The filters are a list, such as - ansible.eda.json_filter: {exclude_keys: [password]}.') }
  for (const [i, f] of v.entries()) {
    const keys = f && typeof f === 'object' && !Array.isArray(f) ? Object.keys(f) : []
    if (keys.length !== 1) return { error: tr('Filter {n} names one plugin, with its arguments.', { n: i + 1 }) }
    if (!pluginName.test(keys[0])) return { error: tr('Filter {n}: give the plugin’s full name, such as ansible.eda.json_filter.', { n: i + 1 }) }
    const args = (f as Filter)[keys[0]]
    if (args !== null && (typeof args !== 'object' || Array.isArray(args))) return { error: tr('Filter {n}: its arguments are a mapping.', { n: i + 1 }) }
  }
  return { filters: v as Filter[] }
}

/** YAML for an editor, empty for nothing. */
export function yamlText(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (Array.isArray(v) ? !v.length : typeof v === 'object' && !Object.keys(v as object).length) return ''
  return stringifyYaml(v)
}

/** "ansible.eda.kafka" → "kafka", for a list's second line. */
export const pluginShort = (source: string) => source.split('.').pop() ?? source
