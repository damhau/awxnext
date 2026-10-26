-- Data encryption keys, wrapped by the master key (never stored in plaintext).
CREATE TABLE data_keys (
    id text PRIMARY KEY,
    created timestamptz NOT NULL DEFAULT now(),
    wrapped text NOT NULL,
    kek_id text NOT NULL,
    active boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX data_keys_active ON data_keys (active) WHERE active;
