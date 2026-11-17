/** The setting that chooses the object storage a credential setting signs
 * in to. */
export const storageOfCredential: Record<string, string> = {
  JOB_EVENTS_ARCHIVE_CREDENTIAL: 'JOB_EVENTS_ARCHIVE_STORAGE',
  HUB_STORAGE_CREDENTIAL: 'HUB_STORAGE',
}

/** The credentials an object storage credential setting may be, for the
 * storage chosen: Amazon Web Services for S3, Azure Resource Manager for
 * Azure Blob Storage (both while it is not chosen yet). */
export function storageCredentialParams(storage: unknown): Record<string, string> {
  if (storage === 's3') return { credential_type__namespace: 'aws' }
  if (storage === 'azure_blob') return { credential_type__namespace: 'azure_rm' }
  return { credential_type__namespace__in: 'aws,azure_rm' }
}
