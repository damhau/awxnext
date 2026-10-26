-- Full snapshots of objects after every change, for history, diff and revert.
CREATE TABLE object_versions (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    resource text NOT NULL,
    object_id bigint NOT NULL,
    version integer NOT NULL,
    operation text NOT NULL,
    actor_id bigint REFERENCES users(id) ON DELETE SET NULL,
    data jsonb NOT NULL,
    changes jsonb NOT NULL DEFAULT '{}',
    comment text NOT NULL DEFAULT '',
    UNIQUE (resource, object_id, version)
);
CREATE INDEX object_versions_created ON object_versions (created);
