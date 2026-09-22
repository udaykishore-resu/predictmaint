package features

import "math"

// Goertzel returns the amplitude of the sinusoidal component of xs closest to
// targetHz, given samples taken at sampleRateHz. It is an O(N) single-bin DFT,
// which is all a band-energy proxy needs: we do not want a full FFT per asset
// per sample on the hot path.
//
// The bin is snapped to k = round(N*f/fs) so the estimate is exact for a sine
// at a bin centre and degrades gracefully in between (standard DFT leakage).
// The result is scaled so a pure sine A*sin(2*pi*f*t) yields ~A.
//
// Degenerate inputs (fewer than 2 samples, non-positive rates, target above
// Nyquist) return 0 rather than NaN.
func Goertzel(xs []float64, sampleRateHz, targetHz float64) float64 {
	n := len(xs)
	if n < 2 || sampleRateHz <= 0 || targetHz < 0 || targetHz > sampleRateHz/2 {
		return 0
	}
	k := math.Round(float64(n) * targetHz / sampleRateHz)
	omega := 2 * math.Pi * k / float64(n)
	coeff := 2 * math.Cos(omega)

	var s0, s1, s2 float64
	for _, x := range xs {
		s0 = x + coeff*s1 - s2
		s2 = s1
		s1 = s0
	}
	power := s1*s1 + s2*s2 - coeff*s1*s2
	if power < 0 { // rounding noise on silent windows
		power = 0
	}
	amp := 2 * math.Sqrt(power) / float64(n)
	if k == 0 || 2*k == float64(n) { // DC and Nyquist bins are not doubled
		amp /= 2
	}
	if math.IsNaN(amp) || math.IsInf(amp, 0) {
		return 0
	}
	return amp
}
