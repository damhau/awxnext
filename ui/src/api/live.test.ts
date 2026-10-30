import { describe, expect, it } from 'vitest'
import { matches } from './live'

describe('live message routing', () => {
  it('routes groups subscribed by event to every listener of the group', () => {
    expect(matches({ jobs: ['summary'] }, { group_name: 'jobs', unified_job_id: 7, final_counter: 12 })).toBe(true)
    expect(matches({ schedules: ['changed'] }, { group_name: 'schedules', id: 3 })).toBe(true)
    expect(matches({ control: ['limit_reached_4'] }, { group_name: 'control', reason: 'limit_reached' })).toBe(true)
    expect(matches({ jobs: ['status_changed'] }, { group_name: 'schedules', id: 3 })).toBe(false)
  })
  it('routes the others by object id', () => {
    expect(matches({ job_events: [7] }, { group_name: 'job_events', job: 7 })).toBe(true)
    expect(matches({ job_events: [7] }, { group_name: 'job_events', job: 8 })).toBe(false)
    expect(matches({ workflow_events: [9] }, { group_name: 'workflow_events', workflow_job_id: 9 })).toBe(true)
  })
})
