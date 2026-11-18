import { describe, expect, it } from 'vitest'
import { approvalProgress, approvalRequest, decisionBody, needsComment } from './approvals'

const request = {
  id: 7,
  summary_fields: {
    catalog_request: { id: 3, item_name: 'Large VM', requester_username: 'alice' },
    request_approval: { rule: 'Big', approved: 1, required: 2, fallback: false },
  },
}
const workflow = { id: 8, summary_fields: { source_workflow_job: { id: 1, name: 'wf' } } }

describe('approvals', () => {
  it('finds the request an approval holds', () => {
    expect(approvalRequest(request)).toEqual({ id: 3, item: 'Large VM', requester: 'alice' })
    expect(approvalRequest(workflow)).toBeUndefined()
  })

  it('asks for a comment to deny a request only', () => {
    expect(needsComment([request], 'deny')).toBe(true)
    expect(needsComment([workflow, request], 'deny')).toBe(true)
    expect(needsComment([request], 'approve')).toBe(false)
    expect(needsComment([workflow], 'deny')).toBe(false)
  })

  it('sends the comment when there is one', () => {
    expect(decisionBody('  too big ')).toEqual({ comment: 'too big' })
    expect(decisionBody('   ')).toEqual({})
  })

  it('reads a request approval progress', () => {
    expect(approvalProgress(request)).toEqual({ approved: 1, required: 2, fallback: false, rule: 'Big' })
    expect(approvalProgress(workflow)).toBeUndefined()
  })
})
