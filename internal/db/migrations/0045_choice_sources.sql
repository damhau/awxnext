-- Choice sources (roadmap M15-02): an organization's lists for survey
-- questions, from a JSON API the server calls or from what a job template's
-- run publishes with set_stats.
CREATE TABLE choice_sources (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind text NOT NULL DEFAULT 'http',
    url text NOT NULL DEFAULT '',
    parameters jsonb NOT NULL DEFAULT '{}',
    credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    job_template_id bigint REFERENCES unified_job_templates(id) ON DELETE SET NULL,
    items text NOT NULL DEFAULT '',
    value text NOT NULL DEFAULT '',
    label text NOT NULL DEFAULT '',
    filter text NOT NULL DEFAULT '',
    cache_seconds integer NOT NULL DEFAULT 300,
    refresh_minutes integer NOT NULL DEFAULT 0,
    next_refresh timestamptz,
    last_job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    last_refresh timestamptz,
    last_refresh_job_id bigint REFERENCES unified_jobs(id) ON DELETE SET NULL,
    last_error text NOT NULL DEFAULT '',
    UNIQUE (organization_id, name)
);
CREATE INDEX choice_sources_job_template ON choice_sources (job_template_id) WHERE job_template_id IS NOT NULL;
CREATE INDEX choice_sources_credential ON choice_sources (credential_id) WHERE credential_id IS NOT NULL;
CREATE INDEX choice_sources_last_job ON choice_sources (last_job_id) WHERE last_job_id IS NOT NULL;

-- A source's lists: one per set of parameters for an http source (key: a
-- hash of the URL called), one for a job source (key ''). Entries are
-- {value, label, item}, before the source's filter.
CREATE TABLE choice_source_lists (
    source_id bigint NOT NULL REFERENCES choice_sources(id) ON DELETE CASCADE,
    key text NOT NULL,
    entries jsonb NOT NULL,
    fetched timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_id, key)
);

-- The labels of a request's answers from labelled lists, as they were when
-- it was made.
ALTER TABLE catalog_requests ADD COLUMN answer_labels jsonb NOT NULL DEFAULT '{}';
