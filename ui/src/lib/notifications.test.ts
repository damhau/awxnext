import { describe, expect, it } from 'vitest'
import { buildMessages, emailConfig, emailValues, hasCustomMessages, isSlackColor, messageEntries, parseRecipients, securityOf, slackChannel, slackConfig, slackValues } from './notifications'

describe('email settings', () => {
  it('reads the security mode from use_tls and use_ssl', () => {
    expect(securityOf({})).toBe('none')
    expect(securityOf({ use_tls: true })).toBe('starttls')
    expect(securityOf({ use_ssl: true })).toBe('ssl')
  })
  it('splits recipients on lines, commas and semicolons', () => {
    expect(parseRecipients(' ops@example.com\n\ndev@example.com, a@b.c;d@e.f ')).toEqual(['ops@example.com', 'dev@example.com', 'a@b.c', 'd@e.f'])
  })
  it('round-trips a configuration', () => {
    const cfg = { host: 'smtp.example.com', port: 587, username: 'u', password: '$encrypted$', use_tls: true, use_ssl: false, sender: 'awx@example.com', recipients: ['a@example.com', 'b@example.com'], timeout: 10 }
    expect(emailConfig(emailValues(cfg))).toEqual(cfg)
  })
  it('fills the usual port for the security mode and the default timeout', () => {
    const v = { ...emailValues({}), host: ' smtp.example.com ', security: 'ssl' as const, sender: 'awx@example.com', recipients: 'a@example.com', timeout: '' }
    expect(emailConfig(v)).toMatchObject({ host: 'smtp.example.com', port: 465, use_ssl: true, use_tls: false, timeout: 30 })
    expect(emailConfig({ ...v, security: 'starttls' }).port).toBe(587)
  })
})

describe('Slack settings', () => {
  it('round-trips a configuration, with hex_color only when set', () => {
    const cfg = { token: '$encrypted$', channels: ['#ops', '#dev, 1700000000.000100'], hex_color: '#36a64f' }
    expect(slackConfig(slackValues(cfg))).toEqual(cfg)
    expect(slackConfig({ ...slackValues(cfg), color: ' ' })).toEqual({ token: '$encrypted$', channels: cfg.channels })
  })
  it('accepts the colors Slack shows', () => {
    expect(['#36a64f', '#F00', ' #abc '].every(isSlackColor)).toBe(true)
    expect(['36a64f', '#36a64', '#ggg', 'red', ''].some(isSlackColor)).toBe(false)
  })
  it('reads a channel and its thread as the server does', () => {
    expect(slackChannel('#ops')).toEqual({ channel: 'ops', thread: '' })
    expect(slackChannel(' #ops, 1700000000.000100 ')).toEqual({ channel: 'ops', thread: '1700000000.000100' })
    expect(slackChannel('C0123ABC')).toEqual({ channel: 'C0123ABC', thread: '' })
    for (const bad of ['#', '', '#ops, 1, 2', '#my ops', '##ops']) expect(slackChannel(bad)).toBeNull()
  })
})

describe('custom messages', () => {
  const saved = {
    success: { message: '{{ job.name }} ok', body: null },
    error: null,
    workflow_approval: { running: { message: null, body: 'Review {{ workflow_url }}' } },
  }
  it('reads saved messages by event', () => {
    expect(messageEntries(saved)).toEqual({ success: { subject: '{{ job.name }} ok', body: '' }, running: { subject: '', body: 'Review {{ workflow_url }}' } })
    expect(hasCustomMessages(saved)).toBe(true)
    expect(hasCustomMessages({ success: null })).toBe(false)
    expect(hasCustomMessages(null)).toBe(false)
  })
  it('builds email messages with subjects and bodies', () => {
    expect(buildMessages('email', messageEntries(saved))).toEqual({
      success: { message: '{{ job.name }} ok', body: null },
      workflow_approval: { running: { message: null, body: 'Review {{ workflow_url }}' } },
    })
  })
  it('keeps only the message for Slack, which has no body', () => {
    expect(buildMessages('slack', { started: { subject: ' {{ job.name }} started ', body: 'ignored' }, error: { subject: '', body: 'ignored' } })).toEqual({
      started: { message: '{{ job.name }} started', body: null },
    })
  })
  it('keeps only the message for Teams, whose card awxnext builds', () => {
    expect(buildMessages('teams', { running: { subject: 'Review {{ approval_node_name }}', body: 'ignored' } })).toEqual({
      workflow_approval: { running: { message: 'Review {{ approval_node_name }}', body: null } },
    })
  })
  it('drops subjects for webhooks and returns null when nothing is set', () => {
    expect(buildMessages('webhook', { success: { subject: 'ignored', body: ' {"ok": true} ' } })).toEqual({ success: { message: null, body: '{"ok": true}' } })
    expect(buildMessages('webhook', { success: { subject: 'ignored', body: '' } })).toBeNull()
    expect(buildMessages('email', {})).toBeNull()
  })
})
