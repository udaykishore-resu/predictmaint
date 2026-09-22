package features

import "math"

// Stats is the set of time-domain descriptors computed over one window.
//
// Kurtosis is the standard (non-excess) fourth standardised moment, so a
// Gaussian window scores ~3 and impulsive bearing faults score well above.
// Crest is peak/RMS: ~1.41 for a pure sine, rising when spikes appear.
type Stats struct {
	Mean     float64
	Std      float64
	RMS      float64
	Kurtosis float64
	Crest    float64
	Peak     float64
}

// Compute returns the time-domain statistics of xs. It never returns NaN or
// Inf: degenerate windows (empty, constant) yield zero-valued moments and a
// crest factor of zero.
func Compute(xs []float64) Stats {
	n := float64(len(xs))
	if n == 0 {
		return Stats{}
	}
	var sum, sumSq, peak float64
	for _, x := range xs {
		sum += x
		sumSq += x * x
		if a := math.Abs(x); a > peak {
			peak = a
		}
	}
	mean := sum / n
	rms := math.Sqrt(sumSq / n)

	var m2, m4 float64
	for _, x := range xs {
		d := x - mean
		d2 := d * d
		m2 += d2
		m4 += d2 * d2
	}
	m2 /= n
	m4 /= n

	s := Stats{Mean: mean, RMS: rms, Peak: peak}
	if m2 > 0 {
		s.Std = math.Sqrt(m2)
		s.Kurtosis = m4 / (m2 * m2)
	}
	if rms > 0 {
		s.Crest = peak / rms
	}
	return sanitize(s)
}

func sanitize(s Stats) Stats {
	fix := func(v *float64) {
		if math.IsNaN(*v) || math.IsInf(*v, 0) {
			*v = 0
		}
	}
	fix(&s.Mean)
	fix(&s.Std)
	fix(&s.RMS)
	fix(&s.Kurtosis)
	fix(&s.Crest)
	fix(&s.Peak)
	return s
}
