import { describe, expect, it } from 'vitest'
import { checkGroupForm, describePolicy, installerGroup, memberReason, parseHosts } from './instanceGroups'

const policy = (minimum: number, percentage: number, list: string[] = []) => ({
  policy_instance_minimum: minimum,
  policy_instance_percentage: percentage,
  policy_instance_list: list,
})
const form = (over: Partial<Record<string, string>> = {}) => ({
  max_concurrent_jobs: '0',
  max_forks: '0',
  policy_instance_minimum: '0',
  policy_instance_percentage: '0',
  policy_instance_list: '',
  ...over,
})

describe('instance group policies', () => {
  it('knows the installer groups', () => {
    expect(installerGroup('default')).toBe(true)
    expect(installerGroup('controlplane')).toBe(true)
    expect(installerGroup('dmz')).toBe(false)
  })
  it('reads typed hostnames', () => {
    expect(parseHosts(' a.example, b.example\nc.example  ')).toEqual(['a.example', 'b.example', 'c.example'])
    expect(parseHosts('')).toEqual([])
  })
  it('says why an instance is in a group', () => {
    expect(memberReason('a', policy(0, 0, ['a']))).toBe('listed')
    expect(memberReason('b', policy(2, 0, ['a']))).toBe('policy')
  })
  it('describes the policies', () => {
    expect(describePolicy(policy(0, 100))).toBe('Every instance.')
    expect(describePolicy(policy(2, 50, ['a']))).toBe('At least 2 instances and 50% of instances, plus 1 listed.')
    expect(describePolicy(policy(1, 0))).toBe('At least 1 instance.')
    expect(describePolicy(policy(0, 0, ['a']))).toBe('Only the listed instance.')
    expect(describePolicy(policy(0, 0, ['a', 'b']))).toBe('Only the 2 listed instances.')
    expect(describePolicy(policy(0, 0))).toBe('Only instances added by hand.')
  })
  it('checks the form as the server does', () => {
    expect(checkGroupForm(form())).toEqual({})
    expect(checkGroupForm(form({ policy_instance_percentage: '101' }))).toEqual({ policy_instance_percentage: 'Ensure this value is less than or equal to 100.' })
    expect(checkGroupForm(form({ max_forks: '-1', policy_instance_minimum: '1.5' }))).toEqual({
      max_forks: 'Ensure this value is greater than or equal to 0.',
      policy_instance_minimum: 'A valid integer is required.',
    })
    expect(checkGroupForm(form({ policy_instance_list: 'a, b a' }))).toEqual({ policy_instance_list: 'Duplicate entry a.' })
  })
})
