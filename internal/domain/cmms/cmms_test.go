package cmms

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func policy() Policy {
	return Policy{
		ConfidenceThreshold: 0.7,
		FailureMode:         "bearing_wear",
		FailureModeBySignal: map[string]string{"motor_temp": "overheating"},
		Priority:            2,
		LeadTime:            Duration{72 * time.Hour},
	}
}

func TestPolicyValidate(t *testing.T) {
	require.NoError(t, policy().Validate())
	for i, m := range []func(*Policy){
		func(p *Policy) { p.ConfidenceThreshold = 1.1 },
		func(p *Policy) { p.ConfidenceThreshold = -0.1 },
		func(p *Policy) { p.FailureMode = "" },
		func(p *Policy) { p.Priority = 0 },
		func(p *Policy) { p.Priority = 6 },
		func(p *Policy) { p.LeadTime = Duration{-1} },
	} {
		p := policy()
		m(&p)
		assert.Error(t, p.Validate(), "case %d", i)
	}
}

func TestResolveFailureMode(t *testing.T) {
	p := policy()
	assert.Equal(t, "overheating", p.ResolveFailureMode("motor_temp.mean"))
	assert.Equal(t, "bearing_wear", p.ResolveFailureMode("vibration.kurtosis"))
	assert.Equal(t, "bearing_wear", p.ResolveFailureMode("nodot"))
	assert.Equal(t, "bearing_wear", p.ResolveFailureMode(""))
}

func TestDecide(t *testing.T) {
	p := policy()
	tests := []struct {
		name       string
		conf       float64
		open       *WorkOrder
		wantCreate bool
		contains   string
	}{
		{"below threshold", 0.5, nil, false, "below threshold"},
		{"at threshold no open", 0.7, nil, true, "no open work order"},
		{"open duplicate", 0.9, &WorkOrder{ID: "wo-1", Status: StatusOpen, AssetID: "p1", FailureMode: "bearing_wear"}, false, "already covers"},
		{"closed one does not block", 0.9, &WorkOrder{ID: "wo-1", Status: StatusClosed}, true, "no open work order"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := p.Decide(tc.conf, "vibration.rms", tc.open)
			assert.Equal(t, tc.wantCreate, d.Create)
			assert.Contains(t, d.Reason, tc.contains)
			assert.Equal(t, RuleWorkOrder, d.Rule)
			assert.Equal(t, "bearing_wear", d.FailureMode)
		})
	}
	assert.Equal(t, "p1|bearing_wear|a1", Key("p1", "bearing_wear", "a1"))
}

func TestDurationJSON(t *testing.T) {
	var p Policy
	require.NoError(t, json.Unmarshal([]byte(`{"lead_time":"48h"}`), &p))
	assert.Equal(t, 48*time.Hour, p.LeadTime.Duration)
	require.NoError(t, json.Unmarshal([]byte(`{"lead_time":5}`), &p))
	assert.Equal(t, time.Duration(5), p.LeadTime.Duration)
	assert.Error(t, json.Unmarshal([]byte(`{"lead_time":"x"}`), &p))
	assert.Error(t, json.Unmarshal([]byte(`{"lead_time":{}}`), &p))
	b, _ := json.Marshal(Duration{time.Hour})
	assert.Equal(t, `"1h0m0s"`, string(b))
}

func sampleWO() WorkOrder {
	created := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	return WorkOrder{
		ID: "wo-1", AlertID: "al-1", AssetID: "pump-001", Site: "plant-a", AssetClass: "pump",
		FailureMode: "bearing_wear", Priority: 2, Confidence: 0.82, Risk: 0.79,
		Description: strings.Repeat("Bearing wear suspected on pump-001. ", 5),
		Status:      StatusOpen, CreatedAt: created, RequiredBy: created.Add(72 * time.Hour),
		IdempotencyKey: Key("pump-001", "bearing_wear", "al-1"), Rule: RuleWorkOrder,
	}
}

// Golden test: the vendor payload shapes are a contract with the integration
// team, so they are pinned byte-for-byte.
func TestMappersGolden(t *testing.T) {
	wo := sampleWO()
	mx, err := json.Marshal(ToMaximo(wo))
	require.NoError(t, err)
	wantMaximo := `{"spi:description":"Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. Bearing wear suspected on pu","spi:description_longdescription":"Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. ","spi:assetnum":"pump-001","spi:siteid":"plant-a","spi:worktype":"PM","spi:wopriority":2,"spi:status":"WAPPR","spi:failurecode":"bearing_wear","spi:problemcode":"PDM","spi:targstartdate":"2026-03-01T10:00:00Z","spi:targcompdate":"2026-03-04T10:00:00Z","spi:reportdate":"2026-03-01T10:00:00Z","spi:reportedby":"PREDICTMAINT","spi:externalrefid":"pump-001|bearing_wear|al-1"}`
	assert.JSONEq(t, wantMaximo, string(mx))

	sap, err := json.Marshal(ToSAPPM(wo))
	require.NoError(t, err)
	wantSAP := `{"MaintenanceOrderType":"PM01","MaintenanceOrderDesc":"Bearing wear suspected on pump-001. Bear","MaintenancePlanningPlant":"plant-a","Equipment":"pump-001","MaintPriority":"2","MaintenanceActivityType":"003","BasicSchedulingType":"1","MaintOrdBasicStartDate":"2026-03-01","MaintOrdBasicEndDate":"2026-03-04","ExternalReference":"pump-001|bearing_wear|al-1","LongText":"Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. Bearing wear suspected on pump-001. "}`
	assert.JSONEq(t, wantSAP, string(sap))

	for p, want := range map[int]string{0: "1", 1: "1", 2: "2", 3: "3", 4: "4", 5: "4"} {
		assert.Equal(t, want, sapPriority(p))
	}
}

func TestSimulatedIdempotent(t *testing.T) {
	s := NewSimulated()
	assert.Equal(t, "simulated", s.Name())
	ctx := context.Background()
	wo := sampleWO()

	ref1, err := s.CreateWorkOrder(ctx, wo)
	require.NoError(t, err)
	ref2, err := s.CreateWorkOrder(ctx, wo)
	require.NoError(t, err)
	assert.Equal(t, ref1, ref2, "same idempotency key -> same external ref")
	assert.Equal(t, "WO-000001", ref1.ID)

	wo2 := wo
	wo2.IdempotencyKey = "other"
	ref3, err := s.CreateWorkOrder(ctx, wo2)
	require.NoError(t, err)
	assert.Equal(t, "WO-000002", ref3.ID)

	_, err = s.CreateWorkOrder(ctx, WorkOrder{})
	assert.Error(t, err)

	require.NoError(t, s.CancelWorkOrder(ctx, ref1, "dismissed"))
	assert.Error(t, s.CancelWorkOrder(ctx, ExternalRef{ID: "nope"}, ""))
	recs := s.Records()
	require.Len(t, recs, 2)
	assert.True(t, recs[0].Cancelled)
	assert.Equal(t, "dismissed", recs[0].Reason)
	assert.Equal(t, "pump-001", recs[0].Maximo.AssetNum)
	assert.False(t, recs[1].Cancelled)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.CreateWorkOrder(cancelled, wo)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, s.CancelWorkOrder(cancelled, ref1, ""), context.Canceled)
}
