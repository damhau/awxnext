import { tr } from '../../lib/i18n'
// Survey rules shared by the editor, the launch form and their tests. They
// mirror the server (internal/resources/survey.go), which has the last word.

/** Where a choice question's choices come from, instead of a fixed list. */
export interface ChoicesFrom {
  type: 'hosts' | 'groups' | 'git_refs' | 'resource' | 'source'
  inventory?: number | null
  source?: number
  project?: number | null
  refs?: 'tags' | 'branches' | 'all'
  resource?: string
  field?: string
  filter?: Record<string, unknown>
}

/** Show a question only when an earlier question's answer is one of values. */
export interface ShowIf {
  variable: string
  values: (string | number)[]
}

interface Conditional {
  variable: string
  default?: unknown
  show_if?: ShowIf
  value_from?: string
}

/** The answer types the server can compute (value_from). */
export const COMPUTABLE_TYPES = ['text', 'textarea', 'password', 'integer', 'float']

/** The questions someone answers: computed questions (value_from) are
 * never asked. */
export function askedQuestions<Q extends { value_from?: string }>(questions: Q[]): Q[] {
  return questions.filter((q) => !q.value_from)
}

const empty = (v: unknown) => v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)

/** The questions shown for these answers (or defaults), in order. A question
 * whose show_if names a hidden question is hidden too. Computed questions
 * are never shown. */
export function visibleQuestions<Q extends Conditional>(questions: Q[], values: Record<string, unknown>): Q[] {
  const answered: Record<string, unknown> = {}
  const out: Q[] = []
  for (const q of askedQuestions(questions)) {
    if (q.show_if) {
      const ans = answered[q.show_if.variable]
      if (empty(ans)) continue
      const given = Array.isArray(ans) ? ans : [ans]
      const want = q.show_if.values.map(String)
      if (!given.some((g) => want.includes(String(g)))) continue
    }
    out.push(q)
    const v = values[q.variable] ?? q.default
    if (!empty(v)) answered[q.variable] = v
  }
  return out
}

/** The error for a text answer that does not match the question's pattern
 * (which must match the whole answer), or null. */
export function patternError(q: { pattern?: string; pattern_hint?: string }, v: string): string | null {
  if (!q.pattern || v === '' || v === '$encrypted$') return null
  let re: RegExp
  try {
    re = new RegExp(`^(?:${q.pattern})$`, 'u')
  } catch {
    return null
  }
  return re.test(v) ? null : q.pattern_hint || tr('Does not match the expected format.')
}

/** Says where a question's choices come from, for the survey editor. */
export function describeSource(src: ChoicesFrom): string {
  const inv = src.inventory ? `inventory #${src.inventory}` : 'the launch inventory'
  const project = src.project ? `project #${src.project}` : "the template's project"
  switch (src.type) {
    case 'hosts':
      return `hosts of ${inv}`
    case 'groups':
      return `groups of ${inv}`
    case 'git_refs':
      return `${src.refs === 'branches' ? 'branches' : src.refs === 'all' ? 'tags and branches' : 'tags'} of ${project}`
    case 'resource':
      return `${src.field || 'name'} of ${src.resource}`
    case 'source':
      return `choice source #${src.source}`
  }
}

/** The answers a question's dynamic list may use: those to the questions
 * shown before it, without passwords, as the server reads them. */
export function earlierAnswers<Q extends Conditional & { type: string }>(questions: Q[], variable: string, values: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const q of visibleQuestions(questions, values)) {
    if (q.variable === variable) break
    const v = values[q.variable] ?? q.default
    if (q.type !== 'password' && !empty(v)) out[q.variable] = v
  }
  return out
}
