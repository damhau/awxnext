// AWX's capacity_adjustment: an instance's capacity goes from the smaller
// of its CPU and memory capacities (0) to the larger (1), as the server
// computes it (the adjustment has two decimals, the result is truncated).

export function adjustedCapacity(cpuCapacity: number, memCapacity: number, adjustment: number): number {
  const lo = Math.min(cpuCapacity, memCapacity)
  const hi = Math.max(cpuCapacity, memCapacity)
  return lo + Math.trunc(((hi - lo) * Math.round(adjustment * 100)) / 100)
}

const forks = (n: number) => `${n} ${n === 1 ? 'fork' : 'forks'}`

/** What the two ends of the adjustment mean for an instance. */
export function describeAdjustment(cpuCapacity: number, memCapacity: number): string {
  if (!cpuCapacity && !memCapacity) return 'Capacity is measured at the next health check.'
  if (cpuCapacity === memCapacity) return `CPU and memory both allow ${forks(cpuCapacity)}.`
  const [low, high] = cpuCapacity < memCapacity ? (['CPU', 'memory'] as const) : (['memory', 'CPU'] as const)
  return `0% sizes capacity by ${low} (${forks(Math.min(cpuCapacity, memCapacity))}), 100% by ${high} (${forks(Math.max(cpuCapacity, memCapacity))}).`
}
