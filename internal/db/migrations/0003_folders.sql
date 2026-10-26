-- Hierarchical folders inside organizations. Role assignments on a folder apply
-- to every object in the folder and its descendants.
CREATE TABLE folders (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    parent_id bigint REFERENCES folders(id) ON DELETE RESTRICT,
    -- ids of this folder and all its ancestors (root first); maintained by the API.
    ancestors bigint[] NOT NULL DEFAULT '{}'
);
CREATE UNIQUE INDEX folders_name ON folders (organization_id, parent_id, name) NULLS NOT DISTINCT;
CREATE INDEX folders_ancestors ON folders USING gin (ancestors);

ALTER TABLE inventories ADD COLUMN folder_id bigint REFERENCES folders(id) ON DELETE RESTRICT;
ALTER TABLE credentials ADD COLUMN folder_id bigint REFERENCES folders(id) ON DELETE RESTRICT;
ALTER TABLE unified_job_templates ADD COLUMN folder_id bigint REFERENCES folders(id) ON DELETE RESTRICT;
ALTER TABLE notification_templates ADD COLUMN folder_id bigint REFERENCES folders(id) ON DELETE RESTRICT;
CREATE INDEX inventories_folder ON inventories (folder_id) WHERE folder_id IS NOT NULL;
CREATE INDEX credentials_folder ON credentials (folder_id) WHERE folder_id IS NOT NULL;
CREATE INDEX ujt_folder ON unified_job_templates (folder_id) WHERE folder_id IS NOT NULL;
CREATE INDEX notification_templates_folder ON notification_templates (folder_id) WHERE folder_id IS NOT NULL;

-- Teams a user belongs to, following nested team membership (cycle-safe via UNION).
CREATE FUNCTION user_teams(uid bigint) RETURNS SETOF bigint LANGUAGE sql STABLE AS $$
    WITH RECURSIVE member_of(id) AS (
        SELECT ra.object_id FROM role_assignments ra
        JOIN role_definitions rd ON rd.id = ra.role_definition_id
        WHERE ra.user_id = uid AND ra.content_type = 'team' AND 'member_team' = ANY(rd.permissions)
        UNION
        SELECT ra.object_id FROM role_assignments ra
        JOIN role_definitions rd ON rd.id = ra.role_definition_id
        JOIN member_of m ON ra.team_id = m.id
        WHERE ra.content_type = 'team' AND 'member_team' = ANY(rd.permissions)
    ) SELECT id FROM member_of
$$;
