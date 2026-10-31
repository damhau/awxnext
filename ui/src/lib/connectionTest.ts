// The login methods' connection tests (POST settings/connections/test/):
// the settings each sends, unsaved changes included, what its steps are
// called, and SAML's identity providers whose metadata it reads
// (internal/auth/conntest.go).

import { tr } from './i18n'

export type StepStatus = 'ok' | 'warning' | 'skipped' | 'failed'

export interface TestStep {
  step: string
  /** The SAML identity provider the step is about. */
  idp?: string
  status: StepStatus
  detail: string
}

/** Whether a setting is one of a login's: ldap, ldap1… ldap5, oidc or saml. */
export function testsKey(login: string, key: string, category: string | undefined): boolean {
  if (login === 'oidc' || login === 'saml') return category === login
  const server = /^ldap([1-5]?)$/.exec(login)
  if (!server || category !== 'ldap') return false
  return (/^AUTH_LDAP_([1-5])_/.exec(key)?.[1] ?? '') === server[1]
}

const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null)

/** A login's settings changed and not saved: the test runs with them over the saved ones. */
export function unsavedFor(login: string, categories: Record<string, string>, values: Record<string, unknown>, saved: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.keys(values).filter((k) => testsKey(login, k, categories[k]) && !same(values[k], saved[k])).map((k) => [k, values[k]]))
}

/** What a step checks, in a few words. */
export function stepLabel(step: string): string {
  switch (step) {
    case 'settings':
      return tr('Settings')
    case 'connect':
      return tr('Connection')
    case 'bind':
      return tr('Bind')
    case 'user':
      return tr('User')
    case 'password':
      return tr('Password')
    case 'groups':
      return tr('Groups')
    case 'allowed':
      return tr('Required and denied groups')
    case 'access':
      return tr('Organizations and teams')
    case 'discovery':
      return tr('Discovery document')
    case 'scopes':
      return tr('Scopes')
    case 'keys':
      return tr('Signing keys')
    case 'service-provider':
      return tr('Service provider')
    case 'certificate':
      return tr('Certificate')
    case 'metadata':
      return tr('Metadata')
    case 'sso':
      return tr('Sign-in URL')
  }
  return step
}

/** A step's status, as a status cell shows it and as words. */
export const stepState: Record<StepStatus, string> = { ok: 'ok', warning: 'changed', skipped: 'skipped', failed: 'failed' }

export function statusText(s: StepStatus): string {
  return { ok: tr('Passed'), warning: tr('Warning'), skipped: tr('Skipped'), failed: tr('Failed') }[s]
}

/** SAML's identity providers, by name: their entity ID, and whether the test can read metadata there. */
export function samlIdps(v: unknown): { name: string; entityId: string; isUrl: boolean }[] {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return []
  return Object.entries(v as Record<string, unknown>)
    .map(([name, idp]) => {
      const entityId = idp && typeof idp === 'object' ? String((idp as Record<string, unknown>).entity_id ?? '') : ''
      return { name, entityId, isUrl: /^https?:\/\/[^/]/.test(entityId) }
    })
    .sort((a, b) => a.name.localeCompare(b.name))
}
