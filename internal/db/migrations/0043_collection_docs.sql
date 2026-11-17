-- What a collection version says of itself: its README, read from the
-- artifact, and the docs of its modules and plugins, read by ansible-doc in
-- the background after the import (roadmap M13-05).
ALTER TABLE collection_versions
    ADD COLUMN readme_name text NOT NULL DEFAULT '',
    ADD COLUMN readme text NOT NULL DEFAULT '',
    ADD COLUMN docs_state text NOT NULL DEFAULT 'pending',
    ADD COLUMN docs_message text NOT NULL DEFAULT '',
    ADD COLUMN docs_started timestamptz,
    ADD COLUMN docs_read timestamptz;
CREATE INDEX collection_versions_docs_waiting ON collection_versions (id) WHERE docs_state IN ('pending', 'reading');

CREATE TABLE collection_plugin_docs (
    version_id bigint NOT NULL REFERENCES collection_versions(id) ON DELETE CASCADE,
    plugin_type text NOT NULL,
    name text NOT NULL,
    short_description text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    doc jsonb,
    PRIMARY KEY (version_id, plugin_type, name)
);
