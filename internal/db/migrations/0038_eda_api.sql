-- EDA's API (roadmap M14-10): what the ansible.eda collection sets that
-- awxnext's objects had no place for.

-- A project's HTTP proxy for git, encrypted as other secrets are, and
-- whether git checks the server's certificate.
ALTER TABLE unified_job_templates
    ADD COLUMN scm_proxy text NOT NULL DEFAULT '',
    ADD COLUMN scm_verify_ssl boolean NOT NULL DEFAULT true;

-- The request headers a stream's events keep, separated by commas.
ALTER TABLE event_streams ADD COLUMN additional_data_headers text NOT NULL DEFAULT '';

-- A rulebook's sources fed by an existing event stream instead of their
-- own stream or connector: the source's name, and the stream's id.
ALTER TABLE rulebooks ADD COLUMN source_streams jsonb NOT NULL DEFAULT '{}';

-- EDA's controller tokens. awxnext launches as each rule's owner and
-- uses none: they are kept, encrypted, for the collection's modules.
CREATE TABLE eda_controller_tokens (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    token text NOT NULL,
    UNIQUE (user_id, name)
);

-- An activation is a rulebook of its own, copied from a project's
-- rulebook (source_rulebook) and imported with the activation's
-- variables: turning it on turns on its connectors and rules. The rest is
-- what EDA's API shows of it.
CREATE TABLE eda_activations (
    rulebook_id bigint PRIMARY KEY REFERENCES rulebooks(id) ON DELETE CASCADE,
    source_rulebook_id bigint REFERENCES rulebooks(id) ON DELETE SET NULL,
    organization_id bigint REFERENCES organizations(id) ON DELETE CASCADE,
    decision_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL,
    is_enabled boolean NOT NULL DEFAULT false,
    restart_policy text NOT NULL DEFAULT 'on-failure',
    restart_count integer NOT NULL DEFAULT 0,
    log_level text NOT NULL DEFAULT 'error',
    extra_var text,
    source_mappings text NOT NULL DEFAULT '',
    credentials jsonb NOT NULL DEFAULT '[]',
    restart_on_project_update boolean NOT NULL DEFAULT false,
    enable_persistence boolean NOT NULL DEFAULT false,
    rule_engine_credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    awx_token_id bigint REFERENCES eda_controller_tokens(id) ON DELETE SET NULL,
    k8s_service_name text NOT NULL DEFAULT '',
    log_tracking_id text NOT NULL DEFAULT '',
    rulebook_name text NOT NULL DEFAULT '',
    rulebook_hash text NOT NULL DEFAULT ''
);

CREATE INDEX eda_activations_source ON eda_activations (source_rulebook_id);
