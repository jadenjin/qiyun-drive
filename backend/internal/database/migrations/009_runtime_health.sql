CREATE TABLE runtime_health (
 component text PRIMARY KEY,
 updated_at timestamptz NOT NULL DEFAULT now()
);
