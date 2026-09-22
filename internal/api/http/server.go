// Package http exposes the engine over a small JSON API on net/http with
// Go 1.22+ ServeMux patterns. Handlers translate; they never decide.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/udaykishore-resu/predictmaint/internal/domain/engine"
	"github.com/udaykishore-resu/predictmaint/internal/observability"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
)

// ReadinessCheck reports whether a dependency is usable.
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// Options configure the server.
type Options struct {
	Engine       *engine.Engine
	Store        ports.Store
	Logger       *slog.Logger
	Metrics      *observability.Metrics
	Tracer       trace.Tracer
	MaxBodyBytes int64
	Readiness    []ReadinessCheck
	Version      string
}

// Server is the HTTP API.
type Server struct {
	eng     *engine.Engine
	store   ports.Store
	log     *slog.Logger
	metrics *observability.Metrics
	tracer  trace.Tracer
	maxBody int64
	ready   []ReadinessCheck
	version string
	mux     *http.ServeMux
}

// New builds the server and its routes.
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 16 << 20
	}
	s := &Server{eng: o.Engine, store: o.Store, log: o.Logger, metrics: o.Metrics, tracer: o.Tracer,
		maxBody: o.MaxBodyBytes, ready: o.Readiness, version: o.Version, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", s.handleHealthz)
	m.HandleFunc("GET /readyz", s.handleReadyz)
	if s.metrics != nil {
		m.Handle("GET /metrics", s.metrics.Handler())
	}
	m.HandleFunc("POST /v1/metrics", s.handleIngest)
	m.HandleFunc("GET /v1/assets", s.handleListAssets)
	m.HandleFunc("GET /v1/assets/{id}/health", s.handleAssetHealth)
	m.HandleFunc("GET /v1/alerts", s.handleListAlerts)
	m.HandleFunc("GET /v1/alerts/{id}", s.handleGetAlert)
	m.HandleFunc("POST /v1/alerts/{id}/feedback", s.handleFeedback)
	m.HandleFunc("GET /v1/workorders", s.handleListWorkOrders)
	m.HandleFunc("GET /v1/workorders/{id}", s.handleGetWorkOrder)
	m.HandleFunc("GET /v1/templates", s.handleListTemplates)
	m.HandleFunc("GET /v1/templates/{class}", s.handleGetTemplate)
	m.HandleFunc("PUT /v1/templates/{class}", s.handlePutTemplate)
	m.HandleFunc("GET /v1/stats", s.handleStats)
}

// Handler returns the fully wrapped http.Handler.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = s.withObservability(h)
	h = s.withRecover(h)
	return h
}

// --- response helpers ---

type errorBody struct {
	Error   string   `json:"error"`
	Code    string   `json:"code"`
	Details []string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: msg, Code: code})
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds limit")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) domainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ports.ErrNotFound), errors.Is(err, engine.ErrUnknownAsset):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, engine.ErrAlreadyJudged):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, engine.ErrInvalidTemplate):
		writeError(w, http.StatusUnprocessableEntity, "invalid_template", err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "timeout", "request cancelled or timed out")
	default:
		s.log.Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// defaultLimit is the page size used when a list request omits or
// mis-specifies ?limit; maxLimit caps what a caller may ask for.
const (
	defaultLimit = 100
	maxLimit     = 1000
)

func clampLimit(raw string) int {
	if raw == "" {
		return defaultLimit
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return defaultLimit
		}
		n = n*10 + int(c-'0')
		if n > maxLimit {
			return maxLimit
		}
	}
	if n == 0 {
		return defaultLimit
	}
	return n
}

func sanitizeSite(s string) string { return strings.TrimSpace(s) }

// ListenAndServe runs the server until ctx is cancelled, then drains within
// drain. It returns nil on a clean shutdown.
func ListenAndServe(ctx context.Context, addr string, h http.Handler, readTO, writeTO, drain time.Duration, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       readTO,
		WriteTimeout:      writeTO,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	log.Info("http draining", "timeout", drain)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	<-errCh
	return nil
}
