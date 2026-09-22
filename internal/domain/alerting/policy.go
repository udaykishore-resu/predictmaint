// Package alerting decides when a risk score becomes an alert. It exists
// because nuisance alerts kill predictive-maintenance programmes faster than
// missed detections do: every rule here (hysteresis, persistence,
// suppression, rate limits, feedback adaptation) trades a little latency for
// a lot of trust.
package alerting

import (
	"fmt"
	"time"
)

// Rule versions logged with every decision.
const (
	RulePolicy     = "ALERT-1"
	RuleAdaptation = "ADAPT-1"
)

// Params are the per-asset-class alerting hyperparameters.
type Params struct {
	// OnThreshold is the risk at which an episode starts; OffThreshold (lower)
	// is where it clears. The gap is the hysteresis band.
	OnThreshold  float64 `json:"on_threshold"`
	OffThreshold float64 `json:"off_threshold"`
	// MinPersistence is how many consecutive evaluations must exceed
	// OnThreshold before an alert fires.
	MinPersistence int `json:"min_persistence"`
	// ClearPersistence is how many consecutive evaluations must fall below
	// OffThreshold before an active episode clears.
	ClearPersistence int `json:"clear_persistence"`
	// Suppression is the quiet period after an episode clears during which a
	// new episode on the same asset is recorded but not alerted.
	Suppression Duration `json:"suppression"`
	// Rate limits, sliding one-hour windows. 0 disables the limit.
	MaxAlertsPerAssetPerHour int `json:"max_alerts_per_asset_per_hour"`
	MaxAlertsPerSitePerHour  int `json:"max_alerts_per_site_per_hour"`
	// Adaptation governs how technician feedback moves the per-asset
	// threshold offset.
	Adaptation Adaptation `json:"adaptation"`
}

// Adaptation bounds the feedback loop. A dismissal raises the asset's
// on-threshold by DismissStep; a confirmation lowers it by ConfirmStep. The
// offset never leaves [-MaxOffset, +MaxOffset], and the effective threshold
// never leaves (OffThreshold, MaxThreshold].
type Adaptation struct {
	DismissStep  float64 `json:"dismiss_step"`
	ConfirmStep  float64 `json:"confirm_step"`
	MaxOffset    float64 `json:"max_offset"`
	MaxThreshold float64 `json:"max_threshold"`
}

// Validate checks the parameters are coherent.
func (p Params) Validate() error {
	if p.OnThreshold <= 0 || p.OnThreshold > 1 {
		return fmt.Errorf("alerting: on_threshold must be in (0,1], got %v", p.OnThreshold)
	}
	if p.OffThreshold < 0 || p.OffThreshold >= p.OnThreshold {
		return fmt.Errorf("alerting: off_threshold must be in [0,on_threshold), got %v", p.OffThreshold)
	}
	if p.MinPersistence < 1 {
		return fmt.Errorf("alerting: min_persistence must be >= 1")
	}
	if p.ClearPersistence < 1 {
		return fmt.Errorf("alerting: clear_persistence must be >= 1")
	}
	if p.Suppression.Duration < 0 {
		return fmt.Errorf("alerting: suppression must be >= 0")
	}
	if p.MaxAlertsPerAssetPerHour < 0 || p.MaxAlertsPerSitePerHour < 0 {
		return fmt.Errorf("alerting: rate limits must be >= 0")
	}
	a := p.Adaptation
	if a.DismissStep < 0 || a.ConfirmStep < 0 || a.MaxOffset < 0 {
		return fmt.Errorf("alerting: adaptation steps and max_offset must be >= 0")
	}
	if a.MaxThreshold != 0 && (a.MaxThreshold <= p.OnThreshold || a.MaxThreshold > 1) {
		return fmt.Errorf("alerting: max_threshold must be in (on_threshold,1]")
	}
	return nil
}

// Duration is a time.Duration that marshals as a Go duration string.
type Duration struct{ time.Duration }

// MarshalJSON renders "10m0s"-style strings.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Duration.String() + `"`), nil
}

// UnmarshalJSON accepts duration strings or integer nanoseconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' {
		v, err := time.ParseDuration(s[1 : len(s)-1])
		if err != nil {
			return fmt.Errorf("alerting: bad duration %s: %w", s, err)
		}
		d.Duration = v
		return nil
	}
	var ns int64
	if _, err := fmt.Sscan(s, &ns); err != nil {
		return fmt.Errorf("alerting: bad duration %s: %w", s, err)
	}
	d.Duration = time.Duration(ns)
	return nil
}

// State is the per-asset episode state.
type State string

const (
	StateNormal  State = "normal"
	StatePending State = "pending" // above threshold, persistence not yet met
	StateActive  State = "active"  // episode open (alerted or suppressed)
)

// Outcome enumerates what an evaluation decided.
type Outcome string

const (
	OutcomeNone        Outcome = "none"
	OutcomePending     Outcome = "pending"
	OutcomeFire        Outcome = "fire"
	OutcomeSuppressed  Outcome = "suppressed"   // episode opened inside the suppression window
	OutcomeRateLimited Outcome = "rate_limited" // episode opened but a rate limit held it
	OutcomeHold        Outcome = "hold"         // already active; nothing new
	OutcomeClear       Outcome = "clear"
)

// Decision is the auditable result of one evaluation.
type Decision struct {
	Outcome            Outcome `json:"outcome"`
	State              State   `json:"state"`
	Risk               float64 `json:"risk"`
	EffectiveThreshold float64 `json:"effective_threshold"`
	Persistence        int     `json:"persistence"`
	Reason             string  `json:"reason"`
	Rule               string  `json:"rule"`
	// MeanRisk is the mean risk over the persistence run that led to a fire.
	MeanRisk float64 `json:"mean_risk,omitempty"`
}

// AssetPolicy is the alerting state machine for one asset.
type AssetPolicy struct {
	p               Params
	state           State
	aboveCount      int
	belowCount      int
	riskSum         float64
	suppressedUntil time.Time
	offset          float64
	alerted         bool // whether the current episode produced an alert
	assetLimiter    *RateLimiter
}

// NewAssetPolicy creates the state machine with a zero threshold offset.
func NewAssetPolicy(p Params) *AssetPolicy {
	ap := &AssetPolicy{p: p, state: StateNormal}
	if p.MaxAlertsPerAssetPerHour > 0 {
		ap.assetLimiter = NewRateLimiter(p.MaxAlertsPerAssetPerHour, time.Hour)
	}
	return ap
}

// SetParams swaps the class parameters (template update) keeping the asset's
// learned offset and episode state.
func (a *AssetPolicy) SetParams(p Params) {
	a.p = p
	if p.MaxAlertsPerAssetPerHour > 0 {
		a.assetLimiter = NewRateLimiter(p.MaxAlertsPerAssetPerHour, time.Hour)
	} else {
		a.assetLimiter = nil
	}
	a.offset = a.clampOffset(a.offset)
}

// State returns the current episode state.
func (a *AssetPolicy) State() State { return a.state }

// Offset is the learned threshold offset.
func (a *AssetPolicy) Offset() float64 { return a.offset }

// SetOffset restores a learned offset (e.g. from persistence), clamped.
func (a *AssetPolicy) SetOffset(o float64) { a.offset = a.clampOffset(o) }

// EffectiveThreshold is OnThreshold plus the learned offset, bounded so the
// hysteresis band always keeps some width.
func (a *AssetPolicy) EffectiveThreshold() float64 {
	t := a.p.OnThreshold + a.offset
	maxT := a.p.Adaptation.MaxThreshold
	if maxT == 0 {
		maxT = 1
	}
	if t > maxT {
		t = maxT
	}
	minT := a.p.OffThreshold + 0.01
	if t < minT {
		t = minT
	}
	return t
}

func (a *AssetPolicy) clampOffset(o float64) float64 {
	m := a.p.Adaptation.MaxOffset
	if o > m {
		return m
	}
	if o < -m {
		return -m
	}
	return o
}

// SuppressedUntil reports the end of the current suppression window.
func (a *AssetPolicy) SuppressedUntil() time.Time { return a.suppressedUntil }

// Evaluate feeds one risk observation at event time now. siteLimiter may be
// nil. It is the caller's job to emit an alert when Outcome == OutcomeFire.
func (a *AssetPolicy) Evaluate(now time.Time, risk float64, siteLimiter *RateLimiter) Decision {
	thr := a.EffectiveThreshold()
	d := Decision{State: a.state, Risk: risk, EffectiveThreshold: thr, Rule: RulePolicy, Outcome: OutcomeNone}

	switch a.state {
	case StateNormal, StatePending:
		if risk < thr {
			a.aboveCount, a.riskSum = 0, 0
			a.state = StateNormal
			d.State, d.Reason = a.state, "below threshold"
			return d
		}
		a.aboveCount++
		a.riskSum += risk
		d.Persistence = a.aboveCount
		if a.aboveCount < a.p.MinPersistence {
			a.state = StatePending
			d.State, d.Outcome = a.state, OutcomePending
			d.Reason = fmt.Sprintf("above threshold %d/%d", a.aboveCount, a.p.MinPersistence)
			return d
		}
		// Persistence met: open an episode.
		a.state = StateActive
		a.belowCount = 0
		d.State = a.state
		d.MeanRisk = a.riskSum / float64(a.aboveCount)
		switch {
		case now.Before(a.suppressedUntil):
			a.alerted = false
			d.Outcome = OutcomeSuppressed
			d.Reason = fmt.Sprintf("episode opened inside suppression window (until %s)", a.suppressedUntil.UTC().Format(time.RFC3339))
		case a.assetLimiter != nil && !a.assetLimiter.Allow(now):
			a.alerted = false
			d.Outcome = OutcomeRateLimited
			d.Reason = "per-asset hourly alert limit reached"
		case siteLimiter != nil && !siteLimiter.Allow(now):
			a.alerted = false
			d.Outcome = OutcomeRateLimited
			d.Reason = "per-site hourly alert limit reached"
		default:
			a.alerted = true
			d.Outcome = OutcomeFire
			d.Reason = fmt.Sprintf("risk %.3f >= %.3f for %d evaluations", risk, thr, a.aboveCount)
		}
		return d

	case StateActive:
		if risk >= a.p.OffThreshold {
			a.belowCount = 0
			d.Outcome, d.Reason = OutcomeHold, "episode active"
			return d
		}
		a.belowCount++
		if a.belowCount < a.p.ClearPersistence {
			d.Outcome = OutcomeHold
			d.Reason = fmt.Sprintf("clearing %d/%d", a.belowCount, a.p.ClearPersistence)
			return d
		}
		a.state = StateNormal
		a.aboveCount, a.riskSum, a.belowCount = 0, 0, 0
		a.suppressedUntil = now.Add(a.p.Suppression.Duration)
		d.State = a.state
		d.Outcome = OutcomeClear
		if a.alerted {
			d.Reason = fmt.Sprintf("risk %.3f < %.3f; suppressing new alerts until %s", risk, a.p.OffThreshold, a.suppressedUntil.UTC().Format(time.RFC3339))
		} else {
			d.Reason = "unalerted episode cleared"
		}
		a.alerted = false
		return d
	}
	return d
}

// Verdict is technician feedback on an alert.
type Verdict string

const (
	VerdictConfirm Verdict = "confirm"
	VerdictDismiss Verdict = "dismiss"
)

// ParseVerdict validates a verdict string.
func ParseVerdict(s string) (Verdict, error) {
	switch Verdict(s) {
	case VerdictConfirm, VerdictDismiss:
		return Verdict(s), nil
	}
	return "", fmt.Errorf("alerting: verdict must be %q or %q", VerdictConfirm, VerdictDismiss)
}

// AdaptationResult records how feedback moved the threshold.
type AdaptationResult struct {
	Verdict         Verdict `json:"verdict"`
	OffsetBefore    float64 `json:"offset_before"`
	OffsetAfter     float64 `json:"offset_after"`
	ThresholdBefore float64 `json:"threshold_before"`
	ThresholdAfter  float64 `json:"threshold_after"`
	Clamped         bool    `json:"clamped"`
	Rule            string  `json:"rule"`
}

// ApplyFeedback moves the asset's threshold offset per rule ADAPT-1.
func (a *AssetPolicy) ApplyFeedback(v Verdict) AdaptationResult {
	r := AdaptationResult{Verdict: v, OffsetBefore: a.offset, ThresholdBefore: a.EffectiveThreshold(), Rule: RuleAdaptation}
	var want float64
	switch v {
	case VerdictDismiss:
		want = a.offset + a.p.Adaptation.DismissStep
	case VerdictConfirm:
		want = a.offset - a.p.Adaptation.ConfirmStep
	default:
		want = a.offset
	}
	a.offset = a.clampOffset(want)
	r.Clamped = a.offset != want
	r.OffsetAfter = a.offset
	r.ThresholdAfter = a.EffectiveThreshold()
	return r
}
