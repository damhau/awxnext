import { describe, expect, it } from 'vitest'
import { actionLabel, groupPerms, missingViews, modelLabel, parsePerm, summary, togglePerm } from './roleDefinitions'

describe('role definitions', () => {
  it('reads permission names', () => {
    expect(parsePerm('awx.execute_jobtemplate')).toEqual({ perm: 'awx.execute_jobtemplate', action: 'execute', model: 'jobtemplate' })
    expect(parsePerm('shared.member_team')).toMatchObject({ action: 'member', model: 'team' })
    expect(actionLabel(parsePerm('awx.adhoc_inventory'))).toBe('Run commands')
    expect(modelLabel('awx.jobtemplate')).toBe('Job template')
    expect(modelLabel('inventory', true)).toBe('Inventories')
  })
  it('groups by kind of object, the role’s own first', () => {
    const g = groupPerms(['awx.view_inventory', 'awx.change_jobtemplate', 'awx.view_jobtemplate', 'shared.view_organization'], 'shared.organization')
    expect(g.map((x) => x.model)).toEqual(['organization', 'inventory', 'jobtemplate'])
    expect(g[2].perms.map((p) => p.action)).toEqual(['view', 'change'])
  })
  it('keeps view on for what it grants', () => {
    expect(togglePerm([], 'awx.execute_jobtemplate').sort()).toEqual(['awx.execute_jobtemplate', 'awx.view_jobtemplate'])
    expect(togglePerm(['awx.view_jobtemplate', 'awx.execute_jobtemplate', 'awx.add_jobtemplate', 'awx.view_inventory'], 'awx.view_jobtemplate').sort()).toEqual(['awx.add_jobtemplate', 'awx.view_inventory'])
    expect(togglePerm([], 'awx.add_jobtemplate')).toEqual(['awx.add_jobtemplate'])
    expect(missingViews(['awx.execute_jobtemplate', 'awx.add_inventory'])).toEqual(['awx.view_jobtemplate'])
    expect(missingViews(['awx.view_jobtemplate', 'awx.execute_jobtemplate'])).toEqual([])
  })
  it('summarises a role', () => {
    expect(summary(['awx.view_jobtemplate', 'awx.execute_jobtemplate'], 'awx.jobtemplate')).toBe('View, Launch')
    expect(summary(['awx.view_inventory', 'awx.use_inventory'], 'shared.organization')).toBe('Inventories: View, Use')
  })
})
