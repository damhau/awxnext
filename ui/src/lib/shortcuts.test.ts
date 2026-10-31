import { describe, expect, it } from 'vitest'
import { JUMPS, keyCaps, SHORTCUTS } from './shortcuts'

describe('shortcuts', () => {
  it('shows keys for the platform', () => {
    expect(keyCaps('mod+k', true)).toEqual(['⌘', 'K'])
    expect(keyCaps('mod+k', false)).toEqual(['Ctrl', 'K'])
    expect(keyCaps('?', false)).toEqual(['?'])
    expect(keyCaps('escape', true)).toEqual(['Esc'])
    expect(keyCaps('arrowdown', false)).toEqual(['↓'])
  })
  it('lists every page jump', () => {
    const anywhere = SHORTCUTS.find((g) => g.group === 'Anywhere')!.items
    for (const k of Object.keys(JUMPS)) expect(anywhere.some((s) => s.keys.join(' ') === `g ${k}`), k).toBe(true)
  })
  it('gives no key two meanings in one place', () => {
    for (const g of SHORTCUTS) {
      const keys = g.items.map((s) => s.keys.join(' '))
      expect(new Set(keys).size, g.group).toBe(keys.length)
    }
  })
})
