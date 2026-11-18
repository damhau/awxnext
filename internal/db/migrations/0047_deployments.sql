-- Deployments (roadmap M15-05): what a request of an item that creates one
-- provisioned, who owns it and the outputs of its runs. Its day-2 actions
-- and its teardown are catalog requests on it, so they share the requests'
-- approvals and history.
ALTER TABLE catalog_items
    ADD COLUMN deployment_name text NOT NULL DEFAULT '',
    ADD COLUMN connection jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN actions jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN teardown_template_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL;

CREATE TABLE deployments (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint REFERENCES users(id) ON DELETE SET NULL,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    catalog_item_id bigint REFERENCES catalog_items(id) ON DELETE SET NULL,
    item_name text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    request_id bigint REFERENCES catalog_requests(id) ON DELETE SET NULL,
    owner_id bigint REFERENCES users(id) ON DELETE SET NULL,
    team_id bigint REFERENCES teams(id) ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'active',
    explanation text NOT NULL DEFAULT '',
    -- Secret outputs stay encrypted, as in the jobs' artifacts.
    outputs jsonb NOT NULL DEFAULT '{}',
    left_behind jsonb,
    expires_at timestamptz,
    deleted_at timestamptz
);
CREATE INDEX deployments_owner ON deployments (owner_id);
CREATE INDEX deployments_team ON deployments (team_id);
CREATE INDEX deployments_item ON deployments (catalog_item_id);

ALTER TABLE catalog_requests ADD COLUMN deployment_id bigint REFERENCES deployments(id) ON DELETE SET NULL;
CREATE INDEX catalog_requests_deployment ON catalog_requests (deployment_id);
