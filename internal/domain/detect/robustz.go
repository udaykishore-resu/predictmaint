// Package detect holds the anomaly, drift and failure-prior detectors.
// Every detector is deterministic given its inputs and seed, returns scores in
// [0,1], and reports the rule version that produced the score so decisions
// can be audited.
package detect

import (
	"math"
	"sort"
)

// Rule versions, logged with every decision that uses them.
const (
	RuleRobustZ = "ZS-1"
	RuleHST     = "HST-1"
	RuleCUSUM   = "CUSUM-1"
	RuleWeibull = "WB-1"
	RuleFusion  = "FUSE-1"
)

// madScale converts a MAD into a consistent estimator of sigma for Gaussians.
const madScale = 1.4826

// Baseline is a robust location/scale pair for one feature.
type Baseline struct {
	Median float64 `json:"median"`
	MAD    float64 `json:"mad"`
}

// Baselines maps feature name -> baseline.
type Baselines map[string]Baseline

// RobustZParams are per-class hyperparameters for the robust z-score detector.
type RobustZParams struct {
	// Threshold is the |z| at which the score reaches 0.5.
	Threshold float64 `json:"threshold"`
	// MinMAD floors the scale so a constant warm-up window cannot make every
	// later sample look infinitely anomalous. Expressed as a fraction of |median|
	// with an absolute floor of MinMADAbs.
	MinMADRel float64 `json:"min_mad_rel"`
	MinMADAbs float64 `json:"min_mad_abs"`
	// Direction restricts which deviations count: "both" (default), "up" or "down".
	Direction map[string]string `json:"direction,omitempty"`
}

// Contribution is one feature's standardised deviation.
type Contribution struct {
	Feature string  `json:"feature"`
	Z       float64 `json:"z"`
	Value   float64 `json:"value"`
}

// RobustZResult is the detector output.
type RobustZResult struct {
	Score        float64        `json:"score"`
	MaxZ         float64        `json:"max_z"`
	Contributors []Contribution `json:"contributors"`
	Rule         string         `json:"rule"`
}

// Z returns the robust z-score of v against baseline b using the MAD floor
// rules of p.
func (p RobustZParams) Z(v float64, b Baseline) float64 {
	mad := b.MAD
	floor := math.Max(p.MinMADAbs, p.MinMADRel*math.Abs(b.Median))
	if mad < floor {
		mad = floor
	}
	if mad <= 0 {
		return 0
	}
	return (v - b.Median) / (madScale * mad)
}

// Score evaluates a feature vector against baselines. Features without a
// baseline are ignored. The score maps max|z| through
// 1-exp(-ln2*(z/threshold)^2): 0.5 at threshold, ~0.94 at 2x threshold.
func (p RobustZParams) Score(features map[string]float64, base Baselines) RobustZResult {
	res := RobustZResult{Rule: RuleRobustZ}
	thr := p.Threshold
	if thr <= 0 {
		thr = 3
	}
	for name, v := range features {
		b, ok := base[name]
		if !ok {
			continue
		}
		z := p.Z(v, b)
		if math.IsNaN(z) || math.IsInf(z, 0) {
			continue
		}
		switch p.Direction[name] {
		case "up":
			if z < 0 {
				z = 0
			}
		case "down":
			if z > 0 {
				z = 0
			}
		}
		res.Contributors = append(res.Contributors, Contribution{Feature: name, Z: z, Value: v})
		if a := math.Abs(z); a > res.MaxZ {
			res.MaxZ = a
		}
	}
	sort.Slice(res.Contributors, func(i, j int) bool {
		ai, aj := math.Abs(res.Contributors[i].Z), math.Abs(res.Contributors[j].Z)
		if ai != aj {
			return ai > aj
		}
		return res.Contributors[i].Feature < res.Contributors[j].Feature
	})
	r := res.MaxZ / thr
	res.Score = clamp01(1 - math.Exp(-math.Ln2*r*r))
	return res
}

// BaselineLearner accumulates warm-up samples per feature and fits robust
// baselines once enough have been seen.
type BaselineLearner struct {
	need    int
	samples map[string][]float64
	count   int
}

// NewBaselineLearner returns a learner that needs `need` vectors.
func NewBaselineLearner(need int) *BaselineLearner {
	if need < 3 {
		need = 3
	}
	return &BaselineLearner{need: need, samples: map[string][]float64{}}
}

// Observe adds one feature vector. It returns true when the learner has
// collected enough samples to fit.
func (l *BaselineLearner) Observe(features map[string]float64) bool {
	if l.count >= l.need {
		return true
	}
	for k, v := range features {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		l.samples[k] = append(l.samples[k], v)
	}
	l.count++
	return l.count >= l.need
}

// Count is the number of vectors observed so far.
func (l *BaselineLearner) Count() int { return l.count }

// Need is the number of vectors required.
func (l *BaselineLearner) Need() int { return l.need }

// Fit returns median/MAD baselines from the collected samples.
func (l *BaselineLearner) Fit() Baselines {
	out := make(Baselines, len(l.samples))
	for k, xs := range l.samples {
		if len(xs) == 0 {
			continue
		}
		med, mad := MedianMAD(xs)
		out[k] = Baseline{Median: med, MAD: mad}
	}
	return out
}

// MedianMAD returns the median and median absolute deviation of xs.
// xs is not modified.
func MedianMAD(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	tmp := make([]float64, len(xs))
	copy(tmp, xs)
	med := median(tmp)
	for i, x := range tmp {
		tmp[i] = math.Abs(x - med)
	}
	return med, median(tmp)
}

// median sorts xs in place and returns its median.
func median(xs []float64) float64 {
	sort.Float64s(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}
