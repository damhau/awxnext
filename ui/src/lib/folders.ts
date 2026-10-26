import { useQuery } from '@tanstack/react-query'
import { getAll } from '../api/client'
import type { Folder } from '../api/types'
import { useOrg } from '../app/context'

export interface FolderNode {
  folder: Folder
  depth: number
  children: FolderNode[]
  /** Templates in this folder and all subfolders. */
  total: number
  path: string
}

/** Folders of the organization chosen in the top bar, or of every organization. */
export function useFolders({ allOrgs = false }: { allOrgs?: boolean } = {}) {
  const { scope } = useOrg()
  const params = allOrgs ? {} : scope
  return useQuery({
    queryKey: ['folders/', 'all', params],
    queryFn: () => getAll<Folder>('folders/', { order_by: 'name', ...params }),
    staleTime: 30_000,
  })
}

/** Objects directly in a folder, by type (from the API's related_field_counts). */
export function directCounts(f: Folder): { templates: number; inventories: number; projects: number; credentials: number; folders: number } {
  const c = f.summary_fields?.related_field_counts ?? {}
  return {
    templates: (c.job_templates ?? 0) + (c.workflow_job_templates ?? 0),
    inventories: c.inventories ?? 0,
    projects: c.projects ?? 0,
    credentials: c.credentials ?? 0,
    folders: c.folders ?? 0,
  }
}

/** Templates, inventories, projects and credentials in a folder and its subfolders. */
export function totalObjects(n: FolderNode): number {
  const d = directCounts(n.folder)
  return d.templates + d.inventories + d.projects + d.credentials + n.children.reduce((sum, c) => sum + totalObjects(c), 0)
}

export function buildTree(folders: Folder[]): { roots: FolderNode[]; byId: Map<number, FolderNode> } {
  const byId = new Map<number, FolderNode>()
  for (const f of folders) {
    const c = f.summary_fields?.related_field_counts ?? {}
    byId.set(f.id, { folder: f, depth: 0, children: [], total: (c.job_templates ?? 0) + (c.workflow_job_templates ?? 0), path: f.name })
  }
  const roots: FolderNode[] = []
  for (const n of byId.values()) {
    const p = n.folder.parent ? byId.get(n.folder.parent) : undefined
    if (p) p.children.push(n)
    else roots.push(n)
  }
  const walk = (n: FolderNode, depth: number, prefix: string): number => {
    n.depth = depth
    n.path = prefix ? `${prefix}/${n.folder.name}` : n.folder.name
    n.children.sort((a, b) => a.folder.name.localeCompare(b.folder.name))
    for (const c of n.children) n.total += walk(c, depth + 1, n.path)
    return n.total
  }
  roots.sort((a, b) => a.folder.name.localeCompare(b.folder.name))
  roots.forEach((r) => walk(r, 0, ''))
  return { roots, byId }
}

export function flatten(roots: FolderNode[]): FolderNode[] {
  const out: FolderNode[] = []
  const visit = (n: FolderNode) => {
    out.push(n)
    n.children.forEach(visit)
  }
  roots.forEach(visit)
  return out
}

/** The folder and all its descendants. */
export function subtreeIds(n: FolderNode): number[] {
  return [n.folder.id, ...n.children.flatMap(subtreeIds)]
}

export function ancestors(n: FolderNode | undefined, byId: Map<number, FolderNode>): FolderNode[] {
  const out: FolderNode[] = []
  let cur = n
  while (cur) {
    out.unshift(cur)
    cur = cur.folder.parent ? byId.get(cur.folder.parent) : undefined
  }
  return out
}
