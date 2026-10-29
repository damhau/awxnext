import { describe, expect, it } from 'vitest'
import { addToCondition, ancestors, cleanOutputs, outputRef, outputSuggestions, type OutputDecl } from './outputs'

const node = (id: number, identifier: string, ujt: number | null, success: number[] = [], failure: number[] = [], always: number[] = []) => ({
  id,
  identifier,
  unified_job_template: ujt,
  success_nodes: success,
  failure_nodes: failure,
  always_nodes: always,
})

// discover -> build -> deploy, cleanup --always--> deploy, other is unrelated.
const nodes = [node(1, 'discover', 10, [2]), node(2, 'build', 20, [3]), node(3, 'deploy', 30), node(4, 'cleanup', 10, [], [], [3]), node(5, 'other', 10)]

const outputs: Record<number, OutputDecl[]> = {
  10: [
    { name: 'regions', type: 'list', required: true },
    { name: 'replicas', type: 'integer', required: false },
  ],
  20: [{ name: 'image_tag', type: 'string', required: true }],
}

describe('outputRef', () => {
  it('uses dots for plain identifiers and brackets otherwise', () => {
    expect(outputRef('build', 'tag')).toBe('nodes.build.artifacts.tag')
    expect(outputRef('smoke-test', 'ok')).toBe("nodes['smoke-test'].artifacts.ok")
    expect(outputRef("it's", 'ok')).toBe("nodes['it\\'s'].artifacts.ok")
  })
})

describe('ancestors', () => {
  it('lists every step a path leads from, nearest first', () => {
    expect(ancestors(3, nodes).map((n) => n.identifier)).toEqual(['build', 'cleanup', 'discover'])
    expect(ancestors(1, nodes)).toEqual([])
  })
  it('stops on cycles', () => {
    expect(ancestors(1, [node(1, 'a', null, [2]), node(2, 'b', null, [1])]).map((n) => n.identifier)).toEqual(['b'])
  })
})

describe('outputSuggestions', () => {
  const of = (t: number) => outputs[t]
  it('offers the outputs of earlier steps', () => {
    expect(outputSuggestions(3, nodes, of).map((s) => s.ref)).toEqual([
      'nodes.build.artifacts.image_tag',
      'nodes.cleanup.artifacts.regions',
      'nodes.cleanup.artifacts.replicas',
      'nodes.discover.artifacts.regions',
      'nodes.discover.artifacts.replicas',
    ])
  })
  it('offers only lists for loops', () => {
    expect(outputSuggestions(2, nodes, of, true).map((s) => `${s.ref} ${s.output.type}`)).toEqual(['nodes.discover.artifacts.regions list'])
  })
  it('skips steps without an identifier or a template', () => {
    expect(outputSuggestions(2, [node(1, '', 10, [2]), node(2, 'b', 20)], of)).toEqual([])
  })
})

describe('cleanOutputs', () => {
  it('trims names and drops empty rows', () => {
    expect(
      cleanOutputs([
        { name: ' regions ', type: 'list', required: true },
        { name: '  ', type: 'string', required: false },
      ]),
    ).toEqual([{ name: 'regions', type: 'list', required: true }])
  })
})

describe('addToCondition', () => {
  it('starts or extends a condition', () => {
    expect(addToCondition('', 'nodes.a.artifacts.ok')).toBe('nodes.a.artifacts.ok')
    expect(addToCondition("env == 'prod' ", 'nodes.a.artifacts.ok')).toBe("env == 'prod' and nodes.a.artifacts.ok")
  })
})
