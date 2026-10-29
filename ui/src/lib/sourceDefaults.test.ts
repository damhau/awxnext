import { describe, expect, it } from 'vitest'
import { enabledDefault, enabledHelp } from './sourceDefaults'

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
