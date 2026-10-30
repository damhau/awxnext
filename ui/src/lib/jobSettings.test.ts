import { describe, expect, it } from 'vitest'
import { archiveCredentialParams, compareSettings, jobsSection } from './jobSettings'

describe('jobs settings', () => {
  it('groups the settings', () => {
    expect(jobsSection('JOB_EVENTS_ARCHIVE_BUCKET')).toBe('Event archives')
    expect(jobsSection('JOB_EVENTS_ARCHIVE_CREDENTIAL')).toBe('Event archives')
    expect(jobsSection('JOB_EVENTS_HOT_DAYS')).toBe('Event archives')
    expect(jobsSection('JOB_EVENT_MAX_RATE')).toBe('General')
    expect(jobsSection('DEFAULT_JOB_TIMEOUT')).toBe('Timeouts')
    expect(jobsSection('AWX_COLLECTIONS_ENABLED')).toBe('Content')
    expect(jobsSection('AWX_ISOLATION_SHOW_PATHS')).toBe('Execution')
  })
  it('orders the archive settings as they are filled in', () => {
    const keys = ['JOB_EVENTS_ARCHIVE_CREDENTIAL', 'JOB_EVENTS_ARCHIVE_BUCKET', 'JOB_EVENTS_HOT_DAYS', 'JOB_EVENTS_ARCHIVE_STORAGE', 'MAX_FORKS', 'AD_HOC_COMMANDS']
    expect([...keys].sort(compareSettings)).toEqual([
      'AD_HOC_COMMANDS',
      'MAX_FORKS',
      'JOB_EVENTS_HOT_DAYS',
      'JOB_EVENTS_ARCHIVE_STORAGE',
      'JOB_EVENTS_ARCHIVE_BUCKET',
      'JOB_EVENTS_ARCHIVE_CREDENTIAL',
    ])
  })
  it('offers the credentials of the storage', () => {
    expect(archiveCredentialParams('s3')).toEqual({ credential_type__namespace: 'aws' })
    expect(archiveCredentialParams('azure_blob')).toEqual({ credential_type__namespace: 'azure_rm' })
    expect(archiveCredentialParams('database')).toEqual({ credential_type__namespace__in: 'aws,azure_rm' })
  })
})
