-- Event rules (roadmap M14-02): CEL filters over the event log, a key per
-- event, throttling per key, and an action run as the rule's owner.
CREATE TABLE event_rules (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- The ids of the streams whose events the rule reads.
    streams jsonb NOT NULL DEFAULT '[]',
    enabled boolean NOT NULL DEFAULT true,
    owner_id bigint REFERENCES users(id) ON DELETE SET NULL,
    filter text NOT NULL,
    key text NOT NULL DEFAULT '',
    throttle text NOT NULL DEFAULT '',
    throttle_window integer NOT NULL DEFAULT 0,
    action text NOT NULL DEFAULT 'launch',
    unified_job_template_id bigint REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    extra_vars jsonb NOT NULL DEFAULT '{}',
    job_limit text NOT NULL DEFAULT '',
    notification_template_id bigint REFERENCES notification_templates(id) ON DELETE SET NULL,
    message text NOT NULL DEFAULT '',
    target_stream_id bigint REFERENCES event_streams(id) ON DELETE SET NULL,
    event_type text NOT NULL DEFAULT '',
    event_data text NOT NULL DEFAULT '',
    stopped_reason text NOT NULL DEFAULT '',
    last_fired timestamptz,
    managed_by text NOT NULL DEFAULT '',
    UNIQUE (organization_id, name)
);
CREATE INDEX event_rules_streams ON event_rules USING gin (streams jsonb_path_ops);
CREATE INDEX event_rules_template ON event_rules (unified_job_template_id);

-- once_within: until when each key's events are held back.
CREATE TABLE event_rule_keys (
    rule_id bigint NOT NULL REFERENCES event_rules(id) ON DELETE CASCADE,
    key text NOT NULL,
    held_until timestamptz NOT NULL,
    PRIMARY KEY (rule_id, key)
);

-- once_after: the open window of a rule, which fires once at fire_at with
-- the first event of each key it collected.
CREATE TABLE event_rule_windows (
    id bigserial PRIMARY KEY,
    rule_id bigint NOT NULL UNIQUE REFERENCES event_rules(id) ON DELETE CASCADE,
    opened timestamptz NOT NULL,
    fire_at timestamptz NOT NULL
);
CREATE INDEX event_rule_windows_fire_at ON event_rule_windows (fire_at);

-- What each rule decided about each event it matched. (rule, stream,
-- sequence) is the action's idempotency key: it is written in the
-- transaction that acts and saves the stream's position, so an event read
-- again never acts twice. details has room for the evaluation state and
-- guardrails of later tasks.
CREATE TABLE event_rule_decisions (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    rule_id bigint NOT NULL REFERENCES event_rules(id) ON DELETE CASCADE,
    stream_id bigint NOT NULL,
    sequence bigint NOT NULL,
    event_id text NOT NULL,
    event_source text NOT NULL,
    event_type text NOT NULL,
    received timestamptz NOT NULL,
    node text NOT NULL DEFAULT '',
    key text NOT NULL DEFAULT '',
    outcome text NOT NULL,
    reason text NOT NULL DEFAULT '',
    window_id bigint,
    job_id bigint,
    details jsonb NOT NULL DEFAULT '{}',
    UNIQUE (rule_id, stream_id, sequence)
);
CREATE INDEX event_rule_decisions_event ON event_rule_decisions (stream_id, sequence);
CREATE INDEX event_rule_decisions_created ON event_rule_decisions (created);
CREATE INDEX event_rule_decisions_window ON event_rule_decisions (window_id, key) WHERE window_id IS NOT NULL;
CREATE INDEX event_rule_decisions_job ON event_rule_decisions (job_id) WHERE job_id IS NOT NULL;

-- The rule and the event that launched a job.
ALTER TABLE unified_jobs ADD COLUMN event_rule_id bigint REFERENCES event_rules(id) ON DELETE SET NULL;
ALTER TABLE unified_jobs ADD COLUMN stream_event jsonb NOT NULL DEFAULT '{}';
CREATE INDEX unified_jobs_event_rule ON unified_jobs (event_rule_id) WHERE event_rule_id IS NOT NULL;
