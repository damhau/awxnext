-- Notifications use the common audit columns of API resources.
ALTER TABLE notifications ADD COLUMN created_by_id bigint, ADD COLUMN modified_by_id bigint;
