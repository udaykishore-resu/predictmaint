package detect

import "math"

// CUSUMParams configure a two-sided tabular CUSUM on a robust z-scored
// feature. K is the allowance (half the shift worth detecting, in sigmas),
// H the decision interval (in sigmas). Classic defaults: K=0.5, H=5.
type CUSUMParams struct {
	Feature string  `json:"feature"` // feature the CUSUM tracks
	K       float64 `json:"k"`
	H       float64 `json:"h"`
	// Direction: "both" (default), "up" (only increases are drift) or "down".
	Direction string `json:"direction,omitempty"`
}

func (p CUSUMParams) withDefaults() CUSUMParams {
	if p.K <= 0 {
		p.K = 0.5
	}
	if p.H <= 0 {
		p.H = 5
	}
	return p
}

// CUSUM is the per-asset drift tracker state.
type CUSUM struct {
	p    CUSUMParams
	gPos float64
	gNeg float64
}

// CUSUMResult is the detector output.
type CUSUMResult struct {
	Score float64 `json:"score"` // max(g+,g-)/H clamped to [0,1]
	GPos  float64 `json:"g_pos"`
	GNeg  float64 `json:"g_neg"`
	Alarm bool    `json:"alarm"`
	Rule  string  `json:"rule"`
}

// NewCUSUM creates a tracker.
func NewCUSUM(p CUSUMParams) *CUSUM {
	return &CUSUM{p: p.withDefaults()}
}

// Reset clears the accumulated statistics (used when baselines are refit).
func (c *CUSUM) Reset() { c.gPos, c.gNeg = 0, 0 }

// Update feeds one standardised observation z. The statistics are capped at
// 2H so a long-lived drift does not take forever to recover after repair.
func (c *CUSUM) Update(z float64) CUSUMResult {
	if math.IsNaN(z) || math.IsInf(z, 0) {
		z = 0
	}
	c.gPos = math.Max(0, c.gPos+z-c.p.K)
	c.gNeg = math.Max(0, c.gNeg-z-c.p.K)
	capv := 2 * c.p.H
	c.gPos = math.Min(c.gPos, capv)
	c.gNeg = math.Min(c.gNeg, capv)

	g := math.Max(c.gPos, c.gNeg)
	switch c.p.Direction {
	case "up":
		g = c.gPos
	case "down":
		g = c.gNeg
	}
	return CUSUMResult{
		Score: clamp01(g / c.p.H),
		GPos:  c.gPos,
		GNeg:  c.gNeg,
		Alarm: g >= c.p.H,
		Rule:  RuleCUSUM,
	}
}

// Feature returns the feature this CUSUM tracks.
func (c *CUSUM) Feature() string { return c.p.Feature }
