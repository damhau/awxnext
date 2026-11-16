-- Checking that a fix worked (roadmap M14-08): after its action, a rule
-- waits for an event that resolves the key, or runs a check template, and
-- records whether the action fixed it. A rule whose fix doesn't work
-- escalates: a notification, or an approval before the key's next try.
ALTER TABLE event_rules
    ADD COLUMN check_kind text NOT NULL DEFAULT '',
    ADD COLUMN check_condition text NOT NULL DEFAULT '',
    ADD COLUMN check_window integer NOT NULL DEFAULT 0,
    ADD COLUMN check_template_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    ADD COLUMN check_notification_template_id bigint REFERENCES notification_templates(id) ON DELETE SET NULL,
    ADD COLUMN check_approval boolean NOT NULL DEFAULT false;

-- One check per key an action ran for. state moves from acting (the
-- action's job runs) to watching (an event check waits for the resolving
-- event until deadline) or checking (a check template launches at
-- deadline, then runs), then settled. opened is when the event that fired
-- the action was received: resolving events count from then. A check of
-- the rule in shadow mode only watches: the alert resolving shows that a
-- fix would have looked fixed anyway.
CREATE TABLE event_rule_checks (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    rule_id bigint NOT NULL REFERENCES event_rules(id) ON DELETE CASCADE,
    shadow boolean NOT NULL DEFAULT false,
    mode text NOT NULL DEFAULT 'auto',
    kind text NOT NULL,
    key text NOT NULL DEFAULT '',
    decisions jsonb NOT NULL DEFAULT '[]',
    event jsonb NOT NULL DEFAULT '{}',
    action text NOT NULL DEFAULT '',
    job_id bigint,
    check_job_id bigint,
    state text NOT NULL,
    outcome text NOT NULL DEFAULT 'pending',
    reason text NOT NULL DEFAULT '',
    opened timestamptz NOT NULL,
    action_finished timestamptz,
    deadline timestamptz,
    resolved_by jsonb,
    settled timestamptz,
    escalated boolean NOT NULL DEFAULT false,
    details jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX event_rule_checks_open ON event_rule_checks (rule_id, key) WHERE state <> 'settled';
CREATE INDEX event_rule_checks_due ON event_rule_checks (state, deadline) WHERE state <> 'settled';
CREATE INDEX event_rule_checks_settled ON event_rule_checks (rule_id, key, settled) WHERE state = 'settled';
CREATE INDEX event_rule_checks_job ON event_rule_checks (job_id) WHERE job_id IS NOT NULL;
CREATE INDEX event_rule_checks_check_job ON event_rule_checks (check_job_id) WHERE check_job_id IS NOT NULL;

-- A decision whose action is checked names its check, and its outcome once
-- settled.
ALTER TABLE event_rule_decisions
    ADD COLUMN check_id bigint,
    ADD COLUMN check_outcome text NOT NULL DEFAULT '';
CREATE INDEX event_rule_decisions_check ON event_rule_decisions (check_id) WHERE check_id IS NOT NULL;
