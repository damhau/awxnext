import { tr } from './i18n'
// AWX's six LDAP servers share one settings category: AUTH_LDAP_* is the
// default server, AUTH_LDAP_1_* to AUTH_LDAP_5_* the others, tried in order.

const serverRe = /^AUTH_LDAP_(?:([1-5])_)?/

/** The settings prefix of the server a key belongs to. */
export function ldapPrefix(key: string): string {
  return key.match(serverRe)?.[0] ?? ''
}

/** The section heading of a key's server. */
export function ldapServer(key: string): string {
  const n = key.match(serverRe)?.[1]
  return n ? tr('Server {n}', { n }) : tr('Default server')
}

export const ldapServerOrder = [tr('Default server'), ...[1, 2, 3, 4, 5].map((n) => tr('Server {n}', { n }))]

/** Whether a server has a URI, so its section can start open. */
export function ldapServerOn(values: Record<string, unknown>, server: string): boolean {
  const n = server.match(/\d/)?.[0]
  const uri = values[n ? `AUTH_LDAP_${n}_SERVER_URI` : 'AUTH_LDAP_SERVER_URI']
  return typeof uri === 'string' && uri.trim() !== ''
}
