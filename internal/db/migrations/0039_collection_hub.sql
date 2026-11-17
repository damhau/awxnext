-- The collection hub (roadmap M13): collections served on Galaxy's v3 API,
-- in namespaces that organizations own, kept in repositories.

CREATE TABLE collection_namespaces (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    company text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    avatar_url text NOT NULL DEFAULT ''
);
CREATE INDEX collection_namespaces_org ON collection_namespaces (organization_id);

CREATE TABLE collections (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    -- Changes whenever one of its versions enters or leaves a repository:
    -- ansible-galaxy keeps a collection's version list until it changes.
    modified timestamptz NOT NULL DEFAULT now(),
    namespace_id bigint NOT NULL REFERENCES collection_namespaces(id) ON DELETE CASCADE,
    name text NOT NULL,
    deprecated boolean NOT NULL DEFAULT false,
    UNIQUE (namespace_id, name)
);

CREATE TABLE collection_versions (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    collection_id bigint NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    version text NOT NULL,
    requires_ansible text NOT NULL DEFAULT '',
    -- MANIFEST.json's collection_info, FILES.json and the rest of MANIFEST.json.
    metadata jsonb NOT NULL DEFAULT '{}',
    files jsonb NOT NULL DEFAULT '{}',
    manifest jsonb NOT NULL DEFAULT '{}',
    filename text NOT NULL,
    sha256 text NOT NULL,
    size bigint NOT NULL,
    -- Where the artifact is: 'database' (collection_version_data), 's3' or
    -- 'azure_blob' at location.
    storage text NOT NULL DEFAULT 'database',
    location text NOT NULL DEFAULT '',
    UNIQUE (collection_id, version)
);

CREATE TABLE collection_version_data (
    version_id bigint PRIMARY KEY REFERENCES collection_versions(id) ON DELETE CASCADE,
    data bytea NOT NULL
);

-- Repositories hold versions: 'published' is what clients install from.
-- Staging and rejected (M13-02) and remotes' repositories (M13-04) are rows
-- of their own.
CREATE TABLE collection_repositories (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT ''
);
INSERT INTO collection_repositories (name, description) VALUES ('published', 'The collections clients install.');

CREATE TABLE collection_repository_contents (
    repository_id bigint NOT NULL REFERENCES collection_repositories(id) ON DELETE CASCADE,
    version_id bigint NOT NULL REFERENCES collection_versions(id) ON DELETE CASCADE,
    added timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repository_id, version_id)
);
CREATE INDEX collection_repository_contents_version ON collection_repository_contents (version_id);

-- Read tokens minted for one requirements install of a job, revoked when
-- it ends; expires is the backstop.
CREATE TABLE hub_tokens (
    id bigserial PRIMARY KEY,
    token_hash text NOT NULL UNIQUE,
    organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    unified_job_id bigint,
    created timestamptz NOT NULL DEFAULT now(),
    expires timestamptz NOT NULL
);

-- Objects of deleted versions in object storage, removed by the next
-- maintenance.
CREATE TABLE collection_object_deletions (
    id bigserial PRIMARY KEY,
    storage text NOT NULL,
    location text NOT NULL,
    created timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION collection_version_deleted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.storage <> 'database' THEN
        INSERT INTO collection_object_deletions (storage, location) VALUES (OLD.storage, OLD.location);
    END IF;
    RETURN OLD;
END $$;

CREATE TRIGGER collection_version_deleted AFTER DELETE ON collection_versions
    FOR EACH ROW EXECUTE FUNCTION collection_version_deleted();
