package kafka

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestDecode(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := []*kgo.Record{
		{Topic: "uns.metrics", Partition: 1, Offset: 10, Key: []byte("pump-001"), Timestamp: ts,
			Value: []byte(`{"signal":"bearing_temp","unit":"C","value":61.2}`)},
		{Value: []byte(`[{"asset_id":"pump-002","signal":"vibration","unit":"mm/s","samples":[1,2,3],"ts":"2026-01-01T00:00:01Z","source":"gw-1"}]`)},
		{Value: []byte(`not json`)},
		{Value: []byte(`[{"asset_id": 3}]`)},
		{Value: nil},
	}
	ms, skipped := Decode(recs)
	assert.Equal(t, 3, skipped)
	require.Len(t, ms, 2)
	assert.Equal(t, "pump-001", ms[0].AssetID, "key used as asset id")
	assert.Equal(t, ts, ms[0].Timestamp, "record timestamp used when payload has none")
	assert.Equal(t, "kafka:uns.metrics/1@10", ms[0].Source)
	assert.Equal(t, "pump-002", ms[1].AssetID)
	assert.Equal(t, "gw-1", ms[1].Source)
	assert.Equal(t, []float64{1, 2, 3}, ms[1].Samples)
}

func TestNewValidation(t *testing.T) {
	_, err := New(Config{}, nil, nil)
	assert.Error(t, err)
	_, err = New(Config{Brokers: []string{"localhost:1"}}, nil, nil)
	assert.Error(t, err)
	c, err := New(Config{Brokers: []string{"localhost:1"}, Topic: "t", GroupID: "g"}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1000, c.cfg.MaxBatch)
	assert.Equal(t, time.Second, c.cfg.PollTimeout)
	c.Close()
}
