-- What changed since the last good run (roadmap M12-01): the image each job
-- ran in, and its digest, since a floating tag such as :latest hides a new
-- image. Empty when the job ran without a container or before this.
ALTER TABLE unified_jobs
    ADD COLUMN execution_environment_image text NOT NULL DEFAULT '',
    ADD COLUMN execution_environment_digest text NOT NULL DEFAULT '';
