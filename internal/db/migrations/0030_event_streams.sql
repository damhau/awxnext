-- Event streams (roadmap M14-01): where outside systems post events, and the
-- log that keeps them for the rules that read it.
CREATE TABLE event_streams (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    uuid text NOT NULL UNIQUE DEFAULT gen_random_uuid()::text,
    stream_type text NOT NULL DEFAULT 'generic',
    credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    test_mode boolean NOT NULL DEFAULT false,
    retention_days integer NOT NULL DEFAULT 7,
    max_payload_bytes integer NOT NULL DEFAULT 1048576,
    rate_limit integer NOT NULL DEFAULT 6000,
    mapping jsonb NOT NULL DEFAULT '{}',
    last_sequence bigint NOT NULL DEFAULT 0,
    last_event_at timestamptz,
    UNIQUE (organization_id, name)
);

-- The log, partitioned by day as job events are. Each stream numbers its
-- events: sequence is given under the stream's row lock, so a reader that
-- sees an event's number has every lower one too.
CREATE TABLE stream_events (
    id bigserial,
    stream_id bigint NOT NULL,
    sequence bigint NOT NULL,
    received timestamptz NOT NULL DEFAULT now(),
    event_id text NOT NULL,
    source text NOT NULL,
    event_type text NOT NULL,
    subject text NOT NULL DEFAULT '',
    event_time timestamptz,
    data_content_type text NOT NULL DEFAULT '',
    data_schema text NOT NULL DEFAULT '',
    data jsonb,
    extensions jsonb NOT NULL DEFAULT '{}',
    test_mode boolean NOT NULL DEFAULT false
) PARTITION BY RANGE (received);
CREATE TABLE stream_events_default PARTITION OF stream_events DEFAULT;
CREATE INDEX stream_events_stream ON stream_events (stream_id, sequence);
CREATE INDEX stream_events_id ON stream_events (id);

-- The IDs already received, outside the partitioned log: a unique index
-- there would have to include the day. Neither table references the
-- stream, so deleting one does not delete its log at once: the cleanup job
-- removes what streams no longer exist left.
CREATE TABLE event_stream_dedup (
    stream_id bigint NOT NULL,
    source text NOT NULL,
    event_id text NOT NULL,
    sequence bigint NOT NULL,
    received timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (stream_id, source, event_id)
);
CREATE INDEX event_stream_dedup_received ON event_stream_dedup (received);

-- Where each consumer of the log (a rulebook, a backtest) has read to.
CREATE TABLE event_consumer_positions (
    consumer text NOT NULL,
    stream_id bigint NOT NULL REFERENCES event_streams(id) ON DELETE CASCADE,
    position bigint NOT NULL DEFAULT 0,
    modified timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, stream_id)
);
