import { describe, expect, it } from 'vitest'
import { aggregatorTarget, loggingSection, testOutcome } from './logging'

const v = (host: unknown, protocol = 'https', port: unknown = null) => ({ LOG_AGGREGATOR_HOST: host, LOG_AGGREGATOR_PROTOCOL: protocol, LOG_AGGREGATOR_PORT: port })

describe('external logging', () => {
  it('groups the settings', () => {
    expect(loggingSection('LOG_AGGREGATOR_HOST')).toBe('Aggregator')
    expect(loggingSection('LOG_AGGREGATOR_VERIFY_CERT')).toBe('Aggregator')
    expect(loggingSection('LOG_AGGREGATOR_LEVEL')).toBe('Messages')
    expect(loggingSection('LOG_AGGREGATOR_LOGGERS')).toBe('Messages')
    expect(loggingSection('LOG_AGGREGATOR_ACTION_QUEUE_SIZE')).toBe('Queue')
    expect(loggingSection('LOG_AGGREGATOR_RSYSLOGD_DEBUG')).toBe('Queue')
  })
  it('builds the address as the server does', () => {
    expect(aggregatorTarget(v('logs.example.com'))).toBe('https://logs.example.com:443')
    expect(aggregatorTarget(v('logs.example.com', 'https', 8088))).toBe('https://logs.example.com:8088')
    expect(aggregatorTarget(v('logs.example.com:9000/in?x=1', 'https', 8088))).toBe('https://logs.example.com:9000/in?x=1')
    expect(aggregatorTarget(v('http://logs.example.com/services/collector/event'))).toBe('http://logs.example.com:80/services/collector/event')
    expect(aggregatorTarget(v('splunk.example.com', 'tcp', 514))).toBe('tcp://splunk.example.com:514')
    expect(aggregatorTarget(v('splunk.example.com', 'udp'))).toBeNull()
    expect(aggregatorTarget(v(''))).toBeNull()
    expect(aggregatorTarget(v(null))).toBeNull()
  })
  it('says how the test went', () => {
    expect(testOutcome(null).ok).toBe(true)
    expect(testOutcome(new Error('Port required for tcp'))).toEqual({ ok: false, text: 'Port required for tcp' })
  })
})
