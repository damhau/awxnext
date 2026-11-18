-- Deployment lifetimes (roadmap M15-06): an item's extension steps and
-- caps, and what each deployment used of them.
ALTER TABLE catalog_items
    ADD COLUMN extension_days jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN max_extensions integer,
    ADD COLUMN expiry_waits_for_freeze boolean NOT NULL DEFAULT false;

ALTER TABLE deployments
    ADD COLUMN extensions integer NOT NULL DEFAULT 0,
    -- The last reminder sent, in hours before expiry: 24, then 2. An
    -- extension clears it, so the new expiry is reminded again.
    ADD COLUMN reminded_hours integer;
CREATE INDEX deployments_expiry ON deployments (expires_at) WHERE status = 'active';
