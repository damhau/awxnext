-- Remotes of the collection hub (roadmap M13-04): collection_remote
-- templates mirror collections from another Galaxy server into a repository
-- of their own, in collection_sync jobs.

-- The remote's requirements file, and whether its dependencies come too.
-- Its server is credential_id (a Galaxy credential), its proxy and TLS check
-- scm_proxy and scm_verify_ssl, the keys its signatures must verify with
-- signature_validation_credential_id.
ALTER TABLE unified_job_templates
    ADD COLUMN collection_remote_requirements text NOT NULL DEFAULT '',
    ADD COLUMN collection_sync_dependencies boolean NOT NULL DEFAULT true,
    ADD COLUMN collection_repository text NOT NULL DEFAULT '';

-- A remote's repository goes with it.
ALTER TABLE collection_repositories
    ADD COLUMN remote_id bigint UNIQUE REFERENCES unified_job_templates(id) ON DELETE CASCADE;
