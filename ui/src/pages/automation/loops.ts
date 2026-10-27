// Progress of workflow loop nodes, which run their template once per item.

export interface Iteration {
  index: number
  job: { id: number; type: string; name: string; status: string; failed: boolean; elapsed: number }
}

const FINISHED = new Set(['successful', 'failed', 'error', 'canceled'])

export interface LoopProgress {
  /** Combined status, like a job's: running until every item finished, then failed if any failed. */
  status: string
  done: number
  failed: number
  total: number
}

export function loopProgress(items: unknown[], iterations: Iteration[]): LoopProgress {
  const done = iterations.filter((i) => FINISHED.has(i.job.status)).length
  const failed = iterations.filter((i) => FINISHED.has(i.job.status) && i.job.status !== 'successful').length
  const total = items.length
  let status = 'successful'
  if (done < total) status = iterations.length ? 'running' : 'pending'
  else if (failed) status = 'failed'
  return { status, done, failed, total }
}

/** How a loop item reads in a list: itself when short and scalar, its position otherwise. */
export function itemLabel(item: unknown, index: number): string {
  if (['string', 'number', 'boolean'].includes(typeof item)) {
    const s = String(item)
    if (s.length <= 60) return s
  }
  return `#${index + 1}`
}
