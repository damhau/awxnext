import { describe, expect, it } from 'vitest'
import { diffLines, hunks, taskDiffs, tooLargeToCompare, unifiedKind } from './diff'

describe('diffLines', () => {
  it('marks changed lines', () => {
    const d = diffLines('a\nserial: 4\nc\n', 'a\nserial: 8\nc\nd\n')
    expect(d.map((l) => l.kind + ' ' + l.text)).toEqual(['ctx a', 'del serial: 4', 'add serial: 8', 'ctx c', 'add d'])
    expect(d[3]).toMatchObject({ a: 3, b: 3 })
  })
  it('handles empty sides', () => {
    expect(diffLines('', 'x').map((l) => l.kind)).toEqual(['add'])
    expect(diffLines('x', '').map((l) => l.kind)).toEqual(['del'])
  })
})

describe('task diffs', () => {
  it('reads a template task’s before and after, named by its destination', () => {
    const res = {
      changed: true,
      dest: '/etc/app.conf',
      diff: [{ before: 'port = 8080\n', after: 'port = 9090\n', before_header: '/etc/app.conf', after_header: '/tmp/ansible-local/tmpx/app.conf.j2' }],
    }
    expect(taskDiffs(res)).toEqual([{ path: '/etc/app.conf', before: 'port = 8080\n', after: 'port = 9090\n', prepared: undefined, item: undefined }])
  })
  it('names a new file by the destination, not the temporary source', () => {
    const res = { dest: '/etc/new.conf', diff: [{ before: '', after: 'x\n', after_header: '/tmp/ansible-local/tmpx/new.j2' }] }
    expect(taskDiffs(res)[0].path).toBe('/etc/new.conf')
  })
  it('reads states, prepared text and loop items', () => {
    const file = { path: '/srv/app', diff: { before: { path: '/srv/app', state: 'absent' }, after: { path: '/srv/app', state: 'directory' } } }
    expect(taskDiffs(file)[0]).toMatchObject({ path: '/srv/app', before: 'path: /srv/app\nstate: absent', after: 'path: /srv/app\nstate: directory' })
    expect(taskDiffs({ diff: { prepared: '+ nginx 1.24' } })[0].prepared).toBe('+ nginx 1.24')
    const loop = { results: [{ item: 'a.conf', dest: '/etc/a.conf', diff: [{ before: '1', after: '2' }] }, { item: 'b.conf', diff: [] }] }
    expect(taskDiffs(loop)).toEqual([{ path: '/etc/a.conf', before: '1', after: '2', prepared: undefined, item: 'a.conf' }])
  })
  it('ignores results without a change', () => {
    expect(taskDiffs({ diff: [{ before: 'same', after: 'same' }] })).toEqual([])
    expect(taskDiffs(undefined)).toEqual([])
    expect(taskDiffs({ msg: 'ok' })).toEqual([])
  })
  it('knows when a file is too large to compare here', () => {
    expect(tooLargeToCompare('a\nb', 'c')).toBe(false)
    const big = Array.from({ length: 3000 }, (_, i) => `line ${i}`).join('\n')
    expect(tooLargeToCompare(big, big)).toBe(true)
  })
  it('classes unified diff lines', () => {
    expect(['--- before: /etc/a', '+++ after: /tmp/x', '@@ -1,2 +1,2 @@', '-port = 1', '+port = 2', ' workers = 4'].map(unifiedKind)).toEqual(['head', 'head', 'hunk', 'del', 'add', 'ctx'])
  })
})

describe('hunks', () => {
  it('keeps the changes with their context and folds the rest', () => {
    const before = Array.from({ length: 20 }, (_, i) => `l${i + 1}`).join('\n')
    const after = before.replace('l10', 'L10')
    const h = hunks(diffLines(before, after), 2)
    expect(h.map((l) => (l.kind === 'skip' ? `skip ${l.n}` : `${l.kind} ${l.text}`))).toEqual(['skip 7', 'ctx l8', 'ctx l9', 'del l10', 'add L10', 'ctx l11', 'ctx l12', 'skip 8'])
  })
  it('keeps everything when it all changed', () => {
    expect(hunks(diffLines('', 'a\nb')).map((l) => l.kind)).toEqual(['add', 'add'])
  })
})
