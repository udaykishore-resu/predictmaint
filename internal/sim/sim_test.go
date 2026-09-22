package sim

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/predictmaint/internal/domain/features"
)

func TestSimulatorTrajectory(t *testing.T) {
	cfg := Defaults()
	cfg.Pumps, cfg.Degrading, cfg.DegradeFrom, cfg.RampSteps = 2, []int{1}, 5, 10
	s := New(cfg)
	assert.Equal(t, "pump-001", s.AssetID(0))
	assert.Equal(t, cfg.Start, s.Time())

	batch := s.Next()
	require.Len(t, batch, 2*5)
	for _, m := range batch {
		require.NoError(t, m.Validate())
		assert.Equal(t, cfg.Start, m.Timestamp)
	}
	assert.Equal(t, 1, s.Step())
	assert.Equal(t, cfg.Start.Add(cfg.StepInterval), s.Time())

	// Healthy pump never degrades; bad pump ramps to 1 and saturates.
	for i := 1; i < 20; i++ {
		s.Next()
	}
	assert.Equal(t, 0.0, s.Severity(0))
	assert.Equal(t, 1.0, s.Severity(1))
	assert.Equal(t, 0.0, s.severityAt(1, 4))
	assert.InDelta(t, 0.5, s.severityAt(1, 10), 1e-9)

	// Waveform physics: degraded burst has higher RMS, kurtosis and BPFO line.
	healthy := s.burst(0)
	faulty := s.burst(1)
	hs, fs := features.Compute(healthy), features.Compute(faulty)
	assert.Greater(t, fs.RMS, hs.RMS)
	assert.Greater(t, fs.Kurtosis, hs.Kurtosis)
	assert.InDelta(t, 1.0, features.Goertzel(healthy, SampleRateHz, ShaftHz), 0.25, "1x line present when healthy")
	assert.Less(t, features.Goertzel(healthy, SampleRateHz, BPFOHz), 0.3)
	assert.Greater(t, features.Goertzel(faulty, SampleRateHz, BPFOHz), 1.0)
}

func TestNewFillsDefaults(t *testing.T) {
	s := New(Config{})
	assert.Equal(t, Defaults().Pumps, s.cfg.Pumps)
	assert.Equal(t, Defaults().Site, s.cfg.Site)
	assert.Equal(t, Defaults().Seed, s.cfg.Seed)
	assert.Equal(t, Defaults().InitialRuntimeHours, s.cfg.InitialRuntimeHours)
	assert.Equal(t, 0.0, s.Severity(0), "no degrading pumps unless configured")
}
