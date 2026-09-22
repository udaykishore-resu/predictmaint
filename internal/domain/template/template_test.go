package template

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultsValidate(t *testing.T) {
	defs := Defaults()
	require.Len(t, defs, 3)
	for _, d := range defs {
		t.Run(d.Class, func(t *testing.T) {
			require.NoError(t, d.Validate())
			assert.NotEmpty(t, d.FeatureNames())
			// Round-trips through JSON (the API representation) unchanged.
			b, err := json.Marshal(d)
			require.NoError(t, err)
			var back Template
			require.NoError(t, json.Unmarshal(b, &back))
			require.NoError(t, back.Validate())
			assert.Equal(t, d.Alerting.Suppression, back.Alerting.Suppression)
			assert.Equal(t, d.WorkOrder.LeadTime, back.WorkOrder.LeadTime)
		})
	}
	_, ok := DefaultFor(ClassPump)
	assert.True(t, ok)
	_, ok = DefaultFor("turbine")
	assert.False(t, ok)
}

func TestValidateRejectsBrokenTemplates(t *testing.T) {
	mut := map[string]func(*Template){
		"no class":           func(x *Template) { x.Class = " " },
		"no version":         func(x *Template) { x.Version = "" },
		"no signals":         func(x *Template) { x.Signals = nil },
		"bad signal":         func(x *Template) { x.Signals[0].Window = 1 },
		"runtime is feature": func(x *Template) { x.RuntimeSignal = "vibration" },
		"cusum missing":      func(x *Template) { x.Detectors.CUSUM.Feature = "" },
		"cusum unknown":      func(x *Template) { x.Detectors.CUSUM.Feature = "nope.rms" },
		"direction unknown":  func(x *Template) { x.Detectors.RobustZ.Direction["x.y"] = "up" },
		"baseline unknown":   func(x *Template) { x.ClassBaselines["x.y"] = x.ClassBaselines["vibration.rms"] },
		"z threshold":        func(x *Template) { x.Detectors.RobustZ.Threshold = 0 },
		"hst negative":       func(x *Template) { x.Detectors.HST.Depth = -1 },
		"prior":              func(x *Template) { x.Prior.ScaleHours = 0 },
		"fusion negative":    func(x *Template) { x.Fusion.WDrift = -1 },
		"warmup":             func(x *Template) { x.WarmupEvaluations = 1 },
		"alerting":           func(x *Template) { x.Alerting.OnThreshold = 0 },
		"work order":         func(x *Template) { x.WorkOrder.Priority = 0 },
	}
	for name, m := range mut {
		t.Run(name, func(t *testing.T) {
			x, _ := DefaultFor(ClassPump)
			// Deep-copy the maps that the mutation may touch.
			dir := map[string]string{}
			for k, v := range x.Detectors.RobustZ.Direction {
				dir[k] = v
			}
			x.Detectors.RobustZ.Direction = dir
			m(&x)
			assert.Error(t, x.Validate())
		})
	}
}
