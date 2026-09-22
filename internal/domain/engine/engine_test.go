package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/predictmaint/internal/adapters/memory"
	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/detect"
	"github.com/udaykishore-resu/predictmaint/internal/domain/metric"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
	"github.com/udaykishore-resu/predictmaint/internal/sim"
)

type counting struct {
	mu       sync.Mutex
	outcomes map[alerting.Outcome]int
	wo       map[string]int
	feedback int
}

func (c *counting) MetricsIngested(int, int, int)       {}
func (c *counting) Evaluated(string, detect.Assessment) {}
func (c *counting) AlertOutcome(_, _ string, o alerting.Outcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outcomes[o]++
}
func (c *counting) WorkOrder(_, _, r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wo[r]++
}
func (c *counting) Feedback(string, alerting.Verdict) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.feedback++
}

func newCounting() *counting {
	return &counting{outcomes: map[alerting.Outcome]int{}, wo: map[string]int{}}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newEngine(t *testing.T, store ports.Store, c cmms.CMMS, obs Observer) *Engine {
	t.Helper()
	var seq int
	e, err := New(context.Background(), Options{
		Store: store, CMMS: c, Logger: quiet(), Observer: obs,
		Now:   func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) },
		NewID: func() string { seq++; return fmt.Sprintf("id-%03d", seq) },
	})
	require.NoError(t, err)
	return e
}

// TestBearingDegradationEndToEnd is the headline scenario: five pumps, one
// degrades. Expect exactly one alert on the bad pump, none on the healthy
// ones, one work order, and a feedback loop that moves the threshold.
func TestBearingDegradationEndToEnd(t *testing.T) {
	store := memory.New()
	simCMMS := cmms.NewSimulated()
	obs := newCounting()
	e := newEngine(t, store, simCMMS, obs)
	ctx := context.Background()

	cfg := sim.Defaults()
	s := sim.New(cfg)
	const steps = 200
	var firstAlertStep = -1
	riskAtDegradeStart := -1.0
	for i := 0; i < steps; i++ {
		res, err := e.Ingest(ctx, s.Next())
		require.NoError(t, err, "step %d", i)
		assert.Zero(t, res.Rejected, "step %d: %v", i, res.Errors)
		if res.Alerts > 0 && firstAlertStep < 0 {
			firstAlertStep = i
		}
		if i == cfg.DegradeFrom {
			h, err := e.Health(s.AssetID(2))
			require.NoError(t, err)
			riskAtDegradeStart = h.Risk
		}
	}
	bad := s.AssetID(2)
	h, err := e.Health(bad)
	require.NoError(t, err)
	riskAtEnd := h.Risk

	alerts, err := store.ListAlerts(ctx, ports.AlertFilter{})
	require.NoError(t, err)
	require.Len(t, alerts, 1, "degrading pump alerts exactly once, not once per sample")
	a := alerts[0]
	assert.Equal(t, bad, a.AssetID)
	assert.Equal(t, "plant-a", a.Site)
	assert.Equal(t, alerting.StatusOpen, a.Status)
	assert.Equal(t, "bearing_wear", a.FailureMode)
	assert.NotEmpty(t, a.Contributors)
	assert.Contains(t, a.Rules, "ZS-1")
	assert.Contains(t, a.Rules, "ALERT-1")
	assert.NotEmpty(t, a.WorkOrderID)
	assert.Greater(t, firstAlertStep, cfg.DegradeFrom, "no alert before degradation starts")
	assert.Less(t, firstAlertStep, cfg.DegradeFrom+cfg.RampSteps/2, "alert well before full severity")
	assert.Greater(t, riskAtEnd, riskAtDegradeStart)
	assert.Greater(t, riskAtEnd, 0.6)

	// Health of the bad pump vs a healthy pump.
	assert.Equal(t, alerting.StateActive, h.State)
	assert.Equal(t, BaselineAsset, h.BaselineSource)
	assert.InDelta(t, 1, h.WarmupProgress, 1e-9)
	assert.Equal(t, a.ID, h.OpenAlertID)
	assert.NotEmpty(t, h.TopContributors)
	assert.Contains(t, h.TopContributors[0].Feature, "vibration")
	good, err := e.Health(s.AssetID(0))
	require.NoError(t, err)
	assert.Less(t, good.Risk, 0.35)
	assert.Equal(t, alerting.StateNormal, good.State)
	assert.Greater(t, good.AgeHours, 8000.0)

	// Fleet list sorted by risk.
	fleet := e.Assets("plant-a")
	require.Len(t, fleet, 5)
	assert.Equal(t, bad, fleet[0].AssetID)
	assert.Empty(t, e.Assets("other-site"))

	// Exactly one work order, in the CMMS too, with vendor payloads rendered.
	wos, err := store.ListWorkOrders(ctx, ports.WorkOrderFilter{})
	require.NoError(t, err)
	require.Len(t, wos, 1)
	assert.Equal(t, a.ID, wos[0].AlertID)
	assert.Equal(t, cmms.StatusOpen, wos[0].Status)
	require.NotNil(t, wos[0].External)
	assert.Equal(t, "WO-000001", wos[0].External.ID)
	recs := simCMMS.Records()
	require.Len(t, recs, 1)
	assert.Equal(t, bad, recs[0].Maximo.AssetNum)
	assert.Equal(t, bad, recs[0].SAPPM.Equipment)
	assert.Equal(t, 1, obs.wo["created"])
	assert.Equal(t, 1, obs.outcomes[alerting.OutcomeFire])

	// Feedback: dismiss raises the threshold within guardrails.
	before := h.EffectiveThreshold
	fb, err := e.Feedback(ctx, FeedbackRequest{AlertID: a.ID, Verdict: alerting.VerdictDismiss, Technician: "t.nguyen", Note: "sensor cable"})
	require.NoError(t, err)
	assert.Equal(t, alerting.StatusDismissed, fb.Status)
	require.NotNil(t, fb.Adaptation)
	assert.InDelta(t, before+0.05, fb.Adaptation.ThresholdAfter, 1e-9)
	h2, _ := e.Health(bad)
	assert.InDelta(t, before+0.05, h2.EffectiveThreshold, 1e-9)
	assert.InDelta(t, 0.05, h2.ThresholdOffset, 1e-9)
	assert.Equal(t, 1, obs.feedback)

	// Same verdict again is idempotent; a conflicting one is rejected.
	again, err := e.Feedback(ctx, FeedbackRequest{AlertID: a.ID, Verdict: alerting.VerdictDismiss})
	require.NoError(t, err)
	assert.Equal(t, alerting.StatusDismissed, again.Status)
	h2b, _ := e.Health(bad)
	assert.InDelta(t, 0.05, h2b.ThresholdOffset, 1e-9, "repeated verdict does not adapt twice")
	_, err = e.Feedback(ctx, FeedbackRequest{AlertID: a.ID, Verdict: alerting.VerdictConfirm})
	assert.ErrorIs(t, err, ErrAlreadyJudged)
	_, err = e.Feedback(ctx, FeedbackRequest{AlertID: "nope", Verdict: alerting.VerdictConfirm})
	assert.ErrorIs(t, err, ports.ErrNotFound)

	// Snapshot persisted the learned offset and baselines.
	snaps, err := store.ListAssetSnapshots(ctx)
	require.NoError(t, err)
	var snap ports.AssetSnapshot
	for _, sn := range snaps {
		if sn.AssetID == bad {
			snap = sn
		}
	}
	assert.InDelta(t, 0.05, snap.ThresholdOffset, 1e-9)
	assert.NotEmpty(t, snap.Baselines)

	// Stats.
	st, err := e.Stats(ctx, "plant-a")
	require.NoError(t, err)
	assert.Equal(t, 1, st.TotalAlerts)
	assert.Equal(t, 1, st.Dismissed)
	assert.Equal(t, 0.0, st.PrecisionProxy)
	assert.Equal(t, 1, st.Acknowledged)

	// Replaying the whole trajectory is a no-op: everything is a duplicate.
	replay := sim.New(cfg)
	var dups, accepted, newAlerts int
	for i := 0; i < steps; i++ {
		res, err := e.Ingest(ctx, replay.Next())
		require.NoError(t, err)
		dups += res.Duplicates
		accepted += res.Accepted
		newAlerts += res.Alerts
	}
	assert.Equal(t, steps*5*5, dups)
	assert.Zero(t, accepted)
	assert.Zero(t, newAlerts)

	// A restarted engine restores learned state from snapshots.
	e2 := newEngine(t, store, simCMMS, NopObserver{})
	h3, err := e2.Health(bad)
	require.NoError(t, err)
	assert.Equal(t, BaselineAsset, h3.BaselineSource)
	assert.InDelta(t, 0.05, h3.ThresholdOffset, 1e-9)
}

func TestIngestRejectsAndDedupes(t *testing.T) {
	e := newEngine(t, memory.New(), cmms.NewSimulated(), NopObserver{})
	ctx := context.Background()
	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	res, err := e.Ingest(ctx, []metric.Metric{
		{Signal: "x", Timestamp: ts}, // invalid
		{AssetID: "m1", Signal: "vibration", Unit: "mm/s", Value: 1, Timestamp: ts},                       // unknown asset, no class
		{AssetID: "m2", AssetClass: "turbine", Signal: "vibration", Unit: "mm/s", Timestamp: ts},          // unknown class
		{AssetID: "p1", AssetClass: "pump", Signal: "bogus", Unit: "mm/s", Value: 1, Timestamp: ts},       // unknown signal
		{AssetID: "p1", AssetClass: "pump", Signal: "bearing_temp", Unit: "psi", Value: 1, Timestamp: ts}, // wrong dimension
		{AssetID: "p1", AssetClass: "motor", Signal: "bearing_temp", Unit: "C", Value: 1, Timestamp: ts},  // class mismatch
		{AssetID: "p1", AssetClass: "pump", Signal: "bearing_temp", Unit: "F", Value: 140, Timestamp: ts}, // ok
		{AssetID: "p1", Signal: "bearing_temp", Unit: "C", Value: 60, Timestamp: ts},                      // duplicate ts
		{AssetID: "p1", Signal: "runtime_hours", Unit: "min", Value: 600, Timestamp: ts},                  // runtime in minutes
		{AssetID: "p1", Signal: "runtime_hours", Unit: "psi", Value: 600, Timestamp: ts.Add(time.Second)}, // bad runtime unit
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Accepted)
	assert.Equal(t, 7, res.Rejected)
	assert.Equal(t, 1, res.Duplicates)
	assert.Len(t, res.Errors, 7)
	h, err := e.Health("p1")
	require.NoError(t, err)
	assert.Equal(t, 10.0, h.AgeHours)
	assert.Equal(t, 0, h.Evaluations, "windows not full: no evaluation")
	assert.Less(t, h.WindowFill, 1.0)
	assert.Nil(t, h.LastEventTime)
	assert.NotNil(t, h.LastSeen)
	_, err = e.Health("ghost")
	assert.ErrorIs(t, err, ErrUnknownAsset)

	// Error cap on very bad batches.
	bad := make([]metric.Metric, 50)
	res, err = e.Ingest(ctx, bad)
	require.NoError(t, err)
	assert.Equal(t, 50, res.Rejected)
	assert.Len(t, res.Errors, maxReportedErrors)
}

func TestTemplatesCRUDAndRebuild(t *testing.T) {
	store := memory.New()
	e := newEngine(t, store, cmms.NewSimulated(), NopObserver{})
	ctx := context.Background()
	assert.Len(t, e.Templates(), 3)
	_, ok := e.Template("pump")
	assert.True(t, ok)
	_, ok = e.Template("nope")
	assert.False(t, ok)

	// Register an asset and warm it up so it has learned baselines.
	s := sim.New(sim.Config{Pumps: 1, Degrading: nil})
	for i := 0; i < 40; i++ {
		_, err := e.Ingest(ctx, s.Next())
		require.NoError(t, err)
	}
	h, _ := e.Health(s.AssetID(0))
	require.Equal(t, BaselineAsset, h.BaselineSource)

	tpl, _ := template.DefaultFor("pump")
	tpl.Version = "2026.2"
	tpl.Alerting.OnThreshold = 0.7
	got, err := e.PutTemplate(ctx, tpl)
	require.NoError(t, err)
	assert.Equal(t, "2026.2", got.Version)
	assert.False(t, got.UpdatedAt.IsZero())
	stored, err := store.GetTemplate(ctx, "pump")
	require.NoError(t, err)
	assert.Equal(t, "2026.2", stored.Version)

	h, _ = e.Health(s.AssetID(0))
	assert.Equal(t, "2026.2", h.TemplateVersion)
	assert.Equal(t, BaselineAsset, h.BaselineSource, "learned baselines survive a template update")
	assert.InDelta(t, 0.7, h.EffectiveThreshold, 1e-9)

	tpl.Signals = nil
	_, err = e.PutTemplate(ctx, tpl)
	assert.ErrorIs(t, err, ErrInvalidTemplate)

	// A stored template is loaded in preference to the built-in default.
	e2 := newEngine(t, store, cmms.NewSimulated(), NopObserver{})
	t2, _ := e2.Template("pump")
	assert.Equal(t, "2026.2", t2.Version)
}

func TestNewRequiresStoreAndRejectsBadStoredTemplate(t *testing.T) {
	_, err := New(context.Background(), Options{})
	assert.Error(t, err)
	store := memory.New()
	require.NoError(t, store.PutTemplate(context.Background(), template.Template{Class: "pump"}))
	_, err = New(context.Background(), Options{Store: store, Logger: quiet()})
	assert.Error(t, err)

	// Snapshot for an unknown class is skipped, not fatal.
	store = memory.New()
	require.NoError(t, store.SaveAssetSnapshot(context.Background(), ports.AssetSnapshot{AssetID: "x", AssetClass: "turbine"}))
	e, err := New(context.Background(), Options{Store: store, Logger: quiet()})
	require.NoError(t, err)
	assert.Empty(t, e.Assets(""))
}

type failingCMMS struct {
	fail  bool
	calls int
	inner *cmms.Simulated
}

func (f *failingCMMS) Name() string { return "failing" }
func (f *failingCMMS) CreateWorkOrder(ctx context.Context, wo cmms.WorkOrder) (cmms.ExternalRef, error) {
	f.calls++
	if f.fail {
		return cmms.ExternalRef{}, errors.New("cmms down")
	}
	return f.inner.CreateWorkOrder(ctx, wo)
}
func (f *failingCMMS) CancelWorkOrder(ctx context.Context, ref cmms.ExternalRef, reason string) error {
	return f.inner.CancelWorkOrder(ctx, ref, reason)
}

func TestWorkOrderRetryAndAutoCancel(t *testing.T) {
	store := memory.New()
	fc := &failingCMMS{fail: true, inner: cmms.NewSimulated()}
	e := newEngine(t, store, fc, NopObserver{})
	ctx := context.Background()

	// Make the pump policy cancel work orders on dismissal.
	tpl, _ := template.DefaultFor("pump")
	tpl.WorkOrder.AutoCancelOnDismiss = true
	_, err := e.PutTemplate(ctx, tpl)
	require.NoError(t, err)

	cfg := sim.Defaults()
	cfg.Pumps = 1
	cfg.Degrading = []int{0}
	s := sim.New(cfg)
	var alertID string
	for i := 0; i < 130 && alertID == ""; i++ {
		_, err := e.Ingest(ctx, s.Next())
		require.NoError(t, err)
		alerts, _ := store.ListAlerts(ctx, ports.AlertFilter{})
		if len(alerts) > 0 {
			alertID = alerts[0].ID
		}
	}
	require.NotEmpty(t, alertID, "alert must fire even when the CMMS is down")
	assert.Equal(t, 1, fc.calls)
	a, _ := store.GetAlert(ctx, alertID)
	assert.Empty(t, a.WorkOrderID)

	// CMMS recovers: the next hold evaluation retries and links the WO.
	fc.fail = false
	_, err = e.Ingest(ctx, s.Next())
	require.NoError(t, err)
	a, _ = store.GetAlert(ctx, alertID)
	require.NotEmpty(t, a.WorkOrderID)
	assert.Equal(t, 2, fc.calls)
	_, err = e.Ingest(ctx, s.Next())
	require.NoError(t, err)
	assert.Equal(t, 2, fc.calls, "no further CMMS calls once linked")

	// Dismissal cancels the linked work order.
	_, err = e.Feedback(ctx, FeedbackRequest{AlertID: alertID, Verdict: alerting.VerdictDismiss, Technician: "qa"})
	require.NoError(t, err)
	wo, err := store.GetWorkOrder(ctx, a.WorkOrderID)
	require.NoError(t, err)
	assert.Equal(t, cmms.StatusCancelled, wo.Status)
	recs := fc.inner.Records()
	require.Len(t, recs, 1)
	assert.True(t, recs[0].Cancelled)
}

func TestWorkOrderRetriesAreBounded(t *testing.T) {
	store := memory.New()
	fc := &failingCMMS{fail: true, inner: cmms.NewSimulated()}
	var seq int
	e, err := New(context.Background(), Options{Store: store, CMMS: fc, Logger: quiet(), MaxWorkOrderRetries: 2,
		NewID: func() string { seq++; return fmt.Sprintf("id-%d", seq) }})
	require.NoError(t, err)
	ctx := context.Background()
	cfg := sim.Defaults()
	cfg.Pumps, cfg.Degrading = 1, []int{0}
	s := sim.New(cfg)
	for i := 0; i < 160; i++ {
		_, err := e.Ingest(ctx, s.Next())
		require.NoError(t, err)
	}
	assert.Equal(t, 1+2, fc.calls, "initial attempt plus MaxWorkOrderRetries")
}

func TestDuplicateWorkOrderPerFailureMode(t *testing.T) {
	// Two alerts for the same asset/failure mode (separate episodes) must
	// share one open work order.
	store := memory.New()
	simCMMS := cmms.NewSimulated()
	e := newEngine(t, store, simCMMS, NopObserver{})
	ctx := context.Background()
	tpl, _ := template.DefaultFor("pump")
	tpl.Alerting.Suppression = alerting.Duration{Duration: 0}
	tpl.Alerting.MaxAlertsPerAssetPerHour = 0
	tpl.Alerting.ClearPersistence = 1
	_, err := e.PutTemplate(ctx, tpl)
	require.NoError(t, err)

	// Degrade, then recover (healthy sim with same asset id), then degrade again.
	run := func(cfg sim.Config, steps int) {
		s := sim.New(cfg)
		for i := 0; i < steps; i++ {
			_, err := e.Ingest(ctx, s.Next())
			require.NoError(t, err)
		}
	}
	cfg := sim.Defaults()
	cfg.Pumps, cfg.Degrading, cfg.DegradeFrom, cfg.RampSteps = 1, []int{0}, 40, 40
	run(cfg, 100)
	alerts, _ := store.ListAlerts(ctx, ports.AlertFilter{})
	require.Len(t, alerts, 1)

	healthy := cfg
	healthy.Degrading = nil
	healthy.Start = cfg.Start.Add(100 * cfg.StepInterval)
	healthy.Seed = 7
	run(healthy, 60)
	h, _ := e.Health("pump-001")
	require.Equal(t, alerting.StateNormal, h.State, "episode cleared after recovery")

	again := cfg
	again.Start = healthy.Start.Add(60 * cfg.StepInterval)
	again.DegradeFrom, again.RampSteps = 0, 20
	again.Seed = 9
	run(again, 40)
	alerts, _ = store.ListAlerts(ctx, ports.AlertFilter{})
	require.Len(t, alerts, 2, "second episode alerts again")
	wos, _ := store.ListWorkOrders(ctx, ports.WorkOrderFilter{})
	require.Len(t, wos, 1, "but the open work order is reused, not duplicated")
	for _, a := range alerts {
		assert.Equal(t, wos[0].ID, a.WorkOrderID)
	}
	first, _ := store.GetAlert(ctx, alerts[1].ID)
	assert.NotNil(t, first.ClearedAt)
}
