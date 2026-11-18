import type { Any } from '../api/types'

export type Decision = 'approve' | 'deny'

/** The catalog request an approval holds; undefined for a workflow's or an event rule's. */
export function approvalRequest(a: Any): { id: number; item: string; requester?: string } | undefined {
  const r = a?.summary_fields?.catalog_request
  if (!r) return undefined
  return { id: r.id, item: r.item_name ?? '', requester: r.requester_username ?? undefined }
}

/** A deny of a catalog request needs a comment, which its requester reads. */
export function needsComment(approvals: Any[], action: Decision): boolean {
  return action === 'deny' && approvals.some((a) => approvalRequest(a) !== undefined)
}

/** The body of an approve or deny call: the comment, when there is one. */
export function decisionBody(comment: string): Record<string, string> {
  const c = comment.trim()
  return c ? { comment: c } : {}
}

/** How far a request's approval is: the approvals it has and needs, and whether the organization's admins stand in. */
export function approvalProgress(a: Any): { approved: number; required: number; fallback: boolean; rule: string } | undefined {
  const g = a?.summary_fields?.request_approval
  if (!g) return undefined
  return { approved: g.approved ?? 0, required: g.required ?? 1, fallback: !!g.fallback, rule: g.rule ?? '' }
}
