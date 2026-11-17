-- Publishing to the collection hub (roadmap M13-02): uploads land in
-- staging, approval moves them to published, rejection to rejected.

INSERT INTO collection_repositories (name, description) VALUES
    ('staging', 'Uploaded collections waiting for approval.'),
    ('rejected', 'Collections their namespace''s approvers rejected.')
ON CONFLICT (name) DO NOTHING;

-- Uploads to the namespace go to published without review.
ALTER TABLE collection_namespaces ADD COLUMN auto_approve boolean NOT NULL DEFAULT false;

-- Who last moved the version between staging, published and rejected.
ALTER TABLE collection_versions
    ADD COLUMN reviewed_by_id bigint,
    ADD COLUMN reviewed timestamptz;

-- An upload's import, as galaxy_ng's import task: its state and the log of
-- each check. namespace_id is set once the uploader may upload there, so a
-- namespace's curators don't see others' attempts.
CREATE TABLE collection_imports (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    started timestamptz,
    finished timestamptz,
    state text NOT NULL DEFAULT 'running',
    filename text NOT NULL DEFAULT '',
    namespace_id bigint REFERENCES collection_namespaces(id) ON DELETE CASCADE,
    namespace_name text NOT NULL DEFAULT '',
    collection_name text NOT NULL DEFAULT '',
    version text NOT NULL DEFAULT '',
    collection_version_id bigint REFERENCES collection_versions(id) ON DELETE SET NULL,
    messages jsonb NOT NULL DEFAULT '[]',
    error jsonb
);
CREATE INDEX collection_imports_namespace ON collection_imports (namespace_id);
CREATE INDEX collection_imports_created_by ON collection_imports (created_by_id);
CREATE INDEX collection_imports_version ON collection_imports (collection_version_id);

-- The collections a project's requirement files name, read at each sync:
-- [{"name": "ns.name", "version": "...", "source": "...", "file": "..."}].
ALTER TABLE unified_job_templates ADD COLUMN collection_requirements jsonb NOT NULL DEFAULT '[]';
