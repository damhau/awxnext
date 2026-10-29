-- Labels and instance groups saved as launch prompts on schedules and
-- workflow nodes, as AWX's launch configurations keep them. Instance groups
-- are ordered: the first one with capacity runs the job.
CREATE TABLE schedule_labels (
    schedule_id bigint NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
    label_id bigint NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (schedule_id, label_id)
);
CREATE TABLE schedule_instance_groups (
    schedule_id bigint NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
    instance_group_id bigint NOT NULL REFERENCES instance_groups(id) ON DELETE CASCADE,
    position integer NOT NULL DEFAULT 0,
    PRIMARY KEY (schedule_id, instance_group_id)
);
CREATE TABLE workflow_job_template_node_instance_groups (
    node_id bigint NOT NULL REFERENCES workflow_job_template_nodes(id) ON DELETE CASCADE,
    instance_group_id bigint NOT NULL REFERENCES instance_groups(id) ON DELETE CASCADE,
    position integer NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, instance_group_id)
);
CREATE TABLE workflow_job_node_instance_groups (
    node_id bigint NOT NULL REFERENCES workflow_job_nodes(id) ON DELETE CASCADE,
    instance_group_id bigint NOT NULL REFERENCES instance_groups(id) ON DELETE CASCADE,
    position integer NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, instance_group_id)
);
