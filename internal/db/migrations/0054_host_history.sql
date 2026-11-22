-- Host history and drift (roadmap M12-04).
--
-- Each host's result for each task of a job, written as the job's events are
-- stored, so that a host's history across jobs needs no scan of job events
-- and outlives them (HOST_HISTORY_DAYS, cleaned by cleanup_host_history).
-- A task that ran more than once in a job on a host keeps its worst result.
CREATE TABLE host_task_results (
    job_id bigint NOT NULL REFERENCES unified_jobs(id) ON DELETE CASCADE,
    host_id bigint NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    unified_job_template_id bigint NOT NULL,
    play text NOT NULL,
    role text NOT NULL,
    task text NOT NULL,
    status text NOT NULL,
    check_mode boolean NOT NULL,
    counter integer NOT NULL,
    created timestamptz NOT NULL,
    PRIMARY KEY (job_id, host_id, play, role, task)
);
CREATE INDEX host_task_results_host ON host_task_results (host_id, created);
CREATE INDEX host_task_results_created ON host_task_results (created);

-- The tasks a check-mode run found would change a host, per template: open
-- until a later run of the template reports the task unchanged on it. The
-- diff is the latest check run's, as its event data held it.
CREATE TABLE host_drift (
    id bigserial PRIMARY KEY,
    host_id bigint NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    inventory_id bigint NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    unified_job_template_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    play text NOT NULL,
    role text NOT NULL,
    task text NOT NULL,
    since timestamptz NOT NULL,
    job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    last_job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    last_seen timestamptz NOT NULL,
    diff jsonb,
    UNIQUE (host_id, unified_job_template_id, play, role, task)
);
CREATE INDEX host_drift_inventory ON host_drift (inventory_id);
