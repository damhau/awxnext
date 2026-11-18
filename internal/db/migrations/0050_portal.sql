-- The self-service portal (roadmap M15-08): an organization's name and logo
-- in the portal, and how an item names a request's steps.
ALTER TABLE organizations
    ADD COLUMN portal_title text NOT NULL DEFAULT '',
    ADD COLUMN portal_logo text NOT NULL DEFAULT '';
ALTER TABLE catalog_items ADD COLUMN step_labels jsonb NOT NULL DEFAULT '{}';
