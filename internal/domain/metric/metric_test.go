package metric

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	ts := time.Now()
	good := Metric{AssetID: " pump-001 ", Signal: "vibration", Unit: "mm/s", Value: 1, Timestamp: ts, AssetClass: "Pump"}
	require.NoError(t, good.Validate())
	assert.Equal(t, "pump-001", good.AssetID)
	assert.Equal(t, DefaultSite, good.Site)
	assert.Equal(t, "pump", good.AssetClass)
	assert.Equal(t, []float64{1}, good.Values())

	burst := Metric{AssetID: "a", Signal: "s", Timestamp: ts, Samples: []float64{1, 2}}
	require.NoError(t, burst.Validate())
	assert.Equal(t, []float64{1, 2}, burst.Values())

	cases := []struct {
		name string
		m    Metric
		err  error
	}{
		{"no asset", Metric{Signal: "s", Timestamp: ts}, ErrNoAsset},
		{"bad asset", Metric{AssetID: "a b", Signal: "s", Timestamp: ts}, ErrBadAssetName},
		{"no signal", Metric{AssetID: "a", Timestamp: ts}, ErrNoSignal},
		{"bad site", Metric{AssetID: "a", Signal: "s", Site: "x y", Timestamp: ts}, ErrBadSite},
		{"no ts", Metric{AssetID: "a", Signal: "s"}, ErrNoTimestamp},
		{"nan", Metric{AssetID: "a", Signal: "s", Timestamp: ts, Value: math.NaN()}, ErrNonFinite},
		{"inf sample", Metric{AssetID: "a", Signal: "s", Timestamp: ts, Samples: []float64{1, math.Inf(1)}}, ErrNonFinite},
		{"too many", Metric{AssetID: "a", Signal: "s", Timestamp: ts, Samples: make([]float64, MaxSamples+1)}, ErrTooMany},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.m
			assert.ErrorIs(t, m.Validate(), tc.err)
		})
	}
	long := Metric{AssetID: string(make([]byte, 129)), Signal: "s", Timestamp: ts}
	assert.Error(t, long.Validate())
}
