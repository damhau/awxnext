import { describe, expect, it } from 'vitest'
import { describeSource, patternError, visibleQuestions } from './surveyLogic'

const qs = [
  { variable: 'env', default: 'dev' },
  { variable: 'ticket', show_if: { variable: 'env', values: ['prod'] } },
  { variable: 'window', show_if: { variable: 'ticket', values: ['CHG-1'] } },
  { variable: 'regions' },
  { variable: 'eu_only', show_if: { variable: 'regions', values: ['eu'] } },
]
const vars = (v: Record<string, unknown>) => visibleQuestions(qs, v).map((q) => q.variable)

describe('survey visibility', () => {
  it('shows a question when the earlier answer matches', () => {
    expect(vars({})).toEqual(['env', 'regions'])
    expect(vars({ env: 'prod' })).toEqual(['env', 'ticket', 'regions'])
    expect(vars({ env: 'prod', ticket: 'CHG-1' })).toEqual(['env', 'ticket', 'window', 'regions'])
  })
  it('hides questions that depend on hidden ones', () => {
    expect(vars({ env: 'dev', ticket: 'CHG-1' })).toEqual(['env', 'regions'])
  })
  it('matches any answer of a multiselect', () => {
    expect(vars({ regions: ['us', 'eu'] })).toContain('eu_only')
    expect(vars({ regions: [] })).not.toContain('eu_only')
  })
})

describe('survey patterns', () => {
  const q = { pattern: 'CHG-[0-9]+', pattern_hint: 'Use CHG-123.' }
  it('matches the whole answer', () => {
    expect(patternError(q, 'CHG-42')).toBeNull()
    expect(patternError(q, 'x CHG-42')).toBe('Use CHG-123.')
    expect(patternError({ pattern: '[a-z]+' }, 'ABC')).toBe('Does not match the expected format.')
  })
  it('ignores empty answers, kept passwords and invalid patterns', () => {
    expect(patternError(q, '')).toBeNull()
    expect(patternError(q, '$encrypted$')).toBeNull()
    expect(patternError({ pattern: '(' }, 'x')).toBeNull()
  })
})

describe('choice sources', () => {
  it('describes where choices come from', () => {
    expect(describeSource({ type: 'hosts' })).toBe('hosts of the launch inventory')
    expect(describeSource({ type: 'git_refs', refs: 'branches', project: 4 })).toBe('branches of project #4')
    expect(describeSource({ type: 'resource', resource: 'inventories' })).toBe('name of inventories')
  })
})
