-- The decision log (M14-04): every event a rule reads leaves a decision,
-- a filter it didn't match too, so the log is partitioned by day like the
-- event log, and the cleanup job drops whole days. Its unique keys must
-- hold the day: a decision is received when its event is.
ALTER SEQUENCE event_rule_decisions_id_seq OWNED BY NONE;
CREATE TABLE event_rule_decisions_log (
    id bigint NOT NULL DEFAULT nextval('event_rule_decisions_id_seq'),
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    rule_id bigint NOT NULL,
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
    mode text NOT NULL DEFAULT 'auto',
    fired boolean NOT NULL DEFAULT false,
    guardrail text NOT NULL DEFAULT '',
    approval_id bigint,
    trace_id text NOT NULL DEFAULT ''
) PARTITION BY RANGE (received);
CREATE TABLE event_rule_decisions_default PARTITION OF event_rule_decisions_log DEFAULT;
INSERT INTO event_rule_decisions_log (id, created, modified, rule_id, stream_id, sequence, event_id, event_source, event_type,
        received, node, key, outcome, reason, window_id, job_id, details, mode, fired, guardrail, approval_id)
    SELECT id, created, modified, rule_id, stream_id, sequence, event_id, event_source, event_type,
        received, node, key, outcome, reason, window_id, job_id, details, mode, fired, guardrail, approval_id
    FROM event_rule_decisions;
DROP TABLE event_rule_decisions;
ALTER TABLE event_rule_decisions_log RENAME TO event_rule_decisions;
ALTER SEQUENCE event_rule_decisions_id_seq OWNED BY event_rule_decisions.id;
ALTER TABLE event_rule_decisions
    ADD CONSTRAINT event_rule_decisions_pkey PRIMARY KEY (id, received),
    ADD CONSTRAINT event_rule_decisions_once UNIQUE (rule_id, stream_id, sequence, received),
    ADD CONSTRAINT event_rule_decisions_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES event_rules(id) ON DELETE CASCADE;
CREATE INDEX event_rule_decisions_event ON event_rule_decisions (stream_id, sequence);
CREATE INDEX event_rule_decisions_rule ON event_rule_decisions (rule_id, id);
CREATE INDEX event_rule_decisions_window ON event_rule_decisions (window_id, key) WHERE window_id IS NOT NULL;
CREATE INDEX event_rule_decisions_job ON event_rule_decisions (job_id) WHERE job_id IS NOT NULL;
CREATE INDEX event_rule_decisions_firings ON event_rule_decisions (rule_id, key, received) WHERE fired;
CREATE INDEX event_rule_decisions_approval ON event_rule_decisions (approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX event_rule_decisions_awaiting ON event_rule_decisions (rule_id) WHERE outcome = 'awaiting_approval';

-- The trace a rule launched a job in, as a W3C traceparent: the job's
-- spans join the event's trace.
ALTER TABLE unified_jobs ADD COLUMN trace_parent text NOT NULL DEFAULT '';
