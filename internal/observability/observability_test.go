package observability

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/detect"
)

func TestLoggerEnrichment(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "json", "debug", "svc", "test")
	ctx := WithRequestID(context.Background(), "req-1")
	log.InfoContext(ctx, "hello", "k", "v")
	out := buf.String()
	assert.Contains(t, out, `"request_id":"req-1"`)
	assert.Contains(t, out, `"service":"svc"`)
	assert.Contains(t, out, `"k":"v"`)
	assert.Equal(t, "req-1", RequestID(ctx))
	assert.Equal(t, "", RequestID(context.Background()))

	buf.Reset()
	text := NewLogger(&buf, "text", "warn", "svc", "test")
	text.Info("dropped")
	text.WithGroup("g").With("a", 1).Warn("kept")
	assert.NotContains(t, buf.String(), "dropped")
	assert.Contains(t, buf.String(), "kept")

	// nil writer defaults to stdout without panicking; error level parses.
	assert.NotNil(t, NewLogger(nil, "json", "error", "svc", "test"))
}

func TestMetricsExposition(t *testing.T) {
	m := NewMetrics("t")
	m.ObserveHTTP("GET /x", "GET", 200, 5*time.Millisecond)
	m.InFlight(1)
	m.InFlight(-1)
	m.KafkaBatch("ok")
	m.MetricsIngested(3, 1, 2)
	m.Evaluated("pump", detect.Assessment{Risk: 0.7})
	m.AlertOutcome("s", "pump", alerting.OutcomeFire)
	m.WorkOrder("s", "pump", "created")
	m.Feedback("pump", alerting.VerdictConfirm)
	m.SetStats(alerting.Stats{PrecisionProxy: 0.5, AlertsPerDay: 2, MTTASeconds: 30})
	m.SetAssetsTracked(5)
	assert.NotNil(t, m.Registry())

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`t_http_requests_total{method="GET",route="GET /x",status="2xx"} 1`,
		`t_metrics_ingested_total{result="accepted"} 3`,
		`t_metrics_ingested_total{result="duplicate"} 2`,
		`t_alert_outcomes_total{class="pump",outcome="fire",site="s"} 1`,
		`t_work_orders_total{class="pump",result="created",site="s"} 1`,
		`t_feedback_total{class="pump",verdict="confirm"} 1`,
		`t_precision_proxy{site="all"} 0.5`,
		`t_assets_tracked 5`,
		`t_kafka_batches_total{result="ok"} 1`,
	} {
		assert.Contains(t, body, want)
	}
}

func TestSetupTracingNoop(t *testing.T) {
	tr, shutdown, err := SetupTracing(context.Background(), TracingConfig{ServiceName: "svc"})
	require.NoError(t, err)
	require.NotNil(t, tr)
	_, span := tr.Start(context.Background(), "x")
	span.End()
	assert.NoError(t, shutdown(context.Background()))

	// Exporter construction with an endpoint succeeds without connecting.
	tr, shutdown, err = SetupTracing(context.Background(), TracingConfig{ServiceName: "svc", Endpoint: "127.0.0.1:1", Insecure: true, Ratio: 0.5})
	require.NoError(t, err)
	_, span = tr.Start(context.Background(), "y")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx) // export to a dead endpoint fails; that is expected here
}
