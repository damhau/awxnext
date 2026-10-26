import { describe, expect, it } from 'vitest'
import { compile, setTerm, statusValue, tokenize } from './query'

const keys = {
  status: { field: 'status', map: statusValue },
  inventory: { field: 'inventory__name' },
  label: { field: 'labels__name' },
}

describe('query language', () => {
  it('tokenizes keys, negation and quotes', () => {
    expect(tokenize('deploy -label:legacy inventory:"prod eu"').map((t) => [t.key, t.value, t.negate])).toEqual([
      [null, 'deploy', false],
      ['label', 'legacy', true],
      ['inventory', 'prod eu', false],
    ])
  })
  it('compiles to API params', () => {
    const c = compile('checkout status:failed inventory:prod-* -label:legacy owner:x', keys)
    expect(c.params).toEqual({ search: 'checkout', status__in: 'failed,error', inventory__name__istartswith: 'prod-', not__labels__name: 'legacy' })
    expect(c.unknown).toEqual(['owner'])
  })
  it('wildcards choose the lookup', () => {
    expect(compile('inventory:*eu*', keys).params).toEqual({ inventory__name__icontains: 'eu' })
    expect(compile('inventory:*-west', keys).params).toEqual({ inventory__name__iendswith: '-west' })
  })
  it('replaces terms', () => {
    expect(setTerm('a status:ok', 'status', 'failed')).toBe('a status:failed')
    expect(setTerm('a status:ok', 'status', null)).toBe('a')
  })
})
