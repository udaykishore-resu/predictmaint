package engine

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/detect"
	"github.com/udaykishore-resu/predictmaint/internal/domain/features"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
)

// Baseline sources reported in health.
const (
	BaselineClass = "class" // cold start: transferred from the class template
	BaselineAsset = "asset" // learned from this asset's own warm-up
)

// asset is the in-memory state of one monitored machine. All access is
// serialised by the engine mutex.
type asset struct {
	id, site, class string
	tpl             template.Template

	extractor *features.Extractor
	learner   *detect.BaselineLearner
	baselines detect.Baselines
	baseSrc   string
	hst       *detect.HST
	cusum     *detect.CUSUM
	policy    *alerting.AssetPolicy

	ageHours  float64
	lastTS    map[string]time.Time // per-signal high-water mark (replay guard)
	lastEval  time.Time
	lastSeen  time.Time
	firstSeen time.Time
	evals     int

	last struct {
		features   features.Vector
		assessment detect.Assessment
		decision   alerting.Decision
		z          detect.RobustZResult
		hst        detect.HSTResult
		cusum      detect.CUSUMResult
	}

	openAlertID    string // alert of the current active episode, if alerted
	pendingWOTop   string // top feature of an alert whose WO creation failed
	pendingWORetry int
}

func newAsset(id, site, class string, tpl template.Template) (*asset, error) {
	a := &asset{id: id, site: site, class: class, lastTS: map[string]time.Time{}}
	if err := a.applyTemplate(tpl, nil, 0); err != nil {
		return nil, err
	}
	return a, nil
}

// applyTemplate (re)builds the per-asset pipeline from a template, keeping
// learned baselines when they are still compatible and the given offset.
func (a *asset) applyTemplate(tpl template.Template, learned detect.Baselines, offset float64) error {
	ext, err := features.NewExtractor(tpl.Signals)
	if err != nil {
		return fmt.Errorf("asset %s: %w", a.id, err)
	}
	a.tpl = tpl
	a.class = tpl.Class
	a.extractor = ext
	a.learner = detect.NewBaselineLearner(tpl.WarmupEvaluations)

	names := tpl.FeatureNames()
	a.baselines, a.baseSrc = mergeBaselines(tpl.ClassBaselines, learned, names)
	a.hst = detect.NewHST(tpl.Detectors.HST, names, a.id, a.baselines, tpl.Detectors.RobustZ)
	a.cusum = detect.NewCUSUM(tpl.Detectors.CUSUM)
	if a.policy == nil {
		a.policy = alerting.NewAssetPolicy(tpl.Alerting)
	} else {
		a.policy.SetParams(tpl.Alerting)
	}
	a.policy.SetOffset(offset)
	a.lastEval = time.Time{}
	return nil
}

// mergeBaselines overlays learned baselines on the class defaults. Learned
// values win when present; if any learned value is present the source is
// "asset". Features unknown to the template are dropped.
func mergeBaselines(class, learned detect.Baselines, names []string) (detect.Baselines, string) {
	out := make(detect.Baselines, len(names))
	src := BaselineClass
	for _, n := range names {
		if b, ok := class[n]; ok {
			out[n] = b
		}
	}
	for _, n := range names {
		if b, ok := learned[n]; ok {
			out[n] = b
			src = BaselineAsset
		}
	}
	return out, src
}

// fitBaselines swaps the class baselines for the asset's own once warm-up
// completes. Where the learned MAD is degenerate, the class MAD is kept so a
// suspiciously quiet warm-up does not make the asset hypersensitive.
func (a *asset) fitBaselines() {
	learned := a.learner.Fit()
	for k, b := range learned {
		if cb, ok := a.tpl.ClassBaselines[k]; ok && b.MAD < cb.MAD*0.25 {
			b.MAD = cb.MAD * 0.25
			learned[k] = b
		}
	}
	a.baselines, a.baseSrc = mergeBaselines(a.tpl.ClassBaselines, learned, a.tpl.FeatureNames())
	a.hst.SetBaselines(a.baselines)
	a.cusum.Reset()
}
