-- Keep uploads made from the file browser and the photo library in separate
-- namespaces. Existing album items are the only historical uploads whose
-- photo-library origin can be identified reliably.
ALTER TABLE nodes
  ADD COLUMN section text NOT NULL DEFAULT 'files'
  CHECK (section IN ('files','photos'));

UPDATE nodes
SET section='photos'
WHERE kind='file' AND id IN (SELECT node_id FROM album_items);

ALTER TABLE nodes
  ADD CONSTRAINT nodes_section_kind_check
  CHECK (kind='file' OR section='files');

DROP INDEX nodes_active_name_idx;
CREATE UNIQUE INDEX nodes_active_name_idx
  ON nodes(space_id, section, COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name))
  WHERE deleted_at IS NULL;

CREATE INDEX nodes_photo_timeline_idx
  ON nodes(space_id, created_at DESC)
  WHERE section='photos' AND deleted_at IS NULL;
