import { describe, expect, it } from 'vitest'
import type { JobEvent } from '../../api/types'
import { addWithin, firstGap, peers, projectPath, toGrid, toLines } from './events'

let counter = 0
function ev(event: string, fields: Partial<JobEvent> & { task_uuid?: string; ignore_errors?: boolean } = {}): JobEvent {
  const { task_uuid, ignore_errors, ...rest } = fields
  counter++
  return {
    id: counter,
    counter,
    event,
    failed: false,
    changed: false,
    host_name: '',
    task: '',
    play: 'site',
    role: '',
    stdout: '',
    start_line: counter * 3,
    end_line: counter * 3 + 2,
    uuid: `u${counter}`,
    parent_uuid: '',
    created: '2026-10-27T10:00:00Z',
    event_data: { task_uuid, ignore_errors },
    ...rest,
  } as JobEvent
}

// Two tasks on three hosts: web2 fails the second one, web3 never reaches it.
function run(): JobEvent[] {
  counter = 0
  return [
    ev('playbook_on_task_start', { task: 'Install', uuid: 't1', stdout: 'TASK [Install] ****' }),
    ev('runner_on_ok', { task: 'Install', host_name: 'web1', host: 11, task_uuid: 't1' }),
    ev('runner_on_ok', { task: 'Install', host_name: 'web2', host: 12, task_uuid: 't1', changed: true }),
    ev('runner_on_ok', { task: 'Install', host_name: 'web3', host: 13, task_uuid: 't1' }),
    ev('playbook_on_task_start', { task: 'Start', uuid: 't2', stdout: 'TASK [Start] ****' }),
    ev('runner_on_ok', { task: 'Start', host_name: 'web1', host: 11, task_uuid: 't2' }),
    ev('runner_on_failed', { task: 'Start', host_name: 'web2', host: 12, task_uuid: 't2', stdout: 'fatal: [web2]: FAILED!' }),
  ]
}

describe('toGrid', () => {
  it('keeps the deciding event, host ids and failing columns', () => {
    const g = toGrid(run())
    expect(g.tasks.map((t) => [t.name, t.bad])).toEqual([
      ['Install', false],
      ['Start', true],
    ])
    const web2 = g.hosts.find((h) => h.name === 'web2')!
    expect(web2).toMatchObject({ id: 12, cells: ['changed', 'failed'], bad: true })
    expect(web2.events[1]?.stdout).toBe('fatal: [web2]: FAILED!')
    expect(g.hosts.find((h) => h.name === 'web3')!.cells).toEqual(['ok', 'none'])
  })

  it('counts an ignored error as ok', () => {
    counter = 0
    const g = toGrid([ev('playbook_on_task_start', { task: 'Probe', uuid: 't1' }), ev('runner_on_failed', { task: 'Probe', host_name: 'web1', task_uuid: 't1', ignore_errors: true })])
    expect(g.hosts[0].cells).toEqual(['ok'])
    expect(g.tasks[0].bad).toBe(false)
  })
})

describe('peers', () => {
  const g = toGrid(run())
  const at = (name: string) => g.hosts.findIndex((h) => h.name === name)

  it('says when only this host failed', () => {
    expect(peers(g, at('web2'), 1)).toBe('Only this host failed · others: 1 ok · 1 not run')
  })

  it('says when all hosts did the same', () => {
    counter = 0
    const same = toGrid([ev('playbook_on_task_start', { task: 'A', uuid: 't1' }), ev('runner_on_ok', { task: 'A', host_name: 'a', task_uuid: 't1' }), ev('runner_on_ok', { task: 'A', host_name: 'b', task_uuid: 't1' })])
    expect(peers(same, 0, 0)).toBe('Same on all 2 hosts')
  })

  it('tallies the others otherwise', () => {
    expect(peers(g, at('web1'), 0)).toBe('Others: 1 ok · 1 changed')
  })
})

describe('toLines', () => {
  it('numbers task headers like the grid columns and gives their worst result', () => {
    const heads = toLines(run()).filter((l) => l.kind === 'task')
    expect(heads.map((l) => [l.plain, l.taskNo, l.taskState])).toEqual([
      ['TASK [Install]', 1, 'changed'],
      ['TASK [Start]', 2, 'failed'],
    ])
  })
  it('ties a task’s lines to its header, after the blank line the runner prints first', () => {
    const lines = toLines([
      ev('playbook_on_play_start', { stdout: '\r\nPLAY [site] ****' }),
      ev('playbook_on_task_start', { stdout: '\r\nTASK [config] ****', task: 'config' }),
      ev('runner_on_ok', { stdout: 'changed: [web1]', changed: true }),
    ])
    const head = lines.findIndex((l) => l.kind === 'task')
    expect(lines[head].task).toBe(head)
    expect(lines.find((l) => l.kind === 'changed')?.task).toBe(head)
  })
  it('marks the lines of a diff mode’s file diff', () => {
    const stdout = '\x1b[0;31m--- before: /etc/app.conf\x1b[0m\r\n\x1b[0;32m+++ after: /tmp/x/app.conf.j2\x1b[0m\r\n@@ -1,2 +1,2 @@\r\n\x1b[0;31m-port = 8080\x1b[0m\r\n\x1b[0;32m+port = 9090\x1b[0m\r\n workers = 4'
    const lines = toLines([ev('playbook_on_task_start', { stdout: 'TASK [config] ***', task: 'config' }), ev('runner_on_file_diff', { stdout, host_name: 'web1' }), ev('runner_on_ok', { stdout: 'changed: [web1]', changed: true })])
    const diff = lines.filter((l) => l.kind === 'diff')
    expect(diff.map((l) => l.diff)).toEqual(['head', 'head', 'hunk', 'del', 'add', 'ctx'])
    expect(diff.every((l) => l.task === 0)).toBe(true)
  })
})

describe('projectPath', () => {
  it('cuts the job work directory', () => {
    expect(projectPath('/var/lib/awxnext/jobs/27/work/project/deploy/site.yml:20')).toBe('deploy/site.yml:20')
  })
})

describe('addWithin', () => {
  it('holds at most the limit and says when events were left out', () => {
    counter = 0
    const store = new Map<number, JobEvent>()
    expect(addWithin(store, [ev('a'), ev('b'), ev('c')], 2)).toBe(true)
    expect([...store.keys()]).toEqual([1, 2])
    // An event already held is replaced even at the limit.
    expect(addWithin(store, [{ ...store.get(2)!, stdout: 'new' }], 2)).toBe(false)
    expect(store.get(2)!.stdout).toBe('new')
  })

  it('takes a folded event again, grown, and leaves no gap', () => {
    // Over JOB_EVENT_MAX_RATE the server folds verbose lines into the event
    // before, and sends it again whole under its counter.
    counter = 0
    const store = new Map<number, JobEvent>()
    addWithin(store, [ev('a'), ev('b')], 0)
    addWithin(store, [{ ...store.get(2)!, stdout: 'b\nc\nd' }], 0)
    addWithin(store, [ev('e')], 0)
    expect(store.get(2)!.stdout).toBe('b\nc\nd')
    expect([...store.keys()]).toEqual([1, 2, 3])
    expect(firstGap(store.keys())).toBeNull()
  })

  it('has no limit at 0', () => {
    counter = 0
    const store = new Map<number, JobEvent>()
    expect(addWithin(store, [ev('a'), ev('b'), ev('c')], 0)).toBe(false)
    expect(store.size).toBe(3)
  })
})

describe('firstGap', () => {
  it('finds the first counter missing between the lowest and the highest held', () => {
    expect(firstGap([1, 2, 3])).toBeNull()
    expect(firstGap([5, 6, 7])).toBeNull()
    expect(firstGap([3, 1, 2, 7, 9])).toBe(4)
    expect(firstGap([])).toBeNull()
  })
})
