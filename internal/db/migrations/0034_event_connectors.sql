-- Event connectors (roadmap M14-06): an ansible.eda source plugin run where
-- jobs run, posting its events to an event stream. A connector runs on one
-- node at a time: the leader places it, the controller node starts it and
-- applies its restart policy. Each placement is a new generation, with a
-- token of its own; moving the connector revokes the old one.
CREATE TABLE event_connectors (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    event_stream_id bigint NOT NULL REFERENCES event_streams(id) ON DELETE CASCADE,
    source text NOT NULL,
    args jsonb NOT NULL DEFAULT '{}',
    filters jsonb NOT NULL DEFAULT '[]',
    credentials jsonb NOT NULL DEFAULT '[]',
    execution_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL,
    instance_group_id bigint REFERENCES instance_groups(id) ON DELETE SET NULL,
    python text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    restart_policy text NOT NULL DEFAULT 'on-failure',
    restart_delay integer NOT NULL DEFAULT 5,
    max_restarts integer NOT NULL DEFAULT 0,
    buffer_size integer NOT NULL DEFAULT 1000,
    managed_by text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending',
    status_explanation text NOT NULL DEFAULT '',
    controller_node text NOT NULL DEFAULT '',
    execution_node text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 0,
    token_hash text NOT NULL DEFAULT '',
    started timestamptz,
    next_start timestamptz,
    restart_count integer NOT NULL DEFAULT 0,
    stats jsonb NOT NULL DEFAULT '{}',
    -- The node that gave the connector back as it stopped: placement passes
    -- it over until it is seen again.
    released_by text NOT NULL DEFAULT '',
    released_at timestamptz,
    UNIQUE (organization_id, name)
);
CREATE INDEX event_connectors_stream ON event_connectors (event_stream_id);
CREATE INDEX event_connectors_controller ON event_connectors (controller_node) WHERE controller_node <> '';
CREATE INDEX event_connectors_credentials ON event_connectors USING gin (credentials jsonb_path_ops);

-- Each run of a connector, with the tail of its log. The last runs of each
-- connector are kept.
CREATE TABLE event_connector_runs (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    connector_id bigint NOT NULL REFERENCES event_connectors(id) ON DELETE CASCADE,
    generation bigint NOT NULL,
    controller_node text NOT NULL DEFAULT '',
    execution_node text NOT NULL DEFAULT '',
    started timestamptz NOT NULL DEFAULT now(),
    finished timestamptz,
    status text NOT NULL DEFAULT 'running',
    rc integer,
    explanation text NOT NULL DEFAULT '',
    log text NOT NULL DEFAULT '',
    stats jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX event_connector_runs_connector ON event_connector_runs (connector_id, id);
