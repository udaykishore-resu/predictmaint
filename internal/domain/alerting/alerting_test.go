package alerting

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func params() Params {
	return Params{
		OnThreshold:              0.6,
		OffThreshold:             0.4,
		MinPersistence:           3,
		ClearPersistence:         2,
		Suppression:              Duration{30 * time.Minute},
		MaxAlertsPerAssetPerHour: 2,
		MaxAlertsPerSitePerHour:  3,
		Adaptation:               Adaptation{DismissStep: 0.05, ConfirmStep: 0.02, MaxOffset: 0.2, MaxThreshold: 0.9},
	}
}

func TestParamsValidate(t *testing.T) {
	require.NoError(t, params().Validate())
	mut := []func(*Params){
		func(p *Params) { p.OnThreshold = 0 },
		func(p *Params) { p.OnThreshold = 1.5 },
		func(p *Params) { p.OffThreshold = 0.7 },
		func(p *Params) { p.OffThreshold = -0.1 },
		func(p *Params) { p.MinPersistence = 0 },
		func(p *Params) { p.ClearPersistence = 0 },
		func(p *Params) { p.Suppression = Duration{-time.Second} },
		func(p *Params) { p.MaxAlertsPerAssetPerHour = -1 },
		func(p *Params) { p.Adaptation.MaxOffset = -1 },
		func(p *Params) { p.Adaptation.MaxThreshold = 0.5 },
	}
	for i, m := range mut {
		p := params()
		m(&p)
		assert.Error(t, p.Validate(), "mutation %d", i)
	}
}

func TestDurationJSON(t *testing.T) {
	var p Params
	require.NoError(t, json.Unmarshal([]byte(`{"suppression":"15m"}`), &p))
	assert.Equal(t, 15*time.Minute, p.Suppression.Duration)
	require.NoError(t, json.Unmarshal([]byte(`{"suppression":1000}`), &p))
	assert.Equal(t, time.Duration(1000), p.Suppression.Duration)
	assert.Error(t, json.Unmarshal([]byte(`{"suppression":"bogus"}`), &p))
	assert.Error(t, json.Unmarshal([]byte(`{"suppression":true}`), &p))
	b, err := json.Marshal(Duration{90 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, `"1m30s"`, string(b))
}

// The headline behaviour: a degrading asset alerts once, not 50 times.
func TestEpisodeFiresOnce(t *testing.T) {
	ap := NewAssetPolicy(params())
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }

	var fires, holds int
	for i := 0; i < 50; i++ {
		d := ap.Evaluate(tick(i), 0.8, nil)
		switch d.Outcome {
		case OutcomeFire:
			fires++
			assert.Equal(t, 3, d.Persistence)
			assert.InDelta(t, 0.8, d.MeanRisk, 1e-9)
			assert.Equal(t, RulePolicy, d.Rule)
		case OutcomePending:
			assert.Less(t, i, 2)
		case OutcomeHold:
			holds++
		default:
			t.Fatalf("unexpected outcome %s at %d", d.Outcome, i)
		}
	}
	assert.Equal(t, 1, fires)
	assert.Equal(t, 47, holds)
	assert.Equal(t, StateActive, ap.State())
}

func TestHysteresisAndClearPersistence(t *testing.T) {
	ap := NewAssetPolicy(params())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		ap.Evaluate(now, 0.7, nil)
	}
	require.Equal(t, StateActive, ap.State())
	// Inside the hysteresis band: still active.
	assert.Equal(t, OutcomeHold, ap.Evaluate(now, 0.5, nil).Outcome)
	// One sample below off: not yet cleared.
	assert.Equal(t, OutcomeHold, ap.Evaluate(now, 0.3, nil).Outcome)
	// Bounce back resets the clear counter.
	assert.Equal(t, OutcomeHold, ap.Evaluate(now, 0.5, nil).Outcome)
	assert.Equal(t, OutcomeHold, ap.Evaluate(now, 0.3, nil).Outcome)
	d := ap.Evaluate(now, 0.3, nil)
	assert.Equal(t, OutcomeClear, d.Outcome)
	assert.Equal(t, StateNormal, ap.State())
	assert.Equal(t, now.Add(30*time.Minute), ap.SuppressedUntil())
}

func TestPendingResetsWhenRiskDrops(t *testing.T) {
	ap := NewAssetPolicy(params())
	now := time.Now()
	assert.Equal(t, OutcomePending, ap.Evaluate(now, 0.7, nil).Outcome)
	assert.Equal(t, OutcomePending, ap.Evaluate(now, 0.7, nil).Outcome)
	d := ap.Evaluate(now, 0.1, nil)
	assert.Equal(t, OutcomeNone, d.Outcome)
	assert.Equal(t, StateNormal, d.State)
	assert.Equal(t, 1, ap.Evaluate(now, 0.7, nil).Persistence, "counter restarted from zero")
	assert.Equal(t, 2, ap.Evaluate(now, 0.7, nil).Persistence)
}

func TestSuppressionWindow(t *testing.T) {
	p := params()
	p.MinPersistence, p.ClearPersistence = 1, 1
	ap := NewAssetPolicy(p)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, OutcomeFire, ap.Evaluate(t0, 0.9, nil).Outcome)
	assert.Equal(t, OutcomeClear, ap.Evaluate(t0.Add(time.Minute), 0.1, nil).Outcome)
	// Flapping back up inside the window: recorded, not alerted.
	d := ap.Evaluate(t0.Add(2*time.Minute), 0.9, nil)
	assert.Equal(t, OutcomeSuppressed, d.Outcome)
	assert.Equal(t, StateActive, d.State)
	d = ap.Evaluate(t0.Add(3*time.Minute), 0.1, nil)
	assert.Equal(t, OutcomeClear, d.Outcome)
	assert.Contains(t, d.Reason, "unalerted")
	// After the window, alerts again (asset limit is 2/h: this is the 2nd).
	assert.Equal(t, OutcomeFire, ap.Evaluate(t0.Add(40*time.Minute), 0.9, nil).Outcome)
}

func TestRateLimits(t *testing.T) {
	p := params()
	p.MinPersistence, p.ClearPersistence = 1, 1
	p.Suppression = Duration{0}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ap := NewAssetPolicy(p)
	outcomes := []Outcome{}
	for i := 0; i < 3; i++ {
		outcomes = append(outcomes, ap.Evaluate(t0.Add(time.Duration(i)*time.Minute), 0.9, nil).Outcome)
		ap.Evaluate(t0.Add(time.Duration(i)*time.Minute+30*time.Second), 0.1, nil)
	}
	assert.Equal(t, []Outcome{OutcomeFire, OutcomeFire, OutcomeRateLimited}, outcomes)
	// An hour later the asset limiter has room again.
	assert.Equal(t, OutcomeFire, ap.Evaluate(t0.Add(2*time.Hour), 0.9, nil).Outcome)

	// Site limiter shared across assets.
	site := NewRateLimiter(p.MaxAlertsPerSitePerHour, time.Hour)
	var fired, limited int
	for i := 0; i < 5; i++ {
		a := NewAssetPolicy(p)
		switch a.Evaluate(t0, 0.9, site).Outcome {
		case OutcomeFire:
			fired++
		case OutcomeRateLimited:
			limited++
		}
	}
	assert.Equal(t, 3, fired)
	assert.Equal(t, 2, limited)
	assert.Equal(t, 3, site.Count(t0))
	assert.Equal(t, 0, site.Count(t0.Add(2*time.Hour)))

	var nilLimiter *RateLimiter
	assert.True(t, nilLimiter.Allow(t0))
	assert.Equal(t, 0, nilLimiter.Count(t0))
	assert.True(t, NewRateLimiter(0, time.Hour).Allow(t0))
}

func TestFeedbackAdaptationWithGuardrails(t *testing.T) {
	ap := NewAssetPolicy(params())
	assert.InDelta(t, 0.6, ap.EffectiveThreshold(), 1e-9)

	r := ap.ApplyFeedback(VerdictDismiss)
	assert.InDelta(t, 0.65, r.ThresholdAfter, 1e-9)
	assert.False(t, r.Clamped)
	assert.Equal(t, RuleAdaptation, r.Rule)

	for i := 0; i < 10; i++ {
		r = ap.ApplyFeedback(VerdictDismiss)
	}
	assert.True(t, r.Clamped)
	assert.InDelta(t, 0.2, ap.Offset(), 1e-9, "offset capped at max_offset")
	assert.InDelta(t, 0.8, ap.EffectiveThreshold(), 1e-9)

	for i := 0; i < 30; i++ {
		r = ap.ApplyFeedback(VerdictConfirm)
	}
	assert.InDelta(t, -0.2, ap.Offset(), 1e-9)
	assert.InDelta(t, 0.4+0.01, ap.EffectiveThreshold(), 1e-9, "never below off threshold + band")

	unknown := ap.ApplyFeedback(Verdict("meh"))
	assert.Equal(t, unknown.OffsetBefore, unknown.OffsetAfter)

	// Max threshold guardrail.
	p := params()
	p.OnThreshold, p.Adaptation.MaxThreshold, p.Adaptation.MaxOffset = 0.85, 0.9, 0.5
	ap = NewAssetPolicy(p)
	ap.SetOffset(0.4)
	assert.InDelta(t, 0.9, ap.EffectiveThreshold(), 1e-9)
	ap.SetParams(params())
	assert.InDelta(t, 0.2, ap.Offset(), 1e-9, "offset re-clamped to new params")
	p.MaxAlertsPerAssetPerHour = 0
	ap.SetParams(p)
	assert.Nil(t, ap.assetLimiter)

	_, err := ParseVerdict("confirm")
	require.NoError(t, err)
	_, err = ParseVerdict("nope")
	assert.Error(t, err)
}

func TestComputeStats(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, Stats{}, ComputeStats(nil, now))
	fb := func(d time.Duration, base time.Time) *time.Time { t := base.Add(d); return &t }
	a1 := now.Add(-2 * time.Hour)
	a2 := now.Add(-40 * time.Hour)
	a3 := now.Add(-10 * time.Hour)
	alerts := []Alert{
		{Status: StatusConfirmed, DetectedAt: a1, FeedbackAt: fb(10*time.Minute, a1)},
		{Status: StatusDismissed, DetectedAt: a2, FeedbackAt: fb(30*time.Minute, a2)},
		{Status: StatusOpen, DetectedAt: a3},
		{Status: StatusConfirmed, DetectedAt: a3, FeedbackAt: fb(-time.Minute, a3)}, // bad clock: ignored for MTTA
	}
	s := ComputeStats(alerts, now)
	assert.Equal(t, 4, s.TotalAlerts)
	assert.Equal(t, 1, s.Open)
	assert.Equal(t, 2, s.Confirmed)
	assert.Equal(t, 1, s.Dismissed)
	assert.Equal(t, 3, s.AlertsLast24h)
	assert.InDelta(t, 4.0/38*24, s.AlertsPerDay, 1e-9)
	assert.InDelta(t, 2.0/3, s.PrecisionProxy, 1e-9)
	assert.Equal(t, 2, s.Acknowledged)
	assert.InDelta(t, 20*60, s.MTTASeconds, 1e-9)

	one := ComputeStats([]Alert{{Status: StatusOpen, DetectedAt: now}}, now)
	assert.InDelta(t, 1, one.AlertsPerDay, 1e-9, "span floored to one day")
	assert.Equal(t, 0.0, one.PrecisionProxy)
}
