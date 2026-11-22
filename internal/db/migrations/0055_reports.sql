-- Reports (/api/v2/reports/) read their own copy of each finished job and of
-- each host's results, so that they outlive cleanup_jobs. Triggers fill them
-- from every path that finishes a job or stores its host summaries; the
-- source views below are shared by the triggers and the backfill at the end.
--
-- The columns that decide who may read a run keep the names of unified_jobs'
-- (created_by_id, catalog_request_id, unified_job_template_id,
-- organization_id), so the jobs resource's view applies to them as is.

CREATE FUNCTION report_launch_type(launch_type text, catalog_request_id bigint, event_rule_id bigint) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN catalog_request_id IS NOT NULL THEN 'portal' WHEN event_rule_id IS NOT NULL THEN 'event_rule' ELSE launch_type END
$$;

CREATE TABLE report_runs (
    job_id bigint PRIMARY KEY,
    unified_job_template_id bigint,
    name text NOT NULL,
    organization_id bigint,
    inventory_id bigint,
    project_id bigint,
    launch_type text NOT NULL,
    created_by_id bigint,
    catalog_request_id bigint,
    status text NOT NULL,
    started timestamptz,
    finished timestamptz NOT NULL,
    elapsed numeric(12,3) NOT NULL
);
CREATE INDEX report_runs_finished ON report_runs (finished);

CREATE VIEW report_runs_source AS
SELECT j.id AS job_id, j.unified_job_template_id, j.name, j.organization_id, j.inventory_id, j.project_id,
    report_launch_type(j.launch_type, j.catalog_request_id, j.event_rule_id) AS launch_type,
    j.created_by_id, j.catalog_request_id, j.status, j.started, coalesce(j.finished, j.modified) AS finished, j.elapsed
FROM unified_jobs j
WHERE j.type = 'job' AND j.status IN ('successful', 'failed', 'error', 'canceled');

CREATE FUNCTION report_run_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO report_runs (job_id, unified_job_template_id, name, organization_id, inventory_id, project_id, launch_type,
            created_by_id, catalog_request_id, status, started, finished, elapsed)
        SELECT job_id, unified_job_template_id, name, organization_id, inventory_id, project_id, launch_type,
            created_by_id, catalog_request_id, status, started, finished, elapsed
        FROM report_runs_source WHERE job_id = NEW.id
    ON CONFLICT (job_id) DO UPDATE SET status = EXCLUDED.status, started = EXCLUDED.started,
        finished = EXCLUDED.finished, elapsed = EXCLUDED.elapsed;
    RETURN NULL;
END $$;

CREATE TRIGGER report_run_changed AFTER INSERT OR UPDATE OF status, finished, elapsed ON unified_jobs
    FOR EACH ROW WHEN (NEW.type = 'job' AND NEW.status IN ('successful', 'failed', 'error', 'canceled'))
    EXECUTE FUNCTION report_run_changed();

-- One row per host, day (UTC) and kind of run: a host that runs the same
-- template every hour takes one row a day, not 24.
CREATE TABLE report_host_days (
    day date NOT NULL,
    host_name text NOT NULL,
    host_id bigint,
    unified_job_template_id bigint,
    organization_id bigint,
    inventory_id bigint,
    project_id bigint,
    launch_type text NOT NULL,
    created_by_id bigint,
    catalog_request_id bigint,
    runs integer NOT NULL,
    failed integer NOT NULL,
    unreachable integer NOT NULL,
    CONSTRAINT report_host_days_key UNIQUE NULLS NOT DISTINCT (day, host_name, host_id, unified_job_template_id,
        organization_id, inventory_id, project_id, launch_type, created_by_id, catalog_request_id)
);

CREATE VIEW report_host_runs_source AS
SELECT s.id AS summary_id, (s.created AT TIME ZONE 'UTC')::date AS day, s.host_name, s.host_id,
    j.unified_job_template_id, j.organization_id, j.inventory_id, j.project_id,
    report_launch_type(j.launch_type, j.catalog_request_id, j.event_rule_id) AS launch_type,
    j.created_by_id, j.catalog_request_id, (s.failures > 0)::int AS failed, (s.dark > 0)::int AS unreachable
FROM job_host_summaries s JOIN unified_jobs j ON j.id = s.job_id
WHERE j.type = 'job';

-- A summary stored again (the same job and host) changes the counts, not the runs.
CREATE FUNCTION report_host_summary_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO report_host_days (day, host_name, host_id, unified_job_template_id, organization_id, inventory_id, project_id,
            launch_type, created_by_id, catalog_request_id, runs, failed, unreachable)
        SELECT day, host_name, host_id, unified_job_template_id, organization_id, inventory_id, project_id,
            launch_type, created_by_id, catalog_request_id,
            CASE WHEN TG_OP = 'INSERT' THEN 1 ELSE 0 END,
            failed - CASE WHEN TG_OP = 'INSERT' THEN 0 ELSE (OLD.failures > 0)::int END,
            unreachable - CASE WHEN TG_OP = 'INSERT' THEN 0 ELSE (OLD.dark > 0)::int END
        FROM report_host_runs_source WHERE summary_id = NEW.id
    ON CONFLICT ON CONSTRAINT report_host_days_key DO UPDATE SET runs = report_host_days.runs + EXCLUDED.runs,
        failed = report_host_days.failed + EXCLUDED.failed, unreachable = report_host_days.unreachable + EXCLUDED.unreachable;
    RETURN NULL;
END $$;

CREATE TRIGGER report_host_summary_changed AFTER INSERT OR UPDATE OF failures, dark ON job_host_summaries
    FOR EACH ROW EXECUTE FUNCTION report_host_summary_changed();

CREATE INDEX report_host_days_day ON report_host_days (day);

-- Who may read a row depends on these columns only. A report checks the
-- jobs' rights once per key of its days, then keeps the rows whose key
-- passed: checked on each row, the role lookups take seconds on a large
-- history. portal stands for catalog_request_id IS NOT NULL.
CREATE TABLE report_keys (
    day date NOT NULL,
    unified_job_template_id bigint,
    organization_id bigint,
    created_by_id bigint,
    portal boolean NOT NULL,
    CONSTRAINT report_keys_key UNIQUE NULLS NOT DISTINCT (day, unified_job_template_id, organization_id, created_by_id, portal)
);

CREATE FUNCTION report_runs_inserted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO report_keys (day, unified_job_template_id, organization_id, created_by_id, portal)
        SELECT DISTINCT (finished AT TIME ZONE 'UTC')::date, unified_job_template_id, organization_id, created_by_id,
            catalog_request_id IS NOT NULL
        FROM inserted
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;

CREATE TRIGGER report_runs_inserted AFTER INSERT ON report_runs REFERENCING NEW TABLE AS inserted
    FOR EACH STATEMENT EXECUTE FUNCTION report_runs_inserted();

-- A job finished again moves its run to another day.
CREATE TRIGGER report_runs_updated AFTER UPDATE ON report_runs REFERENCING NEW TABLE AS inserted
    FOR EACH STATEMENT EXECUTE FUNCTION report_runs_inserted();

CREATE FUNCTION report_host_days_inserted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO report_keys (day, unified_job_template_id, organization_id, created_by_id, portal)
        SELECT DISTINCT day, unified_job_template_id, organization_id, created_by_id, catalog_request_id IS NOT NULL
        FROM inserted
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;

CREATE TRIGGER report_host_days_inserted AFTER INSERT ON report_host_days REFERENCING NEW TABLE AS inserted
    FOR EACH STATEMENT EXECUTE FUNCTION report_host_days_inserted();

INSERT INTO report_runs (job_id, unified_job_template_id, name, organization_id, inventory_id, project_id, launch_type,
        created_by_id, catalog_request_id, status, started, finished, elapsed)
    SELECT job_id, unified_job_template_id, name, organization_id, inventory_id, project_id, launch_type,
        created_by_id, catalog_request_id, status, started, finished, elapsed
    FROM report_runs_source;

INSERT INTO report_host_days (day, host_name, host_id, unified_job_template_id, organization_id, inventory_id, project_id,
        launch_type, created_by_id, catalog_request_id, runs, failed, unreachable)
    SELECT day, host_name, host_id, unified_job_template_id, organization_id, inventory_id, project_id,
        launch_type, created_by_id, catalog_request_id, count(*), sum(failed), sum(unreachable)
    FROM report_host_runs_source
    GROUP BY day, host_name, host_id, unified_job_template_id, organization_id, inventory_id, project_id,
        launch_type, created_by_id, catalog_request_id;
