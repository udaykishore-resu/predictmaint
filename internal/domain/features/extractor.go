package features

import (
	"fmt"
	"sort"
)

// Band names a narrow frequency band whose amplitude is tracked with Goertzel.
// Typical use: the ball-pass frequency of a bearing, or 1x/2x shaft speed.
type Band struct {
	Name   string  `json:"name"`
	FreqHz float64 `json:"freq_hz"`
}

// SignalSpec describes one sensor channel of an asset class.
type SignalSpec struct {
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`           // canonical unit features are computed in
	Window       int     `json:"window"`         // number of samples per rolling window
	SampleRateHz float64 `json:"sample_rate_hz"` // needed only for bands
	Bands        []Band  `json:"bands,omitempty"`
	MinSamples   int     `json:"min_samples"` // samples required before features are emitted (0 = window)
	// Stats selects which time-domain statistics to emit. Empty means all.
	// Kurtosis and crest factor are only meaningful on waveform bursts;
	// slow process variables should stick to mean/std.
	Stats []string `json:"stats,omitempty"`
}

// AllStats is the full set of time-domain statistics.
var AllStats = []string{"mean", "std", "rms", "kurtosis", "crest"}

func (s SignalSpec) stats() []string {
	if len(s.Stats) == 0 {
		return AllStats
	}
	return s.Stats
}

func validStat(name string) bool {
	for _, s := range AllStats {
		if s == name {
			return true
		}
	}
	return false
}

// Vector is a named feature vector. Keys are "<signal>.<stat>" or
// "<signal>.band.<band>". Maps are used because templates address features
// by name; Keys returns a stable ordering for deterministic consumers.
type Vector map[string]float64

// Keys returns the feature names in sorted order.
func (v Vector) Keys() []string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Key builds the canonical feature name for a signal statistic.
func Key(signal, stat string) string { return signal + "." + stat }

// BandKey builds the canonical feature name for a band amplitude.
func BandKey(signal, band string) string { return signal + ".band." + band }

// Extractor owns one rolling window per signal of a single asset and
// produces a Vector on demand.
type Extractor struct {
	specs   map[string]SignalSpec
	windows map[string]*Window
	scratch []float64
}

// NewExtractor creates an extractor for the given signal specs.
func NewExtractor(specs []SignalSpec) (*Extractor, error) {
	e := &Extractor{specs: map[string]SignalSpec{}, windows: map[string]*Window{}}
	for _, s := range specs {
		if s.Name == "" {
			return nil, fmt.Errorf("features: signal spec with empty name")
		}
		if s.Window < 2 {
			return nil, fmt.Errorf("features: signal %q: window must be >= 2", s.Name)
		}
		if _, err := UnitDimension(s.Unit); err != nil {
			return nil, fmt.Errorf("features: signal %q: %w", s.Name, err)
		}
		if len(s.Bands) > 0 && s.SampleRateHz <= 0 {
			return nil, fmt.Errorf("features: signal %q: bands require sample_rate_hz > 0", s.Name)
		}
		for _, st := range s.Stats {
			if !validStat(st) {
				return nil, fmt.Errorf("features: signal %q: unknown stat %q", s.Name, st)
			}
		}
		if _, dup := e.specs[s.Name]; dup {
			return nil, fmt.Errorf("features: duplicate signal %q", s.Name)
		}
		e.specs[s.Name] = s
		e.windows[s.Name] = NewWindow(s.Window)
	}
	return e, nil
}

// Push converts value from unit into the signal's canonical unit and appends
// it to the signal's window. Unknown signals are ignored and reported.
func (e *Extractor) Push(signal string, value float64, unit string) (bool, error) {
	spec, ok := e.specs[signal]
	if !ok {
		return false, nil
	}
	v, err := Convert(value, unit, spec.Unit)
	if err != nil {
		return false, fmt.Errorf("signal %q: %w", signal, err)
	}
	e.windows[signal].Push(v)
	return true, nil
}

// Ready reports whether every signal has at least its minimum sample count.
func (e *Extractor) Ready() bool {
	for name, spec := range e.specs {
		min := spec.MinSamples
		if min <= 0 || min > spec.Window {
			min = spec.Window
		}
		if e.windows[name].Len() < min {
			return false
		}
	}
	return true
}

// Fill returns the fraction (0..1) of the required samples collected across
// all signals; used for cold-start reporting.
func (e *Extractor) Fill() float64 {
	var have, need int
	for name, spec := range e.specs {
		min := spec.MinSamples
		if min <= 0 || min > spec.Window {
			min = spec.Window
		}
		need += min
		if l := e.windows[name].Len(); l < min {
			have += l
		} else {
			have += min
		}
	}
	if need == 0 {
		return 1
	}
	return float64(have) / float64(need)
}

// Extract computes the feature vector from the current windows. Signals with
// no samples yet are skipped.
func (e *Extractor) Extract() Vector {
	out := make(Vector, len(e.specs)*6)
	for name, spec := range e.specs {
		w := e.windows[name]
		if w.Len() == 0 {
			continue
		}
		e.scratch = w.Values(e.scratch)
		st := Compute(e.scratch)
		for _, stat := range spec.stats() {
			switch stat {
			case "mean":
				out[Key(name, stat)] = st.Mean
			case "std":
				out[Key(name, stat)] = st.Std
			case "rms":
				out[Key(name, stat)] = st.RMS
			case "kurtosis":
				out[Key(name, stat)] = st.Kurtosis
			case "crest":
				out[Key(name, stat)] = st.Crest
			}
		}
		for _, b := range spec.Bands {
			out[BandKey(name, b.Name)] = Goertzel(e.scratch, spec.SampleRateHz, b.FreqHz)
		}
	}
	return out
}

// FeatureNames lists every feature key the extractor can produce, sorted.
func FeatureNames(specs []SignalSpec) []string {
	var names []string
	for _, s := range specs {
		for _, stat := range s.stats() {
			names = append(names, Key(s.Name, stat))
		}
		for _, b := range s.Bands {
			names = append(names, BandKey(s.Name, b.Name))
		}
	}
	sort.Strings(names)
	return names
}
