package cmms

import "time"

// MaximoWorkOrder is the shape of an IBM Maximo MXWO object-structure POST
// (JSON, "spi:" namespace as used by Maximo REST/OSLC). Only the fields a
// predictive-maintenance integration realistically sets are included.
type MaximoWorkOrder struct {
	Description   string `json:"spi:description"`
	LongDesc      string `json:"spi:description_longdescription,omitempty"`
	AssetNum      string `json:"spi:assetnum"`
	SiteID        string `json:"spi:siteid"`
	WorkType      string `json:"spi:worktype"`
	WOPriority    int    `json:"spi:wopriority"`
	Status        string `json:"spi:status"`
	FailureCode   string `json:"spi:failurecode,omitempty"`
	ProblemCode   string `json:"spi:problemcode,omitempty"`
	TargetStart   string `json:"spi:targstartdate"`
	TargetFinish  string `json:"spi:targcompdate"`
	ReportDate    string `json:"spi:reportdate"`
	ReportedBy    string `json:"spi:reportedby"`
	ExternalRefID string `json:"spi:externalrefid"`
}

// ToMaximo maps a work order to a Maximo payload.
func ToMaximo(wo WorkOrder) MaximoWorkOrder {
	return MaximoWorkOrder{
		Description:   truncate(wo.Description, 100),
		LongDesc:      wo.Description,
		AssetNum:      wo.AssetID,
		SiteID:        wo.Site,
		WorkType:      "PM",
		WOPriority:    wo.Priority,
		Status:        "WAPPR",
		FailureCode:   wo.FailureMode,
		ProblemCode:   "PDM",
		TargetStart:   wo.CreatedAt.UTC().Format(time.RFC3339),
		TargetFinish:  wo.RequiredBy.UTC().Format(time.RFC3339),
		ReportDate:    wo.CreatedAt.UTC().Format(time.RFC3339),
		ReportedBy:    "PREDICTMAINT",
		ExternalRefID: wo.IdempotencyKey,
	}
}

// SAPPMOrder is the shape of an SAP PM maintenance order as exposed through
// an OData API (API_MAINTENANCEORDER-like field names).
type SAPPMOrder struct {
	MaintenanceOrderType     string `json:"MaintenanceOrderType"`
	MaintenanceOrderDesc     string `json:"MaintenanceOrderDesc"`
	MaintenancePlanningPlant string `json:"MaintenancePlanningPlant"`
	Equipment                string `json:"Equipment"`
	FunctionalLocation       string `json:"FunctionalLocation,omitempty"`
	MaintPriority            string `json:"MaintPriority"`
	MaintenanceActivityType  string `json:"MaintenanceActivityType"`
	BasicSchedulingType      string `json:"BasicSchedulingType"`
	MaintOrdBasicStartDate   string `json:"MaintOrdBasicStartDate"`
	MaintOrdBasicEndDate     string `json:"MaintOrdBasicEndDate"`
	MaintenanceNotification  string `json:"MaintenanceNotification,omitempty"`
	ExternalReference        string `json:"ExternalReference"`
	LongText                 string `json:"LongText,omitempty"`
}

// ToSAPPM maps a work order to an SAP PM order payload. PM01 is the usual
// "corrective/planned" order type; activity type 003 is condition-based.
func ToSAPPM(wo WorkOrder) SAPPMOrder {
	return SAPPMOrder{
		MaintenanceOrderType:     "PM01",
		MaintenanceOrderDesc:     truncate(wo.Description, 40),
		MaintenancePlanningPlant: wo.Site,
		Equipment:                wo.AssetID,
		MaintPriority:            sapPriority(wo.Priority),
		MaintenanceActivityType:  "003",
		BasicSchedulingType:      "1",
		MaintOrdBasicStartDate:   wo.CreatedAt.UTC().Format("2006-01-02"),
		MaintOrdBasicEndDate:     wo.RequiredBy.UTC().Format("2006-01-02"),
		ExternalReference:        wo.IdempotencyKey,
		LongText:                 wo.Description,
	}
}

func sapPriority(p int) string {
	switch {
	case p <= 1:
		return "1" // very high
	case p == 2:
		return "2" // high
	case p == 3:
		return "3" // medium
	default:
		return "4" // low
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
