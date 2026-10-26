-- AWX Next initial schema.

CREATE TABLE users (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    username text NOT NULL UNIQUE,
    first_name text NOT NULL DEFAULT '',
    last_name text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    password text NOT NULL DEFAULT '',
    is_superuser boolean NOT NULL DEFAULT false,
    is_system_auditor boolean NOT NULL DEFAULT false,
    is_active boolean NOT NULL DEFAULT true,
    external_source text NOT NULL DEFAULT '',
    external_id text NOT NULL DEFAULT '',
    last_login timestamptz
);

CREATE TABLE settings (
    key text PRIMARY KEY,
    value jsonb NOT NULL,
    modified timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE execution_environments (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint,
    image text NOT NULL,
    managed boolean NOT NULL DEFAULT false,
    credential_id bigint,
    pull text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX execution_environments_name ON execution_environments (name, organization_id) NULLS NOT DISTINCT;

CREATE TABLE organizations (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    max_hosts integer NOT NULL DEFAULT 0,
    default_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL
);
ALTER TABLE execution_environments ADD FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;

CREATE TABLE teams (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    UNIQUE (organization_id, name)
);

-- RBAC (django-ansible-base style role definitions + assignments)
CREATE TABLE role_definitions (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    content_type text,
    permissions text[] NOT NULL DEFAULT '{}',
    managed boolean NOT NULL DEFAULT false
);

CREATE TABLE role_assignments (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    role_definition_id bigint NOT NULL REFERENCES role_definitions(id) ON DELETE CASCADE,
    user_id bigint REFERENCES users(id) ON DELETE CASCADE,
    team_id bigint REFERENCES teams(id) ON DELETE CASCADE,
    content_type text,
    object_id bigint,
    CHECK ((user_id IS NULL) <> (team_id IS NULL))
);
CREATE UNIQUE INDEX role_assignments_uniq ON role_assignments (role_definition_id, user_id, team_id, content_type, object_id) NULLS NOT DISTINCT;
CREATE INDEX role_assignments_obj ON role_assignments (content_type, object_id);
CREATE INDEX role_assignments_user ON role_assignments (user_id);
CREATE INDEX role_assignments_team ON role_assignments (team_id);

-- Legacy AWX object roles (admin_role, execute_role, ...) mapped onto role definitions.
CREATE TABLE object_roles (
    id bigserial PRIMARY KEY,
    content_type text NOT NULL,
    object_id bigint NOT NULL,
    role_field text NOT NULL,
    role_definition_id bigint NOT NULL REFERENCES role_definitions(id) ON DELETE CASCADE,
    UNIQUE (content_type, object_id, role_field)
);

CREATE TABLE auth_tokens (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    scope text NOT NULL DEFAULT 'write',
    expires timestamptz NOT NULL,
    last_used timestamptz
);

CREATE TABLE sessions (
    id text PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf text NOT NULL,
    created timestamptz NOT NULL DEFAULT now(),
    expires timestamptz NOT NULL
);

CREATE TABLE activity_stream (
    id bigserial PRIMARY KEY,
    timestamp timestamptz NOT NULL DEFAULT now(),
    actor_id bigint REFERENCES users(id) ON DELETE SET NULL,
    operation text NOT NULL,
    object1 text NOT NULL,
    object1_id bigint,
    object2 text NOT NULL DEFAULT '',
    object2_id bigint,
    object_relationship_type text NOT NULL DEFAULT '',
    changes jsonb NOT NULL DEFAULT '{}',
    summary jsonb NOT NULL DEFAULT '{}',
    organization_id bigint
);
CREATE INDEX activity_stream_obj1 ON activity_stream (object1, object1_id);
CREATE INDEX activity_stream_ts ON activity_stream (timestamp);

-- Credentials
CREATE TABLE credential_types (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    kind text NOT NULL,
    namespace text,
    managed boolean NOT NULL DEFAULT false,
    inputs jsonb NOT NULL DEFAULT '{}',
    injectors jsonb NOT NULL DEFAULT '{}',
    UNIQUE (name, kind)
);

CREATE TABLE credentials (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint REFERENCES organizations(id) ON DELETE CASCADE,
    credential_type_id bigint NOT NULL REFERENCES credential_types(id),
    inputs jsonb NOT NULL DEFAULT '{}',
    managed boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX credentials_name ON credentials (organization_id, name, credential_type_id) NULLS NOT DISTINCT;
ALTER TABLE execution_environments ADD FOREIGN KEY (credential_id) REFERENCES credentials(id) ON DELETE SET NULL;

CREATE TABLE credential_input_sources (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    description text NOT NULL DEFAULT '',
    target_credential_id bigint NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    source_credential_id bigint NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    input_field_name text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    UNIQUE (target_credential_id, input_field_name)
);

CREATE TABLE organization_galaxy_credentials (
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    credential_id bigint NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    position integer NOT NULL DEFAULT 0,
    PRIMARY KEY (organization_id, credential_id)
);

-- Instances
CREATE TABLE instances (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    hostname text NOT NULL UNIQUE,
    uuid text NOT NULL DEFAULT gen_random_uuid()::text,
    ip_address text NOT NULL DEFAULT '',
    version text NOT NULL DEFAULT '',
    node_type text NOT NULL DEFAULT 'hybrid',
    node_state text NOT NULL DEFAULT 'ready',
    capacity integer NOT NULL DEFAULT 0,
    capacity_adjustment numeric NOT NULL DEFAULT 1,
    cpu numeric NOT NULL DEFAULT 0,
    memory bigint NOT NULL DEFAULT 0,
    cpu_capacity integer NOT NULL DEFAULT 0,
    mem_capacity integer NOT NULL DEFAULT 0,
    enabled boolean NOT NULL DEFAULT true,
    managed_by_policy boolean NOT NULL DEFAULT true,
    managed boolean NOT NULL DEFAULT false,
    listener_port integer,
    peers_from_control_nodes boolean NOT NULL DEFAULT false,
    errors text NOT NULL DEFAULT '',
    last_seen timestamptz,
    last_health_check timestamptz,
    agent_token_hash text NOT NULL DEFAULT ''
);

CREATE TABLE instance_groups (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL UNIQUE,
    is_container_group boolean NOT NULL DEFAULT false,
    credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    policy_instance_percentage integer NOT NULL DEFAULT 0,
    policy_instance_minimum integer NOT NULL DEFAULT 0,
    policy_instance_list jsonb NOT NULL DEFAULT '[]',
    pod_spec_override text NOT NULL DEFAULT '',
    max_concurrent_jobs integer NOT NULL DEFAULT 0,
    max_forks integer NOT NULL DEFAULT 0
);

CREATE TABLE instance_group_instances (
    instance_group_id bigint NOT NULL REFERENCES instance_groups(id) ON DELETE CASCADE,
    instance_id bigint NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    PRIMARY KEY (instance_group_id, instance_id)
);

-- Ordered instance group preferences for organizations, inventories and templates.
CREATE TABLE instance_group_links (
    content_type text NOT NULL,
    object_id bigint NOT NULL,
    instance_group_id bigint NOT NULL REFERENCES instance_groups(id) ON DELETE CASCADE,
    position integer NOT NULL DEFAULT 0,
    PRIMARY KEY (content_type, object_id, instance_group_id)
);

-- Inventories
CREATE TABLE inventories (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind text NOT NULL DEFAULT '',
    host_filter text NOT NULL DEFAULT '',
    variables text NOT NULL DEFAULT '',
    has_active_failures boolean NOT NULL DEFAULT false,
    total_hosts integer NOT NULL DEFAULT 0,
    hosts_with_active_failures integer NOT NULL DEFAULT 0,
    total_groups integer NOT NULL DEFAULT 0,
    has_inventory_sources boolean NOT NULL DEFAULT false,
    total_inventory_sources integer NOT NULL DEFAULT 0,
    inventory_sources_with_failures integer NOT NULL DEFAULT 0,
    pending_deletion boolean NOT NULL DEFAULT false,
    prevent_instance_group_fallback boolean NOT NULL DEFAULT false,
    UNIQUE (organization_id, name)
);

CREATE TABLE inventory_input_inventories (
    inventory_id bigint NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    input_inventory_id bigint NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    position integer NOT NULL DEFAULT 0,
    PRIMARY KEY (inventory_id, input_inventory_id)
);

CREATE TABLE hosts (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    inventory_id bigint NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT true,
    instance_id text NOT NULL DEFAULT '',
    variables text NOT NULL DEFAULT '',
    last_job_id bigint,
    last_job_host_summary_id bigint,
    ansible_facts jsonb NOT NULL DEFAULT '{}',
    ansible_facts_modified timestamptz,
    UNIQUE (inventory_id, name)
);
CREATE INDEX hosts_name ON hosts (name);

CREATE TABLE groups (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    inventory_id bigint NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    variables text NOT NULL DEFAULT '',
    UNIQUE (inventory_id, name)
);

CREATE TABLE group_hosts (
    group_id bigint NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    host_id bigint NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, host_id)
);
CREATE INDEX group_hosts_host ON group_hosts (host_id);

CREATE TABLE group_children (
    parent_id bigint NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    child_id bigint NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    PRIMARY KEY (parent_id, child_id)
);
CREATE INDEX group_children_child ON group_children (child_id);

-- Unified job templates: job_template, project, inventory_source,
-- workflow_job_template, workflow_approval_template, system_job_template.
CREATE TABLE unified_job_templates (
    id bigserial PRIMARY KEY,
    type text NOT NULL,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint REFERENCES organizations(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'never updated',
    last_job_id bigint,
    last_job_failed boolean NOT NULL DEFAULT false,
    last_job_run timestamptz,
    next_job_run timestamptz,
    next_schedule_id bigint,
    current_job_id bigint,
    execution_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL,

    -- shared launch-time fields
    job_type text NOT NULL DEFAULT '',
    inventory_id bigint REFERENCES inventories(id) ON DELETE SET NULL,
    credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    scm_branch text NOT NULL DEFAULT '',
    "limit" text NOT NULL DEFAULT '',
    extra_vars text NOT NULL DEFAULT '',
    timeout integer NOT NULL DEFAULT 0,
    verbosity integer NOT NULL DEFAULT 0,
    allow_simultaneous boolean NOT NULL DEFAULT false,
    survey_enabled boolean NOT NULL DEFAULT false,
    survey_spec jsonb NOT NULL DEFAULT '{}',
    webhook_service text NOT NULL DEFAULT '',
    webhook_credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    webhook_key text NOT NULL DEFAULT '',
    prevent_instance_group_fallback boolean NOT NULL DEFAULT false,

    -- project
    scm_type text NOT NULL DEFAULT '',
    scm_url text NOT NULL DEFAULT '',
    scm_refspec text NOT NULL DEFAULT '',
    scm_clean boolean NOT NULL DEFAULT false,
    scm_track_submodules boolean NOT NULL DEFAULT false,
    scm_delete_on_update boolean NOT NULL DEFAULT false,
    scm_update_on_launch boolean NOT NULL DEFAULT false,
    scm_update_cache_timeout integer NOT NULL DEFAULT 0,
    scm_revision text NOT NULL DEFAULT '',
    allow_override boolean NOT NULL DEFAULT false,
    local_path text NOT NULL DEFAULT '',
    playbook_files jsonb NOT NULL DEFAULT '[]',
    inventory_files jsonb NOT NULL DEFAULT '[]',
    default_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL,
    signature_validation_credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,

    -- job template
    project_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    playbook text NOT NULL DEFAULT '',
    forks integer NOT NULL DEFAULT 0,
    job_tags text NOT NULL DEFAULT '',
    skip_tags text NOT NULL DEFAULT '',
    start_at_task text NOT NULL DEFAULT '',
    force_handlers boolean NOT NULL DEFAULT false,
    use_fact_cache boolean NOT NULL DEFAULT false,
    host_config_key text NOT NULL DEFAULT '',
    become_enabled boolean NOT NULL DEFAULT false,
    diff_mode boolean NOT NULL DEFAULT false,
    job_slice_count integer NOT NULL DEFAULT 1,
    ask_scm_branch_on_launch boolean NOT NULL DEFAULT false,
    ask_diff_mode_on_launch boolean NOT NULL DEFAULT false,
    ask_variables_on_launch boolean NOT NULL DEFAULT false,
    ask_limit_on_launch boolean NOT NULL DEFAULT false,
    ask_tags_on_launch boolean NOT NULL DEFAULT false,
    ask_skip_tags_on_launch boolean NOT NULL DEFAULT false,
    ask_job_type_on_launch boolean NOT NULL DEFAULT false,
    ask_verbosity_on_launch boolean NOT NULL DEFAULT false,
    ask_inventory_on_launch boolean NOT NULL DEFAULT false,
    ask_credential_on_launch boolean NOT NULL DEFAULT false,
    ask_execution_environment_on_launch boolean NOT NULL DEFAULT false,
    ask_labels_on_launch boolean NOT NULL DEFAULT false,
    ask_forks_on_launch boolean NOT NULL DEFAULT false,
    ask_job_slice_count_on_launch boolean NOT NULL DEFAULT false,
    ask_timeout_on_launch boolean NOT NULL DEFAULT false,
    ask_instance_groups_on_launch boolean NOT NULL DEFAULT false,

    -- inventory source
    source text NOT NULL DEFAULT '',
    source_path text NOT NULL DEFAULT '',
    source_vars text NOT NULL DEFAULT '',
    source_project_id bigint REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    enabled_var text NOT NULL DEFAULT '',
    enabled_value text NOT NULL DEFAULT '',
    host_filter text NOT NULL DEFAULT '',
    overwrite boolean NOT NULL DEFAULT false,
    overwrite_vars boolean NOT NULL DEFAULT false,
    update_on_launch boolean NOT NULL DEFAULT false,
    update_cache_timeout integer NOT NULL DEFAULT 0
);
CREATE INDEX ujt_type ON unified_job_templates (type);
CREATE UNIQUE INDEX ujt_name_org ON unified_job_templates (type, organization_id, name) NULLS NOT DISTINCT
    WHERE type NOT IN ('inventory_source', 'workflow_approval_template');
CREATE UNIQUE INDEX ujt_invsrc_name ON unified_job_templates (inventory_id, name) WHERE type = 'inventory_source';

CREATE TABLE unified_job_template_credentials (
    unified_job_template_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    credential_id bigint NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    PRIMARY KEY (unified_job_template_id, credential_id)
);

CREATE TABLE labels (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    UNIQUE (organization_id, name)
);

CREATE TABLE unified_job_template_labels (
    unified_job_template_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    label_id bigint NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (unified_job_template_id, label_id)
);

-- Unified jobs: job, project_update, inventory_update, ad_hoc_command,
-- workflow_job, workflow_approval, system_job.
CREATE TABLE unified_jobs (
    id bigserial PRIMARY KEY,
    type text NOT NULL,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    unified_job_template_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    organization_id bigint REFERENCES organizations(id) ON DELETE SET NULL,
    launch_type text NOT NULL DEFAULT 'manual',
    status text NOT NULL DEFAULT 'new',
    failed boolean NOT NULL DEFAULT false,
    started timestamptz,
    finished timestamptz,
    canceled_on timestamptz,
    elapsed numeric(12,3) NOT NULL DEFAULT 0,
    job_args text NOT NULL DEFAULT '',
    job_cwd text NOT NULL DEFAULT '',
    job_env jsonb NOT NULL DEFAULT '{}',
    job_explanation text NOT NULL DEFAULT '',
    result_traceback text NOT NULL DEFAULT '',
    execution_node text NOT NULL DEFAULT '',
    controller_node text NOT NULL DEFAULT '',
    schedule_id bigint,
    instance_group_id bigint REFERENCES instance_groups(id) ON DELETE SET NULL,
    execution_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL,
    work_unit_id text NOT NULL DEFAULT '',
    cancel_flag boolean NOT NULL DEFAULT false,
    dependencies_processed boolean NOT NULL DEFAULT false,
    emitted_events integer NOT NULL DEFAULT 0,
    event_processing_finished boolean NOT NULL DEFAULT false,
    task_impact integer NOT NULL DEFAULT 0,
    launched_by jsonb NOT NULL DEFAULT '{}',

    -- shared launch-time fields
    job_type text NOT NULL DEFAULT '',
    inventory_id bigint REFERENCES inventories(id) ON DELETE SET NULL,
    credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    project_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    scm_branch text NOT NULL DEFAULT '',
    scm_revision text NOT NULL DEFAULT '',
    "limit" text NOT NULL DEFAULT '',
    extra_vars text NOT NULL DEFAULT '',
    timeout integer NOT NULL DEFAULT 0,
    verbosity integer NOT NULL DEFAULT 0,
    forks integer NOT NULL DEFAULT 0,
    allow_simultaneous boolean NOT NULL DEFAULT false,
    become_enabled boolean NOT NULL DEFAULT false,
    diff_mode boolean NOT NULL DEFAULT false,
    survey_passwords jsonb NOT NULL DEFAULT '{}',
    webhook_service text NOT NULL DEFAULT '',
    webhook_credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    webhook_guid text NOT NULL DEFAULT '',

    -- job
    playbook text NOT NULL DEFAULT '',
    job_tags text NOT NULL DEFAULT '',
    skip_tags text NOT NULL DEFAULT '',
    start_at_task text NOT NULL DEFAULT '',
    force_handlers boolean NOT NULL DEFAULT false,
    use_fact_cache boolean NOT NULL DEFAULT false,
    artifacts jsonb NOT NULL DEFAULT '{}',
    job_slice_number integer NOT NULL DEFAULT 0,
    job_slice_count integer NOT NULL DEFAULT 1,
    project_update_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,

    -- project update
    scm_type text NOT NULL DEFAULT '',
    scm_url text NOT NULL DEFAULT '',
    scm_refspec text NOT NULL DEFAULT '',
    scm_clean boolean NOT NULL DEFAULT false,
    scm_track_submodules boolean NOT NULL DEFAULT false,
    scm_delete_on_update boolean NOT NULL DEFAULT false,
    local_path text NOT NULL DEFAULT '',

    -- inventory update
    source text NOT NULL DEFAULT '',
    source_path text NOT NULL DEFAULT '',
    source_vars text NOT NULL DEFAULT '',
    source_project_update_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    enabled_var text NOT NULL DEFAULT '',
    enabled_value text NOT NULL DEFAULT '',
    host_filter text NOT NULL DEFAULT '',
    overwrite boolean NOT NULL DEFAULT false,
    overwrite_vars boolean NOT NULL DEFAULT false,

    -- ad hoc command
    module_name text NOT NULL DEFAULT '',
    module_args text NOT NULL DEFAULT '',

    -- workflow job / approval
    is_sliced_job boolean NOT NULL DEFAULT false,
    job_template_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    approval_timeout integer NOT NULL DEFAULT 0,
    timed_out boolean NOT NULL DEFAULT false,
    approved_or_denied_by_id bigint REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX unified_jobs_type ON unified_jobs (type);
CREATE INDEX unified_jobs_status ON unified_jobs (status) WHERE status IN ('pending', 'waiting', 'running');
CREATE INDEX unified_jobs_ujt ON unified_jobs (unified_job_template_id);
CREATE INDEX unified_jobs_created ON unified_jobs (created);
CREATE INDEX unified_jobs_finished ON unified_jobs (finished);

ALTER TABLE unified_job_templates ADD FOREIGN KEY (last_job_id) REFERENCES unified_jobs(id) ON DELETE SET NULL;
ALTER TABLE unified_job_templates ADD FOREIGN KEY (current_job_id) REFERENCES unified_jobs(id) ON DELETE SET NULL;
ALTER TABLE hosts ADD FOREIGN KEY (last_job_id) REFERENCES unified_jobs(id) ON DELETE SET NULL;

CREATE TABLE unified_job_credentials (
    unified_job_id bigint NOT NULL REFERENCES unified_jobs(id) ON DELETE CASCADE,
    credential_id bigint NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    PRIMARY KEY (unified_job_id, credential_id)
);

CREATE TABLE unified_job_labels (
    unified_job_id bigint NOT NULL REFERENCES unified_jobs(id) ON DELETE CASCADE,
    label_id bigint NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (unified_job_id, label_id)
);

CREATE TABLE unified_job_dependencies (
    unified_job_id bigint NOT NULL REFERENCES unified_jobs(id) ON DELETE CASCADE,
    depends_on_id bigint NOT NULL REFERENCES unified_jobs(id) ON DELETE CASCADE,
    PRIMARY KEY (unified_job_id, depends_on_id)
);

-- Job events for every unified job type.
CREATE TABLE job_events (
    id bigserial,
    job_id bigint NOT NULL,
    job_created timestamptz NOT NULL,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    counter integer NOT NULL,
    event text NOT NULL,
    event_data jsonb NOT NULL DEFAULT '{}',
    failed boolean NOT NULL DEFAULT false,
    changed boolean NOT NULL DEFAULT false,
    uuid text NOT NULL DEFAULT '',
    parent_uuid text NOT NULL DEFAULT '',
    host_id bigint,
    host_name text NOT NULL DEFAULT '',
    playbook text NOT NULL DEFAULT '',
    play text NOT NULL DEFAULT '',
    role text NOT NULL DEFAULT '',
    task text NOT NULL DEFAULT '',
    stdout text NOT NULL DEFAULT '',
    start_line integer NOT NULL DEFAULT 0,
    end_line integer NOT NULL DEFAULT 0,
    verbosity integer NOT NULL DEFAULT 0
) PARTITION BY RANGE (job_created);
CREATE TABLE job_events_default PARTITION OF job_events DEFAULT;
CREATE INDEX job_events_job ON job_events (job_id, counter);
CREATE INDEX job_events_host ON job_events (host_id) WHERE host_id IS NOT NULL;

CREATE TABLE job_host_summaries (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    job_id bigint NOT NULL REFERENCES unified_jobs(id) ON DELETE CASCADE,
    host_id bigint REFERENCES hosts(id) ON DELETE SET NULL,
    host_name text NOT NULL DEFAULT '',
    changed integer NOT NULL DEFAULT 0,
    dark integer NOT NULL DEFAULT 0,
    failures integer NOT NULL DEFAULT 0,
    ignored integer NOT NULL DEFAULT 0,
    ok integer NOT NULL DEFAULT 0,
    processed integer NOT NULL DEFAULT 0,
    rescued integer NOT NULL DEFAULT 0,
    skipped integer NOT NULL DEFAULT 0,
    failed boolean NOT NULL DEFAULT false,
    UNIQUE (job_id, host_name)
);
CREATE INDEX job_host_summaries_host ON job_host_summaries (host_id);
ALTER TABLE hosts ADD FOREIGN KEY (last_job_host_summary_id) REFERENCES job_host_summaries(id) ON DELETE SET NULL;

CREATE TABLE host_inventory_sources (
    host_id bigint NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    inventory_source_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    PRIMARY KEY (host_id, inventory_source_id)
);
CREATE TABLE group_inventory_sources (
    group_id bigint NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    inventory_source_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, inventory_source_id)
);

CREATE TABLE host_metrics (
    id bigserial PRIMARY KEY,
    hostname text NOT NULL UNIQUE,
    first_automation timestamptz NOT NULL DEFAULT now(),
    last_automation timestamptz NOT NULL DEFAULT now(),
    last_deleted timestamptz,
    automated_counter bigint NOT NULL DEFAULT 0,
    deleted_counter integer NOT NULL DEFAULT 0,
    deleted boolean NOT NULL DEFAULT false,
    used_in_inventories integer
);

-- Schedules
CREATE TABLE schedules (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    unified_job_template_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT true,
    rrule text NOT NULL,
    dtstart timestamptz,
    dtend timestamptz,
    next_run timestamptz,
    extra_data jsonb NOT NULL DEFAULT '{}',
    inventory_id bigint REFERENCES inventories(id) ON DELETE SET NULL,
    execution_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL,
    scm_branch text,
    job_type text,
    job_tags text,
    skip_tags text,
    "limit" text,
    diff_mode boolean,
    verbosity integer,
    forks integer,
    job_slice_count integer,
    timeout integer,
    UNIQUE (unified_job_template_id, name)
);
CREATE INDEX schedules_next_run ON schedules (next_run) WHERE enabled;
ALTER TABLE unified_jobs ADD FOREIGN KEY (schedule_id) REFERENCES schedules(id) ON DELETE SET NULL;

CREATE TABLE schedule_credentials (
    schedule_id bigint NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
    credential_id bigint NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    PRIMARY KEY (schedule_id, credential_id)
);

-- Notifications
CREATE TABLE notification_templates (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    notification_type text NOT NULL,
    notification_configuration jsonb NOT NULL DEFAULT '{}',
    messages jsonb,
    UNIQUE (organization_id, name)
);

CREATE TABLE notifications (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    notification_template_id bigint NOT NULL REFERENCES notification_templates(id) ON DELETE CASCADE,
    unified_job_id bigint REFERENCES unified_jobs(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending',
    error text NOT NULL DEFAULT '',
    notifications_sent integer NOT NULL DEFAULT 0,
    notification_type text NOT NULL,
    recipients text NOT NULL DEFAULT '',
    subject text NOT NULL DEFAULT '',
    body jsonb NOT NULL DEFAULT '{}'
);

-- event: started, success, error, approvals
CREATE TABLE notification_template_links (
    content_type text NOT NULL,
    object_id bigint NOT NULL,
    notification_template_id bigint NOT NULL REFERENCES notification_templates(id) ON DELETE CASCADE,
    event text NOT NULL,
    PRIMARY KEY (content_type, object_id, notification_template_id, event)
);

-- Workflows
CREATE TABLE workflow_job_template_nodes (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    workflow_job_template_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    unified_job_template_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    identifier text NOT NULL DEFAULT gen_random_uuid()::text,
    all_parents_must_converge boolean NOT NULL DEFAULT false,
    extra_data jsonb NOT NULL DEFAULT '{}',
    inventory_id bigint REFERENCES inventories(id) ON DELETE SET NULL,
    execution_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL,
    scm_branch text,
    job_type text,
    job_tags text,
    skip_tags text,
    "limit" text,
    diff_mode boolean,
    verbosity integer,
    forks integer,
    job_slice_count integer,
    timeout integer,
    UNIQUE (workflow_job_template_id, identifier)
);

CREATE TABLE workflow_job_template_node_edges (
    from_node_id bigint NOT NULL REFERENCES workflow_job_template_nodes(id) ON DELETE CASCADE,
    to_node_id bigint NOT NULL REFERENCES workflow_job_template_nodes(id) ON DELETE CASCADE,
    kind text NOT NULL,
    PRIMARY KEY (from_node_id, to_node_id)
);

CREATE TABLE workflow_job_template_node_credentials (
    node_id bigint NOT NULL REFERENCES workflow_job_template_nodes(id) ON DELETE CASCADE,
    credential_id bigint NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    PRIMARY KEY (node_id, credential_id)
);

CREATE TABLE workflow_job_nodes (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    workflow_job_id bigint NOT NULL REFERENCES unified_jobs(id) ON DELETE CASCADE,
    unified_job_template_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    identifier text NOT NULL DEFAULT '',
    do_not_run boolean NOT NULL DEFAULT false,
    all_parents_must_converge boolean NOT NULL DEFAULT false,
    extra_data jsonb NOT NULL DEFAULT '{}',
    inventory_id bigint REFERENCES inventories(id) ON DELETE SET NULL,
    execution_environment_id bigint REFERENCES execution_environments(id) ON DELETE SET NULL,
    scm_branch text,
    job_type text,
    job_tags text,
    skip_tags text,
    "limit" text,
    diff_mode boolean,
    verbosity integer,
    forks integer,
    job_slice_count integer,
    timeout integer
);
CREATE INDEX workflow_job_nodes_wf ON workflow_job_nodes (workflow_job_id);

CREATE TABLE workflow_job_node_edges (
    from_node_id bigint NOT NULL REFERENCES workflow_job_nodes(id) ON DELETE CASCADE,
    to_node_id bigint NOT NULL REFERENCES workflow_job_nodes(id) ON DELETE CASCADE,
    kind text NOT NULL,
    PRIMARY KEY (from_node_id, to_node_id)
);

CREATE TABLE workflow_job_node_credentials (
    node_id bigint NOT NULL REFERENCES workflow_job_nodes(id) ON DELETE CASCADE,
    credential_id bigint NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    PRIMARY KEY (node_id, credential_id)
);
