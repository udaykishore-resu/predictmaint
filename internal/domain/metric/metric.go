// Package metric defines the ingest contract: plantstream-shaped, asset-keyed
// sensor readings. A reading is either a scalar (process variables sampled
// slowly) or a burst of waveform samples taken at one timestamp (vibration
// snapshots), which keeps high-frequency channels from exploding into one
// message per sample.
package metric

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Metric is one reading of one signal on one asset.
type Metric struct {
	AssetID    string `json:"asset_id"`
	Site       string `json:"site,omitempty"`
	AssetClass string `json:"asset_class,omitempty"`
	Signal     string `json:"signal"`
	Unit       string `json:"unit"`
	// Value is the scalar reading. Ignored when Samples is non-empty.
	Value float64 `json:"value"`
	// Samples is a waveform burst captured at Timestamp.
	Samples   []float64 `json:"samples,omitempty"`
	Timestamp time.Time `json:"ts"`
	// Source is free-form provenance (topic, gateway id) for audit logs.
	Source string `json:"source,omitempty"`
}

// DefaultSite is used when a metric carries no site.
const DefaultSite = "default"

// MaxSamples bounds a burst so a single message cannot pin a CPU.
const MaxSamples = 16384

var (
	ErrNoAsset      = errors.New("asset_id is required")
	ErrNoSignal     = errors.New("signal is required")
	ErrNoTimestamp  = errors.New("ts is required")
	ErrNonFinite    = errors.New("value must be finite")
	ErrTooMany      = errors.New("too many samples")
	ErrBadSite      = errors.New("site contains invalid characters")
	ErrBadAssetName = errors.New("asset_id contains invalid characters")
)

// Validate checks the metric and normalises trivially fixable fields (site
// default, trimmed identifiers).
func (m *Metric) Validate() error {
	m.AssetID = strings.TrimSpace(m.AssetID)
	m.Signal = strings.TrimSpace(m.Signal)
	m.Site = strings.TrimSpace(m.Site)
	m.AssetClass = strings.TrimSpace(strings.ToLower(m.AssetClass))
	if m.AssetID == "" {
		return ErrNoAsset
	}
	if !identOK(m.AssetID) {
		return ErrBadAssetName
	}
	if m.Signal == "" {
		return ErrNoSignal
	}
	if m.Site == "" {
		m.Site = DefaultSite
	}
	if !identOK(m.Site) {
		return ErrBadSite
	}
	if m.Timestamp.IsZero() {
		return ErrNoTimestamp
	}
	if len(m.Samples) > MaxSamples {
		return fmt.Errorf("%w: %d > %d", ErrTooMany, len(m.Samples), MaxSamples)
	}
	if len(m.Samples) == 0 {
		if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			return ErrNonFinite
		}
		return nil
	}
	for i, s := range m.Samples {
		if math.IsNaN(s) || math.IsInf(s, 0) {
			return fmt.Errorf("%w: samples[%d]", ErrNonFinite, i)
		}
	}
	return nil
}

// Values returns the readings carried by the metric (burst or scalar).
func (m Metric) Values() []float64 {
	if len(m.Samples) > 0 {
		return m.Samples
	}
	return []float64{m.Value}
}

func identOK(s string) bool {
	if len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':', r == '/':
		default:
			return false
		}
	}
	return true
}
