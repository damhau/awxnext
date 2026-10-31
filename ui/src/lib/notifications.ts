import { asIs, tr } from './i18n'
/** Notification template forms: email settings and custom messages per event. */

export type Security = 'none' | 'starttls' | 'ssl'

export interface EmailConfig {
  host?: string
  port?: number
  username?: string
  password?: string
  use_tls?: boolean
  use_ssl?: boolean
  sender?: string
  recipients?: string[]
  timeout?: number
}

/** How an email configuration secures the connection. */
export function securityOf(cfg: EmailConfig): Security {
  if (cfg.use_ssl) return 'ssl'
  if (cfg.use_tls) return 'starttls'
  return 'none'
}

/** The usual port for a security mode. */
export const DEFAULT_PORT: Record<Security, number> = { none: 25, starttls: 587, ssl: 465 }

/** Recipients typed one per line, or separated by commas or semicolons. */
export function parseRecipients(text: string): string[] {
  return text
    .split(/[\n,;]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

export interface EmailValues {
  host: string
  port: string
  security: Security
  username: string
  password: string
  sender: string
  recipients: string
  timeout: string
}

export function emailValues(cfg: EmailConfig): EmailValues {
  return {
    host: cfg.host ?? '',
    port: cfg.port ? String(cfg.port) : '',
    security: securityOf(cfg),
    username: cfg.username ?? '',
    password: cfg.password ?? '',
    sender: cfg.sender ?? '',
    recipients: (cfg.recipients ?? []).join('\n'),
    timeout: cfg.timeout ? String(cfg.timeout) : '30',
  }
}

/** The notification_configuration an email form sends. */
export function emailConfig(v: EmailValues): EmailConfig {
  return {
    host: v.host.trim(),
    port: Number(v.port) || DEFAULT_PORT[v.security],
    username: v.username,
    password: v.password,
    use_tls: v.security === 'starttls',
    use_ssl: v.security === 'ssl',
    sender: v.sender.trim(),
    recipients: parseRecipients(v.recipients),
    timeout: Number(v.timeout) || 30,
  }
}

export type Message = { message?: string | null; body?: string | null } | null
export type Messages = {
  started?: Message
  success?: Message
  error?: Message
  workflow_approval?: { approved?: Message; denied?: Message; running?: Message; timed_out?: Message } | null
} | null

export interface MessageEvent {
  key: string
  label: string
  group?: 'workflow_approval'
}

export const MESSAGE_EVENTS: MessageEvent[] = [
  { key: 'started', label: tr('Job started') },
  { key: 'success', label: tr('Job succeeded') },
  { key: 'error', label: tr('Job failed') },
  { key: 'running', label: tr('Approval requested'), group: asIs('workflow_approval') },
  { key: 'approved', label: tr('Approval granted'), group: asIs('workflow_approval') },
  { key: 'denied', label: tr('Approval denied'), group: asIs('workflow_approval') },
  { key: 'timed_out', label: tr('Approval timed out'), group: asIs('workflow_approval') },
]

/** A custom message being edited: the subject (email only) and the body. */
export interface MessageEntry {
  subject: string
  body: string
}

function messageOf(m: Messages, e: MessageEvent): Message | undefined {
  if (!m) return undefined
  return e.group ? (m.workflow_approval as Record<string, Message> | null | undefined)?.[e.key] : (m as Record<string, Message>)[e.key]
}

/** The entries of saved messages, by event key. */
export function messageEntries(m: Messages): Record<string, MessageEntry> {
  const out: Record<string, MessageEntry> = {}
  for (const e of MESSAGE_EVENTS) {
    const msg = messageOf(m, e)
    if (msg?.message || msg?.body) out[e.key] = { subject: msg.message ?? '', body: msg.body ?? '' }
  }
  return out
}

/**
 * The messages to save: an event's subject and body when set (webhooks have
 * no subject), or null when nothing is customized.
 */
export function buildMessages(type: string, entries: Record<string, MessageEntry>): Messages {
  const m: Record<string, unknown> = {}
  const wa: Record<string, unknown> = {}
  for (const e of MESSAGE_EVENTS) {
    const entry = entries[e.key]
    const subject = type === 'email' ? entry?.subject.trim() : ''
    const body = entry?.body.trim()
    if (!subject && !body) continue
    const msg = { message: subject || null, body: body || null }
    if (e.group) wa[e.key] = msg
    else m[e.key] = msg
  }
  if (Object.keys(wa).length) m.workflow_approval = wa
  return Object.keys(m).length ? (m as Messages) : null
}

/** Whether saved messages customize anything. */
export function hasCustomMessages(m: Messages): boolean {
  return Object.keys(messageEntries(m)).length > 0
}
