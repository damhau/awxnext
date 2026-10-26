import { api } from '../api/client'
import type { Job } from '../api/types'
import { jobApi } from './format'

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
