import { describe, expect, it } from 'vitest'
import { archiveCredentialParams } from './jobSettings'

describe('jobs settings', () => {
  it('offers the credentials of the storage', () => {
    expect(archiveCredentialParams('s3')).toEqual({ credential_type__namespace: 'aws' })
    expect(archiveCredentialParams('azure_blob')).toEqual({ credential_type__namespace: 'azure_rm' })
    expect(archiveCredentialParams('database')).toEqual({ credential_type__namespace__in: 'aws,azure_rm' })
  })
})
