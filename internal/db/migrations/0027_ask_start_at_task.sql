-- awxnext's own launch prompt for start_at_task (AWX has none).
ALTER TABLE unified_job_templates ADD COLUMN ask_start_at_task_on_launch boolean NOT NULL DEFAULT false;
