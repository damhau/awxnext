import { describe, expect, it } from 'vitest'
import { storageCredentialParams, storageOfCredential } from './jobSettings'

describe('jobs settings', () => {
  it('offers the credentials of the storage', () => {
    expect(storageCredentialParams('s3')).toEqual({ credential_type__namespace: 'aws' })
    expect(storageCredentialParams('azure_blob')).toEqual({ credential_type__namespace: 'azure_rm' })
    expect(storageCredentialParams('database')).toEqual({ credential_type__namespace__in: 'aws,azure_rm' })
  })

  it('knows the storage of each credential setting', () => {
    expect(storageOfCredential.JOB_EVENTS_ARCHIVE_CREDENTIAL).toBe('JOB_EVENTS_ARCHIVE_STORAGE')
    expect(storageOfCredential.HUB_STORAGE_CREDENTIAL).toBe('HUB_STORAGE')
  })
})
