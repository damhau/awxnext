-- The resource registry's ansible_id (django-ansible-base) of the shared
-- resource types: role assignments name their user, team and object by it.
ALTER TABLE users ADD COLUMN ansible_id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE;
ALTER TABLE teams ADD COLUMN ansible_id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE;
ALTER TABLE organizations ADD COLUMN ansible_id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE;
