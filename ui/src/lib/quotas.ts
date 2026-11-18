// Catalog quotas (roadmap M15-07): what each team, or each user requesting
// for no team, uses of an item's quotas.

export interface QuotaUsage {
  team: { id: number; name: string } | null
  user: { id: number; username: string } | null
  deployments: number
  requests: number
  max_deployments: number | null
  max_requests_per_day: number | null
}

export interface ItemQuotaUsage {
  max_deployments: number | null
  max_requests_per_day: number | null
  over_quota: 'refuse' | 'approve'
  count: number
  results: QuotaUsage[]
}

export type QuotaSlot = 'used' | 'adding' | 'free' | 'over'

/**
 * A quota's places, a cell each: used, taken by the request being made,
 * free, and past the quota. Null when there are too many to draw.
 */
export function quotaSlots(used: number, max: number, adding = 0, most = 20): QuotaSlot[] | null {
  const total = Math.max(max, used + adding)
  if (total > most) return null
  return Array.from({ length: total }, (_, i) => (i >= used + adding ? 'free' : i >= max ? 'over' : i < used ? 'used' : 'adding'))
}

/** Whom a usage row counts for: a team, or a user. */
export function holderName(u: Pick<QuotaUsage, 'team' | 'user'>): string {
  return u.team?.name ?? u.user?.username ?? ''
}

/** Whether an item limits anything: a deployment quota only counts on an item that creates deployments. */
export function hasQuota(item: { kind?: string; max_deployments?: number | null; max_requests_per_day?: number | null }): boolean {
  return (item.kind === 'deployment' && item.max_deployments != null) || item.max_requests_per_day != null
}
