CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE households (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  timezone text NOT NULL DEFAULT 'Asia/Shanghai',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id uuid PRIMARY KEY,
  username text NOT NULL,
  display_name text NOT NULL,
  password_hash text NOT NULL,
  disabled boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_username_lower_idx ON users(lower(username));

CREATE TABLE household_members (
  household_id uuid NOT NULL REFERENCES households(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('owner','admin','member')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (household_id, user_id)
);

CREATE TABLE spaces (
  id uuid PRIMARY KEY,
  household_id uuid NOT NULL REFERENCES households(id) ON DELETE CASCADE,
  owner_user_id uuid REFERENCES users(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('personal','family')),
  name text NOT NULL,
  quota_bytes bigint NOT NULL DEFAULT 0 CHECK (quota_bytes >= 0),
  used_bytes bigint NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
  reserved_bytes bigint NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((kind='personal' AND owner_user_id IS NOT NULL) OR (kind='family' AND owner_user_id IS NULL))
);
CREATE UNIQUE INDEX spaces_personal_owner_idx ON spaces(owner_user_id) WHERE kind='personal';
CREATE UNIQUE INDEX spaces_family_household_idx ON spaces(household_id) WHERE kind='family';

CREATE TABLE sessions (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE invitations (
  id uuid PRIMARY KEY,
  household_id uuid NOT NULL REFERENCES households(id) ON DELETE CASCADE,
  created_by uuid NOT NULL REFERENCES users(id),
  token_hash text NOT NULL UNIQUE,
  role text NOT NULL DEFAULT 'member' CHECK (role IN ('admin','member')),
  expires_at timestamptz NOT NULL,
  accepted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE password_resets (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE assets (
  id uuid PRIMARY KEY,
  space_id uuid NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  object_key text NOT NULL UNIQUE,
  size_bytes bigint NOT NULL DEFAULT 0,
  mime_type text NOT NULL DEFAULT 'application/octet-stream',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','failed','deleted')),
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE nodes (
  id uuid PRIMARY KEY,
  space_id uuid NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  parent_id uuid REFERENCES nodes(id) ON DELETE CASCADE,
  asset_id uuid REFERENCES assets(id) ON DELETE SET NULL,
  kind text NOT NULL CHECK (kind IN ('file','folder')),
  name text NOT NULL,
  inherit_permissions boolean NOT NULL DEFAULT true,
  created_by uuid NOT NULL REFERENCES users(id),
  deleted_at timestamptz,
  original_parent_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((kind='folder' AND asset_id IS NULL) OR (kind='file' AND asset_id IS NOT NULL))
);
CREATE UNIQUE INDEX nodes_active_name_idx ON nodes(space_id, COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name)) WHERE deleted_at IS NULL;
CREATE INDEX nodes_parent_idx ON nodes(space_id, parent_id) WHERE deleted_at IS NULL;

CREATE TABLE upload_batches (
  id uuid PRIMARY KEY,
  space_id uuid NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  created_by uuid NOT NULL REFERENCES users(id),
  state text NOT NULL DEFAULT 'uploading',
  total_files integer NOT NULL DEFAULT 0,
  completed_files integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);

CREATE TABLE upload_sessions (
  id uuid PRIMARY KEY,
  batch_id uuid REFERENCES upload_batches(id) ON DELETE SET NULL,
  asset_id uuid NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  upload_id text,
  method text NOT NULL CHECK (method IN ('put','multipart')),
  expected_size bigint NOT NULL,
  state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','uploading','completing','ready','failed','expired','aborted')),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE photo_details (
  asset_id uuid PRIMARY KEY REFERENCES assets(id) ON DELETE CASCADE,
  taken_at timestamptz,
  width integer,
  height integer,
  camera text,
  remark text NOT NULL DEFAULT '',
  thumb_small_key text,
  thumb_large_key text,
  indexed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE albums (
  id uuid PRIMARY KEY,
  space_id uuid NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  inherit_permissions boolean NOT NULL DEFAULT true,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE album_items (
  album_id uuid NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  position integer NOT NULL DEFAULT 0,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (album_id, node_id)
);

CREATE TABLE acl_entries (
  id uuid PRIMARY KEY,
  resource_type text NOT NULL CHECK (resource_type IN ('node','album')),
  resource_id uuid NOT NULL,
  principal_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  permission text NOT NULL CHECK (permission IN ('viewer','editor','manager')),
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(resource_type, resource_id, principal_user_id)
);

CREATE TABLE public_shares (
  id uuid PRIMARY KEY,
  resource_type text NOT NULL CHECK (resource_type IN ('file','folder','album')),
  resource_id uuid NOT NULL,
  token_hash text NOT NULL UNIQUE,
  password_hash text,
  allow_download boolean NOT NULL DEFAULT true,
  expires_at timestamptz,
  revoked_at timestamptz,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE share_access_tokens (
  id uuid PRIMARY KEY,
  share_id uuid NOT NULL REFERENCES public_shares(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
  id uuid PRIMARY KEY,
  kind text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','running','done','failed')),
  attempts integer NOT NULL DEFAULT 0,
  run_after timestamptz NOT NULL DEFAULT now(),
  locked_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_ready_idx ON jobs(state, run_after);

CREATE TABLE audit_events (
  id uuid PRIMARY KEY,
  household_id uuid REFERENCES households(id) ON DELETE CASCADE,
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  action text NOT NULL,
  resource_type text,
  resource_id uuid,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
