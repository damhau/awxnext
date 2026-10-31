import { tr } from './i18n'
// Instance group membership as the server applies AWX's policies: the
// instances a group lists, then its minimum, then its percentage. Adding
// an instance by hand lists it; the installer's groups (default and
// controlplane) take every instance and keep their name and policies.

export interface GroupPolicy {
  policy_instance_minimum: number
  policy_instance_percentage: number
  policy_instance_list: string[]
}

export const installerGroup = (name: string | undefined) => name === 'default' || name === 'controlplane'

/** Hostnames typed in a list, separated by commas, spaces or new lines. */
export function parseHosts(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

/** Why an instance is in a group: listed (added by hand) or chosen by the minimum or percentage. */
export function memberReason(hostname: string, group: GroupPolicy): 'listed' | 'policy' {
  return group.policy_instance_list.includes(hostname) ? 'listed' : 'policy'
}

const instances = (n: number) => `${n} ${n === 1 ? 'instance' : 'instances'}`

/** The group's policies in a sentence. */
export function describePolicy(g: GroupPolicy): string {
  const listed = g.policy_instance_list.length
  const auto: string[] = []
  if (g.policy_instance_percentage >= 100) auto.push('every instance')
  else {
    if (g.policy_instance_minimum > 0) auto.push(`at least ${instances(g.policy_instance_minimum)}`)
    if (g.policy_instance_percentage > 0) auto.push(`${g.policy_instance_percentage}% of instances`)
  }
  if (!auto.length) return listed ? `Only the ${listed === 1 ? 'listed instance' : `${listed} listed instances`}.` : tr('Only instances added by hand.')
  const text = auto.join(' and ')
  return `${text[0].toUpperCase()}${text.slice(1)}${listed ? `, plus ${listed} listed` : ''}.`
}

export interface GroupFormValues {
  max_concurrent_jobs: string
  max_forks: string
  policy_instance_minimum: string
  policy_instance_percentage: string
  policy_instance_list: string
}

/** The server's checks on the form's numbers and list, in its words. */
export function checkGroupForm(v: GroupFormValues): Partial<Record<keyof GroupFormValues, string>> {
  const errors: Partial<Record<keyof GroupFormValues, string>> = {}
  for (const k of ['max_concurrent_jobs', 'max_forks', 'policy_instance_minimum', 'policy_instance_percentage'] as const) {
    const s = v[k].trim()
    if (s === '') continue
    if (!/^-?\d+$/.test(s)) errors[k] = 'A valid integer is required.'
    else if (Number(s) < 0) errors[k] = tr('Ensure this value is greater than or equal to 0.')
    else if (k === 'policy_instance_percentage' && Number(s) > 100) errors[k] = tr('Ensure this value is less than or equal to 100.')
  }
  const seen = new Set<string>()
  for (const h of parseHosts(v.policy_instance_list)) {
    if (seen.has(h)) {
      errors.policy_instance_list = `Duplicate entry ${h}.`
      break
    }
    seen.add(h)
  }
  return errors
}
