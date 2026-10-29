import { describe, expect, it } from 'vitest'
import { enabledDefault, enabledHelp, isImportedFile, sourceChoices } from './sourceDefaults'

describe('enabled variable defaults', () => {
  it('gives AWX defaults for cloud sources', () => {
    expect(enabledDefault('ec2')).toEqual({ variable: 'ec2_state', value: 'running' })
    expect(enabledDefault('satellite6')).toEqual({ variable: 'foreman_enabled,foreman.enabled', value: 'True' })
    expect(enabledHelp('rhv')).toContain('Empty uses status = up.')
  })
  it('has none for project and constructed sources', () => {
    expect(enabledDefault('scm')).toBeNull()
    expect(enabledDefault('constructed')).toBeNull()
    expect(enabledHelp('scm')).not.toContain('Empty uses')
  })
})

describe('file sources', () => {
  const choices: [string, string][] = [
    ['file', 'File, Directory or Script'],
    ['scm', 'Sourced from a Project'],
  ]
  it('are not offered when creating a source', () => {
    expect(sourceChoices(choices).map(([k]) => k)).toEqual(['scm'])
  })
  it('stay selectable when editing one', () => {
    expect(sourceChoices(choices, 'file').map(([k]) => k)).toEqual(['file', 'scm'])
    expect(isImportedFile('file')).toBe(true)
  })
})
