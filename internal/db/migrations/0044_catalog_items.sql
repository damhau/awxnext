-- Catalog items (roadmap M15-01): a job or workflow template published for
-- people who request it without any role on the template. An item's form is
-- a view over the template's survey; its requests run the template with the
-- item's answers, the requester as their launcher.
CREATE TABLE catalog_items (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    unified_job_template_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    kind text NOT NULL DEFAULT 'run_once',
    icon text NOT NULL DEFAULT '',
    category text NOT NULL DEFAULT '',
    owner_team_id bigint REFERENCES teams(id) ON DELETE SET NULL,
    questions jsonb NOT NULL DEFAULT '{}',
    max_lifetime_days integer,
    default_lifetime_days integer,
    managed_by text NOT NULL DEFAULT '',
    UNIQUE (organization_id, name)
);
CREATE INDEX catalog_items_template ON catalog_items (unified_job_template_id);

CREATE TABLE catalog_requests (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint REFERENCES users(id) ON DELETE SET NULL,
    modified_by_id bigint,
    catalog_item_id bigint REFERENCES catalog_items(id) ON DELETE SET NULL,
    item_name text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    team_id bigint REFERENCES teams(id) ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'pending',
    answers jsonb NOT NULL DEFAULT '{}',
    reason text NOT NULL DEFAULT '',
    lifetime_days integer,
    unified_job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL
);
CREATE INDEX catalog_requests_requester ON catalog_requests (created_by_id);
CREATE INDEX catalog_requests_item ON catalog_requests (catalog_item_id);
CREATE INDEX catalog_requests_job ON catalog_requests (unified_job_id);

-- The jobs of a request, the workflow's nodes' included: their launcher
-- doesn't see them for having launched them.
ALTER TABLE unified_jobs ADD COLUMN catalog_request_id bigint;
CREATE INDEX unified_jobs_catalog_request ON unified_jobs (catalog_request_id) WHERE catalog_request_id IS NOT NULL;
