-- Windows, sequences and absence (roadmap M14-07): a rule can wait, for
-- each key, for a count of events, all of several conditions, a sequence
-- of them, or one not followed by another, within a window.
ALTER TABLE event_rules
    ADD COLUMN pattern text NOT NULL DEFAULT '',
    ADD COLUMN conditions jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN pattern_count integer NOT NULL DEFAULT 0,
    ADD COLUMN pattern_window integer NOT NULL DEFAULT 0;

-- The open window of each key: the events it holds, with their decisions,
-- and its clock. deadline is when the window next changes with time alone:
-- the engine's timer moves the windows due. A rule in shadow mode keeps
-- its windows apart.
CREATE TABLE event_rule_patterns (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    rule_id bigint NOT NULL REFERENCES event_rules(id) ON DELETE CASCADE,
    shadow boolean NOT NULL DEFAULT false,
    key text NOT NULL,
    opened timestamptz NOT NULL,
    clock timestamptz NOT NULL,
    deadline timestamptz NOT NULL,
    events jsonb NOT NULL DEFAULT '[]',
    progress jsonb NOT NULL DEFAULT '{}',
    UNIQUE (rule_id, shadow, key)
);
CREATE INDEX event_rule_patterns_deadline ON event_rule_patterns (deadline);
