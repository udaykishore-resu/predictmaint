// Package engine orchestrates the hot path: metrics in, features, detectors,
// fused risk, alert policy, work order out. It owns per-asset state in memory
// and persists only what must survive a restart (alerts, work orders,
// templates, learned baselines and threshold offsets).
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/detect"
	"github.com/udaykishore-resu/predictmaint/internal/domain/features"
	"github.com/udaykishore-resu/predictmaint/internal/domain/metric"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
)

// Observer receives domain events for metrics/tracing. The engine never
// imports an instrumentation library; the observability package adapts.
type Observer interface {
	MetricsIngested(accepted, rejected, duplicates int)
	Evaluated(class string, a detect.Assessment)
	AlertOutcome(site, class string, outcome alerting.Outcome)
	WorkOrder(site, class, result string) // "created", "skipped", "failed", "cancelled"
	Feedback(class string, verdict alerting.Verdict)
}

// NopObserver ignores everything.
type NopObserver struct{}

func (NopObserver) MetricsIngested(int, int, int)                 {}
func (NopObserver) Evaluated(string, detect.Assessment)           {}
func (NopObserver) AlertOutcome(string, string, alerting.Outcome) {}
func (NopObserver) WorkOrder(string, string, string)              {}
func (NopObserver) Feedback(string, alerting.Verdict)             {}

// Options configure the engine.
type Options struct {
	Store    ports.Store
	CMMS     cmms.CMMS
	Logger   *slog.Logger
	Observer Observer
	Now      func() time.Time
	NewID    func() string
	// MaxWorkOrderRetries bounds retrying a failed CMMS call on later
	// evaluations of the same active episode.
	MaxWorkOrderRetries int
}

// Engine is the predictive-maintenance core. It is safe for concurrent use.
type Engine struct {
	mu sync.Mutex

	store    ports.Store
	cmms     cmms.CMMS
	log      *slog.Logger
	obs      Observer
	now      func() time.Time
	newID    func() string
	maxRetry int

	templates    map[string]template.Template
	assets       map[string]*asset
	siteLimiters map[string]*alerting.RateLimiter
}

// Sentinel errors surfaced to the API layer.
var (
	ErrUnknownAsset    = errors.New("unknown asset")
	ErrUnknownClass    = errors.New("no template for asset class")
	ErrClassRequired   = errors.New("asset_class is required for a first-seen asset")
	ErrAlreadyJudged   = errors.New("alert already has different feedback")
	ErrInvalidTemplate = errors.New("invalid template")
)

// New wires an engine, seeding built-in templates into the store when it
// holds none, and restoring asset snapshots.
func New(ctx context.Context, o Options) (*Engine, error) {
	if o.Store == nil {
		return nil, errors.New("engine: store is required")
	}
	if o.CMMS == nil {
		o.CMMS = cmms.NewSimulated()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Observer == nil {
		o.Observer = NopObserver{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = uuid.NewString
	}
	if o.MaxWorkOrderRetries <= 0 {
		o.MaxWorkOrderRetries = 5
	}
	e := &Engine{
		store: o.Store, cmms: o.CMMS, log: o.Logger, obs: o.Observer, now: o.Now, newID: o.NewID,
		maxRetry:     o.MaxWorkOrderRetries,
		templates:    map[string]template.Template{},
		assets:       map[string]*asset{},
		siteLimiters: map[string]*alerting.RateLimiter{},
	}
	stored, err := e.store.ListTemplates(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: load templates: %w", err)
	}
	for _, t := range stored {
		if err := t.Validate(); err != nil {
			return nil, fmt.Errorf("engine: stored template %q: %w", t.Class, err)
		}
		e.templates[t.Class] = t
	}
	for _, t := range template.Defaults() {
		if _, ok := e.templates[t.Class]; ok {
			continue
		}
		if err := e.store.PutTemplate(ctx, t); err != nil {
			return nil, fmt.Errorf("engine: seed template %q: %w", t.Class, err)
		}
		e.templates[t.Class] = t
	}
	snaps, err := e.store.ListAssetSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: load asset snapshots: %w", err)
	}
	for _, s := range snaps {
		tpl, ok := e.templates[s.AssetClass]
		if !ok {
			e.log.Warn("skipping asset snapshot with unknown class", "asset_id", s.AssetID, "class", s.AssetClass)
			continue
		}
		a := &asset{id: s.AssetID, site: s.Site, class: s.AssetClass, lastTS: map[string]time.Time{}, ageHours: s.AgeHours}
		if err := a.applyTemplate(tpl, s.Baselines, s.ThresholdOffset); err != nil {
			return nil, fmt.Errorf("engine: restore asset %s: %w", s.AssetID, err)
		}
		e.assets[a.id] = a
	}
	e.log.Info("engine ready", "templates", len(e.templates), "assets_restored", len(e.assets), "cmms", e.cmms.Name())
	return e, nil
}

// IngestResult summarises a batch.
type IngestResult struct {
	Accepted   int      `json:"accepted"`
	Rejected   int      `json:"rejected"`
	Duplicates int      `json:"duplicates"`
	Evaluated  int      `json:"evaluations"`
	Alerts     int      `json:"alerts_fired"`
	WorkOrders int      `json:"work_orders_created"`
	Errors     []string `json:"errors,omitempty"`
}

const maxReportedErrors = 20

func (r *IngestResult) reject(msg string) {
	r.Rejected++
	if len(r.Errors) < maxReportedErrors {
		r.Errors = append(r.Errors, msg)
	}
}

// Ingest processes a batch of metrics. It is idempotent: a replayed batch
// is detected per (asset, signal) by event-time high-water mark and skipped.
// Persistence errors abort the batch so the source can retry it.
func (e *Engine) Ingest(ctx context.Context, batch []metric.Metric) (IngestResult, error) {
	var res IngestResult
	byAsset := map[string][]metric.Metric{}
	var order []string
	for i := range batch {
		m := batch[i]
		if err := m.Validate(); err != nil {
			res.reject(fmt.Sprintf("metric[%d]: %v", i, err))
			continue
		}
		if _, seen := byAsset[m.AssetID]; !seen {
			order = append(order, m.AssetID)
		}
		byAsset[m.AssetID] = append(byAsset[m.AssetID], m)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, id := range order {
		ms := byAsset[id]
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].Timestamp.Before(ms[j].Timestamp) })
		a, err := e.assetFor(ms[0])
		if err != nil {
			for range ms {
				res.reject(fmt.Sprintf("asset %s: %v", id, err))
			}
			continue
		}
		var pendingTS time.Time
		for _, m := range ms {
			if !pendingTS.IsZero() && m.Timestamp.After(pendingTS) {
				if err := e.evaluateIfReady(ctx, a, pendingTS, &res); err != nil {
					return res, err
				}
			}
			pendingTS = m.Timestamp
			e.push(a, m, &res)
		}
		if !pendingTS.IsZero() {
			if err := e.evaluateIfReady(ctx, a, pendingTS, &res); err != nil {
				return res, err
			}
		}
	}
	e.obs.MetricsIngested(res.Accepted, res.Rejected, res.Duplicates)
	return res, nil
}

func (e *Engine) assetFor(m metric.Metric) (*asset, error) {
	if a, ok := e.assets[m.AssetID]; ok {
		if m.Site != metric.DefaultSite && a.site == metric.DefaultSite {
			a.site = m.Site
		}
		return a, nil
	}
	if m.AssetClass == "" {
		return nil, ErrClassRequired
	}
	tpl, ok := e.templates[m.AssetClass]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownClass, m.AssetClass)
	}
	a, err := newAsset(m.AssetID, m.Site, m.AssetClass, tpl)
	if err != nil {
		return nil, err
	}
	a.firstSeen = e.now()
	e.assets[a.id] = a
	e.log.Info("asset registered", "asset_id", a.id, "site", a.site, "class", a.class, "template_version", tpl.Version)
	return a, nil
}

func (e *Engine) push(a *asset, m metric.Metric, res *IngestResult) {
	if m.AssetClass != "" && m.AssetClass != a.class {
		res.reject(fmt.Sprintf("asset %s: class %q does not match registered class %q", a.id, m.AssetClass, a.class))
		return
	}
	if last, ok := a.lastTS[m.Signal]; ok && !m.Timestamp.After(last) {
		res.Duplicates++
		return
	}
	a.lastSeen = e.now()

	if a.tpl.RuntimeSignal != "" && m.Signal == a.tpl.RuntimeSignal {
		h, err := features.Convert(m.Value, m.Unit, "h")
		if err != nil {
			res.reject(fmt.Sprintf("asset %s: runtime signal: %v", a.id, err))
			return
		}
		a.ageHours = h
		a.lastTS[m.Signal] = m.Timestamp
		res.Accepted++
		return
	}
	var known bool
	for _, v := range m.Values() {
		ok, err := a.extractor.Push(m.Signal, v, m.Unit)
		if err != nil {
			res.reject(fmt.Sprintf("asset %s: %v", a.id, err))
			return
		}
		known = ok
		if !ok {
			break
		}
	}
	if !known {
		res.reject(fmt.Sprintf("asset %s: signal %q is not part of template %s", a.id, m.Signal, a.class))
		return
	}
	a.lastTS[m.Signal] = m.Timestamp
	res.Accepted++
}

func (e *Engine) evaluateIfReady(ctx context.Context, a *asset, ts time.Time, res *IngestResult) error {
	if !a.extractor.Ready() || !ts.After(a.lastEval) {
		return nil
	}
	return e.evaluate(ctx, a, ts, res)
}

// evaluate runs the full pipeline for one asset at event time ts.
func (e *Engine) evaluate(ctx context.Context, a *asset, ts time.Time, res *IngestResult) error {
	a.lastEval = ts
	a.evals++
	res.Evaluated++
	feats := a.extractor.Extract()
	a.last.features = feats

	if a.baseSrc == BaselineClass && a.learner.Observe(feats) {
		a.fitBaselines()
		e.log.Info("baselines learned", "asset_id", a.id, "evaluations", a.evals, "features", len(a.baselines))
		if err := e.snapshot(ctx, a); err != nil {
			return err
		}
	}

	tpl := a.tpl
	z := tpl.Detectors.RobustZ.Score(feats, a.baselines)
	h := a.hst.Observe(feats)
	var cz float64
	if b, ok := a.baselines[a.cusum.Feature()]; ok {
		cz = tpl.Detectors.RobustZ.Z(feats[a.cusum.Feature()], b)
	}
	c := a.cusum.Update(cz)
	pr := tpl.Prior.Prior(a.ageHours)
	as := tpl.Fusion.Fuse(z, h, c, pr)
	a.last.z, a.last.hst, a.last.cusum, a.last.assessment = z, h, c, as
	e.obs.Evaluated(a.class, as)

	d := a.policy.Evaluate(ts, as.Risk, e.siteLimiter(a.site, tpl))
	a.last.decision = d
	if d.Outcome != alerting.OutcomeNone && d.Outcome != alerting.OutcomeHold {
		e.obs.AlertOutcome(a.site, a.class, d.Outcome)
	}

	switch d.Outcome {
	case alerting.OutcomeFire:
		res.Alerts++
		alert := e.buildAlert(a, ts, as, z, d)
		e.log.Info("alert fired", "asset_id", a.id, "site", a.site, "alert_id", alert.ID,
			"risk", as.Risk, "confidence", as.Confidence, "threshold", d.EffectiveThreshold,
			"rules", alert.Rules, "top_feature", topFeature(z), "reason", d.Reason)
		if err := e.store.SaveAlert(ctx, alert); err != nil {
			return fmt.Errorf("save alert: %w", err)
		}
		a.openAlertID = alert.ID
		created, err := e.raiseWorkOrder(ctx, a, &alert, topFeature(z))
		if err != nil {
			e.log.Error("work order creation failed; will retry", "asset_id", a.id, "alert_id", alert.ID, "err", err)
			a.pendingWOTop = topFeature(z)
			a.pendingWORetry = 0
		} else if created {
			res.WorkOrders++
		}
	case alerting.OutcomeHold:
		if a.pendingWOTop != "" && a.openAlertID != "" && a.pendingWORetry < e.maxRetry {
			a.pendingWORetry++
			alert, err := e.store.GetAlert(ctx, a.openAlertID)
			if err != nil {
				return fmt.Errorf("load alert for work order retry: %w", err)
			}
			created, err := e.raiseWorkOrder(ctx, a, &alert, a.pendingWOTop)
			if err != nil {
				e.log.Error("work order retry failed", "asset_id", a.id, "attempt", a.pendingWORetry, "err", err)
			} else {
				a.pendingWOTop = ""
				if created {
					res.WorkOrders++
				}
			}
		}
	case alerting.OutcomeClear:
		a.pendingWOTop = ""
		if a.openAlertID != "" {
			alert, err := e.store.GetAlert(ctx, a.openAlertID)
			if err == nil {
				t := ts
				alert.ClearedAt = &t
				if err := e.store.SaveAlert(ctx, alert); err != nil {
					return fmt.Errorf("save cleared alert: %w", err)
				}
			} else if !errors.Is(err, ports.ErrNotFound) {
				return fmt.Errorf("load alert on clear: %w", err)
			}
			e.log.Info("episode cleared", "asset_id", a.id, "alert_id", a.openAlertID, "reason", d.Reason)
			a.openAlertID = ""
		}
	case alerting.OutcomeSuppressed, alerting.OutcomeRateLimited:
		e.log.Warn("alert withheld", "asset_id", a.id, "site", a.site, "outcome", d.Outcome, "reason", d.Reason, "risk", as.Risk, "rule", d.Rule)
	}
	return nil
}

func (e *Engine) siteLimiter(site string, tpl template.Template) *alerting.RateLimiter {
	if tpl.Alerting.MaxAlertsPerSitePerHour <= 0 {
		return nil
	}
	l, ok := e.siteLimiters[site]
	if !ok {
		l = alerting.NewRateLimiter(tpl.Alerting.MaxAlertsPerSitePerHour, time.Hour)
		e.siteLimiters[site] = l
	}
	return l
}

func topFeature(z detect.RobustZResult) string {
	if len(z.Contributors) == 0 {
		return ""
	}
	return z.Contributors[0].Feature
}

func (e *Engine) buildAlert(a *asset, ts time.Time, as detect.Assessment, z detect.RobustZResult, d alerting.Decision) alerting.Alert {
	contribs := make([]alerting.Contributor, 0, 5)
	for i, c := range z.Contributors {
		if i == 5 {
			break
		}
		contribs = append(contribs, alerting.Contributor{Feature: c.Feature, Z: c.Z, Value: c.Value})
	}
	return alerting.Alert{
		ID:           e.newID(),
		AssetID:      a.id,
		Site:         a.site,
		AssetClass:   a.class,
		FailureMode:  a.tpl.WorkOrder.ResolveFailureMode(topFeature(z)),
		Risk:         as.Risk,
		Confidence:   as.Confidence,
		Anomaly:      as.Anomaly,
		Drift:        as.Drift,
		Prior:        as.Prior,
		Status:       alerting.StatusOpen,
		EventTime:    ts,
		DetectedAt:   e.now(),
		Contributors: contribs,
		Decision:     d,
		Rules:        []string{z.Rule, a.last.hst.Rule, a.last.cusum.Rule, detect.RuleWeibull, as.Rule, d.Rule},
		TemplateVer:  a.tpl.Version,
	}
}

// raiseWorkOrder applies the work-order policy for a fired alert. It returns
// true when a new work order was created. Store/CMMS failures are returned
// so the caller can schedule a retry.
func (e *Engine) raiseWorkOrder(ctx context.Context, a *asset, alert *alerting.Alert, top string) (bool, error) {
	pol := a.tpl.WorkOrder
	fm := pol.ResolveFailureMode(top)
	var open *cmms.WorkOrder
	existing, err := e.store.FindOpenWorkOrder(ctx, a.id, fm)
	switch {
	case err == nil:
		open = &existing
	case errors.Is(err, ports.ErrNotFound):
	default:
		return false, fmt.Errorf("find open work order: %w", err)
	}
	dec := pol.Decide(alert.Confidence, top, open)
	if !dec.Create {
		e.obs.WorkOrder(a.site, a.class, "skipped")
		e.log.Info("work order skipped", "asset_id", a.id, "alert_id", alert.ID, "reason", dec.Reason, "rule", dec.Rule)
		if open != nil && alert.WorkOrderID == "" {
			alert.WorkOrderID = open.ID
			if err := e.store.SaveAlert(ctx, *alert); err != nil {
				return false, fmt.Errorf("link alert to work order: %w", err)
			}
		}
		return false, nil
	}
	now := e.now()
	wo := cmms.WorkOrder{
		ID:             e.newID(),
		AlertID:        alert.ID,
		AssetID:        a.id,
		Site:           a.site,
		AssetClass:     a.class,
		FailureMode:    fm,
		Priority:       pol.Priority,
		Confidence:     alert.Confidence,
		Risk:           alert.Risk,
		Description:    describe(a, alert, fm),
		Status:         cmms.StatusOpen,
		CreatedAt:      now,
		RequiredBy:     now.Add(pol.LeadTime.Duration),
		UpdatedAt:      now,
		IdempotencyKey: cmms.Key(a.id, fm, alert.ID),
		Rule:           dec.Rule,
	}
	ref, err := e.cmms.CreateWorkOrder(ctx, wo)
	if err != nil {
		e.obs.WorkOrder(a.site, a.class, "failed")
		return false, fmt.Errorf("cmms %s: %w", e.cmms.Name(), err)
	}
	wo.External = &ref
	if err := e.store.SaveWorkOrder(ctx, wo); err != nil {
		return false, fmt.Errorf("save work order: %w", err)
	}
	alert.WorkOrderID = wo.ID
	if err := e.store.SaveAlert(ctx, *alert); err != nil {
		return false, fmt.Errorf("link alert to work order: %w", err)
	}
	e.obs.WorkOrder(a.site, a.class, "created")
	e.log.Info("work order created", "asset_id", a.id, "alert_id", alert.ID, "work_order_id", wo.ID,
		"external_id", ref.ID, "cmms", ref.System, "failure_mode", fm, "confidence", alert.Confidence, "rule", dec.Rule)
	return true, nil
}

func describe(a *asset, alert *alerting.Alert, fm string) string {
	s := fmt.Sprintf("Predictive maintenance: %s suspected on %s (%s, site %s). Fused risk %.2f, confidence %.2f.",
		fm, a.id, a.class, a.site, alert.Risk, alert.Confidence)
	if len(alert.Contributors) > 0 {
		c := alert.Contributors[0]
		s += fmt.Sprintf(" Top indicator %s=%.3f (z=%.1f).", c.Feature, c.Value, c.Z)
	}
	return s
}

func (e *Engine) snapshot(ctx context.Context, a *asset) error {
	snap := ports.AssetSnapshot{
		AssetID: a.id, Site: a.site, AssetClass: a.class,
		BaselineSource: a.baseSrc, ThresholdOffset: a.policy.Offset(), AgeHours: a.ageHours, UpdatedAt: e.now(),
	}
	if a.baseSrc == BaselineAsset {
		snap.Baselines = a.baselines
	}
	if err := e.store.SaveAssetSnapshot(ctx, snap); err != nil {
		return fmt.Errorf("save asset snapshot: %w", err)
	}
	return nil
}

// FeedbackRequest is technician feedback on an alert.
type FeedbackRequest struct {
	AlertID    string
	Verdict    alerting.Verdict
	Technician string
	Note       string
}

// Feedback records a confirm/dismiss verdict, adapts the asset's threshold
// within guardrails and (on dismiss) cancels the linked work order when the
// class policy says so. Repeating the same verdict is a no-op; a conflicting
// verdict is rejected with ErrAlreadyJudged.
func (e *Engine) Feedback(ctx context.Context, req FeedbackRequest) (alerting.Alert, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	alert, err := e.store.GetAlert(ctx, req.AlertID)
	if err != nil {
		return alerting.Alert{}, err
	}
	want := alerting.StatusConfirmed
	if req.Verdict == alerting.VerdictDismiss {
		want = alerting.StatusDismissed
	}
	if alert.Status != alerting.StatusOpen {
		if alert.Status == want {
			return alert, nil
		}
		return alert, ErrAlreadyJudged
	}
	now := e.now()
	alert.Status = want
	alert.FeedbackAt = &now
	alert.Technician = req.Technician
	alert.Note = req.Note

	a, ok := e.assets[alert.AssetID]
	if ok {
		ad := a.policy.ApplyFeedback(req.Verdict)
		alert.Adaptation = &ad
		e.log.Info("threshold adapted", "asset_id", a.id, "alert_id", alert.ID, "verdict", req.Verdict,
			"threshold_before", ad.ThresholdBefore, "threshold_after", ad.ThresholdAfter, "clamped", ad.Clamped, "rule", ad.Rule)
		if err := e.snapshot(ctx, a); err != nil {
			return alert, err
		}
		e.obs.Feedback(a.class, req.Verdict)
		if req.Verdict == alerting.VerdictDismiss && alert.WorkOrderID != "" && a.tpl.WorkOrder.AutoCancelOnDismiss {
			if err := e.cancelWorkOrder(ctx, a, alert.WorkOrderID, "alert dismissed by "+req.Technician); err != nil {
				e.log.Error("work order cancel failed", "work_order_id", alert.WorkOrderID, "err", err)
			}
		}
	} else {
		e.log.Warn("feedback for asset not in memory; threshold not adapted", "asset_id", alert.AssetID)
	}
	if err := e.store.SaveAlert(ctx, alert); err != nil {
		return alert, fmt.Errorf("save alert: %w", err)
	}
	return alert, nil
}

func (e *Engine) cancelWorkOrder(ctx context.Context, a *asset, id, reason string) error {
	wo, err := e.store.GetWorkOrder(ctx, id)
	if err != nil {
		return err
	}
	if wo.Status != cmms.StatusOpen {
		return nil
	}
	if wo.External != nil {
		if err := e.cmms.CancelWorkOrder(ctx, *wo.External, reason); err != nil {
			return err
		}
	}
	wo.Status = cmms.StatusCancelled
	wo.UpdatedAt = e.now()
	if err := e.store.SaveWorkOrder(ctx, wo); err != nil {
		return err
	}
	e.obs.WorkOrder(a.site, a.class, "cancelled")
	e.log.Info("work order cancelled", "work_order_id", id, "reason", reason)
	return nil
}

// Health is the operator-facing view of one asset.
type Health struct {
	AssetID            string                 `json:"asset_id"`
	Site               string                 `json:"site"`
	AssetClass         string                 `json:"asset_class"`
	TemplateVersion    string                 `json:"template_version"`
	HealthIndex        float64                `json:"health_index"` // 1 - risk
	Risk               float64                `json:"risk"`
	Anomaly            float64                `json:"anomaly"`
	Drift              float64                `json:"drift"`
	Prior              float64                `json:"prior"`
	Confidence         float64                `json:"confidence"`
	State              alerting.State         `json:"alert_state"`
	EffectiveThreshold float64                `json:"effective_threshold"`
	ThresholdOffset    float64                `json:"threshold_offset"`
	BaselineSource     string                 `json:"baseline_source"`
	WarmupProgress     float64                `json:"warmup_progress"` // 0..1 of warm-up evaluations
	WindowFill         float64                `json:"window_fill"`     // 0..1 of required samples
	Evaluations        int                    `json:"evaluations"`
	AgeHours           float64                `json:"age_hours"`
	LastEventTime      *time.Time             `json:"last_event_time,omitempty"`
	LastSeen           *time.Time             `json:"last_seen,omitempty"`
	OpenAlertID        string                 `json:"open_alert_id,omitempty"`
	TopContributors    []alerting.Contributor `json:"top_contributors"`
	Detectors          HealthDetectors        `json:"detectors"`
	Features           features.Vector        `json:"features,omitempty"`
}

// HealthDetectors exposes each detector's latest output for auditability.
type HealthDetectors struct {
	RobustZ detect.RobustZResult `json:"robust_z"`
	HST     detect.HSTResult     `json:"hst"`
	CUSUM   detect.CUSUMResult   `json:"cusum"`
	Fusion  detect.Assessment    `json:"fusion"`
	Policy  alerting.Decision    `json:"policy"`
}

// Health returns the current health of an asset.
func (e *Engine) Health(assetID string) (Health, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.assets[assetID]
	if !ok {
		return Health{}, ErrUnknownAsset
	}
	h := Health{
		AssetID: a.id, Site: a.site, AssetClass: a.class, TemplateVersion: a.tpl.Version,
		Risk: a.last.assessment.Risk, HealthIndex: 1 - a.last.assessment.Risk,
		Anomaly: a.last.assessment.Anomaly, Drift: a.last.assessment.Drift, Prior: a.last.assessment.Prior,
		Confidence: a.last.assessment.Confidence,
		State:      a.policy.State(), EffectiveThreshold: a.policy.EffectiveThreshold(), ThresholdOffset: a.policy.Offset(),
		BaselineSource: a.baseSrc, WindowFill: a.extractor.Fill(), Evaluations: a.evals, AgeHours: a.ageHours,
		OpenAlertID: a.openAlertID, Features: a.last.features,
		Detectors:       HealthDetectors{RobustZ: a.last.z, HST: a.last.hst, CUSUM: a.last.cusum, Fusion: a.last.assessment, Policy: a.last.decision},
		TopContributors: []alerting.Contributor{},
	}
	if a.baseSrc == BaselineAsset {
		h.WarmupProgress = 1
	} else if a.learner.Need() > 0 {
		h.WarmupProgress = float64(a.learner.Count()) / float64(a.learner.Need())
	}
	if !a.lastEval.IsZero() {
		t := a.lastEval
		h.LastEventTime = &t
	}
	if !a.lastSeen.IsZero() {
		t := a.lastSeen
		h.LastSeen = &t
	}
	for i, c := range a.last.z.Contributors {
		if i == 5 {
			break
		}
		h.TopContributors = append(h.TopContributors, alerting.Contributor{Feature: c.Feature, Z: c.Z, Value: c.Value})
	}
	return h, nil
}

// AssetSummary is a fleet-list row.
type AssetSummary struct {
	AssetID     string         `json:"asset_id"`
	Site        string         `json:"site"`
	AssetClass  string         `json:"asset_class"`
	Risk        float64        `json:"risk"`
	HealthIndex float64        `json:"health_index"`
	State       alerting.State `json:"alert_state"`
	Baseline    string         `json:"baseline_source"`
}

// Assets lists known assets, optionally filtered by site, sorted by risk desc.
func (e *Engine) Assets(site string) []AssetSummary {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]AssetSummary, 0, len(e.assets))
	for _, a := range e.assets {
		if site != "" && a.site != site {
			continue
		}
		out = append(out, AssetSummary{
			AssetID: a.id, Site: a.site, AssetClass: a.class,
			Risk: a.last.assessment.Risk, HealthIndex: 1 - a.last.assessment.Risk,
			State: a.policy.State(), Baseline: a.baseSrc,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Risk != out[j].Risk {
			return out[i].Risk > out[j].Risk
		}
		return out[i].AssetID < out[j].AssetID
	})
	return out
}

// Templates lists the active templates sorted by class.
func (e *Engine) Templates() []template.Template {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]template.Template, 0, len(e.templates))
	for _, t := range e.templates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

// Template returns one template by class.
func (e *Engine) Template(class string) (template.Template, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.templates[class]
	return t, ok
}

// PutTemplate validates, persists and activates a template. Assets of that
// class are rebuilt on the new recipe: learned baselines are kept for
// features that still exist, threshold offsets are kept, detector windows
// restart.
func (e *Engine) PutTemplate(ctx context.Context, t template.Template) (template.Template, error) {
	if err := t.Validate(); err != nil {
		return t, fmt.Errorf("%w: %w", ErrInvalidTemplate, err)
	}
	t.UpdatedAt = e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.store.PutTemplate(ctx, t); err != nil {
		return t, fmt.Errorf("save template: %w", err)
	}
	e.templates[t.Class] = t
	rebuilt := 0
	for _, a := range e.assets {
		if a.class != t.Class {
			continue
		}
		var learned detect.Baselines
		if a.baseSrc == BaselineAsset {
			learned = a.baselines
		}
		if err := a.applyTemplate(t, learned, a.policy.Offset()); err != nil {
			return t, fmt.Errorf("rebuild asset %s: %w", a.id, err)
		}
		rebuilt++
	}
	e.log.Info("template updated", "class", t.Class, "version", t.Version, "assets_rebuilt", rebuilt)
	return t, nil
}

// Stats computes programme metrics for a site ("" = all).
func (e *Engine) Stats(ctx context.Context, site string) (alerting.Stats, error) {
	alerts, err := e.store.ListAlerts(ctx, ports.AlertFilter{Site: site})
	if err != nil {
		return alerting.Stats{}, err
	}
	s := alerting.ComputeStats(alerts, e.now())
	s.Site = site
	return s, nil
}
