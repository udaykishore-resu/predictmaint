// Package template defines asset-class templates: the single place where a
// reliability engineer tunes how a *class* of machine (pump, motor,
// compressor) is monitored. Individual assets never own hyperparameters;
// they only learn baselines and a bounded threshold offset. That is what
// makes the approach roll out across a fleet instead of dying as a pilot.
package template

import (
	"fmt"
	"strings"
	"time"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/detect"
	"github.com/udaykishore-resu/predictmaint/internal/domain/features"
)

// Detectors groups the detector hyperparameters of a class.
type Detectors struct {
	RobustZ detect.RobustZParams `json:"robust_z"`
	HST     detect.HSTParams     `json:"hst"`
	CUSUM   detect.CUSUMParams   `json:"cusum"`
}

// Template is a full monitoring recipe for one asset class.
type Template struct {
	Class       string `json:"class"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`

	Signals []features.SignalSpec `json:"signals"`
	// RuntimeSignal, when present in incoming metrics, sets the asset's
	// operating hours for the Weibull prior instead of being a feature.
	RuntimeSignal string `json:"runtime_signal,omitempty"`

	Detectors Detectors           `json:"detectors"`
	Prior     detect.Weibull      `json:"prior"`
	Fusion    detect.FusionParams `json:"fusion"`
	Alerting  alerting.Params     `json:"alerting"`
	WorkOrder cmms.Policy         `json:"work_order"`

	// WarmupEvaluations is how many feature vectors an asset accumulates
	// before fitting its own baselines. Until then ClassBaselines are used.
	WarmupEvaluations int              `json:"warmup_evaluations"`
	ClassBaselines    detect.Baselines `json:"class_baselines"`

	UpdatedAt time.Time `json:"updated_at"`
}

// Validate checks the template is internally consistent. It returns the
// first problem found, phrased for an API consumer.
func (t Template) Validate() error {
	if strings.TrimSpace(t.Class) == "" {
		return fmt.Errorf("template: class is required")
	}
	if strings.TrimSpace(t.Version) == "" {
		return fmt.Errorf("template: version is required")
	}
	if len(t.Signals) == 0 {
		return fmt.Errorf("template: at least one signal is required")
	}
	if _, err := features.NewExtractor(t.Signals); err != nil {
		return fmt.Errorf("template: %w", err)
	}
	for _, s := range t.Signals {
		if s.Name == t.RuntimeSignal {
			return fmt.Errorf("template: runtime_signal %q cannot also be a feature signal", s.Name)
		}
	}
	names := features.FeatureNames(t.Signals)
	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[n] = true
	}
	if t.Detectors.CUSUM.Feature == "" {
		return fmt.Errorf("template: detectors.cusum.feature is required")
	}
	if !known[t.Detectors.CUSUM.Feature] {
		return fmt.Errorf("template: detectors.cusum.feature %q is not a feature of this template", t.Detectors.CUSUM.Feature)
	}
	for f := range t.Detectors.RobustZ.Direction {
		if !known[f] {
			return fmt.Errorf("template: robust_z.direction refers to unknown feature %q", f)
		}
	}
	for f := range t.ClassBaselines {
		if !known[f] {
			return fmt.Errorf("template: class_baselines refers to unknown feature %q", f)
		}
	}
	if t.Detectors.RobustZ.Threshold <= 0 {
		return fmt.Errorf("template: robust_z.threshold must be > 0")
	}
	if t.Detectors.HST.Depth < 0 || t.Detectors.HST.Trees < 0 || t.Detectors.HST.WindowSize < 0 {
		return fmt.Errorf("template: hst parameters must be >= 0")
	}
	if t.Prior.Shape <= 0 || t.Prior.ScaleHours <= 0 || t.Prior.HorizonHours <= 0 {
		return fmt.Errorf("template: prior shape, scale_hours and horizon_hours must be > 0")
	}
	if t.Fusion.WAnomaly < 0 || t.Fusion.WDrift < 0 || t.Fusion.WPrior < 0 {
		return fmt.Errorf("template: fusion weights must be >= 0")
	}
	if t.WarmupEvaluations < 3 {
		return fmt.Errorf("template: warmup_evaluations must be >= 3")
	}
	if err := t.Alerting.Validate(); err != nil {
		return fmt.Errorf("template: %w", err)
	}
	if err := t.WorkOrder.Validate(); err != nil {
		return fmt.Errorf("template: %w", err)
	}
	return nil
}

// FeatureNames lists the features this template produces.
func (t Template) FeatureNames() []string { return features.FeatureNames(t.Signals) }
