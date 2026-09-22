package template

import (
	"time"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/detect"
	"github.com/udaykishore-resu/predictmaint/internal/domain/features"
)

// Built-in class names.
const (
	ClassPump       = "pump"
	ClassMotor      = "motor"
	ClassCompressor = "compressor"
)

// Defaults returns the built-in templates. They encode conservative ISO
// 10816-style vibration expectations and generic Weibull priors; sites are
// expected to override them through PUT /v1/templates/{class}.
func Defaults() []Template {
	return []Template{pump(), motor(), compressor()}
}

// DefaultFor returns the built-in template for a class, if any.
func DefaultFor(class string) (Template, bool) {
	for _, t := range Defaults() {
		if t.Class == class {
			return t, true
		}
	}
	return Template{}, false
}

var defaultUpdated = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// scalarStats are the statistics emitted for slowly sampled process
// variables. Kurtosis and crest factor over a dozen scalar readings are
// noise, so they are reserved for waveform bursts.
var scalarStats = []string{"mean", "std"}

// upOnly marks the given statistics of a signal as anomalous only when they
// increase: a quieter, cooler machine is not a fault.
func upOnly(m map[string]string, signal string, stats []string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	for _, stat := range stats {
		m[features.Key(signal, stat)] = "up"
	}
	return m
}

var waveformUp = []string{"rms", "std", "kurtosis", "crest"}

func pump() Template {
	dir := upOnly(nil, "vibration", waveformUp) // DC offset is sensor bias, not a fault
	dir = upOnly(dir, "bearing_temp", scalarStats)
	dir[features.BandKey("vibration", "1x")] = "up"
	dir[features.BandKey("vibration", "bpfo")] = "up"
	return Template{
		Class:       ClassPump,
		Version:     "2026.1",
		Description: "Centrifugal process pump: bearing wear, cavitation and load anomalies.",
		Signals: []features.SignalSpec{
			{Name: "vibration", Unit: "mm/s", Window: 256, SampleRateHz: 1000, MinSamples: 256,
				Bands: []features.Band{{Name: "1x", FreqHz: 23.4375}, {Name: "bpfo", FreqHz: 101.5625}}},
			{Name: "bearing_temp", Stats: scalarStats, Unit: "C", Window: 12, MinSamples: 3},
			{Name: "discharge_pressure", Stats: scalarStats, Unit: "bar", Window: 12, MinSamples: 3},
			{Name: "motor_current", Stats: scalarStats, Unit: "A", Window: 12, MinSamples: 3},
		},
		RuntimeSignal: "runtime_hours",
		Detectors: Detectors{
			RobustZ: detect.RobustZParams{Threshold: 4, MinMADRel: 0.02, MinMADAbs: 0.01, Direction: dir},
			HST:     detect.HSTParams{Trees: 25, Depth: 5, WindowSize: 64, ZRange: 5},
			CUSUM:   detect.CUSUMParams{Feature: features.Key("vibration", "rms"), K: 0.5, H: 8, Direction: "up"},
		},
		Prior:  detect.Weibull{Shape: 2.0, ScaleHours: 35000, HorizonHours: 720, DefaultAgeHours: 8000},
		Fusion: detect.FusionParams{WAnomaly: 0.6, WDrift: 0.25, WPrior: 0.15, AgreementThreshold: 0.5, HSTShare: 0.35},
		Alerting: alerting.Params{
			OnThreshold: 0.6, OffThreshold: 0.35,
			MinPersistence: 3, ClearPersistence: 3,
			Suppression:              alerting.Duration{Duration: 2 * time.Hour},
			MaxAlertsPerAssetPerHour: 2, MaxAlertsPerSitePerHour: 20,
			Adaptation: alerting.Adaptation{DismissStep: 0.05, ConfirmStep: 0.02, MaxOffset: 0.2, MaxThreshold: 0.9},
		},
		WorkOrder: cmms.Policy{
			ConfidenceThreshold: 0.55,
			FailureMode:         "bearing_wear",
			FailureModeBySignal: map[string]string{
				"bearing_temp":       "bearing_overheating",
				"discharge_pressure": "cavitation_or_impeller_wear",
				"motor_current":      "mechanical_load_increase",
			},
			Priority: 2,
			LeadTime: cmms.Duration{Duration: 72 * time.Hour},
		},
		WarmupEvaluations: 30,
		ClassBaselines: detect.Baselines{
			features.Key("vibration", "mean"):          {Median: 0, MAD: 0.03},
			features.Key("vibration", "std"):           {Median: 0.77, MAD: 0.03},
			features.Key("vibration", "rms"):           {Median: 0.77, MAD: 0.03},
			features.Key("vibration", "kurtosis"):      {Median: 1.9, MAD: 0.12},
			features.Key("vibration", "crest"):         {Median: 2.5, MAD: 0.2},
			features.BandKey("vibration", "1x"):        {Median: 1.0, MAD: 0.05},
			features.BandKey("vibration", "bpfo"):      {Median: 0.06, MAD: 0.03},
			features.Key("bearing_temp", "mean"):       {Median: 58, MAD: 1.0},
			features.Key("bearing_temp", "std"):        {Median: 0.4, MAD: 0.2},
			features.Key("discharge_pressure", "mean"): {Median: 6.2, MAD: 0.1},
			features.Key("discharge_pressure", "std"):  {Median: 0.08, MAD: 0.04},
			features.Key("motor_current", "mean"):      {Median: 42, MAD: 0.8},
			features.Key("motor_current", "std"):       {Median: 0.6, MAD: 0.3},
		},
		UpdatedAt: defaultUpdated,
	}
}

func motor() Template {
	dir := upOnly(nil, "vibration", waveformUp)
	dir = upOnly(dir, "winding_temp", scalarStats)
	dir[features.BandKey("vibration", "1x")] = "up"
	dir[features.BandKey("vibration", "2x")] = "up"
	return Template{
		Class:       ClassMotor,
		Version:     "2026.1",
		Description: "Induction motor: bearing wear, misalignment (2x), winding overheating.",
		Signals: []features.SignalSpec{
			{Name: "vibration", Unit: "mm/s", Window: 256, SampleRateHz: 1000, MinSamples: 256,
				Bands: []features.Band{{Name: "1x", FreqHz: 23.4375}, {Name: "2x", FreqHz: 46.875}}},
			{Name: "winding_temp", Stats: scalarStats, Unit: "C", Window: 12, MinSamples: 3},
			{Name: "current", Stats: scalarStats, Unit: "A", Window: 12, MinSamples: 3},
			{Name: "speed", Stats: scalarStats, Unit: "rpm", Window: 12, MinSamples: 3},
		},
		RuntimeSignal: "runtime_hours",
		Detectors: Detectors{
			RobustZ: detect.RobustZParams{Threshold: 4, MinMADRel: 0.02, MinMADAbs: 0.01, Direction: dir},
			HST:     detect.HSTParams{Trees: 25, Depth: 5, WindowSize: 64, ZRange: 5},
			CUSUM:   detect.CUSUMParams{Feature: features.Key("vibration", "rms"), K: 0.5, H: 8, Direction: "up"},
		},
		Prior:  detect.Weibull{Shape: 1.8, ScaleHours: 60000, HorizonHours: 720, DefaultAgeHours: 10000},
		Fusion: detect.FusionParams{WAnomaly: 0.6, WDrift: 0.25, WPrior: 0.15, AgreementThreshold: 0.5, HSTShare: 0.35},
		Alerting: alerting.Params{
			OnThreshold: 0.6, OffThreshold: 0.35,
			MinPersistence: 3, ClearPersistence: 3,
			Suppression:              alerting.Duration{Duration: 2 * time.Hour},
			MaxAlertsPerAssetPerHour: 2, MaxAlertsPerSitePerHour: 20,
			Adaptation: alerting.Adaptation{DismissStep: 0.05, ConfirmStep: 0.02, MaxOffset: 0.2, MaxThreshold: 0.9},
		},
		WorkOrder: cmms.Policy{
			ConfidenceThreshold: 0.55,
			FailureMode:         "bearing_wear",
			FailureModeBySignal: map[string]string{
				"winding_temp": "winding_overheating",
				"current":      "electrical_imbalance",
				"speed":        "load_or_drive_fault",
			},
			Priority: 2,
			LeadTime: cmms.Duration{Duration: 72 * time.Hour},
		},
		WarmupEvaluations: 30,
		ClassBaselines: detect.Baselines{
			features.Key("vibration", "rms"):      {Median: 0.6, MAD: 0.03},
			features.Key("vibration", "std"):      {Median: 0.6, MAD: 0.03},
			features.Key("vibration", "kurtosis"): {Median: 1.9, MAD: 0.12},
			features.Key("vibration", "crest"):    {Median: 2.5, MAD: 0.2},
			features.BandKey("vibration", "1x"):   {Median: 0.8, MAD: 0.05},
			features.BandKey("vibration", "2x"):   {Median: 0.1, MAD: 0.03},
			features.Key("winding_temp", "mean"):  {Median: 75, MAD: 1.5},
			features.Key("current", "mean"):       {Median: 110, MAD: 2},
			features.Key("speed", "mean"):         {Median: 1480, MAD: 3},
		},
		UpdatedAt: defaultUpdated,
	}
}

func compressor() Template {
	dir := upOnly(nil, "vibration", waveformUp)
	dir = upOnly(dir, "discharge_temp", scalarStats)
	dir[features.BandKey("vibration", "1x")] = "up"
	dir[features.BandKey("vibration", "valve")] = "up"
	return Template{
		Class:       ClassCompressor,
		Version:     "2026.1",
		Description: "Reciprocating/screw compressor: valve wear, bearing wear, discharge overheating.",
		Signals: []features.SignalSpec{
			{Name: "vibration", Unit: "mm/s", Window: 256, SampleRateHz: 1000, MinSamples: 256,
				Bands: []features.Band{{Name: "1x", FreqHz: 23.4375}, {Name: "valve", FreqHz: 156.25}}},
			{Name: "discharge_temp", Stats: scalarStats, Unit: "C", Window: 12, MinSamples: 3},
			{Name: "discharge_pressure", Stats: scalarStats, Unit: "bar", Window: 12, MinSamples: 3},
			{Name: "current", Stats: scalarStats, Unit: "A", Window: 12, MinSamples: 3},
		},
		RuntimeSignal: "runtime_hours",
		Detectors: Detectors{
			RobustZ: detect.RobustZParams{Threshold: 4, MinMADRel: 0.02, MinMADAbs: 0.01, Direction: dir},
			HST:     detect.HSTParams{Trees: 25, Depth: 5, WindowSize: 64, ZRange: 5},
			CUSUM:   detect.CUSUMParams{Feature: features.Key("discharge_temp", "mean"), K: 0.5, H: 8, Direction: "up"},
		},
		Prior:  detect.Weibull{Shape: 2.2, ScaleHours: 25000, HorizonHours: 720, DefaultAgeHours: 6000},
		Fusion: detect.FusionParams{WAnomaly: 0.55, WDrift: 0.3, WPrior: 0.15, AgreementThreshold: 0.5, HSTShare: 0.35},
		Alerting: alerting.Params{
			OnThreshold: 0.6, OffThreshold: 0.35,
			MinPersistence: 3, ClearPersistence: 3,
			Suppression:              alerting.Duration{Duration: 2 * time.Hour},
			MaxAlertsPerAssetPerHour: 2, MaxAlertsPerSitePerHour: 20,
			Adaptation: alerting.Adaptation{DismissStep: 0.05, ConfirmStep: 0.02, MaxOffset: 0.2, MaxThreshold: 0.9},
		},
		WorkOrder: cmms.Policy{
			ConfidenceThreshold: 0.55,
			FailureMode:         "valve_wear",
			FailureModeBySignal: map[string]string{
				"discharge_temp":     "discharge_overheating",
				"discharge_pressure": "valve_leakage",
				"current":            "mechanical_load_increase",
			},
			Priority: 1,
			LeadTime: cmms.Duration{Duration: 48 * time.Hour},
		},
		WarmupEvaluations: 30,
		ClassBaselines: detect.Baselines{
			features.Key("vibration", "rms"):           {Median: 2.2, MAD: 0.1},
			features.Key("vibration", "std"):           {Median: 2.2, MAD: 0.1},
			features.Key("vibration", "kurtosis"):      {Median: 2.4, MAD: 0.2},
			features.Key("vibration", "crest"):         {Median: 3.0, MAD: 0.3},
			features.BandKey("vibration", "1x"):        {Median: 2.5, MAD: 0.15},
			features.BandKey("vibration", "valve"):     {Median: 0.3, MAD: 0.08},
			features.Key("discharge_temp", "mean"):     {Median: 95, MAD: 2},
			features.Key("discharge_pressure", "mean"): {Median: 8.5, MAD: 0.15},
			features.Key("current", "mean"):            {Median: 180, MAD: 4},
		},
		UpdatedAt: defaultUpdated,
	}
}
