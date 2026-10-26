-- Compressed per-job archives of job events that left the hot window.
CREATE TABLE job_event_archives (
    job_id bigint PRIMARY KEY REFERENCES unified_jobs(id) ON DELETE CASCADE,
    job_created timestamptz NOT NULL,
    event_count integer NOT NULL,
    data bytea NOT NULL,
    archived timestamptz NOT NULL DEFAULT now(),
    -- Set while the events are restored into job_events for viewing.
    restored timestamptz
);
