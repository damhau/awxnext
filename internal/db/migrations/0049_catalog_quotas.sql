-- Quotas (roadmap M15-07): how many deployments of an item each team, or
-- each user requesting for no team, keeps at once, how many requests they
-- make in 24 hours, and what a request over quota does.
ALTER TABLE catalog_items
    ADD COLUMN max_deployments integer,
    ADD COLUMN max_requests_per_day integer,
    ADD COLUMN over_quota text NOT NULL DEFAULT 'refuse';
CREATE INDEX catalog_requests_item_created ON catalog_requests (catalog_item_id, created) WHERE kind = 'new';
