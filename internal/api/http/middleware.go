package http

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/udaykishore-resu/predictmaint/internal/observability"
)

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// withObservability assigns a request id, opens a span, logs the request
// and records RED metrics. The route label is the matched mux pattern, so
// path parameters never explode cardinality.
func (s *Server) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := observability.WithRequestID(r.Context(), reqID)

		var span trace.Span
		if s.tracer != nil {
			ctx = propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(r.Header))
			ctx, span = s.tracer.Start(ctx, "http "+r.Method, trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attribute.String("http.method", r.Method), attribute.String("http.target", r.URL.Path)))
			defer span.End()
		}
		if s.metrics != nil {
			s.metrics.InFlight(1)
			defer s.metrics.InFlight(-1)
		}

		sw := &statusWriter{ResponseWriter: w}
		// The mux records the matched pattern on the request it is handed, so
		// keep a handle on that copy rather than the original.
		r = r.WithContext(ctx)
		next.ServeHTTP(sw, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		dur := time.Since(start)
		if s.metrics != nil && route != "GET /metrics" {
			s.metrics.ObserveHTTP(route, r.Method, status, dur)
		}
		if span != nil {
			span.SetName(route)
			span.SetAttributes(attribute.Int("http.status_code", status))
			if status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
		}
		if route != "GET /metrics" && route != "GET /healthz" && route != "GET /readyz" {
			s.log.InfoContext(ctx, "http request", "route", route, "path", r.URL.Path, "status", status,
				"bytes", sw.bytes, "duration_ms", float64(dur.Microseconds())/1000, "remote", r.RemoteAddr)
		}
	})
}

// withRecover turns panics into 500s with a logged stack, never a crashed
// process.
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.ErrorContext(r.Context(), "panic recovered", "panic", rec, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
