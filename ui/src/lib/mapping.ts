// The SSO mapping dry run: sample claims to send, and how its grants read.

export interface MappedGrant {
  role_definition: string
  organization: string
  team?: string
  matched_group: string
  /** The AWX organization or team map entry, when a map granted it. */
  map?: string
}

/** Token claims for a user with this username, email and groups. */
export function testerClaims(groupsClaim: string, username: string, email: string, groups: string): Record<string, unknown> {
  const claims: Record<string, unknown> = { preferred_username: username.trim() || 'test-user' }
  if (email.trim()) claims.email = email.trim()
  claims[groupsClaim] = groups
    .split(/[\n,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  return claims
}

/** Where a grant applies: an organization, or a team in it. */
export function grantTarget(g: MappedGrant): string {
  return g.team ? `team ${g.team} in ${g.organization}` : g.organization
}

/** Why a grant applies: the matched group of a rule, or the AWX map entry. */
export function grantReason(g: MappedGrant): string {
  return g.map ?? `group ${g.matched_group}`
}
