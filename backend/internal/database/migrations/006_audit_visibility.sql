ALTER TABLE audit_events ADD COLUMN visibility text NOT NULL DEFAULT 'actor'
  CHECK (visibility IN ('actor','family'));

CREATE FUNCTION audit_visibility(household uuid, kind text, resource uuid, explicit_space uuid DEFAULT NULL)
RETURNS text LANGUAGE sql STABLE AS $$
  SELECT CASE WHEN kind NOT IN ('node','album','share') OR EXISTS (
    SELECT 1 FROM spaces sp WHERE sp.household_id=household AND sp.kind='family' AND (
      sp.id=explicit_space OR
      EXISTS(SELECT 1 FROM nodes n WHERE kind='node' AND n.id=resource AND n.space_id=sp.id) OR
      EXISTS(SELECT 1 FROM albums al WHERE kind='album' AND al.id=resource AND al.space_id=sp.id) OR
      EXISTS(SELECT 1 FROM public_shares sh WHERE kind='share' AND sh.id=resource AND (
        EXISTS(SELECT 1 FROM nodes n WHERE sh.resource_type IN ('file','folder') AND n.id=sh.resource_id AND n.space_id=sp.id) OR
        EXISTS(SELECT 1 FROM albums al WHERE sh.resource_type='album' AND al.id=sh.resource_id AND al.space_id=sp.id)
      ))
    )
  ) THEN 'family' ELSE 'actor' END
$$;

-- Unknown/deleted historical resources remain actor-only. Future events store
-- their visibility at write time, before the resource can disappear.
UPDATE audit_events SET visibility=audit_visibility(household_id,resource_type,resource_id);
