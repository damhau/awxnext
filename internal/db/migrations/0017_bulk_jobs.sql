-- Workflow jobs created by the bulk API (POST /api/v2/bulk/job_launch/),
-- which have no workflow template, as AWX's WorkflowJob.is_bulk_job.
ALTER TABLE unified_jobs ADD COLUMN is_bulk_job boolean NOT NULL DEFAULT false;
