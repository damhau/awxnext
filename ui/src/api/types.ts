// Shapes of the API objects the UI relies on. AWX objects carry many more
// fields; the index signature keeps the rest reachable without casts.

/* eslint-disable @typescript-eslint/no-explicit-any */
export type Any = Record<string, any>

export interface Ref {
  id: number
  name: string
  description?: string
  [k: string]: any
}

export interface UserRef {
  id: number
  username: string
  first_name?: string
  last_name?: string
}

export type JobStatus = 'new' | 'pending' | 'waiting' | 'running' | 'successful' | 'failed' | 'error' | 'canceled' | 'never updated' | 'ok' | 'missing' | 'none' | 'updating'

export interface RecentJob {
  id: number
  status: JobStatus
  finished: string | null
  canceled_on?: string | null
  type: string
}

export interface Summary {
  organization?: Ref
  folder?: Ref
  inventory?: Ref
  project?: Ref
  created_by?: UserRef
  modified_by?: UserRef
  recent_jobs?: RecentJob[]
  last_job?: { id: number; status: JobStatus; failed: boolean; finished: string | null; name?: string }
  labels?: { count: number; results: Ref[] }
  credentials?: (Ref & { kind?: string; credential_type_id?: number })[]
  user_capabilities?: Record<string, boolean>
  object_roles?: Record<string, { id: number; name: string; description: string }>
  survey?: { title: string; description: string }
  unified_job_template?: Ref & { unified_job_type?: string }
  job_template?: Ref
  workflow_job_template?: Ref
  execution_environment?: Ref & { image?: string }
  instance_group?: Ref
  schedule?: Ref
  [k: string]: any
}

export interface Base {
  id: number
  type: string
  url: string
  related: Record<string, string>
  summary_fields: Summary
  created?: string
  modified?: string
  name: string
  description?: string
  [k: string]: any
}

export interface Template extends Base {
  status: JobStatus
  last_job_run: string | null
  next_job_run: string | null
  last_job_failed: boolean
  organization: number | null
  folder: number | null
  managed_by?: string
}

export interface Job extends Base {
  status: JobStatus
  failed: boolean
  started: string | null
  finished: string | null
  elapsed: number
  launch_type: string
  launched_by?: { id: number; name: string; type: string; url: string }
  job_explanation: string
  unified_job_template: number | null
  execution_node?: string
  controller_node?: string
}

export interface Me {
  id: number
  username: string
  first_name: string
  last_name: string
  email: string
  is_superuser: boolean
  is_system_auditor: boolean
  [k: string]: any
}

export interface Folder extends Base {
  organization: number
  parent: number | null
}

export interface JobEvent {
  id: number
  counter: number
  event: string
  event_display?: string
  event_level?: number
  failed: boolean
  changed: boolean
  host_name: string
  host?: number | null
  task: string
  play: string
  role: string
  stdout: string
  start_line: number
  end_line: number
  uuid: string
  parent_uuid: string
  created: string
  event_data: Any
}

export interface HostSummary {
  id: number
  host: number | null
  host_name: string
  changed: number
  dark: number
  failures: number
  ok: number
  processed: number
  skipped: number
  failed: boolean
  ignored: number
  rescued: number
}

export interface FieldMeta {
  type: string
  label?: string
  help_text?: string
  required?: boolean
  read_only?: boolean
  default?: any
  choices?: [string | number, string][]
  max_length?: number
  filterable?: boolean
  [k: string]: any
}

export interface OptionsMeta {
  name: string
  description: string
  actions?: { POST?: Record<string, FieldMeta>; PUT?: Record<string, FieldMeta>; GET?: Record<string, FieldMeta> }
  search_fields?: string[]
  related_search_fields?: string[]
  [k: string]: any
}
