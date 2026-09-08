-- Album membership delegates access only while the person who added the
-- photo can still publish it: source manager, or its uploader with edit access.
CREATE FUNCTION can_republish_photo(grantor uuid, photo uuid)
RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH RECURSIVE target AS (
   SELECT n.id,n.parent_id,n.inherit_permissions,n.created_by,
     CASE WHEN sp.kind='personal' THEN CASE WHEN sp.owner_user_id=grantor THEN 3 ELSE 0 END
          WHEN hm.role IN ('owner','admin') THEN 3 ELSE 2 END AS base
   FROM nodes n JOIN assets a ON a.id=n.asset_id AND a.status='ready'
   JOIN spaces sp ON sp.id=n.space_id
   JOIN household_members hm ON hm.household_id=sp.household_id AND hm.user_id=grantor
   JOIN users u ON u.id=grantor AND NOT u.disabled
   WHERE n.id=photo AND n.section='photos' AND n.deleted_at IS NULL AND n.purge_job_id IS NULL
 ), chain(id,parent_id,inherit_permissions,depth) AS (
   SELECT id,parent_id,inherit_permissions,0 FROM target
   UNION ALL
   SELECT n.id,n.parent_id,n.inherit_permissions,c.depth+1 FROM nodes n JOIN chain c ON n.id=c.parent_id
 ), permission AS (
   SELECT t.created_by,CASE WHEN t.base IN (0,3) THEN t.base ELSE COALESCE((
     SELECT CASE acl.permission WHEN 'manager' THEN 3 WHEN 'editor' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END
     FROM chain c LEFT JOIN acl_entries acl ON acl.resource_type='node' AND acl.resource_id=c.id AND acl.principal_user_id=grantor
     WHERE acl.permission IS NOT NULL OR NOT c.inherit_permissions ORDER BY c.depth LIMIT 1
   ),t.base) END AS level FROM target t
 )
 SELECT EXISTS(SELECT 1 FROM permission WHERE level>=3 OR (created_by=grantor AND level>=2))
$$;
