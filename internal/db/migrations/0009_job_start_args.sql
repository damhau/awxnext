-- Launch-time secrets (credential passwords) for a job, encrypted; cleared when the job finishes.
ALTER TABLE unified_jobs ADD COLUMN start_args text NOT NULL DEFAULT '';
-- Survey password variable names of a template's survey for display masking.
CREATE INDEX unified_jobs_created_by ON unified_jobs (created_by_id);
