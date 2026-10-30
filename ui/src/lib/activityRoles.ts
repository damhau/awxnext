// Activity stream entries of roles given and taken away: the role is named
// by its role_field and the object it is on ("Execute role on job template
// Deploy"); a role definition without such a role names the object its
// assignment was on, from the entry's changes.

type Any = Record<string, unknown>

const objectTypes: Record<string, string> = {
  jobtemplate: 'job template',
  workflowjobtemplate: 'workflow template',
  executionenvironment: 'execution environment',
  instancegroup: 'instance group',
  notificationtemplate: 'notification template',
  credentialtype: 'credential type',
}

/** "Execute role on job template Deploy" from a role's summary, '' without a role field. */
export function roleName(summary: Any | undefined): string {
  const f = typeof summary?.role_field === 'string' ? summary.role_field : ''
  if (!f) return ''
  const words = f.replace(/_role$/, '').replace(/_/g, ' ')
  const role = `${words.charAt(0).toUpperCase()}${words.slice(1)} role`
  const type = typeof summary?.resource_type === 'string' ? summary.resource_type.replace(/_/g, ' ') : ''
  if (!type) return role
  return `${role} on ${type} ${summary?.resource_name || `#${summary?.resource_id}`}`
}

/** "on job template Deploy" for a role definition's assignment, '' otherwise. */
export function assignmentTarget(object1: string, changes: Any | null | undefined): string {
  if (object1 !== 'role_definition' || !changes) return ''
  const ct = typeof changes.content_type === 'string' ? changes.content_type.split('.').pop() ?? '' : ''
  if (!ct) return 'everywhere'
  const name = changes.object_name ?? `#${changes.object_id}`
  return `on ${objectTypes[ct] ?? ct} ${name}`
}
