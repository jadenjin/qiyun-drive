-- A queued permanent-delete job owns its root until it succeeds or exhausts
-- its retries. This prevents a restored node from being deleted by a worker
-- that was already processing an older purge request.
ALTER TABLE nodes
  ADD COLUMN purge_job_id uuid REFERENCES jobs(id) ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX nodes_purge_job_idx
  ON nodes(purge_job_id)
  WHERE purge_job_id IS NOT NULL;
