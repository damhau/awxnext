-- Job event archives can live in object storage (S3, Azure Blob): the row
-- then says where, and keeps no data.
ALTER TABLE job_event_archives
    ADD COLUMN storage text NOT NULL DEFAULT 'database',
    ADD COLUMN location text NOT NULL DEFAULT '',
    ALTER COLUMN data DROP NOT NULL;

-- Objects of archives whose job was deleted, removed by the next archive run.
CREATE TABLE job_event_archive_deletions (
    id bigserial PRIMARY KEY,
    storage text NOT NULL,
    location text NOT NULL,
    created timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION job_event_archive_deleted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.storage <> 'database' THEN
        INSERT INTO job_event_archive_deletions (storage, location) VALUES (OLD.storage, OLD.location);
    END IF;
    RETURN OLD;
END $$;

CREATE TRIGGER job_event_archive_deleted AFTER DELETE ON job_event_archives
    FOR EACH ROW EXECUTE FUNCTION job_event_archive_deleted();
