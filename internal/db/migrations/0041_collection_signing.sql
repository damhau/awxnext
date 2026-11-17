-- Signing the collection hub's versions (roadmap M13-03): detached GPG
-- signatures of MANIFEST.json, made by awxnext's own keys or uploaded.

-- awxnext's signing keys: one active, which signs approvals, and the
-- retired ones, whose signatures stay until the key is deleted. The
-- private key is wrapped by the master key, as credentials' secrets are.
CREATE TABLE collection_signing_keys (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    modified timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    modified_by_id bigint,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    fingerprint text NOT NULL UNIQUE,
    algorithm text NOT NULL DEFAULT '',
    public_key text NOT NULL,
    private_key text NOT NULL,
    active boolean NOT NULL DEFAULT false,
    retired timestamptz
);
CREATE UNIQUE INDEX collection_signing_keys_active ON collection_signing_keys (active) WHERE active;

-- MANIFEST.json as the artifact holds it, byte for byte: what signatures
-- sign. NULL for versions stored before, read from the artifact when needed.
ALTER TABLE collection_versions ADD COLUMN manifest_json bytea;

-- A version's signatures, served in its v3 detail. origin is 'awxnext'
-- (signing_key_id set) or 'upload'; created_by_id is the approver whose
-- approval signed it, or the uploader.
CREATE TABLE collection_version_signatures (
    id bigserial PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    created_by_id bigint,
    collection_version_id bigint NOT NULL REFERENCES collection_versions(id) ON DELETE CASCADE,
    origin text NOT NULL,
    signature text NOT NULL,
    pubkey_fingerprint text NOT NULL,
    signer text NOT NULL DEFAULT '',
    public_key text NOT NULL DEFAULT '',
    signing_key_id bigint REFERENCES collection_signing_keys(id) ON DELETE CASCADE,
    UNIQUE (collection_version_id, pubkey_fingerprint)
);
CREATE INDEX collection_version_signatures_key ON collection_version_signatures (signing_key_id);

-- Jobs install a project's collections only with this many valid
-- signatures from the keys of a GPG Public Key credential.
ALTER TABLE unified_job_templates
    ADD COLUMN collection_signature_validation_credential_id bigint REFERENCES credentials(id) ON DELETE SET NULL,
    ADD COLUMN collection_signature_count integer NOT NULL DEFAULT 1;
