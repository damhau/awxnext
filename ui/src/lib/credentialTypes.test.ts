import { describe, expect, it } from 'vitest'
import { fieldIds, injectorsExample, inputsExample, parseSpec, specText, undefinedRefs } from './credentialTypes'

describe('credential type editors', () => {
  it('parses YAML and JSON mappings, empty as {}', () => {
    expect(parseSpec('')).toEqual({ value: {} })
    expect(parseSpec('  \n')).toEqual({ value: {} })
    expect(parseSpec('env:\n  A: b\n')).toEqual({ value: { env: { A: 'b' } } })
    expect(parseSpec('{"env": {"A": "b"}}')).toEqual({ value: { env: { A: 'b' } } })
  })
  it('refuses what is not a mapping', () => {
    expect(parseSpec('- a\n- b').error).toMatch(/mapping/)
    expect(parseSpec('just text').error).toMatch(/mapping/)
    expect(parseSpec('env: [').error).toMatch(/Not valid YAML or JSON/)
  })
  it('prints an object back as YAML, {} as empty', () => {
    expect(specText({})).toBe('')
    expect(specText(null)).toBe('')
    const v = parseSpec(inputsExample).value!
    expect(parseSpec(specText(v)).value).toEqual(v)
  })
  it('lists the input fields', () => {
    expect(fieldIds(parseSpec(inputsExample).value!)).toEqual(['token', 'region'])
    expect(fieldIds({})).toEqual([])
  })
  it('points out injector variables that no field defines', () => {
    const inputs = parseSpec(inputsExample).value!
    expect(undefinedRefs(inputs, parseSpec(injectorsExample).value!)).toEqual([])
    expect(undefinedRefs(inputs, { env: { A: '{{ tokn }}', B: '{{tower.filename.cfg}}' }, file: { 'template.cfg': '{{- region }} {{ zone|default("") }}' } })).toEqual([
      'tokn',
      'zone',
    ])
  })
})
