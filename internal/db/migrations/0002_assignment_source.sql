-- Assignments created by external identity mapping (e.g. OIDC groups) are tagged
-- so they can be revoked authoritatively without touching manual grants.
ALTER TABLE role_assignments ADD COLUMN source text NOT NULL DEFAULT '';
CREATE INDEX role_assignments_source ON role_assignments (user_id, source) WHERE source <> '';
