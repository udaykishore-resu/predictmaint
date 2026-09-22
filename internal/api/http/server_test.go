package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/predictmaint/internal/adapters/memory"
	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/engine"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
	"github.com/udaykishore-resu/predictmaint/internal/observability"
	"github.com/udaykishore-resu/predictmaint/internal/sim"
)

type fixture struct {
	srv   *httptest.Server
	store *memory.Store
	eng   *engine.Engine
	ready error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := memory.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := observability.NewMetrics("predictmaint_test")
	eng, err := engine.New(context.Background(), engine.Options{Store: store, CMMS: cmms.NewSimulated(), Logger: log, Observer: metrics})
	require.NoError(t, err)
	f := &fixture{store: store, eng: eng}
	s := New(Options{Engine: eng, Store: store, Logger: log, Metrics: metrics, MaxBodyBytes: 4 << 20, Version: "test",
		Readiness: []ReadinessCheck{{Name: "store", Check: store.Ping}, {Name: "flaky", Check: func(context.Context) error { return f.ready }}}})
	f.srv = httptest.NewServer(s.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) do(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			buf, err := json.Marshal(b)
			require.NoError(t, err)
			rd = bytes.NewReader(buf)
		}
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close() // body fully consumed above; close error is irrelevant in tests
	require.NoError(t, err)
	return resp, data
}

func TestHealthAndReadiness(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do(t, http.MethodGet, "/healthz", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `"version":"test"`)
	assert.NotEmpty(t, resp.Header.Get("X-Request-ID"))

	resp, _ = f.do(t, http.MethodGet, "/readyz", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	f.ready = errors.New("down")
	resp, body = f.do(t, http.MethodGet, "/readyz", nil)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, string(body), "fail: down")

	resp, body = f.do(t, http.MethodGet, "/metrics", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "predictmaint_test_http_requests_total")

	resp, _ = f.do(t, http.MethodGet, "/nope", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = f.do(t, http.MethodDelete, "/v1/alerts", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestEndToEndOverHTTP(t *testing.T) {
	f := newFixture(t)
	cfg := sim.Defaults()
	s := sim.New(cfg)

	// Both accepted body shapes.
	resp, body := f.do(t, http.MethodPost, "/v1/metrics", s.Next())
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(body))
	resp, body = f.do(t, http.MethodPost, "/v1/metrics", map[string]any{"metrics": s.Next()})
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(body))

	var alerts struct {
		Alerts []alerting.Alert `json:"alerts"`
		Count  int              `json:"count"`
	}
	for i := 2; i < 200 && len(alerts.Alerts) == 0; i++ {
		resp, body = f.do(t, http.MethodPost, "/v1/metrics", s.Next())
		require.Equal(t, http.StatusAccepted, resp.StatusCode, string(body))
		_, body = f.do(t, http.MethodGet, "/v1/alerts?site=plant-a&status=open", nil)
		require.NoError(t, json.Unmarshal(body, &alerts))
	}
	require.Len(t, alerts.Alerts, 1)
	alert := alerts.Alerts[0]
	assert.Equal(t, "pump-003", alert.AssetID)

	resp, body = f.do(t, http.MethodGet, "/v1/assets/pump-003/health", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var h engine.Health
	require.NoError(t, json.Unmarshal(body, &h))
	assert.Greater(t, h.Risk, 0.6)
	assert.Equal(t, alerting.StateActive, h.State)
	resp, _ = f.do(t, http.MethodGet, "/v1/assets/ghost/health", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, body = f.do(t, http.MethodGet, "/v1/assets?site=plant-a", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `"asset_id":"pump-003"`)

	resp, _ = f.do(t, http.MethodGet, "/v1/alerts/"+alert.ID, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = f.do(t, http.MethodGet, "/v1/alerts/nope", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = f.do(t, http.MethodGet, "/v1/alerts?status=weird", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, body = f.do(t, http.MethodGet, "/v1/workorders?asset_id=pump-003&status=open&limit=abc", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var wos struct {
		WorkOrders []cmms.WorkOrder `json:"work_orders"`
	}
	require.NoError(t, json.Unmarshal(body, &wos))
	require.Len(t, wos.WorkOrders, 1)
	assert.Equal(t, alert.WorkOrderID, wos.WorkOrders[0].ID)
	resp, _ = f.do(t, http.MethodGet, "/v1/workorders/"+wos.WorkOrders[0].ID, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = f.do(t, http.MethodGet, "/v1/workorders/nope", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = f.do(t, http.MethodGet, "/v1/workorders?status=weird", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Feedback.
	resp, _ = f.do(t, http.MethodPost, "/v1/alerts/"+alert.ID+"/feedback", map[string]string{"verdict": "maybe"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = f.do(t, http.MethodPost, "/v1/alerts/"+alert.ID+"/feedback", `{"verdict":"confirm","bogus":1}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "unknown fields rejected")
	resp, _ = f.do(t, http.MethodPost, "/v1/alerts/"+alert.ID+"/feedback", map[string]string{"verdict": "confirm", "technician": strings.Repeat("x", 200)})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, body = f.do(t, http.MethodPost, "/v1/alerts/"+alert.ID+"/feedback", map[string]string{"verdict": "confirm", "technician": "r.okafor", "note": "outer race spalling confirmed"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var judged alerting.Alert
	require.NoError(t, json.Unmarshal(body, &judged))
	assert.Equal(t, alerting.StatusConfirmed, judged.Status)
	require.NotNil(t, judged.Adaptation)
	assert.Less(t, judged.Adaptation.ThresholdAfter, judged.Adaptation.ThresholdBefore)
	resp, _ = f.do(t, http.MethodPost, "/v1/alerts/"+alert.ID+"/feedback", map[string]string{"verdict": "dismiss"})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp, _ = f.do(t, http.MethodPost, "/v1/alerts/nope/feedback", map[string]string{"verdict": "dismiss"})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Stats.
	resp, body = f.do(t, http.MethodGet, "/v1/stats?site=plant-a", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var st alerting.Stats
	require.NoError(t, json.Unmarshal(body, &st))
	assert.Equal(t, 1, st.Confirmed)
	assert.Equal(t, 1.0, st.PrecisionProxy)
	_, body = f.do(t, http.MethodGet, "/metrics", nil)
	assert.Contains(t, string(body), `predictmaint_test_precision_proxy{site="plant-a"} 1`)
	assert.Contains(t, string(body), `predictmaint_test_alert_outcomes_total{class="pump",outcome="fire",site="plant-a"} 1`)
	assert.Contains(t, string(body), `route="GET /v1/assets/{id}/health"`, "route label is the mux pattern, not the raw path")
	assert.NotContains(t, string(body), `route="unmatched",method="GET",status="2xx"`)
}

func TestIngestValidation(t *testing.T) {
	f := newFixture(t)
	resp, _ := f.do(t, http.MethodPost, "/v1/metrics", `{bad json`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = f.do(t, http.MethodPost, "/v1/metrics", `[{"asset_id": 5}]`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = f.do(t, http.MethodPost, "/v1/metrics", `{"metrics": "x"}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = f.do(t, http.MethodPost, "/v1/metrics", `[]`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, body := f.do(t, http.MethodPost, "/v1/metrics", `[{"asset_id":"x","signal":"vibration","ts":"2026-01-01T00:00:00Z","value":1}]`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "all rejected -> 400")
	assert.Contains(t, string(body), "asset_class is required")

	// Partial acceptance is 202 with the per-metric errors listed.
	resp, body = f.do(t, http.MethodPost, "/v1/metrics", `[
		{"asset_id":"p9","asset_class":"pump","signal":"bearing_temp","unit":"C","ts":"2026-01-01T00:00:00Z","value":60},
		{"asset_id":"p9","signal":"bearing_temp","unit":"psi","ts":"2026-01-01T00:01:00Z","value":60}]`)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	var res engine.IngestResult
	require.NoError(t, json.Unmarshal(body, &res))
	assert.Equal(t, 1, res.Accepted)
	assert.Equal(t, 1, res.Rejected)

	// Oversized body.
	big := `[` + strings.Repeat(`{"asset_id":"p9","signal":"bearing_temp","unit":"C","ts":"2026-01-01T00:00:00Z","value":60},`, 60000) + `]`
	resp, _ = f.do(t, http.MethodPost, "/v1/metrics", big)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestTemplatesOverHTTP(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do(t, http.MethodGet, "/v1/templates", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `"class":"compressor"`)

	resp, body = f.do(t, http.MethodGet, "/v1/templates/pump", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var tpl template.Template
	require.NoError(t, json.Unmarshal(body, &tpl))
	resp, _ = f.do(t, http.MethodGet, "/v1/templates/turbine", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	tpl.Version = "2026.2"
	tpl.Alerting.OnThreshold = 0.65
	resp, body = f.do(t, http.MethodPut, "/v1/templates/pump", tpl)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Contains(t, string(body), `"version":"2026.2"`)

	resp, _ = f.do(t, http.MethodPut, "/v1/templates/motor", tpl)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "class mismatch")

	tpl.Class = ""
	tpl.Alerting.OnThreshold = 5
	resp, _ = f.do(t, http.MethodPut, "/v1/templates/pump", tpl)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	resp, _ = f.do(t, http.MethodPut, "/v1/templates/pump", `{"class":"pump","unknown_field":1}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestRecoverMiddleware(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(Options{Logger: log})
	h := s.withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "internal")
}

func TestClampLimit(t *testing.T) {
	assert.Equal(t, 100, clampLimit(""))
	assert.Equal(t, 100, clampLimit("abc"))
	assert.Equal(t, 100, clampLimit("0"))
	assert.Equal(t, 7, clampLimit("7"))
	assert.Equal(t, 1000, clampLimit("99999999999"))
}

func TestListenAndServeShutsDownCleanly(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ListenAndServe(ctx, "127.0.0.1:18899", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), time.Second, time.Second, 2*time.Second, log)
	}()
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://127.0.0.1:18899/")
		if err == nil {
			_ = resp.Body.Close() // probe only; body content and close error are irrelevant
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err)
	assert.Equal(t, 204, resp.StatusCode)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	// An unusable address surfaces as an error instead of hanging.
	err = ListenAndServe(context.Background(), "256.256.256.256:1", http.NotFoundHandler(), time.Second, time.Second, time.Second, log)
	assert.Error(t, err)
}
