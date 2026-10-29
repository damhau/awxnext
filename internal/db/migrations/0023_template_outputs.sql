-- Outputs a job template declares (name, type, required): its jobs'
-- set_stats artifacts are checked against them (docs/workflows.md).
ALTER TABLE unified_job_templates ADD COLUMN outputs jsonb NOT NULL DEFAULT '[]';
