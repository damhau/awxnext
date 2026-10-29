import { describe, expect, it } from 'vitest'
import { grantReason, grantTarget, testerClaims } from './mapping'

describe('mapping dry run', () => {
  it('builds claims with the groups claim', () => {
    expect(testerClaims('realm_access.roles', ' alice ', 'alice@example.com', 'ops, team-web\nadmins,')).toEqual({
      preferred_username: 'alice',
      email: 'alice@example.com',
      'realm_access.roles': ['ops', 'team-web', 'admins'],
    })
  })
  it('falls back to a test user without an email', () => {
    expect(testerClaims('groups', '', ' ', '')).toEqual({ preferred_username: 'test-user', groups: [] })
  })
  it('explains rule and map grants', () => {
    const rule = { role_definition: 'Team Member', organization: 'Ops', team: 'Web', matched_group: 'ops-web' }
    const map = { role_definition: 'Organization Admin', organization: 'Ops', matched_group: '', map: 'organization map "Ops" admins' }
    expect([grantTarget(rule), grantReason(rule)]).toEqual(['team Web in Ops', 'group ops-web'])
    expect([grantTarget(map), grantReason(map)]).toEqual(['Ops', 'organization map "Ops" admins'])
  })
})
