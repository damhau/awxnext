import { describe, expect, it } from 'vitest'
import {
  ansibleMarkup,
  compareVersions,
  dependencies,
  diffSummary,
  docLink,
  galaxyServer,
  groupContents,
  installCommand,
  installRepository,
  numberedDiff,
  optionRows,
  paragraphs,
  requirementsEntry,
  setTerm,
  sizeText,
  sortVersions,
  termValue,
} from './hub'

describe('versions', () => {
  it('sorts by semantic version, newest first', () => {
    const vs = ['1.0.0', '1.10.0', '1.9.0', '2.0.0-rc.1', '2.0.0', '2.0.0-beta.2', '2.0.0-beta.10', '0.1.0'].map((version) => ({ version }))
    expect(sortVersions(vs).map((v) => v.version)).toEqual(['2.0.0', '2.0.0-rc.1', '2.0.0-beta.10', '2.0.0-beta.2', '1.10.0', '1.9.0', '1.0.0', '0.1.0'])
  })
  it('ignores build metadata', () => {
    expect(compareVersions('1.0.0+abc', '1.0.0+def')).toBe(0)
    expect(compareVersions('1.0.0-1', '1.0.0-alpha')).toBeLessThan(0)
  })
})

describe('installing', () => {
  it('names the repository clients install from', () => {
    expect(galaxyServer('https://awx.example.com/')).toBe('https://awx.example.com/api/galaxy/content/published/')
    expect(galaxyServer('https://awx.example.com', 'mirror')).toBe('https://awx.example.com/api/galaxy/content/mirror/')
    expect(installRepository(['staging'])).toBeNull()
    expect(installRepository(['mirror', 'published'])).toBe('published')
    expect(installRepository(['rejected', 'mirror'])).toBe('mirror')
  })
  it('gives the command and the requirements entry', () => {
    const s = galaxyServer('https://a.example.com')
    expect(installCommand('acme.net', '1.2.0', s)).toBe('ansible-galaxy collection install acme.net:==1.2.0 \\\n  --server https://a.example.com/api/galaxy/content/published/')
    expect(requirementsEntry('acme.net', '1.2.0', s)).toBe('collections:\n  - name: acme.net\n    version: "1.2.0"\n    source: https://a.example.com/api/galaxy/content/published/')
  })
  it('lists dependencies by name', () => {
    expect(dependencies({ dependencies: { 'b.y': '>=1', 'a.x': '*' } })).toEqual([
      { name: 'a.x', spec: '*' },
      { name: 'b.y', spec: '>=1' },
    ])
    expect(dependencies(undefined)).toEqual([])
  })
})

describe('module docs', () => {
  it('groups the index by plugin type, modules first', () => {
    const g = groupContents([
      { type: 'lookup', name: 'b', short_description: '', error: '' },
      { type: 'module', name: 'z', short_description: '', error: '' },
      { type: 'module', name: 'a', short_description: '', error: '' },
      { type: 'odd', name: 'q', short_description: '', error: '' },
    ])
    expect(g.map((x) => [x.type, x.items.map((i) => i.name)])).toEqual([
      ['module', ['a', 'z']],
      ['lookup', ['b']],
      ['odd', ['q']],
    ])
    expect(g[2].label).toBe('odd')
  })
  it('flattens options with their suboptions', () => {
    const rows = optionRows({
      state: { type: 'str', choices: ['present', 'absent'], default: 'present', description: 'What to do.' },
      config: { type: 'dict', required: true, description: ['Settings.', 'More.'], suboptions: { port: { type: 'int', default: 80 } } },
      names: { type: 'list', elements: 'str', aliases: ['name'] },
    })
    expect(rows.map((r) => [r.path, r.depth, r.type])).toEqual([
      ['config', 0, 'dict'],
      ['config.port', 1, 'int'],
      ['names', 0, 'list / str'],
      ['state', 0, 'str'],
    ])
    expect(rows[0]).toMatchObject({ required: true, description: ['Settings.', 'More.'] })
    expect(rows[1].default).toBe('80')
    expect(rows[2].aliases).toEqual(['name'])
    expect(rows[3]).toMatchObject({ choices: ['present', 'absent'], default: 'present' })
    expect(optionRows(null)).toEqual([])
  })
  it('flattens return values with what they contain', () => {
    const rows = optionRows({ result: { type: 'dict', returned: 'always', sample: { a: 1 }, contains: { a: { type: 'int' } } } })
    expect(rows.map((r) => r.path)).toEqual(['result', 'result.a'])
    expect(rows[0]).toMatchObject({ returned: 'always', sample: '{"a":1}' })
  })
  it('reads descriptions as paragraphs', () => {
    expect(paragraphs('One.')).toEqual(['One.'])
    expect(paragraphs(['One.', 'Two.'])).toEqual(['One.', 'Two.'])
    expect(paragraphs(undefined)).toEqual([])
  })
})

describe('Ansible markup', () => {
  it('parses the classic and the semantic markup', () => {
    expect(ansibleMarkup('Use C(state=present) and I(only) B(this), see M(acme.net.vlan).')).toEqual([
      { kind: 'text', text: 'Use ' },
      { kind: 'code', text: 'state=present' },
      { kind: 'text', text: ' and ' },
      { kind: 'italic', text: 'only' },
      { kind: 'text', text: ' ' },
      { kind: 'bold', text: 'this' },
      { kind: 'text', text: ', see ' },
      { kind: 'module', text: 'acme.net.vlan', fqcn: 'acme.net.vlan', type: 'module' },
      { kind: 'text', text: '.' },
    ])
    expect(ansibleMarkup('O(port=80) or O(acme.net.vlan#module:id) gives RV(result.id), from E(HOME) V(a\\)b)')).toEqual([
      { kind: 'option', text: 'port=80', name: 'port', plugin: '', value: '80' },
      { kind: 'text', text: ' or ' },
      { kind: 'option', text: 'id', name: 'id', plugin: 'acme.net.vlan#module', value: undefined },
      { kind: 'text', text: ' gives ' },
      { kind: 'return', text: 'result.id', name: 'result.id', plugin: '', value: undefined },
      { kind: 'text', text: ', from ' },
      { kind: 'env', text: 'HOME' },
      { kind: 'text', text: ' ' },
      { kind: 'value', text: 'a)b' },
    ])
    expect(ansibleMarkup('P(acme.net.items#lookup)')).toEqual([{ kind: 'plugin', text: 'acme.net.items#lookup', fqcn: 'acme.net.items', type: 'lookup' }])
  })
  it('keeps only http and https links', () => {
    expect(ansibleMarkup('U(https://example.com) L(the docs,https://docs.example.com)')).toEqual([
      { kind: 'link', text: 'https://example.com', href: 'https://example.com' },
      { kind: 'text', text: ' ' },
      { kind: 'link', text: 'the docs', href: 'https://docs.example.com' },
    ])
    expect(ansibleMarkup('U(javascript:alert(1\\)) L(x,javascript:y)')).toEqual([
      { kind: 'code', text: 'javascript:alert(1\\)' },
      { kind: 'text', text: ' ' },
      { kind: 'text', text: 'x' },
    ])
  })
  it('leaves words that only look like markup', () => {
    expect(ansibleMarkup('ABC(x) and a HORIZONTALLINE')).toEqual([{ kind: 'text', text: 'ABC(x) and a ' }, { kind: 'rule' }])
  })
  it('links the collection’s own modules', () => {
    expect(docLink('acme.net.vlan', 'module', 'acme.net', '/c/1/docs')).toBe('/c/1/docs/module/vlan')
    expect(docLink('ansible.builtin.copy', 'module', 'acme.net', '/c/1/docs')).toBeNull()
  })
})

describe('the collections filter', () => {
  it('reads and sets key:value terms', () => {
    expect(termValue('net repository:staging', 'repository')).toBe('staging')
    expect(termValue('net -signed:true', 'signed')).toBeNull()
    expect(setTerm('net repository:staging', 'repository', 'published')).toBe('net repository:published')
    expect(setTerm('net signed:true', 'signed', null)).toBe('net')
    expect(setTerm('', 'tag', 'cloud ops')).toBe('tag:"cloud ops"')
  })
})

describe('file diffs', () => {
  it('numbers the lines of each hunk', () => {
    expect(numberedDiff('@@ -2,3 +2,3 @@\n a\n-b\n+B\n c\n\\ No newline at end of file\n')).toEqual([
      { kind: 'hunk', text: '@@ -2,3 +2,3 @@' },
      { kind: 'ctx', text: 'a', a: 2, b: 2 },
      { kind: 'del', text: 'b', a: 3 },
      { kind: 'add', text: 'B', b: 3 },
      { kind: 'ctx', text: 'c', a: 4, b: 4 },
      { kind: 'note', text: 'No newline at end of file' },
    ])
    expect(numberedDiff('@@ -0,0 +1 @@\n+n\n')).toEqual([
      { kind: 'hunk', text: '@@ -0,0 +1 @@' },
      { kind: 'add', text: 'n', b: 1 },
    ])
  })
  it('sums up what changed', () => {
    expect(diffSummary({ added: 2, removed: 0, changed: 1 })).toBe('2 added · 1 changed')
    expect(diffSummary({ added: 0, removed: 0, changed: 0 })).toBe('No file changed')
  })
})

describe('sizes', () => {
  it('reads in bytes, KiB or MiB', () => {
    expect(sizeText(812)).toBe('812 B')
    expect(sizeText(14540)).toBe('14.2 KiB')
    expect(sizeText(3250586)).toBe('3.1 MiB')
  })
})
