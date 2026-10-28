-- Jobs that ended without running (a failed dependency, canceled while
-- pending) kept event_processing_finished false, so clients waiting on it
-- never stopped. Nothing more is stored for a job that has ended.
UPDATE unified_jobs SET event_processing_finished = true
WHERE status IN ('successful', 'failed', 'error', 'canceled') AND NOT event_processing_finished;
