-- Artifacts of ancestor nodes passed to a workflow node's job.
ALTER TABLE workflow_job_nodes ADD COLUMN ancestor_artifacts jsonb NOT NULL DEFAULT '{}';
ALTER TABLE workflow_job_nodes ADD COLUMN created_by_id bigint, ADD COLUMN modified_by_id bigint;
ALTER TABLE workflow_job_template_nodes ADD COLUMN created_by_id bigint, ADD COLUMN modified_by_id bigint;

CREATE TABLE workflow_job_template_node_labels (
    node_id bigint NOT NULL REFERENCES workflow_job_template_nodes(id) ON DELETE CASCADE,
    label_id bigint NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (node_id, label_id)
);
CREATE TABLE workflow_job_node_labels (
    node_id bigint NOT NULL REFERENCES workflow_job_nodes(id) ON DELETE CASCADE,
    label_id bigint NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (node_id, label_id)
);
CREATE INDEX workflow_job_nodes_job ON workflow_job_nodes (job_id);
CREATE INDEX workflow_job_template_nodes_wf ON workflow_job_template_nodes (workflow_job_template_id);

-- Generic webhooks: JSONPath mapping of payload values to launch prompts.
ALTER TABLE unified_job_templates ADD COLUMN webhook_mapping jsonb NOT NULL DEFAULT '{}';
