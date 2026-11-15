-- Guardrails and rule modes (roadmap M14-03): what holds a rule's action
-- back, and whether it acts, asks for approval or only records what it
-- would have done.
ALTER TABLE event_rules
    ADD COLUMN mode text NOT NULL DEFAULT 'auto',
    ADD COLUMN max_concurrent_runs integer NOT NULL DEFAULT 0,
    ADD COLUMN host_lock boolean NOT NULL DEFAULT false,
    ADD COLUMN max_group_share integer NOT NULL DEFAULT 0,
    ADD COLUMN breaker_firings integer NOT NULL DEFAULT 0,
    ADD COLUMN breaker_window integer NOT NULL DEFAULT 3600,
    ADD COLUMN breaker_notification_template_id bigint REFERENCES notification_templates(id) ON DELETE SET NULL,
    ADD COLUMN window_action text NOT NULL DEFAULT 'hold',
    ADD COLUMN approval_timeout integer NOT NULL DEFAULT 3600,
    -- When the rule last entered shadow mode: its shadow breakers count
    -- the firings since.
    ADD COLUMN shadow_since timestamptz;
-- The rules of M14-02 keep acting; new rules start in shadow mode.
ALTER TABLE event_rules ALTER COLUMN mode SET DEFAULT 'shadow';

-- A rule in shadow mode holds its keys apart, so that its decisions don't
-- throttle the rule once it acts.
ALTER TABLE event_rule_keys ADD COLUMN shadow boolean NOT NULL DEFAULT false;
ALTER TABLE event_rule_keys DROP CONSTRAINT event_rule_keys_pkey;
ALTER TABLE event_rule_keys ADD PRIMARY KEY (rule_id, shadow, key);

-- mode is the rule's mode when it decided; fired marks the decisions whose
-- action ran, or would have in shadow mode, or asked for approval: the
-- circuit breaker counts them. guardrail names what held an action back.
ALTER TABLE event_rule_decisions
    ADD COLUMN mode text NOT NULL DEFAULT 'auto',
    ADD COLUMN fired boolean NOT NULL DEFAULT false,
    ADD COLUMN guardrail text NOT NULL DEFAULT '',
    ADD COLUMN approval_id bigint;
UPDATE event_rule_decisions SET fired = true WHERE outcome IN ('launched', 'notified', 'posted', 'failed');
CREATE INDEX event_rule_decisions_firings ON event_rule_decisions (rule_id, key, received) WHERE fired;
CREATE INDEX event_rule_decisions_approval ON event_rule_decisions (approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX event_rule_decisions_awaiting ON event_rule_decisions (rule_id) WHERE outcome = 'awaiting_approval';

-- The circuit breaker of each key a rule tripped, until a person resets it.
CREATE TABLE event_rule_breakers (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    rule_id bigint NOT NULL REFERENCES event_rules(id) ON DELETE CASCADE,
    shadow boolean NOT NULL DEFAULT false,
    key text NOT NULL,
    tripped_at timestamptz,
    firings integer NOT NULL DEFAULT 0,
    reset_at timestamptz,
    reset_by_id bigint REFERENCES users(id) ON DELETE SET NULL,
    UNIQUE (rule_id, shadow, key)
);

-- Maintenance windows and change freezes: recurring periods, as schedules
-- write them, during which rules' actions are held back or wait for
-- approval. A window lists the rules and the inventory groups it covers;
-- one that lists neither covers every rule of its organization.
CREATE TABLE maintenance_windows (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind text NOT NULL DEFAULT 'maintenance',
    rrule text NOT NULL,
    duration integer NOT NULL DEFAULT 3600,
    enabled boolean NOT NULL DEFAULT true,
    event_rules jsonb NOT NULL DEFAULT '[]',
    inventory_groups jsonb NOT NULL DEFAULT '[]',
    managed_by text NOT NULL DEFAULT '',
    UNIQUE (organization_id, name)
);
CREATE INDEX maintenance_windows_rules ON maintenance_windows USING gin (event_rules jsonb_path_ops);
CREATE INDEX maintenance_windows_groups ON maintenance_windows USING gin (inventory_groups jsonb_path_ops);
