// Package cmms models maintenance work orders and the boundary to an external
// Computerised Maintenance Management System (Maximo, SAP PM, ...). The
// engine decides *whether* a work order is warranted; this package decides
// how it looks on the wire and de-duplicates against what is already open.
package cmms

import (
	"context"
	"fmt"
	"time"
)

// RuleWorkOrder is logged with every work-order decision.
const RuleWorkOrder = "WO-1"

// Status is the work-order lifecycle as far as this system tracks it.
type Status string

const (
	StatusOpen      Status = "open"
	StatusCancelled Status = "cancelled"
	StatusClosed    Status = "closed"
)

// Policy is the per-asset-class work-order policy.
type Policy struct {
	// ConfidenceThreshold is the fused confidence required before a work
	// order is raised for a fired alert.
	ConfidenceThreshold float64 `json:"confidence_threshold"`
	// FailureMode is the default failure mode for this class (e.g.
	// "bearing_wear"); a template may override it per contributor prefix.
	FailureMode string `json:"failure_mode"`
	// FailureModeBySignal maps a signal name to the failure mode it implies,
	// used when the top contributor comes from that signal.
	FailureModeBySignal map[string]string `json:"failure_mode_by_signal,omitempty"`
	// Priority is the CMMS priority (1 = highest).
	Priority int `json:"priority"`
	// LeadTime is the requested window between creation and required-by.
	LeadTime Duration `json:"lead_time"`
	// AutoCancelOnDismiss cancels the linked work order when the alert is
	// dismissed by a technician.
	AutoCancelOnDismiss bool `json:"auto_cancel_on_dismiss"`
}

// Validate checks the policy.
func (p Policy) Validate() error {
	if p.ConfidenceThreshold < 0 || p.ConfidenceThreshold > 1 {
		return fmt.Errorf("cmms: confidence_threshold must be in [0,1]")
	}
	if p.FailureMode == "" {
		return fmt.Errorf("cmms: failure_mode is required")
	}
	if p.Priority < 1 || p.Priority > 5 {
		return fmt.Errorf("cmms: priority must be in 1..5")
	}
	if p.LeadTime.Duration < 0 {
		return fmt.Errorf("cmms: lead_time must be >= 0")
	}
	return nil
}

// ResolveFailureMode picks the failure mode for a top-contributing feature
// ("<signal>.<stat>") using FailureModeBySignal, falling back to the default.
func (p Policy) ResolveFailureMode(topFeature string) string {
	for i := 0; i < len(topFeature); i++ {
		if topFeature[i] == '.' {
			if fm, ok := p.FailureModeBySignal[topFeature[:i]]; ok {
				return fm
			}
			break
		}
	}
	return p.FailureMode
}

// Duration marshals as a Go duration string.
type Duration struct{ time.Duration }

// MarshalJSON renders "72h0m0s"-style strings.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Duration.String() + `"`), nil
}

// UnmarshalJSON accepts duration strings or integer nanoseconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' {
		v, err := time.ParseDuration(s[1 : len(s)-1])
		if err != nil {
			return fmt.Errorf("cmms: bad duration %s: %w", s, err)
		}
		d.Duration = v
		return nil
	}
	var ns int64
	if _, err := fmt.Sscan(s, &ns); err != nil {
		return fmt.Errorf("cmms: bad duration %s: %w", s, err)
	}
	d.Duration = time.Duration(ns)
	return nil
}

// WorkOrder is the system-of-record view of a maintenance request.
type WorkOrder struct {
	ID          string    `json:"id"`
	AlertID     string    `json:"alert_id"`
	AssetID     string    `json:"asset_id"`
	Site        string    `json:"site"`
	AssetClass  string    `json:"asset_class"`
	FailureMode string    `json:"failure_mode"`
	Priority    int       `json:"priority"`
	Confidence  float64   `json:"confidence"`
	Risk        float64   `json:"risk"`
	Description string    `json:"description"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	RequiredBy  time.Time `json:"required_by"`
	UpdatedAt   time.Time `json:"updated_at"`
	// External is the CMMS-side identity once created.
	External *ExternalRef `json:"external,omitempty"`
	// IdempotencyKey is asset+failure mode+alert; a replayed alert cannot
	// create a second work order.
	IdempotencyKey string `json:"idempotency_key"`
	Rule           string `json:"rule"`
}

// ExternalRef identifies the work order inside the CMMS.
type ExternalRef struct {
	System string `json:"system"` // "simulated", "maximo", "sap-pm"
	ID     string `json:"id"`
	URL    string `json:"url,omitempty"`
}

// CMMS is the outbound port. Implementations must be idempotent on
// IdempotencyKey: creating the same work order twice returns the same ref.
type CMMS interface {
	Name() string
	CreateWorkOrder(ctx context.Context, wo WorkOrder) (ExternalRef, error)
	CancelWorkOrder(ctx context.Context, ref ExternalRef, reason string) error
}

// Decision explains whether a work order was raised for an alert.
type Decision struct {
	Create      bool    `json:"create"`
	Reason      string  `json:"reason"`
	Confidence  float64 `json:"confidence"`
	Threshold   float64 `json:"threshold"`
	FailureMode string  `json:"failure_mode"`
	ExistingWO  string  `json:"existing_work_order_id,omitempty"`
	Rule        string  `json:"rule"`
}

// Decide applies rule WO-1: create when confidence >= threshold and no open
// work order exists for the same asset and failure mode.
func (p Policy) Decide(confidence float64, topFeature string, openWO *WorkOrder) Decision {
	d := Decision{Confidence: confidence, Threshold: p.ConfidenceThreshold, Rule: RuleWorkOrder}
	d.FailureMode = p.ResolveFailureMode(topFeature)
	if confidence < p.ConfidenceThreshold {
		d.Reason = fmt.Sprintf("confidence %.3f below threshold %.3f", confidence, p.ConfidenceThreshold)
		return d
	}
	if openWO != nil && openWO.Status == StatusOpen {
		d.ExistingWO = openWO.ID
		d.Reason = fmt.Sprintf("open work order %s already covers %s/%s", openWO.ID, openWO.AssetID, openWO.FailureMode)
		return d
	}
	d.Create = true
	d.Reason = fmt.Sprintf("confidence %.3f >= %.3f and no open work order for %s", confidence, p.ConfidenceThreshold, d.FailureMode)
	return d
}

// Key returns the idempotency key for an asset/failure-mode/alert triple.
func Key(assetID, failureMode, alertID string) string {
	return assetID + "|" + failureMode + "|" + alertID
}
