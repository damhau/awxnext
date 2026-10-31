import { tr } from './i18n'
// The external logging settings (LOG_AGGREGATOR_*): their sections, and
// where the server sends messages, built as it builds the address.

export const loggingSections = [tr('Aggregator'), tr('Messages'), tr('Queue')]

export function loggingSection(key: string): string {
  if (/LOGGERS|_LEVEL|INDIVIDUAL_FACTS|TOWER_UUID/.test(key)) return tr('Messages')
  if (/QUEUE|DISK|RSYSLOGD/.test(key)) return tr('Queue')
  return tr('Aggregator')
}

/**
 * The address messages go to: for HTTPS/HTTP, https:// unless the host says
 * http://, the host's port, else LOG_AGGREGATOR_PORT, else 443 or 80; for TCP
 * and UDP, the host and port. Null while it cannot be built.
 */
export function aggregatorTarget(v: Record<string, unknown>): string | null {
  const host = typeof v.LOG_AGGREGATOR_HOST === 'string' ? v.LOG_AGGREGATOR_HOST.trim() : ''
  if (!host) return null
  const port = typeof v.LOG_AGGREGATOR_PORT === 'number' ? v.LOG_AGGREGATOR_PORT : 0
  const protocol = v.LOG_AGGREGATOR_PROTOCOL
  if (protocol === 'tcp' || protocol === 'udp') return port ? `${protocol}://${host}:${port}` : null
  let u: URL
  try {
    u = new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(host) ? host : host.startsWith('//') ? `http:${host}` : `https://${host}`)
  } catch {
    return null
  }
  if (!u.hostname) return null
  const scheme = u.protocol === 'https:' ? 'https' : 'http'
  const p = u.port ? Number(u.port) : port || (scheme === 'https' ? 443 : 80)
  const path = u.pathname === '/' && !/^[^?]*\/\/[^/?]+\/|^[^/?:]+(:\d+)?\//.test(host) ? '' : u.pathname
  return `${scheme}://${u.hostname}:${p}${path}${u.search}`
}

/** What the test button says, from the endpoint's answer. */
export function testOutcome(error: unknown): { ok: boolean; text: string } {
  if (!error) return { ok: true, text: tr('Test message sent: look for "AWX Connection Test Message" at the aggregator.') }
  return { ok: false, text: error instanceof Error ? error.message : String(error) }
}
