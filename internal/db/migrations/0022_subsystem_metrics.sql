-- Each node's subsystem metrics (AWX's are in Redis), published every 15
-- seconds so that /api/v2/metrics/ on any node serves all of them.
CREATE TABLE subsystem_metrics (
    node text PRIMARY KEY,
    data jsonb NOT NULL DEFAULT '{}',
    updated timestamptz NOT NULL DEFAULT now()
);
