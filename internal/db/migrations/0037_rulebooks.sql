-- Rulebooks (roadmap M14-09): ansible-rulebook's rulebooks, from a
-- project's extensions/eda/rulebooks/ and rulebooks/ at each sync or
-- uploaded, translated into event streams, connectors and rules. Each
-- object an import makes names its rulebook and its key in the rulebook,
-- which ties it to the same piece at the next import.
CREATE TABLE rulebooks (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint REFERENCES organizations(id) ON DELETE CASCADE,
    project_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    -- The file's path in the project; empty for an uploaded rulebook.
    path text NOT NULL DEFAULT '',
    rulesets text NOT NULL DEFAULT '',
    variables jsonb NOT NULL DEFAULT '{}',
    revision text NOT NULL DEFAULT '',
    imported timestamptz,
    import_error text NOT NULL DEFAULT '',
    report jsonb NOT NULL DEFAULT '{}',
    UNIQUE (project_id, name)
);

CREATE INDEX rulebooks_organization ON rulebooks (organization_id);

ALTER TABLE event_streams
    ADD COLUMN rulebook_id bigint REFERENCES rulebooks(id) ON DELETE SET NULL,
    ADD COLUMN rulebook_key text NOT NULL DEFAULT '';
ALTER TABLE event_connectors
    ADD COLUMN rulebook_id bigint REFERENCES rulebooks(id) ON DELETE SET NULL,
    ADD COLUMN rulebook_key text NOT NULL DEFAULT '';
ALTER TABLE event_rules
    ADD COLUMN rulebook_id bigint REFERENCES rulebooks(id) ON DELETE SET NULL,
    ADD COLUMN rulebook_key text NOT NULL DEFAULT '';

CREATE INDEX event_streams_rulebook ON event_streams (rulebook_id) WHERE rulebook_id IS NOT NULL;
CREATE INDEX event_connectors_rulebook ON event_connectors (rulebook_id) WHERE rulebook_id IS NOT NULL;
CREATE INDEX event_rules_rulebook ON event_rules (rulebook_id) WHERE rulebook_id IS NOT NULL;

-- What a launch does once its job ends, as ansible-rulebook's
-- run_job_template does: launch again a job that failed, up to retries
-- times, retry_delay seconds after it ended; post the job's artifacts to
-- artifacts_stream.
ALTER TABLE event_rules
    ADD COLUMN retries integer NOT NULL DEFAULT 0,
    ADD COLUMN retry_delay integer NOT NULL DEFAULT 0,
    ADD COLUMN post_artifacts text NOT NULL DEFAULT '',
    ADD COLUMN artifacts_stream_id bigint REFERENCES event_streams(id) ON DELETE SET NULL;

-- One row per job a rule with retries or post_artifacts launched, until
-- its job ended and the rule did what follows: state moves from waiting
-- (the job runs) to retrying (a failed job waits for due) to done.
CREATE TABLE event_rule_followups (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    rule_id bigint NOT NULL REFERENCES event_rules(id) ON DELETE CASCADE,
    job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    template_id bigint NOT NULL,
    prompts jsonb NOT NULL DEFAULT '{}',
    event jsonb NOT NULL DEFAULT '{}',
    depth integer NOT NULL DEFAULT 0,
    attempt integer NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT 'waiting',
    due timestamptz,
    next_job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    result text NOT NULL DEFAULT ''
);

CREATE INDEX event_rule_followups_open ON event_rule_followups (id) WHERE state <> 'done';
CREATE INDEX event_rule_followups_rule ON event_rule_followups (rule_id);
