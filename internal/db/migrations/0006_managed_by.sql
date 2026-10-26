-- Objects owned by an external source of truth (e.g. a GitOps reconciler).
-- The API rejects edits to them unless made by that source.
ALTER TABLE folders ADD COLUMN managed_by text NOT NULL DEFAULT '';
ALTER TABLE teams ADD COLUMN managed_by text NOT NULL DEFAULT '';
ALTER TABLE inventories ADD COLUMN managed_by text NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN managed_by text NOT NULL DEFAULT '';
ALTER TABLE unified_job_templates ADD COLUMN managed_by text NOT NULL DEFAULT '';
ALTER TABLE notification_templates ADD COLUMN managed_by text NOT NULL DEFAULT '';
