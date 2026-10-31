/** The credentials JOB_EVENTS_ARCHIVE_CREDENTIAL may be, for the storage
 * chosen: Amazon Web Services for S3, Azure Resource Manager for Azure Blob
 * Storage (both while it is not chosen yet). */
export function archiveCredentialParams(storage: unknown): Record<string, string> {
  if (storage === 's3') return { credential_type__namespace: 'aws' }
  if (storage === 'azure_blob') return { credential_type__namespace: 'azure_rm' }
  return { credential_type__namespace__in: 'aws,azure_rm' }
}
