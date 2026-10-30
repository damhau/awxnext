/** Sections of Settings, Jobs. */
export function jobsSection(key: string): string {
  if (/^JOB_EVENTS_(ARCHIVE_|HOT_DAYS)/.test(key)) return 'Event archives'
  if (/TIMEOUT/.test(key)) return 'Timeouts'
  if (/GALAXY|COLLECTIONS|ROLES/.test(key)) return 'Content'
  if (/ISOLATION|MOUNT|TASK_ENV|CALLBACK/.test(key)) return 'Execution'
  return 'General'
}

export const jobsSections = ['General', 'Timeouts', 'Content', 'Execution', 'Event archives']

const archiveOrder = [
  'JOB_EVENTS_HOT_DAYS',
  'JOB_EVENTS_ARCHIVE_STORAGE',
  'JOB_EVENTS_ARCHIVE_BUCKET',
  'JOB_EVENTS_ARCHIVE_ENDPOINT',
  'JOB_EVENTS_ARCHIVE_REGION',
  'JOB_EVENTS_ARCHIVE_PREFIX',
  'JOB_EVENTS_ARCHIVE_CREDENTIAL',
]

/** Orders settings: the archive settings in the order they are filled in
 * (where first), the others by key. */
export function compareSettings(a: string, b: string): number {
  return archiveOrder.indexOf(a) - archiveOrder.indexOf(b) || a.localeCompare(b)
}

/** The credentials JOB_EVENTS_ARCHIVE_CREDENTIAL may be, for the storage
 * chosen: Amazon Web Services for S3, Azure Resource Manager for Azure Blob
 * Storage (both while it is not chosen yet). */
export function archiveCredentialParams(storage: unknown): Record<string, string> {
  if (storage === 's3') return { credential_type__namespace: 'aws' }
  if (storage === 'azure_blob') return { credential_type__namespace: 'azure_rm' }
  return { credential_type__namespace__in: 'aws,azure_rm' }
}
