-- Remote agents report how they run ansible-runner; control nodes publish
-- the address other nodes use to reach them (agent and job pod streams).
ALTER TABLE instances ADD COLUMN agent_runtime text NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN agent_work_dir text NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN internal_url text NOT NULL DEFAULT '';
