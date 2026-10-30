-- A user's UI preferences (saved views, starred templates, list settings),
-- kept on the server so every browser sees them.
CREATE TABLE user_preferences (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key text NOT NULL,
    value jsonb NOT NULL,
    modified timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
