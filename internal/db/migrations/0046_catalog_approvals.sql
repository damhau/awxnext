-- Approvals before a request runs (roadmap M15-04): an item's rules, each a
-- condition on the request and a group of approvers, and the windows that
-- freeze its requests. A request that a rule or a freeze holds waits, with
-- what it will launch, for one workflow approval per gate.
ALTER TABLE catalog_items
    ADD COLUMN approval_rules jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN freeze_exception_team_id bigint REFERENCES teams(id) ON DELETE SET NULL,
    ADD COLUMN notification_template_id bigint REFERENCES notification_templates(id) ON DELETE SET NULL;

ALTER TABLE maintenance_windows ADD COLUMN catalog_items jsonb NOT NULL DEFAULT '[]';

ALTER TABLE catalog_requests
    ADD COLUMN kind text NOT NULL DEFAULT 'new',
    ADD COLUMN explanation text NOT NULL DEFAULT '',
    -- The variables to launch with once approved, passwords encrypted.
    ADD COLUMN launch jsonb;

CREATE TABLE catalog_request_approvals (
    approval_id bigint PRIMARY KEY REFERENCES unified_jobs(id) ON DELETE CASCADE,
    request_id bigint NOT NULL REFERENCES catalog_requests(id) ON DELETE CASCADE,
    kind text NOT NULL,
    rule text NOT NULL DEFAULT '',
    condition text NOT NULL DEFAULT '',
    approvers bigint[] NOT NULL DEFAULT '{}',
    fallback boolean NOT NULL DEFAULT false,
    required integer NOT NULL DEFAULT 1
);
CREATE INDEX catalog_request_approvals_request ON catalog_request_approvals (request_id);
CREATE INDEX catalog_request_approvals_approvers ON catalog_request_approvals USING gin (approvers);

CREATE TABLE catalog_approval_answers (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    approval_id bigint NOT NULL REFERENCES catalog_request_approvals(approval_id) ON DELETE CASCADE,
    user_id bigint REFERENCES users(id) ON DELETE SET NULL,
    username text NOT NULL,
    approved boolean NOT NULL,
    comment text NOT NULL DEFAULT '',
    as_admin boolean NOT NULL DEFAULT false,
    UNIQUE (approval_id, user_id)
);

-- A notification to people rather than to its template's recipients: the
-- approvers of a request, its requester.
ALTER TABLE notifications ADD COLUMN addresses text[];
