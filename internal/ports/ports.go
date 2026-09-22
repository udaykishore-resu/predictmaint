// Package ports declares the interfaces through which the domain talks to
// the outside world. Adapters (memory, postgres, kafka) implement them; the
// domain never imports an adapter.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/detect"
	"github.com/udaykishore-resu/predictmaint/internal/domain/metric"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
)

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("not found")

// AlertFilter narrows ListAlerts. Zero values mean "any".
type AlertFilter struct {
	Site    string
	AssetID string
	Status  alerting.Status
	Limit   int
}

// AlertStore persists alerts.
type AlertStore interface {
	SaveAlert(ctx context.Context, a alerting.Alert) error
	GetAlert(ctx context.Context, id string) (alerting.Alert, error)
	ListAlerts(ctx context.Context, f AlertFilter) ([]alerting.Alert, error)
}

// WorkOrderFilter narrows ListWorkOrders.
type WorkOrderFilter struct {
	Site    string
	AssetID string
	Status  cmms.Status
	Limit   int
}

// WorkOrderStore persists work orders.
type WorkOrderStore interface {
	SaveWorkOrder(ctx context.Context, wo cmms.WorkOrder) error
	GetWorkOrder(ctx context.Context, id string) (cmms.WorkOrder, error)
	ListWorkOrders(ctx context.Context, f WorkOrderFilter) ([]cmms.WorkOrder, error)
	// FindOpenWorkOrder returns the open work order for an asset/failure mode
	// pair, or ErrNotFound.
	FindOpenWorkOrder(ctx context.Context, assetID, failureMode string) (cmms.WorkOrder, error)
}

// TemplateStore persists asset-class templates.
type TemplateStore interface {
	PutTemplate(ctx context.Context, t template.Template) error
	GetTemplate(ctx context.Context, class string) (template.Template, error)
	ListTemplates(ctx context.Context) ([]template.Template, error)
}

// AssetSnapshot is the durable part of per-asset learning: what must
// survive a restart so the fleet does not re-enter cold start.
type AssetSnapshot struct {
	AssetID         string           `json:"asset_id"`
	Site            string           `json:"site"`
	AssetClass      string           `json:"asset_class"`
	Baselines       detect.Baselines `json:"baselines,omitempty"`
	BaselineSource  string           `json:"baseline_source"`
	ThresholdOffset float64          `json:"threshold_offset"`
	AgeHours        float64          `json:"age_hours"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// AssetStore persists asset snapshots.
type AssetStore interface {
	SaveAssetSnapshot(ctx context.Context, s AssetSnapshot) error
	ListAssetSnapshots(ctx context.Context) ([]AssetSnapshot, error)
}

// Store is the composite persistence port.
type Store interface {
	AlertStore
	WorkOrderStore
	TemplateStore
	AssetStore
	// Ping reports whether the backing system is reachable (readiness).
	Ping(ctx context.Context) error
	Close() error
}

// MetricHandler consumes one batch of metrics. Returning an error signals
// the source to retry the batch (sources must therefore deliver at least
// once and the handler must be idempotent).
type MetricHandler func(ctx context.Context, batch []metric.Metric) error

// MetricSource streams metric batches until ctx is cancelled.
type MetricSource interface {
	Run(ctx context.Context, h MetricHandler) error
}
