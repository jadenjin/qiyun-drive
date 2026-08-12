-- Read paths dominate a personal cloud drive. These indexes cover timeline,
-- folder, session, share, audit, and maintenance queries as data grows.
CREATE INDEX sessions_user_expiry_idx ON sessions(user_id, expires_at DESC);
CREATE INDEX invitations_household_created_idx ON invitations(household_id, created_at DESC);
CREATE INDEX password_resets_user_open_idx ON password_resets(user_id, created_at DESC) WHERE used_at IS NULL;

CREATE INDEX assets_space_status_idx ON assets(space_id, status);
CREATE INDEX nodes_deleted_at_idx ON nodes(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX nodes_space_updated_idx ON nodes(space_id, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX upload_sessions_user_created_idx ON upload_sessions(user_id, created_at DESC);
CREATE INDEX upload_sessions_cleanup_idx ON upload_sessions(state, expires_at);

CREATE INDEX albums_space_updated_idx ON albums(space_id, updated_at DESC);
CREATE INDEX album_items_album_order_idx ON album_items(album_id, position, created_at);
CREATE INDEX public_shares_creator_created_idx ON public_shares(created_by, created_at DESC);
CREATE INDEX share_access_tokens_share_expiry_idx ON share_access_tokens(share_id, expires_at);
CREATE INDEX audit_events_household_created_idx ON audit_events(household_id, created_at DESC);
