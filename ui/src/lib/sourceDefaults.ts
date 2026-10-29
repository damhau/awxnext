/**
 * AWX's per-source defaults for the enabled variable and value: used when a
 * source sets none (internal/resources/inventory_sources.go keeps the same).
 */
const ENABLED_DEFAULTS: Record<string, [string, string]> = {
  ec2: ['ec2_state', 'running'],
  vmware: ['guest.gueststate', 'running'],
  gce: ['status', 'running'],
  azure_rm: ['powerstate', 'running'],
  openstack: ['status', 'ACTIVE'],
  rhv: ['status', 'up'],
  controller: ['remote_tower_enabled', 'true'],
  satellite6: ['foreman_enabled,foreman.enabled', 'True'],
}

/** The enabled variable and value a source type uses by default, if any. */
export function enabledDefault(source: string): { variable: string; value: string } | null {
  const d = ENABLED_DEFAULTS[source]
  return d ? { variable: d[0], value: d[1] } : null
}

/** Help for the enabled variable field of a source type. */
export function enabledHelp(source: string): string {
  const d = enabledDefault(source)
  const base = 'Host variable deciding whether a host is enabled, e.g. status.power_state.'
  return d ? `${base} Empty uses ${d.variable} = ${d.value}.` : base
}

/** File sources are created by `awxnext inventory-import` and never synced. */
export function isImportedFile(source: string): boolean {
  return source === 'file'
}

/** The source types a form offers: not file, unless the source being edited is one. */
export function sourceChoices(choices: [string, string][], current?: string): [string, string][] {
  return choices.filter(([k]) => !isImportedFile(k) || k === current)
}
