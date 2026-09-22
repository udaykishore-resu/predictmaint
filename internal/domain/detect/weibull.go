package detect

import "math"

// Weibull is the per-asset-class failure-rate prior. Shape > 1 means
// wear-out (hazard rises with age), shape == 1 is memoryless, shape < 1 is
// infant mortality. ScaleHours is the characteristic life.
type Weibull struct {
	Shape      float64 `json:"shape"`
	ScaleHours float64 `json:"scale_hours"`
	// HorizonHours is the look-ahead over which the conditional failure
	// probability is computed (e.g. the maintenance planning window).
	HorizonHours float64 `json:"horizon_hours"`
	// DefaultAgeHours is assumed when an asset reports no runtime.
	DefaultAgeHours float64 `json:"default_age_hours"`
}

func (w Weibull) valid() bool {
	return w.Shape > 0 && w.ScaleHours > 0
}

// Hazard returns the instantaneous failure rate (per hour) at age t.
func (w Weibull) Hazard(ageHours float64) float64 {
	if !w.valid() || ageHours < 0 {
		return 0
	}
	if ageHours == 0 {
		if w.Shape < 1 {
			return math.Inf(1)
		}
		if w.Shape == 1 {
			return 1 / w.ScaleHours
		}
		return 0
	}
	return (w.Shape / w.ScaleHours) * math.Pow(ageHours/w.ScaleHours, w.Shape-1)
}

// Reliability returns the survival probability to age t.
func (w Weibull) Reliability(ageHours float64) float64 {
	if !w.valid() || ageHours <= 0 {
		return 1
	}
	return math.Exp(-math.Pow(ageHours/w.ScaleHours, w.Shape))
}

// ConditionalFailureProb returns P(fail within horizon | survived to age).
func (w Weibull) ConditionalFailureProb(ageHours, horizonHours float64) float64 {
	if !w.valid() || horizonHours <= 0 {
		return 0
	}
	if ageHours < 0 {
		ageHours = 0
	}
	rNow := w.Reliability(ageHours)
	if rNow <= 0 {
		return 1
	}
	return clamp01(1 - w.Reliability(ageHours+horizonHours)/rNow)
}

// PriorResult is the failure-prior output for one asset.
type PriorResult struct {
	Score      float64 `json:"score"` // conditional failure probability over the horizon
	AgeHours   float64 `json:"age_hours"`
	HazardPerH float64 `json:"hazard_per_hour"`
	Rule       string  `json:"rule"`
}

// Prior evaluates the class prior for an asset of the given age. A
// non-positive age falls back to DefaultAgeHours.
func (w Weibull) Prior(ageHours float64) PriorResult {
	if ageHours <= 0 {
		ageHours = w.DefaultAgeHours
	}
	h := w.Hazard(ageHours)
	if math.IsInf(h, 0) || math.IsNaN(h) {
		h = 0
	}
	return PriorResult{
		Score:      w.ConditionalFailureProb(ageHours, w.HorizonHours),
		AgeHours:   ageHours,
		HazardPerH: h,
		Rule:       RuleWeibull,
	}
}
