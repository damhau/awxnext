-- Failure notifications that say why, retry and thread (roadmap M12-05).
--
-- A notification counts its attempts and, while pending, when it is next
-- tried, so that the leader resumes one a stopped server left pending. Rows
-- from before this have no next attempt and stay as they are. sent_to holds
-- what each Slack channel got: the message's ts, or Slack's refusal.
ALTER TABLE notifications
    ADD COLUMN attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN event text NOT NULL DEFAULT '',
    ADD COLUMN next_attempt timestamptz,
    ADD COLUMN sent_to jsonb NOT NULL DEFAULT '{}';
ALTER TABLE notifications ALTER COLUMN next_attempt SET DEFAULT now();
CREATE INDEX notifications_pending ON notifications (next_attempt) WHERE status = 'pending';

-- The open failure thread of a Slack notification template, per template
-- that failed and channel: the first message, which the recovery edits.
CREATE TABLE slack_threads (
    notification_template_id bigint NOT NULL REFERENCES notification_templates(id) ON DELETE CASCADE,
    unified_job_template_id bigint NOT NULL REFERENCES unified_job_templates(id) ON DELETE CASCADE,
    channel text NOT NULL,
    channel_id text NOT NULL DEFAULT '',
    ts text NOT NULL DEFAULT '',
    text text NOT NULL DEFAULT '',
    job_id bigint,
    created timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (notification_template_id, unified_job_template_id, channel)
);
