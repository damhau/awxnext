// Layered layout for workflow graphs: a column per depth (longest path from
// the start), rows ordered to reduce crossings (barycenter sweeps), edges as
// horizontal cubic curves. Small graphs only (tens of nodes), so simple wins.

export type EdgeKind = 'success' | 'failure' | 'always'

export interface GraphNode {
  id: number
  edges: { to: number; kind: EdgeKind }[]
}

export interface Box {
  x: number
  y: number
  w: number
  h: number
}

export interface Layout {
  boxes: Map<number, Box>
  start: Box
  width: number
  height: number
  depth: Map<number, number>
  columns: number[][]
  parents: Map<number, number[]>
  /** y of the lane for edges that skip columns, keyed "from-to". */
  lanes: Map<string, number>
}

export interface LayoutOpts {
  nodeW?: number
  nodeH?: number
  gapX?: number
  gapY?: number
  pad?: number
}

export function layout(nodes: GraphNode[], opts: LayoutOpts = {}): Layout {
  const W = opts.nodeW ?? 184
  const H = opts.nodeH ?? 64
  const GX = opts.gapX ?? 80
  const GY = opts.gapY ?? 22
  const PAD = opts.pad ?? 24
  const SW = 64
  const SH = 32
  const ids = new Set(nodes.map((n) => n.id))
  const parents = new Map<number, number[]>()
  const children = new Map<number, number[]>()
  for (const n of nodes) {
    parents.set(n.id, parents.get(n.id) ?? [])
    children.set(n.id, [])
  }
  for (const n of nodes) {
    for (const e of n.edges) {
      if (!ids.has(e.to)) continue
      children.get(n.id)!.push(e.to)
      parents.get(e.to)!.push(n.id)
    }
  }
  // Longest-path depth via Kahn's algorithm; roots sit in column 1 (after start).
  const indeg = new Map<number, number>()
  for (const n of nodes) indeg.set(n.id, parents.get(n.id)!.length)
  const depth = new Map<number, number>()
  const queue = nodes.filter((n) => indeg.get(n.id) === 0).map((n) => n.id).sort((a, b) => a - b)
  for (const id of queue) depth.set(id, 1)
  const seen = new Set<number>()
  while (queue.length) {
    const id = queue.shift()!
    seen.add(id)
    for (const c of children.get(id)!) {
      depth.set(c, Math.max(depth.get(c) ?? 1, depth.get(id)! + 1))
      indeg.set(c, indeg.get(c)! - 1)
      if (indeg.get(c) === 0) queue.push(c)
    }
  }
  // Nodes on a cycle (the API prevents them) still get a place.
  for (const n of nodes) if (!seen.has(n.id)) depth.set(n.id, depth.get(n.id) ?? 1)

  const maxDepth = Math.max(0, ...depth.values())
  const columns: number[][] = Array.from({ length: maxDepth + 1 }, () => [])
  for (const n of [...nodes].sort((a, b) => a.id - b.id)) columns[depth.get(n.id)!].push(n.id)

  const index = new Map<number, number>()
  const reindex = () => columns.forEach((col) => col.forEach((id, i) => index.set(id, i)))
  reindex()
  const bary = (id: number, rel: Map<number, number[]>) => {
    const r = rel.get(id)!.filter((x) => index.has(x))
    return r.length ? r.reduce((s, x) => s + index.get(x)!, 0) / r.length : index.get(id)!
  }
  for (let iter = 0; iter < 4; iter++) {
    for (let d = 2; d <= maxDepth; d++) {
      columns[d].sort((a, b) => bary(a, parents) - bary(b, parents) || a - b)
      columns[d].forEach((id, i) => index.set(id, i))
    }
    for (let d = maxDepth - 1; d >= 1; d--) {
      columns[d].sort((a, b) => bary(a, children) - bary(b, children) || a - b)
      columns[d].forEach((id, i) => index.set(id, i))
    }
  }

  const tallest = Math.max(1, ...columns.map((c) => c.length))
  const fullH = tallest * H + (tallest - 1) * GY
  const boxes = new Map<number, Box>()
  for (let d = 1; d <= maxDepth; d++) {
    const col = columns[d]
    const colH = col.length * H + (col.length - 1) * GY
    const x = PAD + SW + GX + (d - 1) * (W + GX)
    col.forEach((id, i) => boxes.set(id, { x, y: PAD + (fullH - colH) / 2 + i * (H + GY), w: W, h: H }))
  }
  const start = { x: PAD, y: PAD + fullH / 2 - SH / 2, w: SW, h: SH }
  const width = PAD * 2 + SW + (maxDepth > 0 ? GX + maxDepth * W + (maxDepth - 1) * GX : 0)
  let height = PAD * 2 + Math.max(fullH, SH)
  const lay: Layout = { boxes, start, width, height, depth, columns, parents, lanes: new Map() }
  // Edges that skip columns would cross the nodes in between: route them
  // along a lane under those nodes.
  let lane = 0
  for (const n of nodes) {
    for (const e of n.edges) {
      const a = boxes.get(n.id)
      const b = boxes.get(e.to)
      if (!a || !b) continue
      const from = depth.get(n.id)!
      const to = depth.get(e.to)!
      if (to - from < 2) continue
      const lo = Math.min(a.y, b.y)
      const hi = Math.max(a.y + a.h, b.y + b.h)
      let bottom = -Infinity
      for (let d = from + 1; d < to; d++) {
        for (const id of columns[d]) {
          const c = boxes.get(id)!
          if (c.y < hi && c.y + c.h > lo) bottom = Math.max(bottom, c.y + c.h)
        }
      }
      if (bottom === -Infinity) continue
      const y = bottom + 14 + lane * 8
      lane++
      lay.lanes.set(`${n.id}-${e.to}`, y)
      height = Math.max(height, y + PAD)
    }
  }
  lay.height = height
  return lay
}

/** Cubic curve from the right edge of a to the left edge of b, optionally along a lane. */
export function edgePath(a: Box, b: Box, lane?: number): { d: string; mx: number; my: number } {
  if (lane !== undefined) {
    const x1 = a.x + a.w
    const y1 = a.y + a.h / 2
    const x2 = b.x
    const y2 = b.y + b.h / 2
    const xa = x1 + 28
    const xb = x2 - 28
    return {
      d: `M${x1},${y1} C${x1 + 18},${y1} ${xa - 10},${lane} ${xa + 10},${lane} L${xb - 10},${lane} C${xb + 10},${lane} ${x2 - 18},${y2} ${x2},${y2}`,
      mx: (xa + xb) / 2,
      my: lane,
    }
  }
  const x1 = a.x + a.w
  const y1 = a.y + a.h / 2
  const x2 = b.x
  const y2 = b.y + b.h / 2
  const dx = Math.max(30, (x2 - x1) / 2)
  return { d: `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`, mx: (x1 + x2) / 2, my: (y1 + y2) / 2 }
}
