-- What an execution environment's image holds (collections, ansible-core,
-- Python, ansible.cfg), read from its registry, reused for the same digest.
CREATE TABLE execution_environment_contents (
    execution_environment_id bigint PRIMARY KEY REFERENCES execution_environments(id) ON DELETE CASCADE,
    image text NOT NULL,
    digest text NOT NULL DEFAULT '',
    status text NOT NULL,
    error text NOT NULL DEFAULT '',
    contents jsonb,
    started timestamptz NOT NULL DEFAULT now(),
    finished timestamptz
);
CREATE INDEX execution_environment_contents_digest ON execution_environment_contents (digest) WHERE status = 'successful';
