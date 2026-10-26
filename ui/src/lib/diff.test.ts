import { describe, expect, it } from 'vitest'
import { diffLines } from './diff'

describe('diffLines', () => {
  it('marks changed lines', () => {
    const d = diffLines('a\nserial: 4\nc\n', 'a\nserial: 8\nc\nd\n')
    expect(d.map((l) => l.kind + ' ' + l.text)).toEqual(['ctx a', 'del serial: 4', 'add serial: 8', 'ctx c', 'add d'])
    expect(d[3]).toMatchObject({ a: 3, b: 3 })
  })
  it('handles empty sides', () => {
    expect(diffLines('', 'x').map((l) => l.kind)).toEqual(['add'])
    expect(diffLines('x', '').map((l) => l.kind)).toEqual(['del'])
  })
})
