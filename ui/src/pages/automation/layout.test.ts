import { describe, expect, it } from 'vitest'
import { layout } from './layout'

describe('workflow layout', () => {
  it('places nodes in columns by longest path', () => {
    // 1 -> 2 -> 4, 1 -> 3 -> 4, 4 -> 5; 6 is a second root.
    const l = layout([
      { id: 1, edges: [{ to: 2, kind: 'success' }, { to: 3, kind: 'failure' }] },
      { id: 2, edges: [{ to: 4, kind: 'success' }] },
      { id: 3, edges: [{ to: 4, kind: 'always' }] },
      { id: 4, edges: [{ to: 5, kind: 'success' }] },
      { id: 5, edges: [] },
      { id: 6, edges: [{ to: 5, kind: 'success' }] },
    ])
    expect(l.depth.get(1)).toBe(1)
    expect(l.depth.get(6)).toBe(1)
    expect(l.depth.get(4)).toBe(3)
    expect(l.depth.get(5)).toBe(4)
    const b1 = l.boxes.get(1)!
    const b4 = l.boxes.get(4)!
    expect(b4.x).toBeGreaterThan(b1.x)
    // Every node fits inside the canvas.
    for (const b of l.boxes.values()) {
      expect(b.x + b.w).toBeLessThanOrEqual(l.width)
      expect(b.y + b.h).toBeLessThanOrEqual(l.height)
    }
  })

  it('handles an empty graph', () => {
    const l = layout([])
    expect(l.boxes.size).toBe(0)
    expect(l.width).toBeGreaterThan(0)
  })

  it('reduces crossings by ordering children under their parents', () => {
    const l = layout([
      { id: 1, edges: [{ to: 4, kind: 'success' }] },
      { id: 2, edges: [{ to: 3, kind: 'success' }] },
      { id: 3, edges: [] },
      { id: 4, edges: [] },
    ])
    expect(l.columns[2]).toEqual([4, 3])
  })
})

describe('long edges', () => {
  it('route under the nodes they skip', () => {
    // 1 -> 2 -> 3 and 1 -> 3: the second edge skips column 2.
    const l = layout([
      { id: 1, edges: [{ to: 2, kind: 'success' }, { to: 3, kind: 'always' }] },
      { id: 2, edges: [{ to: 3, kind: 'success' }] },
      { id: 3, edges: [] },
    ])
    const lane = l.lanes.get('1-3')!
    const mid = l.boxes.get(2)!
    expect(lane).toBeGreaterThan(mid.y + mid.h)
    expect(l.height).toBeGreaterThan(lane)
    expect(l.lanes.has('1-2')).toBe(false)
  })
})
