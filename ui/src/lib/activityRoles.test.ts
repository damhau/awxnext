import { describe, expect, it } from 'vitest'
import { assignmentTarget, roleName } from './activityRoles'

describe('activity entries of roles', () => {
  it('names roles by their field', () => {
    expect(roleName({ id: 3, role_field: 'execute_role', resource_type: 'job_template', resource_id: 7, resource_name: 'Deploy' })).toBe('Execute role on job template Deploy')
    expect(roleName({ id: 3, role_field: 'project_admin_role', resource_type: 'organization', resource_id: 2, resource_name: '' })).toBe('Project admin role on organization #2')
    expect(roleName({ id: 3, role_field: 'admin_role' })).toBe('Admin role')
    expect(roleName({ id: 3 })).toBe('')
    expect(roleName(undefined)).toBe('')
  })
  it('names the object of a role definition assignment', () => {
    expect(assignmentTarget('role_definition', { content_type: 'awx.jobtemplate', object_id: 7, object_name: 'Deploy' })).toBe('on job template Deploy')
    expect(assignmentTarget('role_definition', { content_type: 'shared.team', object_id: 2 })).toBe('on team #2')
    expect(assignmentTarget('role_definition', { content_type: null, object_id: null })).toBe('everywhere')
    expect(assignmentTarget('role', {})).toBe('')
  })
})
