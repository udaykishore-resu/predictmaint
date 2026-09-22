package alerting

import "time"

// Status is the lifecycle state of an alert record.
type Status string

const (
	StatusOpen      Status = "open"
	StatusConfirmed Status = "confirmed"
	StatusDismissed Status = "dismissed"
)

// Contributor is a feature that drove the alert, for the technician's eyes.
type Contributor struct {
	Feature string  `json:"feature"`
	Z       float64 `json:"z"`
	Value   float64 `json:"value"`
}

// Alert is the durable record of a fired episode.
type Alert struct {
	ID          string  `json:"id"`
	AssetID     string  `json:"asset_id"`
	Site        string  `json:"site"`
	AssetClass  string  `json:"asset_class"`
	FailureMode string  `json:"failure_mode"`
	Risk        float64 `json:"risk"`
	Confidence  float64 `json:"confidence"`
	Anomaly     float64 `json:"anomaly"`
	Drift       float64 `json:"drift"`
	Prior       float64 `json:"prior"`
	Status      Status  `json:"status"`
	// EventTime is the sensor timestamp at which the episode fired;
	// DetectedAt is the wall-clock time the engine saw it.
	EventTime    time.Time         `json:"event_time"`
	DetectedAt   time.Time         `json:"detected_at"`
	ClearedAt    *time.Time        `json:"cleared_at,omitempty"`
	FeedbackAt   *time.Time        `json:"feedback_at,omitempty"`
	Technician   string            `json:"technician,omitempty"`
	Note         string            `json:"note,omitempty"`
	Contributors []Contributor     `json:"contributors"`
	Decision     Decision          `json:"decision"`
	Rules        []string          `json:"rules"`
	TemplateVer  string            `json:"template_version"`
	WorkOrderID  string            `json:"work_order_id,omitempty"`
	Adaptation   *AdaptationResult `json:"adaptation,omitempty"`
}

// Stats are the programme-health metrics operators actually watch.
type Stats struct {
	Site           string  `json:"site,omitempty"`
	TotalAlerts    int     `json:"total_alerts"`
	Open           int     `json:"open"`
	Confirmed      int     `json:"confirmed"`
	Dismissed      int     `json:"dismissed"`
	AlertsLast24h  int     `json:"alerts_last_24h"`
	AlertsPerDay   float64 `json:"alerts_per_day"`  // over the span of observed alerts, min 1 day
	PrecisionProxy float64 `json:"precision_proxy"` // confirmed / (confirmed + dismissed); NaN-free
	MTTASeconds    float64 `json:"mtta_seconds"`    // mean detected->feedback for acknowledged alerts
	Acknowledged   int     `json:"acknowledged"`
}

// ComputeStats derives Stats from a set of alerts as of now.
func ComputeStats(alerts []Alert, now time.Time) Stats {
	s := Stats{TotalAlerts: len(alerts)}
	if len(alerts) == 0 {
		return s
	}
	var first, last time.Time
	var mttaSum time.Duration
	for _, a := range alerts {
		switch a.Status {
		case StatusOpen:
			s.Open++
		case StatusConfirmed:
			s.Confirmed++
		case StatusDismissed:
			s.Dismissed++
		}
		if now.Sub(a.DetectedAt) <= 24*time.Hour {
			s.AlertsLast24h++
		}
		if first.IsZero() || a.DetectedAt.Before(first) {
			first = a.DetectedAt
		}
		if a.DetectedAt.After(last) {
			last = a.DetectedAt
		}
		if a.FeedbackAt != nil && !a.FeedbackAt.Before(a.DetectedAt) {
			s.Acknowledged++
			mttaSum += a.FeedbackAt.Sub(a.DetectedAt)
		}
	}
	span := last.Sub(first)
	if span < 24*time.Hour {
		span = 24 * time.Hour
	}
	s.AlertsPerDay = float64(len(alerts)) / span.Hours() * 24
	if judged := s.Confirmed + s.Dismissed; judged > 0 {
		s.PrecisionProxy = float64(s.Confirmed) / float64(judged)
	}
	if s.Acknowledged > 0 {
		s.MTTASeconds = mttaSum.Seconds() / float64(s.Acknowledged)
	}
	return s
}
