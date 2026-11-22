-- Launch policies (roadmap M12-07): a CEL condition that refuses a launch
-- when it holds. A policy without an organization is global and checks
-- every launch; the others check the launches of the organizations,
-- inventories and templates they are linked to.
CREATE TABLE launch_policies (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint REFERENCES organizations(id) ON DELETE CASCADE,
    condition text NOT NULL,
    message text NOT NULL DEFAULT '',
    mode text NOT NULL DEFAULT 'shadow',
    managed_by text NOT NULL DEFAULT '',
    CONSTRAINT launch_policies_name UNIQUE NULLS NOT DISTINCT (organization_id, name)
);

CREATE TABLE launch_policy_links (
    content_type text NOT NULL,
    object_id bigint NOT NULL,
    launch_policy_id bigint NOT NULL REFERENCES launch_policies(id) ON DELETE CASCADE,
    PRIMARY KEY (content_type, object_id, launch_policy_id)
);
CREATE INDEX launch_policy_links_policy ON launch_policy_links (launch_policy_id);

-- What each check decided, per policy. A launch refused at its caller has
-- no job. The names are kept for when the policy or template is deleted.
CREATE TABLE launch_policy_decisions (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    launch_policy_id bigint REFERENCES launch_policies(id) ON DELETE SET NULL,
    policy_name text NOT NULL,
    mode text NOT NULL,
    outcome text NOT NULL,
    message text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    unified_job_id bigint,
    job_type text NOT NULL,
    unified_job_template_id bigint,
    template_name text NOT NULL DEFAULT '',
    inventory_id bigint,
    user_id bigint,
    launch_type text NOT NULL DEFAULT ''
);
CREATE INDEX launch_policy_decisions_policy ON launch_policy_decisions (launch_policy_id, id DESC);
CREATE INDEX launch_policy_decisions_job ON launch_policy_decisions (unified_job_id) WHERE unified_job_id IS NOT NULL;
CREATE INDEX launch_policy_decisions_created ON launch_policy_decisions (created);
