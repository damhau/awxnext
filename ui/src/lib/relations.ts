import { api } from '../api/client'

/**
 * The detach and attach calls that turn one related list into another. An
 * ordered list (instance groups: attaching appends) is rebuilt when its
 * order changes; otherwise only the differences are sent.
 */
export function relationOps(had: number[], want: number[], ordered: boolean): { detach: number[]; attach: number[] } {
  const wanted = new Set(want)
  const present = new Set(had)
  const removed = had.filter((id) => !wanted.has(id))
  if (ordered) {
    const kept = had.filter((id) => wanted.has(id))
    if (kept.some((id, i) => want[i] !== id)) return { detach: had, attach: want }
    return { detach: removed, attach: want.slice(kept.length) }
  }
  return { detach: removed, attach: want.filter((id) => !present.has(id)) }
}

/** Applies relationOps to a sub-list such as schedules/5/labels/. */
export async function syncRelation(path: string, had: number[], want: number[], ordered = false): Promise<void> {
  const { detach, attach } = relationOps(had, want, ordered)
  for (const id of detach) await api.post(path, { id, disassociate: true })
  for (const id of attach) await api.post(path, { id })
}
