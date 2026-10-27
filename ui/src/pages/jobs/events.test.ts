import { describe, expect, it } from 'vitest'
import type { JobEvent } from '../../api/types'
import { peers, projectPath, toGrid, toLines } from './events'

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
})

describe('projectPath', () => {
  it('cuts the job work directory', () => {
    expect(projectPath('/var/lib/awxnext/jobs/27/work/project/deploy/site.yml:20')).toBe('deploy/site.yml:20')
  })
})
