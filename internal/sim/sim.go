// Package sim generates a realistic fleet trajectory: several identical
// pumps, one (or more) of which develops an outer-race bearing defect. It is
// used by the engine's end-to-end test and by cmd/simulator for the demo, so
// what the README shows is exactly what the tests assert.
package sim

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/udaykishore-resu/predictmaint/internal/domain/metric"
)

// Config describes the fleet and the failure scenario.
type Config struct {
	Site         string
	Pumps        int
	AssetPrefix  string
	Start        time.Time
	StepInterval time.Duration
	// Degrading lists pump indices (0-based) that develop a bearing fault.
	Degrading []int
	// DegradeFrom is the step index at which degradation starts; RampSteps
	// is how many steps it takes to reach full severity.
	DegradeFrom int
	RampSteps   int
	Seed        int64
	// InitialRuntimeHours seeds the Weibull age reported via runtime_hours.
	InitialRuntimeHours float64
}

// Defaults returns the scenario used by the README demo.
func Defaults() Config {
	return Config{
		Site:                "plant-a",
		Pumps:               5,
		AssetPrefix:         "pump-",
		Start:               time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC),
		StepInterval:        5 * time.Minute,
		Degrading:           []int{2},
		DegradeFrom:         60,
		RampSteps:           80,
		Seed:                2026,
		InitialRuntimeHours: 8000,
	}
}

// Waveform parameters shared with the pump template's band definitions.
const (
	SampleRateHz = 1000.0
	BurstSamples = 256
	ShaftHz      = 23.4375  // 1x, exactly bin 6 of a 256-point window
	BPFOHz       = 101.5625 // outer-race ball pass frequency, exactly bin 26
)

// Simulator produces one fleet snapshot per Step call.
type Simulator struct {
	cfg  Config
	rng  *rand.Rand
	step int
}

// New creates a simulator. Zero-valued config fields fall back to Defaults.
func New(cfg Config) *Simulator {
	d := Defaults()
	if cfg.Site == "" {
		cfg.Site = d.Site
	}
	if cfg.Pumps <= 0 {
		cfg.Pumps = d.Pumps
	}
	if cfg.AssetPrefix == "" {
		cfg.AssetPrefix = d.AssetPrefix
	}
	if cfg.Start.IsZero() {
		cfg.Start = d.Start
	}
	if cfg.StepInterval <= 0 {
		cfg.StepInterval = d.StepInterval
	}
	if cfg.RampSteps <= 0 {
		cfg.RampSteps = d.RampSteps
	}
	if cfg.Seed == 0 {
		cfg.Seed = d.Seed
	}
	if cfg.InitialRuntimeHours <= 0 {
		cfg.InitialRuntimeHours = d.InitialRuntimeHours
	}
	return &Simulator{cfg: cfg, rng: rand.New(rand.NewSource(cfg.Seed))}
}

// AssetID returns the id of pump i.
func (s *Simulator) AssetID(i int) string { return fmt.Sprintf("%s%03d", s.cfg.AssetPrefix, i+1) }

// Step returns the current step index (number of snapshots produced).
func (s *Simulator) Step() int { return s.step }

// Time returns the event time of the next snapshot.
func (s *Simulator) Time() time.Time {
	return s.cfg.Start.Add(time.Duration(s.step) * s.cfg.StepInterval)
}

// Severity returns the degradation level of pump i at the current step, in
// [0,1]; 0 for healthy pumps.
func (s *Simulator) Severity(i int) float64 {
	return s.severityAt(i, s.step)
}

func (s *Simulator) severityAt(i, step int) float64 {
	degrading := false
	for _, d := range s.cfg.Degrading {
		if d == i {
			degrading = true
			break
		}
	}
	if !degrading || step < s.cfg.DegradeFrom {
		return 0
	}
	sev := float64(step-s.cfg.DegradeFrom) / float64(s.cfg.RampSteps)
	return math.Min(1, sev)
}

// Next produces the metrics for all pumps at the next step. Each pump emits
// a 256-sample vibration burst plus bearing temperature, discharge pressure,
// motor current and runtime hours.
func (s *Simulator) Next() []metric.Metric {
	ts := s.Time()
	out := make([]metric.Metric, 0, s.cfg.Pumps*5)
	for i := 0; i < s.cfg.Pumps; i++ {
		sev := s.Severity(i)
		id := s.AssetID(i)
		base := metric.Metric{AssetID: id, Site: s.cfg.Site, AssetClass: "pump", Timestamp: ts, Source: "simulator"}

		vib := base
		vib.Signal, vib.Unit = "vibration", "mm/s"
		vib.Samples = s.burst(sev)
		out = append(out, vib)

		temp := base
		temp.Signal, temp.Unit = "bearing_temp", "C"
		temp.Value = 58 + 25*sev*sev + s.rng.NormFloat64()*0.4
		out = append(out, temp)

		press := base
		press.Signal, press.Unit = "discharge_pressure", "bar"
		press.Value = 6.2 + s.rng.NormFloat64()*0.08
		out = append(out, press)

		cur := base
		cur.Signal, cur.Unit = "motor_current", "A"
		cur.Value = 42 + 4*sev + s.rng.NormFloat64()*0.6
		out = append(out, cur)

		rt := base
		rt.Signal, rt.Unit = "runtime_hours", "h"
		rt.Value = s.cfg.InitialRuntimeHours + float64(i)*500 + float64(s.step)*s.cfg.StepInterval.Hours()
		out = append(out, rt)
	}
	s.step++
	return out
}

// burst synthesises a vibration snapshot. Healthy: 1x shaft component plus
// broadband noise and a faint BPFO line. As severity rises the BPFO line
// grows, noise widens and impulsive spikes (rolling-element impacts) appear,
// which is what drives kurtosis and crest factor up.
func (s *Simulator) burst(sev float64) []float64 {
	xs := make([]float64, BurstSamples)
	bpfoAmp := 0.05 + 1.5*sev
	sigma := 0.3 + 0.4*sev
	impulseP := 0.12 * sev
	impulseAmp := 3 + 5*sev
	phase1 := s.rng.Float64() * 2 * math.Pi
	phase2 := s.rng.Float64() * 2 * math.Pi
	for n := range xs {
		t := float64(n) / SampleRateHz
		v := 1.0*math.Sin(2*math.Pi*ShaftHz*t+phase1) +
			bpfoAmp*math.Sin(2*math.Pi*BPFOHz*t+phase2) +
			sigma*s.rng.NormFloat64()
		if impulseP > 0 && s.rng.Float64() < impulseP {
			sign := 1.0
			if s.rng.Intn(2) == 0 {
				sign = -1
			}
			v += sign * impulseAmp * (0.5 + s.rng.Float64())
		}
		xs[n] = v
	}
	return xs
}
