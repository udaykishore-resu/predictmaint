package detect

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMedianMAD(t *testing.T) {
	tests := []struct {
		name    string
		in      []float64
		med, md float64
	}{
		{"empty", nil, 0, 0},
		{"odd", []float64{5, 1, 3}, 3, 2},
		{"even", []float64{1, 2, 3, 4}, 2.5, 1},
		{"constant", []float64{7, 7, 7}, 7, 0},
		{"outlier-resistant", []float64{1, 1, 1, 1, 1000}, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := append([]float64(nil), tc.in...)
			med, md := MedianMAD(in)
			assert.Equal(t, tc.med, med)
			assert.Equal(t, tc.md, md)
			assert.Equal(t, tc.in, in, "input must not be mutated")
		})
	}
}

func TestRobustZScore(t *testing.T) {
	p := RobustZParams{Threshold: 3, MinMADAbs: 0.01}
	base := Baselines{"a": {Median: 10, MAD: 1}, "b": {Median: 0, MAD: 0}}

	res := p.Score(map[string]float64{"a": 10, "b": 0, "unknown": 99}, base)
	assert.Equal(t, RuleRobustZ, res.Rule)
	assert.InDelta(t, 0, res.Score, 1e-9)
	assert.Len(t, res.Contributors, 2)

	// z = 3*1.4826 -> exactly at threshold when v = med + 3*madScale*MAD
	res = p.Score(map[string]float64{"a": 10 + 3*madScale}, base)
	assert.InDelta(t, 0.5, res.Score, 1e-9)
	assert.InDelta(t, 3, res.MaxZ, 1e-9)

	res = p.Score(map[string]float64{"a": 10 + 6*madScale}, base)
	assert.InDelta(t, 1-math.Pow(0.5, 4), res.Score, 1e-9)
	assert.Equal(t, "a", res.Contributors[0].Feature)

	// MAD floor: zero MAD must not explode.
	res = p.Score(map[string]float64{"b": 0.02}, base)
	assert.False(t, math.IsNaN(res.Score))
	assert.Less(t, res.Score, 1.0)

	// Direction filter.
	pd := RobustZParams{Threshold: 3, Direction: map[string]string{"a": "up"}}
	res = pd.Score(map[string]float64{"a": 0}, base)
	assert.Equal(t, 0.0, res.Score)
	pd.Direction["a"] = "down"
	res = pd.Score(map[string]float64{"a": 20}, base)
	assert.Equal(t, 0.0, res.Score)

	// Zero threshold falls back to 3.
	res = RobustZParams{}.Score(map[string]float64{"a": 10 + 3*madScale}, base)
	assert.InDelta(t, 0.5, res.Score, 1e-9)

	// NaN input is ignored.
	res = p.Score(map[string]float64{"a": math.NaN()}, base)
	assert.Equal(t, 0.0, res.Score)
}

func TestBaselineLearner(t *testing.T) {
	l := NewBaselineLearner(2) // coerced to 3
	assert.Equal(t, 3, l.Need())
	assert.False(t, l.Observe(map[string]float64{"a": 1, "nan": math.NaN()}))
	assert.False(t, l.Observe(map[string]float64{"a": 3}))
	assert.True(t, l.Observe(map[string]float64{"a": 2}))
	assert.True(t, l.Observe(map[string]float64{"a": 100}), "extra samples ignored")
	assert.Equal(t, 3, l.Count())
	b := l.Fit()
	assert.Equal(t, Baseline{Median: 2, MAD: 1}, b["a"])
	_, ok := b["nan"]
	assert.False(t, ok)
}

func TestHSTSeparatesShiftedPoints(t *testing.T) {
	feats := []string{"x", "y", "z"}
	base := Baselines{"x": {0, 1}, "y": {0, 1}, "z": {0, 1}}
	zp := RobustZParams{Threshold: 3}
	h := NewHST(HSTParams{Trees: 30, Depth: 6, WindowSize: 100, Seed: 42, ZRange: 4}, feats, "asset", base, zp)

	r := rand.New(rand.NewSource(3))
	sample := func(shift float64) map[string]float64 {
		return map[string]float64{
			"x": shift + 0.3*r.NormFloat64(),
			"y": shift + 0.3*r.NormFloat64(),
			"z": shift + 0.3*r.NormFloat64(),
		}
	}
	for i := 0; i < 100; i++ {
		res := h.Observe(sample(0))
		assert.False(t, res.Ready)
		assert.Equal(t, 0.0, res.Score)
	}
	assert.True(t, h.Ready())

	var normal, shifted float64
	for i := 0; i < 50; i++ {
		normal += h.Observe(sample(0)).Score
	}
	normal /= 50
	for i := 0; i < 20; i++ {
		shifted += h.Observe(sample(6)).Score
	}
	shifted /= 20
	assert.Less(t, normal, 0.2, "in-distribution points look dense")
	assert.Greater(t, shifted, 0.8, "shifted points look sparse")
	assert.Greater(t, shifted, normal)

	h.SetBaselines(base)
	assert.False(t, h.Ready(), "re-baselining restarts warm-up")
}

func TestHSTDeterministicAndDefaults(t *testing.T) {
	feats := []string{"x"}
	base := Baselines{"x": {0, 1}}
	a := NewHST(HSTParams{}, feats, "pump-1", base, RobustZParams{})
	b := NewHST(HSTParams{}, feats, "pump-1", base, RobustZParams{})
	for i := 0; i < 70; i++ {
		v := map[string]float64{"x": float64(i % 5)}
		ra, rb := a.Observe(v), b.Observe(v)
		assert.Equal(t, ra, rb)
	}
	assert.True(t, a.Ready())
	// missing feature / baseline lands at the cube centre without panicking
	res := a.Observe(map[string]float64{"other": 1})
	assert.False(t, math.IsNaN(res.Score))
	empty := NewHST(HSTParams{Depth: 99}, nil, "", nil, RobustZParams{})
	assert.NotPanics(t, func() { empty.Observe(nil) })
}

func TestCUSUM(t *testing.T) {
	c := NewCUSUM(CUSUMParams{Feature: "vib.rms", K: 0.5, H: 5})
	assert.Equal(t, "vib.rms", c.Feature())
	var res CUSUMResult
	for i := 0; i < 20; i++ {
		res = c.Update(0.2) // below allowance: nothing accumulates
	}
	assert.Equal(t, 0.0, res.Score)
	for i := 0; i < 10; i++ {
		res = c.Update(1.5) // +1.0 per step -> alarm at step 5
	}
	assert.True(t, res.Alarm)
	assert.Equal(t, 1.0, res.Score)
	assert.InDelta(t, 10, res.GPos, 1e-9, "capped at 2H")
	assert.Equal(t, RuleCUSUM, res.Rule)

	// Recovery after repair.
	for i := 0; i < 20; i++ {
		res = c.Update(-1)
	}
	assert.Equal(t, 0.0, res.GPos)
	assert.Greater(t, res.GNeg, 0.0)
	c.Reset()
	assert.Equal(t, 0.0, c.Update(0).Score)

	up := NewCUSUM(CUSUMParams{Direction: "up"})
	for i := 0; i < 20; i++ {
		res = up.Update(-3)
	}
	assert.Equal(t, 0.0, res.Score, "downward shift ignored when direction=up")
	down := NewCUSUM(CUSUMParams{Direction: "down"})
	for i := 0; i < 20; i++ {
		res = down.Update(-3)
	}
	assert.Equal(t, 1.0, res.Score)
	assert.Equal(t, 0.0, NewCUSUM(CUSUMParams{}).Update(math.NaN()).Score)
}

func TestWeibull(t *testing.T) {
	w := Weibull{Shape: 2, ScaleHours: 10000, HorizonHours: 720, DefaultAgeHours: 5000}
	assert.InDelta(t, 1, w.Reliability(0), 1e-12)
	assert.InDelta(t, math.Exp(-1), w.Reliability(10000), 1e-12)
	assert.Greater(t, w.Hazard(20000), w.Hazard(1000), "wear-out: hazard rises with age")
	assert.Equal(t, 0.0, w.Hazard(0))
	assert.Equal(t, 0.0, w.Hazard(-1))

	young := w.ConditionalFailureProb(100, 720)
	old := w.ConditionalFailureProb(30000, 720)
	assert.Greater(t, old, young)
	assert.GreaterOrEqual(t, young, 0.0)
	assert.LessOrEqual(t, old, 1.0)
	assert.Equal(t, 0.0, w.ConditionalFailureProb(100, 0))
	assert.InDelta(t, w.ConditionalFailureProb(0, 720), w.ConditionalFailureProb(-5, 720), 1e-12)

	pr := w.Prior(0)
	assert.Equal(t, 5000.0, pr.AgeHours, "default age when runtime unknown")
	assert.Equal(t, RuleWeibull, pr.Rule)
	assert.Greater(t, pr.HazardPerH, 0.0)

	// Extremes stay finite.
	assert.Equal(t, 1.0, w.ConditionalFailureProb(1e9, 720))
	infant := Weibull{Shape: 0.5, ScaleHours: 100, HorizonHours: 10}
	assert.True(t, math.IsInf(infant.Hazard(0), 1))
	assert.Equal(t, 0.0, infant.Prior(0).HazardPerH)
	memoryless := Weibull{Shape: 1, ScaleHours: 100}
	assert.InDelta(t, 0.01, memoryless.Hazard(0), 1e-12)
	assert.Equal(t, 0.0, Weibull{}.Hazard(10))
	assert.Equal(t, 1.0, Weibull{}.Reliability(10))
	assert.Equal(t, 0.0, Weibull{}.ConditionalFailureProb(10, 10))
}

func TestFuse(t *testing.T) {
	p := FusionParams{WAnomaly: 0.6, WDrift: 0.25, WPrior: 0.15}
	quiet := p.Fuse(RobustZResult{Score: 0}, HSTResult{Ready: true, Score: 0}, CUSUMResult{Score: 0}, PriorResult{Score: 1})
	assert.InDelta(t, 0.15, quiet.Risk, 1e-9, "prior alone stays far below alert level")
	assert.Equal(t, 0, quiet.Votes)
	assert.Equal(t, 3, quiet.Voters)

	loud := p.Fuse(RobustZResult{Score: 0.9}, HSTResult{Ready: true, Score: 0.95}, CUSUMResult{Score: 1}, PriorResult{Score: 0.2})
	wantAnomaly := 0.65*0.9 + 0.35*0.95
	assert.InDelta(t, wantAnomaly, loud.Anomaly, 1e-9)
	assert.InDelta(t, 0.6*wantAnomaly+0.25*1+0.15*0.2, loud.Risk, 1e-9)

	hstOnly := p.Fuse(RobustZResult{Score: 0}, HSTResult{Ready: true, Score: 1}, CUSUMResult{}, PriorResult{})
	assert.InDelta(t, 0.35, hstOnly.Anomaly, 1e-9, "HST alone cannot saturate the anomaly score")
	capped := FusionParams{HSTShare: 0.9}.Fuse(RobustZResult{Score: 0}, HSTResult{Ready: true, Score: 1}, CUSUMResult{}, PriorResult{})
	assert.InDelta(t, 0.5, capped.Anomaly, 1e-9, "hst_share capped at 0.5")
	assert.Equal(t, 3, loud.Votes)
	assert.InDelta(t, loud.Risk, loud.Confidence, 1e-9, "full agreement -> confidence == risk")
	assert.Equal(t, RuleFusion, loud.Rule)

	partial := p.Fuse(RobustZResult{Score: 0.9}, HSTResult{Ready: false, Score: 0.99}, CUSUMResult{Score: 0}, PriorResult{})
	assert.InDelta(t, 0.9, partial.Anomaly, 1e-9, "unready HST is ignored")
	assert.Equal(t, 2, partial.Voters)
	assert.Equal(t, 1, partial.Votes)
	assert.InDelta(t, partial.Risk*0.75, partial.Confidence, 1e-9)

	def := FusionParams{}.Fuse(RobustZResult{Score: 1}, HSTResult{}, CUSUMResult{Score: 1}, PriorResult{Score: 1})
	assert.InDelta(t, 1, def.Risk, 1e-9)
}

func FuzzRobustZ(f *testing.F) {
	f.Add(1.0, 2.0, 0.5)
	f.Add(0.0, 0.0, 0.0)
	f.Fuzz(func(t *testing.T, v, med, mad float64) {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.IsNaN(med) || math.IsInf(med, 0) || math.IsNaN(mad) || math.IsInf(mad, 0) {
			t.Skip()
		}
		p := RobustZParams{Threshold: 3, MinMADAbs: 1e-9}
		res := p.Score(map[string]float64{"f": v}, Baselines{"f": {Median: med, MAD: math.Abs(mad)}})
		require.False(t, math.IsNaN(res.Score))
		require.GreaterOrEqual(t, res.Score, 0.0)
		require.LessOrEqual(t, res.Score, 1.0)
	})
}
