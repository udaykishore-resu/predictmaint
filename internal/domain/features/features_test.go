package features

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowOrderAndEviction(t *testing.T) {
	w := NewWindow(3)
	assert.Equal(t, 0, w.Len())
	for _, v := range []float64{1, 2, 3, 4, 5} {
		w.Push(v)
	}
	assert.True(t, w.Full())
	assert.Equal(t, []float64{3, 4, 5}, w.Values(nil))
	w.Reset()
	assert.Equal(t, 0, w.Len())
	w.Push(9)
	assert.Equal(t, []float64{9}, w.Values(nil))
	assert.Equal(t, 1, NewWindow(0).Cap())
}

func TestComputeTable(t *testing.T) {
	sine := make([]float64, 1000)
	for i := range sine {
		sine[i] = 2 * math.Sin(2*math.Pi*float64(i)/50)
	}
	tests := []struct {
		name string
		in   []float64
		want Stats
		tol  float64
	}{
		{"empty", nil, Stats{}, 0},
		{"constant", []float64{4, 4, 4, 4}, Stats{Mean: 4, RMS: 4, Peak: 4, Crest: 1}, 1e-12},
		{"zeros", []float64{0, 0, 0}, Stats{}, 0},
		{"two", []float64{1, -1}, Stats{Mean: 0, Std: 1, RMS: 1, Kurtosis: 1, Crest: 1, Peak: 1}, 1e-12},
		// pure sine: RMS = A/sqrt2, crest = sqrt2, kurtosis = 1.5
		{"sine", sine, Stats{Mean: 0, Std: math.Sqrt2, RMS: math.Sqrt2, Kurtosis: 1.5, Crest: math.Sqrt2, Peak: 2}, 5e-3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.in)
			assert.InDelta(t, tc.want.Mean, got.Mean, tc.tol+1e-12)
			assert.InDelta(t, tc.want.Std, got.Std, tc.tol+1e-12)
			assert.InDelta(t, tc.want.RMS, got.RMS, tc.tol+1e-12)
			assert.InDelta(t, tc.want.Kurtosis, got.Kurtosis, tc.tol+1e-12)
			assert.InDelta(t, tc.want.Crest, got.Crest, tc.tol+1e-12)
			assert.InDelta(t, tc.want.Peak, got.Peak, tc.tol+1e-12)
		})
	}
}

func TestKurtosisRisesWithImpulses(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	base := make([]float64, 512)
	for i := range base {
		base[i] = r.NormFloat64()
	}
	impulsive := append([]float64(nil), base...)
	for i := 0; i < len(impulsive); i += 64 {
		impulsive[i] += 12
	}
	assert.InDelta(t, 3, Compute(base).Kurtosis, 0.6)
	assert.Greater(t, Compute(impulsive).Kurtosis, 6.0)
	assert.Greater(t, Compute(impulsive).Crest, Compute(base).Crest)
}

// naiveDFTAmplitude is the reference implementation Goertzel must agree with.
func naiveDFTAmplitude(xs []float64, k int) float64 {
	n := float64(len(xs))
	var re, im float64
	for i, x := range xs {
		ang := 2 * math.Pi * float64(k) * float64(i) / n
		re += x * math.Cos(ang)
		im -= x * math.Sin(ang)
	}
	return 2 * math.Hypot(re, im) / n
}

func TestGoertzelMatchesDFT(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for trial := 0; trial < 50; trial++ {
		n := 64 + r.Intn(200)
		xs := make([]float64, n)
		for i := range xs {
			xs[i] = r.NormFloat64()
		}
		fs := 1000.0
		k := 1 + r.Intn(n/2-1)
		f := float64(k) * fs / float64(n)
		got := Goertzel(xs, fs, f)
		want := naiveDFTAmplitude(xs, k)
		assert.InDelta(t, want, got, 1e-9, "n=%d k=%d", n, k)
	}
}

func TestGoertzelRecoversSineAmplitude(t *testing.T) {
	const fs, f, amp = 2000.0, 125.0, 3.7
	xs := make([]float64, 400) // 400 samples -> bin 25 exactly
	for i := range xs {
		xs[i] = amp*math.Sin(2*math.Pi*f*float64(i)/fs) + 0.5*math.Sin(2*math.Pi*400*float64(i)/fs)
	}
	assert.InDelta(t, amp, Goertzel(xs, fs, f), 1e-9)
	assert.InDelta(t, 0.5, Goertzel(xs, fs, 400), 1e-9)
	assert.InDelta(t, 0, Goertzel(xs, fs, 250), 1e-9)
}

func TestGoertzelDegenerate(t *testing.T) {
	assert.Equal(t, 0.0, Goertzel(nil, 100, 10))
	assert.Equal(t, 0.0, Goertzel([]float64{1}, 100, 10))
	assert.Equal(t, 0.0, Goertzel([]float64{1, 2, 3}, 0, 10))
	assert.Equal(t, 0.0, Goertzel([]float64{1, 2, 3}, 100, 60)) // above Nyquist
	assert.Equal(t, 0.0, Goertzel([]float64{1, 2, 3}, 100, -1))
}

func TestConvert(t *testing.T) {
	tests := []struct {
		v        float64
		from, to string
		want     float64
		wantErr  bool
	}{
		{1, "in/s", "mm/s", 25.4, false},
		{212, "F", "C", 100, false},
		{0, "C", "F", 32, false},
		{273.15, "K", "C", 0, false},
		{14.5038, "psi", "bar", 1, false},
		{9.80665, "m/s2", "g", 1, false},
		{60, "Hz", "rpm", 3600, false},
		{50, "%", "%", 50, false},
		{1, "psi", "mm/s", 0, true},
		{1, "furlong", "mm/s", 0, true},
		{1, "mm/s", "cubits", 0, true},
	}
	for _, tc := range tests {
		got, err := Convert(tc.v, tc.from, tc.to)
		if tc.wantErr {
			assert.Error(t, err, "%s->%s", tc.from, tc.to)
			continue
		}
		require.NoError(t, err)
		assert.InDelta(t, tc.want, got, 1e-3, "%s->%s", tc.from, tc.to)
	}
	cu, err := CanonicalUnit("psi")
	require.NoError(t, err)
	assert.Equal(t, "bar", cu)
	_, err = CanonicalUnit("nope")
	assert.Error(t, err)
}

func testSpecs() []SignalSpec {
	return []SignalSpec{
		{Name: "vib", Unit: "mm/s", Window: 8, SampleRateHz: 100, Bands: []Band{{Name: "bpfo", FreqHz: 25}}},
		{Name: "temp", Unit: "C", Window: 4, MinSamples: 2, Stats: []string{"mean", "std"}},
	}
}

func TestExtractorLifecycle(t *testing.T) {
	e, err := NewExtractor(testSpecs())
	require.NoError(t, err)
	assert.False(t, e.Ready())
	assert.Equal(t, 0.0, e.Fill())

	ok, err := e.Push("unknown", 1, "mm/s")
	require.NoError(t, err)
	assert.False(t, ok)

	_, err = e.Push("vib", 1, "psi")
	assert.Error(t, err, "unit mismatch must be rejected")

	for i := 0; i < 8; i++ {
		ok, err := e.Push("vib", float64(i%2), "mm/s")
		require.NoError(t, err)
		assert.True(t, ok)
	}
	assert.False(t, e.Ready(), "temp still cold")
	_, _ = e.Push("temp", 100, "F")
	_, _ = e.Push("temp", 100, "F")
	assert.True(t, e.Ready())
	assert.InDelta(t, 1, e.Fill(), 1e-9)

	v := e.Extract()
	assert.InDelta(t, 37.78, v[Key("temp", "mean")], 0.01)
	assert.Contains(t, v, BandKey("vib", "bpfo"))
	assert.NotContains(t, v, Key("temp", "kurtosis"), "stats selection honoured")
	assert.Contains(t, v, Key("vib", "kurtosis"))
	assert.Equal(t, FeatureNames(testSpecs()), v.Keys())
}

func TestNewExtractorValidation(t *testing.T) {
	bad := [][]SignalSpec{
		{{Name: "", Unit: "C", Window: 4}},
		{{Name: "a", Unit: "C", Window: 1}},
		{{Name: "a", Unit: "??", Window: 4}},
		{{Name: "a", Unit: "C", Window: 4, Bands: []Band{{"x", 1}}}},
		{{Name: "a", Unit: "C", Window: 4}, {Name: "a", Unit: "C", Window: 4}},
		{{Name: "a", Unit: "C", Window: 4, Stats: []string{"median"}}},
	}
	for i, specs := range bad {
		_, err := NewExtractor(specs)
		assert.Error(t, err, "case %d", i)
	}
}

// FuzzCompute guards the invariant that no window, however pathological,
// produces NaN/Inf features or panics.
func FuzzCompute(f *testing.F) {
	f.Add(1.0, 2.0, 3.0, 4.0)
	f.Add(0.0, 0.0, 0.0, 0.0)
	f.Add(math.MaxFloat64, -math.MaxFloat64, 1e-300, 0.0)
	f.Fuzz(func(t *testing.T, a, b, c, d float64) {
		xs := []float64{a, b, c, d}
		for _, x := range xs {
			if math.IsNaN(x) || math.IsInf(x, 0) {
				t.Skip()
			}
		}
		s := Compute(xs)
		for _, v := range []float64{s.Mean, s.Std, s.RMS, s.Kurtosis, s.Crest, s.Peak} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("non-finite stat %+v for %v", s, xs)
			}
		}
		if g := Goertzel(xs, 100, 25); math.IsNaN(g) || math.IsInf(g, 0) {
			t.Fatalf("non-finite goertzel %v for %v", g, xs)
		}
	})
}
