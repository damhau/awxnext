import { api } from '../api/client'
import type { Job } from '../api/types'
import { jobApi } from './format'
import { tr } from './i18n'

/**
 * Runs a job again with the same values. Syncs have no relaunch endpoint in
 * AWX; they start a new update of their project or inventory source.
 */
export async function relaunchJob(j: Pick<Job, 'id' | 'type' | 'unified_job_template'>, body: Record<string, unknown> = {}): Promise<{ id: number; type: string }> {
  let r: { id: number; type?: string; project_update?: number; inventory_update?: number }
  switch (j.type) {
    case 'project_update':
      r = await api.post(`projects/${j.unified_job_template}/update/`, {})
      return { id: r.project_update ?? r.id, type: 'project_update' }
    case 'inventory_update':
      r = await api.post(`inventory_sources/${j.unified_job_template}/update/`, {})
      return { id: r.inventory_update ?? r.id, type: 'inventory_update' }
    case 'system_job':
      r = await api.post(`system_job_templates/${j.unified_job_template}/launch/`, {})
      return { id: r.id, type: 'system_job' }
  }
  r = await api.post(`${jobApi(j.type, j.id)}relaunch/`, body)
  return { id: r.id, type: r.type ?? j.type }
}

const passwordLabels: Record<string, string> = {
  ssh_password: 'SSH password',
  ssh_key_unlock: 'SSH key passphrase',
  become_password: tr('Privilege escalation password'),
  vault_password: tr('Vault password'),
}

// The machine credential inputs a launch must supply when set to ASK, by
// the credential_passwords key they need.
const askInputs: [string, string][] = [
  ['password', 'ssh_password'],
  ['ssh_key_unlock', 'ssh_key_unlock'],
  ['become_password', 'become_password'],
]

/** The passwords a launch with this machine credential needs: its inputs
 * set to ASK (an ad hoc command has no launch info to say so). */
export function askedPasswords(inputs: Record<string, unknown> | null | undefined): string[] {
  return askInputs.filter(([input]) => inputs?.[input] === 'ASK').map(([, key]) => key)
}

/** A credential_passwords key in words: "Vault password (prod)" for vault_password.prod. */
export function passwordLabel(key: string): string {
  const [base, vaultId] = key.split(/\.(.*)/)
  const label = passwordLabels[base] ?? key
  return vaultId ? `${label} (${vaultId})` : label
}
