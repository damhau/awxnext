-- Workflow node conditions ("when") and loops: a node runs only when its
-- condition holds, and a loop node runs its template once per item of a list.
ALTER TABLE workflow_job_template_nodes
    ADD COLUMN when_expr text NOT NULL DEFAULT '',
    ADD COLUMN loop_expr text NOT NULL DEFAULT '',
    ADD COLUMN loop_var text NOT NULL DEFAULT '',
    ADD COLUMN loop_concurrency integer NOT NULL DEFAULT 0;

ALTER TABLE workflow_job_nodes
    ADD COLUMN when_expr text NOT NULL DEFAULT '',
    ADD COLUMN loop_expr text NOT NULL DEFAULT '',
    ADD COLUMN loop_var text NOT NULL DEFAULT '',
    ADD COLUMN loop_concurrency integer NOT NULL DEFAULT 0,
    -- Why a node did not run, when a condition decided it.
    ADD COLUMN skip_reason text NOT NULL DEFAULT '',
    -- The evaluated loop list; NULL until the loop starts.
    ADD COLUMN loop_items jsonb;

-- One job per loop item.
CREATE TABLE workflow_job_node_iterations (
    node_id bigint NOT NULL REFERENCES workflow_job_nodes(id) ON DELETE CASCADE,
    idx integer NOT NULL,
    job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    PRIMARY KEY (node_id, idx)
);
CREATE INDEX workflow_job_node_iterations_job ON workflow_job_node_iterations (job_id);

-- The workflow node each job ran for, loop iterations included.
CREATE VIEW workflow_node_jobs AS
    SELECT id AS node_id, workflow_job_id, job_id FROM workflow_job_nodes WHERE job_id IS NOT NULL
    UNION ALL
    SELECT n.id, n.workflow_job_id, i.job_id FROM workflow_job_node_iterations i
        JOIN workflow_job_nodes n ON n.id = i.node_id WHERE i.job_id IS NOT NULL;
