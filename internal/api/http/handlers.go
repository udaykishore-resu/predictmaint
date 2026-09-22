package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/engine"
	"github.com/udaykishore-resu/predictmaint/internal/domain/metric"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
)

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := map[string]string{}
	ok := true
	for _, c := range s.ready {
		if err := c.Check(ctx); err != nil {
			checks[c.Name] = "fail: " + err.Error()
			ok = false
		} else {
			checks[c.Name] = "ok"
		}
	}
	status := http.StatusOK
	state := "ready"
	if !ok {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	writeJSON(w, status, map[string]any{"status": state, "checks": checks})
}

// ingestRequest accepts either a bare array or {"metrics":[...]}.
type ingestRequest struct {
	Metrics []metric.Metric `json:"metrics"`
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body: "+err.Error())
		return
	}
	var batch []metric.Metric
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &batch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "invalid metrics array: "+err.Error())
			return
		}
	} else {
		var req ingestRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "invalid metrics object: "+err.Error())
			return
		}
		batch = req.Metrics
	}
	if len(batch) == 0 {
		writeError(w, http.StatusBadRequest, "empty_batch", "batch contains no metrics")
		return
	}
	res, err := s.eng.Ingest(r.Context(), batch)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if res.Accepted == 0 && res.Duplicates == 0 && res.Rejected > 0 {
		writeJSON(w, http.StatusBadRequest, res)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"assets": s.eng.Assets(sanitizeSite(r.URL.Query().Get("site")))})
}

func (s *Server) handleAssetHealth(w http.ResponseWriter, r *http.Request) {
	h, err := s.eng.Health(r.PathValue("id"))
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ports.AlertFilter{Site: sanitizeSite(q.Get("site")), AssetID: q.Get("asset_id"), Limit: clampLimit(q.Get("limit"), 100, 1000)}
	if st := q.Get("status"); st != "" {
		switch alerting.Status(st) {
		case alerting.StatusOpen, alerting.StatusConfirmed, alerting.StatusDismissed:
			f.Status = alerting.Status(st)
		default:
			writeError(w, http.StatusBadRequest, "invalid_status", "status must be open|confirmed|dismissed")
			return
		}
	}
	alerts, err := s.store.ListAlerts(r.Context(), f)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if alerts == nil {
		alerts = []alerting.Alert{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts, "count": len(alerts)})
}

func (s *Server) handleGetAlert(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAlert(r.Context(), r.PathValue("id"))
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type feedbackRequest struct {
	Verdict    string `json:"verdict"`
	Technician string `json:"technician"`
	Note       string `json:"note"`
}

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var req feedbackRequest
	if !s.decode(w, r, &req) {
		return
	}
	v, err := alerting.ParseVerdict(req.Verdict)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verdict", err.Error())
		return
	}
	if len(req.Technician) > 128 || len(req.Note) > 2000 {
		writeError(w, http.StatusBadRequest, "too_long", "technician <= 128 chars, note <= 2000 chars")
		return
	}
	a, err := s.eng.Feedback(r.Context(), engine.FeedbackRequest{AlertID: r.PathValue("id"), Verdict: v, Technician: req.Technician, Note: req.Note})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleListWorkOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ports.WorkOrderFilter{Site: sanitizeSite(q.Get("site")), AssetID: q.Get("asset_id"), Limit: clampLimit(q.Get("limit"), 100, 1000)}
	if st := q.Get("status"); st != "" {
		switch cmms.Status(st) {
		case cmms.StatusOpen, cmms.StatusCancelled, cmms.StatusClosed:
			f.Status = cmms.Status(st)
		default:
			writeError(w, http.StatusBadRequest, "invalid_status", "status must be open|cancelled|closed")
			return
		}
	}
	wos, err := s.store.ListWorkOrders(r.Context(), f)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if wos == nil {
		wos = []cmms.WorkOrder{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"work_orders": wos, "count": len(wos)})
}

func (s *Server) handleGetWorkOrder(w http.ResponseWriter, r *http.Request) {
	wo, err := s.store.GetWorkOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wo)
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"templates": s.eng.Templates()})
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.eng.Template(r.PathValue("class"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no template for class "+r.PathValue("class"))
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handlePutTemplate(w http.ResponseWriter, r *http.Request) {
	var t template.Template
	if !s.decode(w, r, &t) {
		return
	}
	class := r.PathValue("class")
	if t.Class == "" {
		t.Class = class
	}
	if t.Class != class {
		writeError(w, http.StatusBadRequest, "class_mismatch", "body class does not match path")
		return
	}
	saved, err := s.eng.PutTemplate(r.Context(), t)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.eng.Stats(r.Context(), sanitizeSite(r.URL.Query().Get("site")))
	if err != nil {
		s.domainError(w, err)
		return
	}
	if s.metrics != nil {
		s.metrics.SetStats(st)
	}
	writeJSON(w, http.StatusOK, st)
}
