ALTER TABLE upload_sessions ADD COLUMN staging_key text;
ALTER TABLE upload_sessions ADD COLUMN completion_parts jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE upload_sessions ADD COLUMN expected_sha256 text;
ALTER TABLE assets ADD COLUMN sha256 text;

-- The queue survives node/session deletion so a still-valid PUT capability
-- cannot recreate a staging object after the owning account is removed.
CREATE TABLE object_cleanup (
  object_key text PRIMARY KEY,
  multipart_id text,
  delete_after timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX object_cleanup_due_idx ON object_cleanup(delete_after);

UPDATE upload_sessions u SET staging_key=a.object_key
FROM assets a WHERE a.id=u.asset_id AND u.state<>'ready';
UPDATE assets a SET object_key='original/'||a.space_id::text||'/'||gen_random_uuid()::text
FROM upload_sessions u WHERE u.asset_id=a.id AND u.staging_key IS NOT NULL;
INSERT INTO object_cleanup(object_key,multipart_id,delete_after)
SELECT staging_key,upload_id,GREATEST(expires_at,now())+interval '25 hours'
FROM upload_sessions WHERE staging_key IS NOT NULL;
INSERT INTO object_cleanup(object_key,delete_after)
SELECT a.object_key,GREATEST(u.expires_at,now())+interval '25 hours'
FROM assets a JOIN upload_sessions u ON u.asset_id=a.id WHERE u.staging_key IS NOT NULL;
