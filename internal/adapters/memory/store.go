// Package memory is the zero-infrastructure Store: everything lives in maps
// guarded by one mutex. It is what `make run` and every test use.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
)

// Store implements ports.Store in memory.
type Store struct {
	mu         sync.RWMutex
	alerts     map[string]alerting.Alert
	workOrders map[string]cmms.WorkOrder
	templates  map[string]template.Template
	assets     map[string]ports.AssetSnapshot
	alertSeq   map[string]int // insertion order for stable listing
	woSeq      map[string]int
	seq        int
}

// New returns an empty store.
func New() *Store {
	return &Store{
		alerts:     map[string]alerting.Alert{},
		workOrders: map[string]cmms.WorkOrder{},
		templates:  map[string]template.Template{},
		assets:     map[string]ports.AssetSnapshot{},
		alertSeq:   map[string]int{},
		woSeq:      map[string]int{},
	}
}

var _ ports.Store = (*Store)(nil)

// SaveAlert upserts an alert.
func (s *Store) SaveAlert(ctx context.Context, a alerting.Alert) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.alertSeq[a.ID]; !ok {
		s.seq++
		s.alertSeq[a.ID] = s.seq
	}
	s.alerts[a.ID] = cloneAlert(a)
	return nil
}

// GetAlert fetches one alert.
func (s *Store) GetAlert(ctx context.Context, id string) (alerting.Alert, error) {
	if err := ctx.Err(); err != nil {
		return alerting.Alert{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.alerts[id]
	if !ok {
		return alerting.Alert{}, ports.ErrNotFound
	}
	return cloneAlert(a), nil
}

// ListAlerts returns alerts newest-first.
func (s *Store) ListAlerts(ctx context.Context, f ports.AlertFilter) ([]alerting.Alert, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]alerting.Alert, 0, len(s.alerts))
	for _, a := range s.alerts {
		if f.Site != "" && a.Site != f.Site {
			continue
		}
		if f.AssetID != "" && a.AssetID != f.AssetID {
			continue
		}
		if f.Status != "" && a.Status != f.Status {
			continue
		}
		out = append(out, cloneAlert(a))
	}
	sort.Slice(out, func(i, j int) bool { return s.alertSeq[out[i].ID] > s.alertSeq[out[j].ID] })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// SaveWorkOrder upserts a work order.
func (s *Store) SaveWorkOrder(ctx context.Context, wo cmms.WorkOrder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.woSeq[wo.ID]; !ok {
		s.seq++
		s.woSeq[wo.ID] = s.seq
	}
	s.workOrders[wo.ID] = wo
	return nil
}

// GetWorkOrder fetches one work order.
func (s *Store) GetWorkOrder(ctx context.Context, id string) (cmms.WorkOrder, error) {
	if err := ctx.Err(); err != nil {
		return cmms.WorkOrder{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	wo, ok := s.workOrders[id]
	if !ok {
		return cmms.WorkOrder{}, ports.ErrNotFound
	}
	return wo, nil
}

// ListWorkOrders returns work orders newest-first.
func (s *Store) ListWorkOrders(ctx context.Context, f ports.WorkOrderFilter) ([]cmms.WorkOrder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]cmms.WorkOrder, 0, len(s.workOrders))
	for _, wo := range s.workOrders {
		if f.Site != "" && wo.Site != f.Site {
			continue
		}
		if f.AssetID != "" && wo.AssetID != f.AssetID {
			continue
		}
		if f.Status != "" && wo.Status != f.Status {
			continue
		}
		out = append(out, wo)
	}
	sort.Slice(out, func(i, j int) bool { return s.woSeq[out[i].ID] > s.woSeq[out[j].ID] })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// FindOpenWorkOrder returns the open work order for asset/failure mode.
func (s *Store) FindOpenWorkOrder(ctx context.Context, assetID, failureMode string) (cmms.WorkOrder, error) {
	if err := ctx.Err(); err != nil {
		return cmms.WorkOrder{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, wo := range s.workOrders {
		if wo.AssetID == assetID && wo.FailureMode == failureMode && wo.Status == cmms.StatusOpen {
			return wo, nil
		}
	}
	return cmms.WorkOrder{}, ports.ErrNotFound
}

// PutTemplate upserts a template.
func (s *Store) PutTemplate(ctx context.Context, t template.Template) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates[t.Class] = t
	return nil
}

// GetTemplate fetches a template by class.
func (s *Store) GetTemplate(ctx context.Context, class string) (template.Template, error) {
	if err := ctx.Err(); err != nil {
		return template.Template{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.templates[class]
	if !ok {
		return template.Template{}, ports.ErrNotFound
	}
	return t, nil
}

// ListTemplates returns templates sorted by class.
func (s *Store) ListTemplates(ctx context.Context) ([]template.Template, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]template.Template, 0, len(s.templates))
	for _, t := range s.templates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out, nil
}

// SaveAssetSnapshot upserts a snapshot.
func (s *Store) SaveAssetSnapshot(ctx context.Context, snap ports.AssetSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[snap.AssetID] = snap
	return nil
}

// ListAssetSnapshots returns snapshots sorted by asset id.
func (s *Store) ListAssetSnapshots(ctx context.Context) ([]ports.AssetSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ports.AssetSnapshot, 0, len(s.assets))
	for _, a := range s.assets {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AssetID < out[j].AssetID })
	return out, nil
}

// Ping always succeeds.
func (s *Store) Ping(ctx context.Context) error { return ctx.Err() }

// Close is a no-op.
func (s *Store) Close() error { return nil }

func cloneAlert(a alerting.Alert) alerting.Alert {
	c := a
	c.Contributors = append([]alerting.Contributor(nil), a.Contributors...)
	c.Rules = append([]string(nil), a.Rules...)
	if a.ClearedAt != nil {
		t := *a.ClearedAt
		c.ClearedAt = &t
	}
	if a.FeedbackAt != nil {
		t := *a.FeedbackAt
		c.FeedbackAt = &t
	}
	if a.Adaptation != nil {
		ad := *a.Adaptation
		c.Adaptation = &ad
	}
	return c
}
