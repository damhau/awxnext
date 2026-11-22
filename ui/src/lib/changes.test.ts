import { describe, expect, it } from 'vitest'
import { changeLines, changesPath, commitsMissing, commitsShown, comparedWith, inputValue, multiline, sections, shortDigest, summarize, type JobChanges, type ProjectChange } from './changes'

const run = (id: number, reason?: 'last_success' | 'previous' | 'chosen') => ({ id, type: 'job', status: 'successful', created: '2026-11-20T10:00:00Z', finished: '2026-11-20T10:01:00Z', reason })

function none(extra: Partial<JobChanges> = {}): JobChanges {
  return { job: { id: 42, type: 'job', status: 'failed' }, compared_with: run(41, 'last_success'), last_success: run(41), previous: run(41), count: 0, template: null, project: null, inventory: null, credentials: [], execution_environment: null, inputs: [], hosts: null, nodes: [], ...extra }
}

const alice = { id: 2, username: 'alice' }
const project: ProjectChange = {
  kind: 'project',
  id: 7,
  name: 'ansible-azure',
  readable: true,
  before: { revision: 'becc45c0000000000000000000000000000000aa' },
  after: { revision: '9f2a1d0000000000000000000000000000000bbb' },
  commits: [{ sha: '9f2a1d0000000000000000000000000000000bbb', author: 'bob', date: '2026-11-21T10:00:00Z', message: 'Read the subscription from the vault' }],
  total_commits: 4,
  commits_available: true,
  diverged: false,
}

const full = none({
  count: 6,
  project,
  template: { kind: 'job_template', id: 3, name: 'Azure resource group', readable: true, versions: [{ version: 5, created: '2026-11-21T09:00:00Z', operation: 'update', comment: '', fields: ['extra_vars'], actor: alice }], fields: [{ field: 'extra_vars', before: 'a: 1\n', after: 'a: 2\n' }] },
  inventory: { kind: 'inventory', id: 4, name: 'Azure', readable: false },
  credentials: [{ kind: 'credential', id: 9, name: 'Azure SP', readable: true, versions: [{ version: 2, created: '2026-11-21T08:00:00Z', operation: 'update', comment: '', fields: ['inputs'], actor: alice }], fields: [{ field: 'inputs', key: 'secret', before: '$encrypted$', after: '$encrypted$', secret: true }] }],
  inputs: [{ field: 'limit', before: '', after: 'web-*' }],
  hosts: { added: ['web-04', 'web-05'], removed: [], added_count: 2, removed_count: 0, before_count: 3, after_count: 5 },
})

describe('the summary under the last success', () => {
  it('shows at most three lines, then links to all the changes', () => {
    const s = summarize(full)!
    expect(s.since.id).toBe(41)
    expect(s.lines.map((l) => [l.label, l.text])).toEqual([
      ['Project', '4 commits · becc45c → 9f2a1d0'],
      ['Template', 'extra_vars'],
      ['Inventory', 'changed (you cannot see how)'],
    ])
    expect(s.lines[1]).toMatchObject({ by: 'alice', at: '2026-11-21T09:00:00Z', anchor: 'template' })
    expect(s.more).toBe(true)
    expect(s.count).toBe(6)
  })
  it('says when nothing changed', () => {
    expect(summarize(none())).toMatchObject({ lines: [], more: false, nothing: 'Nothing changed since #41: same template, inventory, project revision, image and inputs.' })
    expect(summarize(none({ job: { id: 9, type: 'workflow_job', status: 'failed' } }))?.nothing).toBe('Nothing changed since #41: same workflow, nodes and inputs.')
  })
  it('shows nothing more without a run to compare with', () => {
    expect(summarize(none({ compared_with: null, last_success: null, previous: null }))).toBeNull()
    expect(summarize(undefined)).toBeNull()
  })
  it('describes each kind of change', () => {
    const lines = changeLines(full)
    expect(lines.map((l) => l.text).slice(3)).toEqual(['Azure SP: inputs.secret', 'limit (none) → web-*', '2 hosts added'])
    expect(lines[3].anchor).toBe('credential-9')
    const image = changeLines(none({ execution_environment: { kind: 'execution_environment', readable: true, before: { image: 'quay.io/ansible/awx-ee:latest', digest: 'sha256:4a1f' + '0'.repeat(60) }, after: { image: 'quay.io/ansible/awx-ee:latest', digest: 'sha256:9c0e' + '1'.repeat(60) }, same_tag: true } }))
    expect(image[0].text).toBe('same tag, new image · sha256:4a1f… → sha256:9c0e…')
    const gone = changeLines(none({ project: { ...project, commits: [], commits_available: false, missing: 'older', total_commits: undefined } }))
    expect(gone[0].text).toBe('becc45c → 9f2a1d0 · the older revision is gone')
    expect(changeLines(none({ hosts: { added: ['a'], removed: ['b', 'c'], added_count: 1, removed_count: 2, before_count: 3, after_count: 2 } }))[0].text).toBe('1 host added, 2 removed')
    const many = changeLines(none({ inputs: [{ field: 'limit', before: '', after: 'x' }, { field: 'extra_vars', key: 'region', before: 'eu', after: 'us' }, { field: 'extra_vars', key: 'pw', before: '$encrypted$', after: '$encrypted$', secret: true }, { field: 'forks', before: 5, after: 10 }] }))
    expect(many[0].text).toBe('limit, region, pw +1')
    const nodes = changeLines(none({ job: { id: 9, type: 'workflow_job', status: 'failed' }, nodes: [{ identifier: 'a', change: '', template: null, before_template: null, before_job: null, after_job: null }, { identifier: 'b', change: 'added', template: null, before_template: null, before_job: null, after_job: null }] }))
    expect(nodes).toMatchObject([{ label: 'Nodes', text: '1 node added' }])
  })
  it('names who edited an object', () => {
    const two = none({ template: { kind: 'job_template', readable: true, versions: [{ version: 1, created: 'x', operation: 'update', comment: '', fields: ['limit'], actor: alice }, { version: 2, created: 'y', operation: 'update', comment: '', fields: ['forks'], actor: { id: 3, username: 'bob' } }] } })
    expect(changeLines(two)[0]).toMatchObject({ text: 'limit, forks', by: 'bob and 1 other', at: 'y' })
  })
})

describe('the Changes tab', () => {
  it('lists its sections in order', () => {
    expect(sections(full)).toEqual(['project', 'template', 'inventory', 'credentials', 'inputs', 'hosts'])
    expect(sections(none())).toEqual([])
    expect(sections(none({ nodes: [{ identifier: 'a', change: '', template: null, before_template: null, before_job: run(1), after_job: run(2) }] }))).toEqual(['nodes'])
  })
  it('says what it compares with', () => {
    expect(comparedWith(full)).toBe('Compared with #41, the last success,')
    expect(comparedWith(none({ compared_with: run(41, 'chosen') }))).toBe('Compared with #41, the last success,')
    expect(comparedWith(none({ compared_with: run(40, 'chosen'), previous: run(40) }))).toBe('Compared with #40, the previous run,')
    expect(comparedWith(none({ compared_with: run(40, 'previous'), last_success: null, previous: run(40) }))).toBe('Compared with #40, the previous run (no earlier success),')
    expect(comparedWith(none({ compared_with: run(39, 'chosen') }))).toBe('Compared with #39, a run you chose,')
  })
  it('explains missing commits', () => {
    expect(commitsMissing(project)).toBeNull()
    expect(commitsMissing({ ...project, commits_available: false, missing: 'older' })).toMatch(/force push/)
    expect(commitsMissing({ ...project, commits_available: false, missing: 'repository' })).toMatch(/no copy/)
    expect(commitsMissing({ ...project, commits_available: false, readable: false })).toBeNull()
    expect(commitsShown({ ...project, total_commits: 132 })).toBe('1 of 132 commits, the newest first')
    expect(commitsShown({ ...project, total_commits: 1 })).toBeNull()
  })
  it('shows values', () => {
    expect(inputValue('')).toBe('(none)')
    expect(inputValue([])).toBe('(none)')
    expect(inputValue({ id: 3, name: 'prod' })).toBe('prod')
    expect(inputValue([{ id: 3, name: 'a' }, { id: 4, name: 'b' }])).toBe('a, b')
    expect(inputValue(5)).toBe('5')
    expect(shortDigest('sha256:' + 'ab'.repeat(32))).toBe('sha256:abab…')
    expect(multiline({ field: 'extra_vars', before: 'a: 1\n', after: '' })).toBe(true)
    expect(multiline({ field: 'limit', before: 'a', after: 'b' })).toBe(false)
    expect(multiline({ field: 'inputs', before: '$encrypted$', after: '$encrypted$', secret: true })).toBe(false)
  })
  it('builds the endpoint', () => {
    expect(changesPath({ id: 4, type: 'job' })).toBe('jobs/4/changes/')
    expect(changesPath({ id: 4, type: 'workflow_job' }, 2)).toBe('workflow_jobs/4/changes/?since=2')
  })
})
