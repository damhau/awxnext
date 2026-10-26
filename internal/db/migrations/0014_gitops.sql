-- GitOps sources: a git project whose YAML files declare objects of an
-- organization (optionally a folder). Objects they create are locked with
-- managed_by = 'gitops:<id>'.
CREATE TABLE gitops_sources (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    folder_id bigint REFERENCES folders(id) ON DELETE SET NULL,
    project_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    path text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    prune boolean NOT NULL DEFAULT true,
    adopt_existing boolean NOT NULL DEFAULT false,
    sync_on_project_update boolean NOT NULL DEFAULT true,
    status text NOT NULL DEFAULT 'never synced',
    last_sync timestamptz,
    last_revision text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    last_report jsonb NOT NULL DEFAULT '{}',
    UNIQUE (organization_id, name)
);
ALTER TABLE labels ADD COLUMN managed_by text NOT NULL DEFAULT '';
ALTER TABLE schedules ADD COLUMN managed_by text NOT NULL DEFAULT '';
