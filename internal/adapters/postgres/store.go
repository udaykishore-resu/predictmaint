// Package postgres implements ports.Store on PostgreSQL via pgx. Records are
// JSONB documents with the filterable columns promoted; all writes are
// upserts keyed by id so replays are harmless.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
)

// Store is the PostgreSQL adapter.
type Store struct {
	pool *pgxpool.Pool
}

var _ ports.Store = (*Store)(nil)

// Open connects and pings.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Migrate applies every *.sql file in dir, in lexical order, once. Each file
// runs in its own transaction and is recorded in schema_migrations.
func (s *Store) Migrate(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("postgres: read migrations: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if _, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("postgres: ensure schema_migrations: %w", err)
	}
	for _, f := range files {
		version := strings.TrimSuffix(f, ".sql")
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
			return fmt.Errorf("postgres: check migration %s: %w", version, err)
		}
		if exists {
			continue
		}
		// Path is built from the operator-configured migrations directory and a
		// directory-listing entry (not user input); Clean keeps gosec G304 honest.
		sqlBytes, err := os.ReadFile(filepath.Clean(filepath.Join(dir, f)))
		if err != nil {
			return fmt.Errorf("postgres: read %s: %w", f, err)
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("postgres: begin: %w", err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: apply %s: %w", f, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: record %s: %w", f, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("postgres: commit %s: %w", f, err)
		}
	}
	return nil
}

// Ping implements ports.Store.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close implements ports.Store.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

// SaveAlert upserts an alert.
func (s *Store) SaveAlert(ctx context.Context, a alerting.Alert) error {
	payload, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("postgres: marshal alert: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO alerts (id, asset_id, site, status, detected_at, payload)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, payload = EXCLUDED.payload`,
		a.ID, a.AssetID, a.Site, string(a.Status), a.DetectedAt, payload)
	if err != nil {
		return fmt.Errorf("postgres: save alert: %w", err)
	}
	return nil
}

// GetAlert fetches one alert.
func (s *Store) GetAlert(ctx context.Context, id string) (alerting.Alert, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM alerts WHERE id = $1`, id).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return alerting.Alert{}, ports.ErrNotFound
	}
	if err != nil {
		return alerting.Alert{}, fmt.Errorf("postgres: get alert: %w", err)
	}
	var a alerting.Alert
	if err := json.Unmarshal(payload, &a); err != nil {
		return alerting.Alert{}, fmt.Errorf("postgres: decode alert: %w", err)
	}
	return a, nil
}

// ListAlerts returns alerts newest-first.
func (s *Store) ListAlerts(ctx context.Context, f ports.AlertFilter) ([]alerting.Alert, error) {
	q, args := buildList("alerts", map[string]string{"site": f.Site, "asset_id": f.AssetID, "status": string(f.Status)}, f.Limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list alerts: %w", err)
	}
	defer rows.Close()
	var out []alerting.Alert
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("postgres: scan alert: %w", err)
		}
		var a alerting.Alert
		if err := json.Unmarshal(payload, &a); err != nil {
			return nil, fmt.Errorf("postgres: decode alert: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveWorkOrder upserts a work order.
func (s *Store) SaveWorkOrder(ctx context.Context, wo cmms.WorkOrder) error {
	payload, err := json.Marshal(wo)
	if err != nil {
		return fmt.Errorf("postgres: marshal work order: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO work_orders (id, asset_id, site, failure_mode, status, payload)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, payload = EXCLUDED.payload`,
		wo.ID, wo.AssetID, wo.Site, wo.FailureMode, string(wo.Status), payload)
	if err != nil {
		return fmt.Errorf("postgres: save work order: %w", err)
	}
	return nil
}

// GetWorkOrder fetches one work order.
func (s *Store) GetWorkOrder(ctx context.Context, id string) (cmms.WorkOrder, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM work_orders WHERE id = $1`, id).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return cmms.WorkOrder{}, ports.ErrNotFound
	}
	if err != nil {
		return cmms.WorkOrder{}, fmt.Errorf("postgres: get work order: %w", err)
	}
	var wo cmms.WorkOrder
	if err := json.Unmarshal(payload, &wo); err != nil {
		return cmms.WorkOrder{}, fmt.Errorf("postgres: decode work order: %w", err)
	}
	return wo, nil
}

// ListWorkOrders returns work orders newest-first.
func (s *Store) ListWorkOrders(ctx context.Context, f ports.WorkOrderFilter) ([]cmms.WorkOrder, error) {
	q, args := buildList("work_orders", map[string]string{"site": f.Site, "asset_id": f.AssetID, "status": string(f.Status)}, f.Limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list work orders: %w", err)
	}
	defer rows.Close()
	var out []cmms.WorkOrder
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("postgres: scan work order: %w", err)
		}
		var wo cmms.WorkOrder
		if err := json.Unmarshal(payload, &wo); err != nil {
			return nil, fmt.Errorf("postgres: decode work order: %w", err)
		}
		out = append(out, wo)
	}
	return out, rows.Err()
}

// FindOpenWorkOrder returns the open work order for asset/failure mode.
func (s *Store) FindOpenWorkOrder(ctx context.Context, assetID, failureMode string) (cmms.WorkOrder, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM work_orders WHERE asset_id = $1 AND failure_mode = $2 AND status = 'open' LIMIT 1`,
		assetID, failureMode).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return cmms.WorkOrder{}, ports.ErrNotFound
	}
	if err != nil {
		return cmms.WorkOrder{}, fmt.Errorf("postgres: find open work order: %w", err)
	}
	var wo cmms.WorkOrder
	if err := json.Unmarshal(payload, &wo); err != nil {
		return cmms.WorkOrder{}, fmt.Errorf("postgres: decode work order: %w", err)
	}
	return wo, nil
}

// PutTemplate upserts a template.
func (s *Store) PutTemplate(ctx context.Context, t template.Template) error {
	payload, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("postgres: marshal template: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO templates (class, version, payload, updated_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (class) DO UPDATE SET version = EXCLUDED.version, payload = EXCLUDED.payload, updated_at = now()`,
		t.Class, t.Version, payload)
	if err != nil {
		return fmt.Errorf("postgres: save template: %w", err)
	}
	return nil
}

// GetTemplate fetches a template.
func (s *Store) GetTemplate(ctx context.Context, class string) (template.Template, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM templates WHERE class = $1`, class).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return template.Template{}, ports.ErrNotFound
	}
	if err != nil {
		return template.Template{}, fmt.Errorf("postgres: get template: %w", err)
	}
	var t template.Template
	if err := json.Unmarshal(payload, &t); err != nil {
		return template.Template{}, fmt.Errorf("postgres: decode template: %w", err)
	}
	return t, nil
}

// ListTemplates returns all templates sorted by class.
func (s *Store) ListTemplates(ctx context.Context) ([]template.Template, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM templates ORDER BY class`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list templates: %w", err)
	}
	defer rows.Close()
	var out []template.Template
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("postgres: scan template: %w", err)
		}
		var t template.Template
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("postgres: decode template: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveAssetSnapshot upserts a snapshot.
func (s *Store) SaveAssetSnapshot(ctx context.Context, snap ports.AssetSnapshot) error {
	payload, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("postgres: marshal snapshot: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO asset_snapshots (asset_id, site, asset_class, payload, updated_at) VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (asset_id) DO UPDATE SET site = EXCLUDED.site, asset_class = EXCLUDED.asset_class, payload = EXCLUDED.payload, updated_at = now()`,
		snap.AssetID, snap.Site, snap.AssetClass, payload)
	if err != nil {
		return fmt.Errorf("postgres: save snapshot: %w", err)
	}
	return nil
}

// ListAssetSnapshots returns all snapshots.
func (s *Store) ListAssetSnapshots(ctx context.Context) ([]ports.AssetSnapshot, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM asset_snapshots ORDER BY asset_id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list snapshots: %w", err)
	}
	defer rows.Close()
	var out []ports.AssetSnapshot
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("postgres: scan snapshot: %w", err)
		}
		var snap ports.AssetSnapshot
		if err := json.Unmarshal(payload, &snap); err != nil {
			return nil, fmt.Errorf("postgres: decode snapshot: %w", err)
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// buildList composes a filtered, newest-first SELECT. Column names come
// from a fixed map, never from input; values are always parameters.
func buildList(table string, filters map[string]string, limit int) (string, []any) {
	var where []string
	var args []any
	for _, col := range []string{"site", "asset_id", "status"} {
		if v := filters[col]; v != "" {
			args = append(args, v)
			where = append(where, fmt.Sprintf("%s = $%d", col, len(args)))
		}
	}
	q := "SELECT payload FROM " + table
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY seq DESC"
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return q, args
}
