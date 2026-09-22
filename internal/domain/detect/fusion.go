package detect

// FusionParams weight the detector outputs into one risk score. Weights are
// normalised so they need not sum to 1.
type FusionParams struct {
	WAnomaly float64 `json:"w_anomaly"`
	WDrift   float64 `json:"w_drift"`
	WPrior   float64 `json:"w_prior"`
	// AgreementThreshold is the per-detector score at which a detector is
	// counted as "voting" for an anomaly; it feeds the confidence estimate.
	AgreementThreshold float64 `json:"agreement_threshold"`
	// HSTShare is the share of the point-anomaly score taken from the
	// Half-Space Trees detector once it is ready (the rest comes from the
	// robust z-score). It is capped at 0.5 so a single multivariate density
	// dip can never carry an alert on its own.
	HSTShare float64 `json:"hst_share"`
}

func (p FusionParams) withDefaults() FusionParams {
	if p.WAnomaly <= 0 && p.WDrift <= 0 && p.WPrior <= 0 {
		p.WAnomaly, p.WDrift, p.WPrior = 0.6, 0.25, 0.15
	}
	if p.AgreementThreshold <= 0 {
		p.AgreementThreshold = 0.5
	}
	if p.HSTShare <= 0 {
		p.HSTShare = 0.35
	}
	if p.HSTShare > 0.5 {
		p.HSTShare = 0.5
	}
	return p
}

// Assessment is the fused view of one asset at one instant.
type Assessment struct {
	Risk       float64 `json:"risk"`       // fused failure risk in [0,1]
	Anomaly    float64 `json:"anomaly"`    // max of point-anomaly detectors
	Drift      float64 `json:"drift"`      // CUSUM score
	Prior      float64 `json:"prior"`      // Weibull conditional failure probability
	Confidence float64 `json:"confidence"` // risk discounted by detector disagreement
	Votes      int     `json:"votes"`      // detectors above the agreement threshold
	Voters     int     `json:"voters"`     // detectors that were ready to vote
	Rule       string  `json:"rule"`
}

// Fuse combines detector outputs. Rule FUSE-1:
//
//	anomaly    = (1-hstShare)*zscore + hstShare*hst   (zscore alone until HST is ready)
//	risk       = (wA*anomaly + wD*drift + wP*prior) / (wA+wD+wP)
//	confidence = risk * (0.5 + 0.5 * votes/voters)
//
// Blending rather than taking the max is deliberate: in a sparse
// high-dimensional feature cube HST occasionally scores a perfectly normal
// point as isolated, and a single detector must not be able to raise an
// alert by itself. The prior alone can never trip an alert either: with
// default weights a brand-new asset (prior ~ 0) and an ancient one
// (prior ~ 1) differ by only 0.15 risk.
func (p FusionParams) Fuse(z RobustZResult, h HSTResult, c CUSUMResult, pr PriorResult) Assessment {
	p = p.withDefaults()
	a := Assessment{Rule: RuleFusion, Drift: c.Score, Prior: pr.Score}

	a.Anomaly = z.Score
	if h.Ready {
		a.Anomaly = (1-p.HSTShare)*z.Score + p.HSTShare*h.Score
	}

	sum := p.WAnomaly + p.WDrift + p.WPrior
	a.Risk = clamp01((p.WAnomaly*a.Anomaly + p.WDrift*a.Drift + p.WPrior*a.Prior) / sum)

	a.Voters = 2 // z-score and CUSUM are always ready
	if z.Score >= p.AgreementThreshold {
		a.Votes++
	}
	if c.Score >= p.AgreementThreshold {
		a.Votes++
	}
	if h.Ready {
		a.Voters++
		if h.Score >= p.AgreementThreshold {
			a.Votes++
		}
	}
	a.Confidence = clamp01(a.Risk * (0.5 + 0.5*float64(a.Votes)/float64(a.Voters)))
	return a
}
