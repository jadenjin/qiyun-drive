ALTER TABLE sessions ADD COLUMN user_agent text NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN source_ip text NOT NULL DEFAULT '';
CREATE TABLE security_events (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 kind text NOT NULL,
 source_ip text NOT NULL DEFAULT '',
 user_agent text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_events_user_created_idx ON security_events(user_id,created_at DESC);
ALTER TABLE users ADD COLUMN totp_secret bytea;
ALTER TABLE users ADD COLUMN totp_pending_secret bytea;
ALTER TABLE users ADD COLUMN totp_pending_expires timestamptz;
ALTER TABLE users ADD COLUMN totp_last_step bigint NOT NULL DEFAULT -1;
CREATE TABLE recovery_codes (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 code_hash text NOT NULL,
 PRIMARY KEY(user_id,code_hash)
);
