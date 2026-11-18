import { parse as parseYaml } from 'yaml'
import { tr } from './i18n'
// Choice sources (docs/surveys.md, "Choice sources"): lists for survey
// questions from a JSON API or a job.

export interface ChoiceSource {
  id: number
  name: string
  kind: 'http' | 'job'
  url: string
  job_template: number | null
  refresh_minutes: number
  last_refresh: string | null
  last_error: string
  last_job: number | null
}

export interface TestResult {
  count: number
  choices: { value: string; label: string }[]
  sample: unknown[]
  url?: string
  error?: string
  reply?: string
}

/** Where a source's list comes from, in a few words. */
export function sourceOrigin(s: Pick<ChoiceSource, 'kind' | 'url'> & { summary_fields?: { job_template?: { name: string } } }): string {
  if (s.kind === 'job') return tr('Published by {name}', { name: s.summary_fields?.job_template?.name ?? tr('a job template') })
  try {
    return new URL(s.url).host
  } catch {
    return s.url
  }
}

/** A job source's state: 'failed' when its last refresh didn't publish a
 * list, 'empty' before its first, 'ok' otherwise; null for an API. */
export function refreshState(s: Pick<ChoiceSource, 'kind' | 'last_refresh' | 'last_error'>): 'ok' | 'failed' | 'empty' | null {
  if (s.kind !== 'job') return null
  if (s.last_error) return 'failed'
  return s.last_refresh ? 'ok' : 'empty'
}

/** The parameters written as YAML or JSON: a mapping of names to text. */
export function parseParameters(text: string): { value: Record<string, string>; error: string | null } {
  if (!text.trim()) return { value: {}, error: null }
  let v: unknown
  try {
    v = parseYaml(text)
  } catch (e) {
    return { value: {}, error: (e as Error).message.split('\n')[0] }
  }
  if (v === null) return { value: {}, error: null }
  if (typeof v !== 'object' || Array.isArray(v)) return { value: {}, error: tr('Parameters must be a mapping (name: value).') }
  const out: Record<string, string> = {}
  for (const [k, x] of Object.entries(v as Record<string, unknown>)) {
    if (typeof x === 'object' && x !== null) return { value: {}, error: tr('The value of {name} must be text.', { name: k }) }
    out[k] = x === null ? '' : String(x)
  }
  return { value: out, error: null }
}

/** The names of the answers a source's parameters and filter read
 * (answers.x), for the Test form. */
export function answerNames(parameters: Record<string, string>, filter: string): string[] {
  const names = new Set<string>()
  for (const src of [...Object.values(parameters), filter]) for (const m of src.matchAll(/answers\.([A-Za-z_][A-Za-z0-9_]*)/g)) names.add(m[1])
  return [...names].sort()
}
