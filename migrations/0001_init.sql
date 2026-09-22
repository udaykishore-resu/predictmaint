-- predictmaint schema. Documents are stored as JSONB with the columns that
-- queries filter on promoted to real columns. See docs/adr/0002-data-model.md.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS templates (
    class      TEXT PRIMARY KEY,
    version    TEXT NOT NULL,
    payload    JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS asset_snapshots (
    asset_id    TEXT PRIMARY KEY,
    site        TEXT NOT NULL,
    asset_class TEXT NOT NULL,
    payload     JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS alerts (
    id          TEXT PRIMARY KEY,
    seq         BIGSERIAL NOT NULL,
    asset_id    TEXT NOT NULL,
    site        TEXT NOT NULL,
    status      TEXT NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL,
    payload     JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS alerts_site_seq_idx  ON alerts (site, seq DESC);
CREATE INDEX IF NOT EXISTS alerts_asset_seq_idx ON alerts (asset_id, seq DESC);
CREATE INDEX IF NOT EXISTS alerts_status_idx    ON alerts (status);

CREATE TABLE IF NOT EXISTS work_orders (
    id           TEXT PRIMARY KEY,
    seq          BIGSERIAL NOT NULL,
    asset_id     TEXT NOT NULL,
    site         TEXT NOT NULL,
    failure_mode TEXT NOT NULL,
    status       TEXT NOT NULL,
    payload      JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS work_orders_site_seq_idx  ON work_orders (site, seq DESC);
CREATE INDEX IF NOT EXISTS work_orders_asset_seq_idx ON work_orders (asset_id, seq DESC);
-- One open work order per asset and failure mode, enforced by the database
-- as the last line of defence behind the engine's dedupe.
CREATE UNIQUE INDEX IF NOT EXISTS work_orders_open_unique
    ON work_orders (asset_id, failure_mode) WHERE status = 'open';
