import { useQuery } from '@tanstack/react-query'
import { getAll } from '../../api/client'
import type { Folder } from '../../api/types'
import { useOrg } from '../../app/context'

export interface FolderNode {
  folder: Folder
  depth: number
  children: FolderNode[]
  /** Templates in this folder and all subfolders. */
  total: number
  path: string
}

export function useFolders() {
  const { scope } = useOrg()
  return useQuery({
    queryKey: ['folders/', 'all', scope],
    queryFn: () => getAll<Folder>('folders/', { order_by: 'name', ...scope }),
    staleTime: 30_000,
  })
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
