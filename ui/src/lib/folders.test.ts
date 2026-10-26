import { describe, expect, it } from 'vitest'
import type { Folder } from '../api/types'
import { buildTree, directCounts, flatten, totalObjects } from './folders'

const folder = (id: number, name: string, parent: number | null, counts: Record<string, number> = {}): Folder =>
  ({ id, name, parent, organization: 1, type: 'folder', url: '', related: {}, summary_fields: { related_field_counts: counts } }) as Folder

describe('folders', () => {
  const all = [
    folder(1, 'apps', null, { job_templates: 1 }),
    folder(2, 'checkout', 1, { job_templates: 2, workflow_job_templates: 1, inventories: 1, credentials: 2 }),
    folder(3, 'billing', 1, { projects: 1 }),
    folder(4, 'network', null),
  ]
  const { roots, byId } = buildTree(all)

  it('builds paths and depth-first order', () => {
    expect(flatten(roots).map((n) => `${n.depth}:${n.path}`)).toEqual(['0:apps', '1:apps/billing', '1:apps/checkout', '0:network'])
  })

  it('counts direct objects by type', () => {
    expect(directCounts(byId.get(2)!.folder)).toEqual({ templates: 3, inventories: 1, projects: 0, credentials: 2, folders: 0 })
  })

  it('totals objects including subfolders', () => {
    expect(totalObjects(byId.get(1)!)).toBe(1 + 6 + 1)
    expect(totalObjects(byId.get(4)!)).toBe(0)
  })
})
